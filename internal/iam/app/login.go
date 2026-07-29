package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/iam/domain"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

// LoginUseCase handles user authentication.
type LoginUseCase struct {
	repo         UserRepository
	hasher       Hasher
	sessions     SessionStore
	delivery     mailapp.DeliveryPort
	rewardWallet RegistrationRewardWallet
}

// NewLoginUseCase creates a new LoginUseCase.
func NewLoginUseCase(repo UserRepository, hasher Hasher, sessions SessionStore, delivery ...mailapp.DeliveryPort) *LoginUseCase {
	uc := &LoginUseCase{repo: repo, hasher: hasher, sessions: sessions}
	if len(delivery) > 0 {
		uc.delivery = delivery[0]
	}
	return uc
}

// SetRegistrationRewardWallet injects Billing after both modules are constructed.
func (uc *LoginUseCase) SetRegistrationRewardWallet(wallet RegistrationRewardWallet) {
	uc.rewardWallet = wallet
}

type LoginMeta struct {
	ClientIP  string
	UserAgent string
}

// LoginResult contains the outcome of a successful login.
type LoginResult struct {
	Session       *domain.Session
	User          *domain.User
	SessionMaxAge int
}

type LinuxDOProfile struct {
	ID         string
	Username   string
	Name       string
	Active     bool
	Silenced   bool
	TrustLevel int
}

// Login authenticates a user by email and password after the API boundary has
// validated the request's Turnstile token.
// Disabled accounts return the same error to prevent account enumeration
// (docs/8-iam.md:109 — only "Account or password is incorrect" is safe to expose).
func (uc *LoginUseCase) Login(ctx context.Context, email, password string, metadata ...LoginMeta) (*LoginResult, error) {
	user, err := uc.repo.FindByEmail(ctx, normalizeEmail(email))
	if err != nil {
		return nil, fmt.Errorf("login find user: %w", err)
	}
	if user == nil {
		return nil, domain.ErrAccountOrPasswordIncorrect
	}

	// Verify password first to avoid leaking account state.
	// Even disabled accounts get the same error (INV-I2, docs/13-quality-matrices.md:59).
	if !uc.hasher.Verify(password, user.PasswordHash) {
		return nil, domain.ErrAccountOrPasswordIncorrect
	}

	// Check enabled after password verification (INV-I2)
	if !user.IsActive() {
		return nil, domain.ErrAccountOrPasswordIncorrect
	}

	// Record the login only if the enabled/password snapshot verified above is
	// still current, and use the freshly loaded role/token version for session.
	user, err = uc.repo.RecordLogin(ctx, user.ID, user.PasswordHash)
	if err != nil {
		return nil, fmt.Errorf("login update last login: %w", err)
	}
	if user == nil {
		return nil, domain.ErrAccountOrPasswordIncorrect
	}

	return uc.finishLogin(ctx, user, true, metadata...)
}

func (uc *LoginUseCase) LoginLinuxDO(ctx context.Context, profile LinuxDOProfile, metadata ...LoginMeta) (*LoginResult, error) {
	profile, err := normalizeLinuxDOProfile(profile)
	if err != nil {
		return nil, err
	}

	user, err := uc.repo.FindByLinuxDOID(ctx, profile.ID)
	if err != nil {
		return nil, fmt.Errorf("linuxdo login find user: %w", err)
	}
	if user == nil {
		if !runtimeconfig.Bool("register_enabled", true) {
			return nil, domain.ErrRegistrationDisabled
		}
		password, err := newCryptoID()
		if err != nil {
			return nil, fmt.Errorf("linuxdo login generate password: %w", err)
		}
		passwordHash, err := uc.hasher.Hash(password)
		if err != nil {
			return nil, fmt.Errorf("linuxdo login hash password: %w", err)
		}
		user = &domain.User{
			Email:        fmt.Sprintf("linuxdo-%s@oauth.invalid", profile.ID),
			PasswordHash: passwordHash,
			Nickname:     linuxDONickname(profile),
			Status:       domain.UserStatusActive,
			Role:         domain.RoleUser,
			UserGroupID:  1,
		}
		if err := uc.repo.CreateWithLinuxDOIdentity(ctx, user, profile.ID); err != nil {
			if !errors.Is(err, domain.ErrLinuxDOIdentityAlreadyBound) && !errors.Is(err, domain.ErrEmailAlreadyExists) {
				return nil, fmt.Errorf("linuxdo login create user: %w", err)
			}
			user, err = uc.repo.FindByLinuxDOID(ctx, profile.ID)
			if err != nil {
				return nil, fmt.Errorf("linuxdo login resolve concurrent user: %w", err)
			}
			if user == nil {
				return nil, errors.New("linuxdo login concurrent user is missing")
			}
		} else {
			grantRegistrationReward(ctx, uc.rewardWallet, user.ID)
		}
	}

	user, err = uc.repo.RecordLinuxDOLogin(ctx, user.ID, profile.ID)
	if err != nil {
		return nil, fmt.Errorf("linuxdo login update last login: %w", err)
	}
	if user == nil {
		return nil, domain.ErrLinuxDOAccountUnavailable
	}
	notify := !strings.HasSuffix(strings.ToLower(user.Email), "@oauth.invalid")
	return uc.finishLogin(ctx, user, notify, metadata...)
}

func (uc *LoginUseCase) BindLinuxDO(ctx context.Context, userID uint, profile LinuxDOProfile) error {
	profile, err := normalizeLinuxDOProfile(profile)
	if err != nil {
		return err
	}
	if err := uc.repo.BindLinuxDOIdentity(ctx, userID, profile.ID); err != nil {
		return fmt.Errorf("bind linuxdo identity: %w", err)
	}
	return nil
}

func (uc *LoginUseCase) HasLinuxDOIdentity(ctx context.Context, userID uint) (bool, error) {
	bound, err := uc.repo.HasLinuxDOIdentity(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("has linuxdo identity: %w", err)
	}
	return bound, nil
}

func normalizeLinuxDOProfile(profile LinuxDOProfile) (LinuxDOProfile, error) {
	profile.ID = strings.TrimSpace(profile.ID)
	id, err := strconv.ParseUint(profile.ID, 10, 64)
	if err != nil || id == 0 || strconv.FormatUint(id, 10) != profile.ID || !profile.Active || profile.Silenced {
		return LinuxDOProfile{}, domain.ErrLinuxDOAccountUnavailable
	}
	if profile.TrustLevel < runtimeconfig.Int("linuxdo_minimum_trust_level", 0, 0) {
		return LinuxDOProfile{}, domain.ErrLinuxDOTrustLevelTooLow
	}
	return profile, nil
}

func linuxDONickname(profile LinuxDOProfile) string {
	nickname := strings.TrimSpace(profile.Name)
	if nickname == "" {
		nickname = strings.TrimSpace(profile.Username)
	}
	if nickname == "" {
		nickname = "LinuxDO User"
	}
	if runes := []rune(nickname); len(runes) > 100 {
		nickname = string(runes[:100])
	}
	return nickname
}

func (uc *LoginUseCase) finishLogin(ctx context.Context, user *domain.User, notify bool, metadata ...LoginMeta) (*LoginResult, error) {
	now := time.Now()
	sessionID, err := newCryptoID()
	if err != nil {
		return nil, fmt.Errorf("login generate session id: %w", err)
	}
	session := &domain.Session{
		ID:           sessionID,
		UserID:       user.ID,
		Role:         user.Role,
		Email:        user.Email,
		TokenVersion: user.TokenVersion,
		CreatedAt:    now,
	}

	sessionMaxAge := runtimeconfig.Int("session_max_age_seconds", 86400, 300)
	if err := uc.sessions.Create(ctx, session, sessionMaxAge); err != nil {
		return nil, fmt.Errorf("login create session: %w", err)
	}
	if notify && uc.delivery != nil {
		meta := LoginMeta{}
		if len(metadata) > 0 {
			meta = metadata[0]
		}
		if err := uc.delivery.Send(ctx, mailapp.LoginNotificationMessage(user.Email, session.ID, meta.ClientIP, meta.UserAgent, now)); err != nil {
			slog.Warn("send login notification failed", "user_id", user.ID, "error", err)
		}
	}

	return &LoginResult{
		Session:       session,
		User:          user,
		SessionMaxAge: sessionMaxAge,
	}, nil
}
