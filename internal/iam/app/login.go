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
	codeStore    EmailCodeStore
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

func (uc *LoginUseCase) SetEmailCodeStore(store EmailCodeStore) {
	uc.codeStore = store
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
	Email      string
	Active     bool
	Silenced   bool
	TrustLevel int
}

const githubIdentityProvider = "github"

type GitHubProfile struct {
	ID        string
	Username  string
	Name      string
	Email     string
	CreatedAt time.Time
}

type GitHubPending struct {
	Profile   GitHubProfile `json:"profile"`
	Intent    string        `json:"intent"`
	UserID    uint          `json:"userId"`
	Email     string        `json:"email"`
	SessionID string        `json:"sessionId,omitempty"`
}

type LinuxDOAccountMode string

const (
	LinuxDOAccountExisting LinuxDOAccountMode = "existing"
	LinuxDOAccountNew      LinuxDOAccountMode = "new"
)

type LinuxDOPending struct {
	Profile              LinuxDOProfile `json:"profile"`
	LegacyUserID         uint           `json:"legacyUserId,omitempty"`
	SuggestedEmail       string         `json:"suggestedEmail,omitempty"`
	SuggestedEmailExists bool           `json:"suggestedEmailExists"`
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

func (uc *LoginUseCase) LoginLinuxDO(ctx context.Context, profile LinuxDOProfile, metadata ...LoginMeta) (*LoginResult, *LinuxDOPending, error) {
	profile, err := normalizeLinuxDOProfile(profile)
	if err != nil {
		return nil, nil, err
	}

	user, err := uc.repo.FindByLinuxDOID(ctx, profile.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("linuxdo login find user: %w", err)
	}
	if user == nil || isLinuxDOPlaceholderEmail(user.Email) {
		if user != nil && !user.IsActive() {
			return nil, nil, domain.ErrLinuxDOAccountUnavailable
		}
		pending, err := uc.newLinuxDOPending(ctx, profile)
		if err != nil {
			return nil, nil, err
		}
		if user != nil {
			pending.LegacyUserID = user.ID
		}
		return nil, pending, nil
	}

	user, err = uc.repo.RecordLinuxDOLogin(ctx, user.ID, profile.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("linuxdo login update last login: %w", err)
	}
	if user == nil {
		return nil, nil, domain.ErrLinuxDOAccountUnavailable
	}
	result, err := uc.finishLogin(ctx, user, true, metadata...)
	return result, nil, err
}

func (uc *LoginUseCase) LoginGitHub(ctx context.Context, profile GitHubProfile, metadata ...LoginMeta) (*LoginResult, *GitHubPending, error) {
	profile, err := normalizeGitHubProfile(profile)
	if err != nil {
		return nil, nil, err
	}

	user, err := uc.repo.FindByThirdPartyIdentity(ctx, githubIdentityProvider, profile.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("github login find identity: %w", err)
	}
	if user != nil {
		result, err := uc.finishGitHubLogin(ctx, user, profile.ID, metadata...)
		return result, nil, err
	}

	user, err = uc.repo.FindByEmail(ctx, profile.Email)
	if err != nil {
		return nil, nil, fmt.Errorf("github login find email: %w", err)
	}
	if user != nil {
		if !user.IsActive() {
			return nil, nil, domain.ErrGitHubAccountUnavailable
		}
		return nil, githubPending(profile, user, "login", ""), nil
	}
	if !runtimeconfig.Bool("register_enabled", true) {
		return nil, nil, domain.ErrRegistrationDisabled
	}
	passwordMarker, err := newOAuthPasswordMarker()
	if err != nil {
		return nil, nil, fmt.Errorf("github login generate password marker: %w", err)
	}
	user = &domain.User{
		Email:        profile.Email,
		PasswordHash: passwordMarker,
		Nickname:     githubNickname(profile),
		Status:       domain.UserStatusActive,
		Role:         domain.RoleUser,
		UserGroupID:  1,
	}
	if err := uc.repo.CreateWithThirdPartyIdentity(ctx, user, githubIdentityProvider, profile.ID); err != nil {
		if !errors.Is(err, domain.ErrEmailAlreadyExists) && !errors.Is(err, domain.ErrThirdPartyIdentityAlreadyBound) {
			return nil, nil, fmt.Errorf("github login create account: %w", err)
		}
		bound, findErr := uc.repo.FindByThirdPartyIdentity(ctx, githubIdentityProvider, profile.ID)
		if findErr != nil {
			return nil, nil, fmt.Errorf("github login resolve identity conflict: %w", findErr)
		}
		if bound != nil {
			result, loginErr := uc.finishGitHubLogin(ctx, bound, profile.ID, metadata...)
			return result, nil, loginErr
		}
		existing, findErr := uc.repo.FindByEmail(ctx, profile.Email)
		if findErr != nil {
			return nil, nil, fmt.Errorf("github login resolve email conflict: %w", findErr)
		}
		if existing != nil {
			if !existing.IsActive() {
				return nil, nil, domain.ErrGitHubAccountUnavailable
			}
			return nil, githubPending(profile, existing, "login", ""), nil
		}
		return nil, nil, fmt.Errorf("github login create account: %w", err)
	}
	grantRegistrationReward(ctx, uc.rewardWallet, user.ID)
	result, err := uc.finishGitHubLogin(ctx, user, profile.ID, metadata...)
	return result, nil, err
}

func (uc *LoginUseCase) NewGitHubBindPending(ctx context.Context, userID uint, sessionID string, profile GitHubProfile) (*GitHubPending, error) {
	profile, err := normalizeGitHubProfile(profile)
	if err != nil {
		return nil, err
	}
	if userID == 0 || strings.TrimSpace(sessionID) == "" {
		return nil, domain.ErrAuthenticationRequired
	}
	user, err := uc.repo.FindByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("github bind find account: %w", err)
	}
	if user == nil || !user.IsActive() {
		return nil, domain.ErrGitHubAccountUnavailable
	}
	bound, err := uc.repo.FindByThirdPartyIdentity(ctx, githubIdentityProvider, profile.ID)
	if err != nil {
		return nil, fmt.Errorf("github bind find identity: %w", err)
	}
	if bound != nil {
		return nil, domain.ErrThirdPartyIdentityAlreadyBound
	}
	return githubPending(profile, user, "bind", sessionID), nil
}

func (uc *LoginUseCase) CompleteGitHub(ctx context.Context, pending GitHubPending, code string, metadata ...LoginMeta) (*LoginResult, error) {
	if uc.codeStore == nil {
		return nil, errors.New("github email code store is not configured")
	}
	profile, err := normalizeGitHubProfile(pending.Profile)
	if err != nil {
		return nil, err
	}
	if pending.Intent != "login" && pending.Intent != "bind" {
		return nil, domain.ErrGitHubAccountUnavailable
	}
	email := normalizeEmail(pending.Email)
	if validateEmailAddress(email) != nil || pending.UserID == 0 {
		return nil, domain.ErrGitHubAccountUnavailable
	}
	user, err := uc.repo.FindByID(ctx, pending.UserID)
	if err != nil {
		return nil, fmt.Errorf("github verify find account: %w", err)
	}
	if user == nil || !user.IsActive() || normalizeEmail(user.Email) != email {
		return nil, domain.ErrGitHubAccountUnavailable
	}

	key := githubEmailCodeKey(email)
	code = strings.TrimSpace(code)
	claimToken, err := newCryptoID()
	if err != nil {
		return nil, fmt.Errorf("github verify generate email claim: %w", err)
	}
	restore := func(cause error) error {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if _, restoreErr := uc.codeStore.Restore(restoreCtx, key, claimToken, code); restoreErr != nil {
			return fmt.Errorf("restore github email code after %v: %w", cause, restoreErr)
		}
		return cause
	}
	claimed, err := uc.codeStore.Claim(ctx, key, code, claimToken)
	if err != nil {
		return nil, restore(fmt.Errorf("github verify claim email code: %w", err))
	}
	if !claimed {
		return nil, domain.ErrVerificationCodeIncorrect
	}
	if err := uc.repo.BindThirdPartyIdentity(ctx, user.ID, githubIdentityProvider, profile.ID); err != nil {
		return nil, restore(fmt.Errorf("github verify bind identity: %w", err))
	}

	var result *LoginResult
	if pending.Intent == "login" {
		result, err = uc.finishGitHubLogin(ctx, user, profile.ID, metadata...)
		if err != nil {
			return nil, restore(err)
		}
	}
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if committed, commitErr := uc.codeStore.Commit(commitCtx, key, claimToken); commitErr != nil || !committed {
		slog.Warn("commit github email code", "error", commitErr, "committed", committed)
	}
	return result, nil
}

func githubPending(profile GitHubProfile, user *domain.User, intent, sessionID string) *GitHubPending {
	return &GitHubPending{
		Profile:   profile,
		Intent:    intent,
		UserID:    user.ID,
		Email:     normalizeEmail(user.Email),
		SessionID: sessionID,
	}
}

func (uc *LoginUseCase) finishGitHubLogin(ctx context.Context, user *domain.User, providerUserID string, metadata ...LoginMeta) (*LoginResult, error) {
	if user == nil || !user.IsActive() {
		return nil, domain.ErrGitHubAccountUnavailable
	}
	user, err := uc.repo.RecordThirdPartyLogin(ctx, user.ID, githubIdentityProvider, providerUserID)
	if err != nil {
		return nil, fmt.Errorf("github login update last login: %w", err)
	}
	if user == nil {
		return nil, domain.ErrGitHubAccountUnavailable
	}
	return uc.finishLogin(ctx, user, true, metadata...)
}

func (uc *LoginUseCase) HasGitHubIdentity(ctx context.Context, userID uint) (bool, error) {
	bound, err := uc.repo.HasThirdPartyIdentity(ctx, userID, githubIdentityProvider)
	if err != nil {
		return false, fmt.Errorf("has github identity: %w", err)
	}
	return bound, nil
}

func normalizeGitHubProfile(profile GitHubProfile) (GitHubProfile, error) {
	profile.ID = strings.TrimSpace(profile.ID)
	id, err := strconv.ParseUint(profile.ID, 10, 64)
	if err != nil || id == 0 || strconv.FormatUint(id, 10) != profile.ID {
		return GitHubProfile{}, domain.ErrGitHubAccountUnavailable
	}
	profile.Email = normalizeEmail(profile.Email)
	if validateEmailAddress(profile.Email) != nil {
		return GitHubProfile{}, domain.ErrGitHubVerifiedEmailUnavailable
	}
	now := time.Now()
	if profile.CreatedAt.IsZero() || profile.CreatedAt.After(now) {
		return GitHubProfile{}, domain.ErrGitHubAccountUnavailable
	}
	minimumAgeDays := runtimeconfig.Int("github_minimum_account_age_days", 0, 0)
	if minimumAgeDays > 0 && profile.CreatedAt.After(now.AddDate(0, 0, -minimumAgeDays)) {
		return GitHubProfile{}, domain.ErrGitHubAccountTooNew
	}
	return profile, nil
}

func githubNickname(profile GitHubProfile) string {
	nickname := strings.TrimSpace(profile.Name)
	if nickname == "" {
		nickname = strings.TrimSpace(profile.Username)
	}
	if nickname == "" {
		nickname = "GitHub User"
	}
	if runes := []rune(nickname); len(runes) > 100 {
		nickname = string(runes[:100])
	}
	return nickname
}

func (uc *LoginUseCase) newLinuxDOPending(ctx context.Context, profile LinuxDOProfile) (*LinuxDOPending, error) {
	pending := &LinuxDOPending{Profile: profile}
	pending.SuggestedEmail = trustedLinuxDOEmail(profile.Email)
	if pending.SuggestedEmail == "" {
		return pending, nil
	}
	existing, err := uc.repo.FindByEmail(ctx, pending.SuggestedEmail)
	if err != nil {
		return nil, fmt.Errorf("linuxdo login inspect provider email: %w", err)
	}
	pending.SuggestedEmailExists = existing != nil
	return pending, nil
}

func (uc *LoginUseCase) CompleteLinuxDO(ctx context.Context, pending LinuxDOPending, mode LinuxDOAccountMode, email, code string, metadata ...LoginMeta) (*LoginResult, error) {
	if uc.codeStore == nil {
		return nil, errors.New("linuxdo email code store is not configured")
	}
	profile, err := normalizeLinuxDOProfile(pending.Profile)
	if err != nil {
		return nil, err
	}
	mode = LinuxDOAccountMode(strings.TrimSpace(string(mode)))
	normalizedEmail := normalizeEmail(email)
	if err := validateLinuxDOEmail(normalizedEmail, profile.Email, mode); err != nil {
		return nil, err
	}
	if mode == LinuxDOAccountExisting && pending.LegacyUserID != 0 {
		return nil, domain.ErrLinuxDOLegacyMergeUnsupported
	}
	if mode == LinuxDOAccountNew && pending.LegacyUserID == 0 && !runtimeconfig.Bool("register_enabled", true) {
		return nil, domain.ErrRegistrationDisabled
	}

	key := linuxDOEmailCodeKey(normalizedEmail)
	code = strings.TrimSpace(code)
	claimToken, err := newCryptoID()
	if err != nil {
		return nil, fmt.Errorf("linuxdo setup generate email claim: %w", err)
	}
	restore := func(cause error) error {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if _, restoreErr := uc.codeStore.Restore(restoreCtx, key, claimToken, code); restoreErr != nil {
			return fmt.Errorf("restore linuxdo email code after %v: %w", cause, restoreErr)
		}
		return cause
	}
	claimed, err := uc.codeStore.Claim(ctx, key, code, claimToken)
	if err != nil {
		return nil, restore(fmt.Errorf("linuxdo setup claim email code: %w", err))
	}
	if !claimed {
		return nil, domain.ErrVerificationCodeIncorrect
	}

	var user *domain.User
	created := false
	switch mode {
	case LinuxDOAccountExisting:
		user, err = uc.repo.FindByEmail(ctx, normalizedEmail)
		if err != nil {
			return nil, restore(fmt.Errorf("linuxdo setup find existing account: %w", err))
		}
		if user == nil {
			return nil, restore(domain.ErrLinuxDOExistingAccountNotFound)
		}
		if !user.IsActive() {
			return nil, restore(domain.ErrLinuxDOAccountUnavailable)
		}
		err = uc.repo.BindLinuxDOIdentity(ctx, user.ID, profile.ID)
		if err != nil {
			return nil, restore(fmt.Errorf("linuxdo setup bind existing account: %w", err))
		}

	case LinuxDOAccountNew:
		existing, findErr := uc.repo.FindByEmail(ctx, normalizedEmail)
		if findErr != nil {
			return nil, restore(fmt.Errorf("linuxdo setup check new email: %w", findErr))
		}
		if existing != nil {
			bound, boundErr := uc.repo.FindByLinuxDOID(ctx, profile.ID)
			if boundErr != nil {
				return nil, restore(fmt.Errorf("linuxdo setup resolve existing binding: %w", boundErr))
			}
			if bound == nil || bound.ID != existing.ID {
				return nil, restore(domain.ErrLinuxDONewEmailAlreadyExists)
			}
			user = existing
			break
		}
		passwordMarker, markerErr := newOAuthPasswordMarker()
		if markerErr != nil {
			return nil, restore(fmt.Errorf("linuxdo setup generate password marker: %w", markerErr))
		}
		if pending.LegacyUserID != 0 {
			if err := uc.repo.UpdateLinuxDOPlaceholder(ctx, pending.LegacyUserID, profile.ID, normalizedEmail, passwordMarker); err != nil {
				if errors.Is(err, domain.ErrEmailAlreadyExists) {
					err = domain.ErrLinuxDONewEmailAlreadyExists
				}
				return nil, restore(fmt.Errorf("linuxdo setup update placeholder: %w", err))
			}
			user, err = uc.repo.FindByLinuxDOID(ctx, profile.ID)
			if err != nil {
				return nil, restore(fmt.Errorf("linuxdo setup reload placeholder: %w", err))
			}
			if user == nil {
				return nil, restore(domain.ErrLinuxDOAccountUnavailable)
			}
		} else {
			user = &domain.User{
				Email:        normalizedEmail,
				PasswordHash: passwordMarker,
				Nickname:     linuxDONickname(profile),
				Status:       domain.UserStatusActive,
				Role:         domain.RoleUser,
				UserGroupID:  1,
			}
			if err := uc.repo.CreateWithLinuxDOIdentity(ctx, user, profile.ID); err != nil {
				if errors.Is(err, domain.ErrEmailAlreadyExists) {
					err = domain.ErrLinuxDONewEmailAlreadyExists
				}
				return nil, restore(fmt.Errorf("linuxdo setup create account: %w", err))
			}
			created = true
		}

	default:
		return nil, restore(domain.ErrLinuxDOAccountModeInvalid)
	}

	user, err = uc.repo.RecordLinuxDOLogin(ctx, user.ID, profile.ID)
	if err != nil {
		return nil, restore(fmt.Errorf("linuxdo setup update last login: %w", err))
	}
	if user == nil {
		return nil, restore(domain.ErrLinuxDOAccountUnavailable)
	}
	if created {
		grantRegistrationReward(ctx, uc.rewardWallet, user.ID)
	}
	result, err := uc.finishLogin(ctx, user, true, metadata...)
	if err != nil {
		return nil, restore(fmt.Errorf("linuxdo setup finish login: %w", err))
	}
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if committed, commitErr := uc.codeStore.Commit(commitCtx, key, claimToken); commitErr != nil || !committed {
		slog.Warn("commit linuxdo email code", "error", commitErr, "committed", committed)
	}
	return result, nil
}

func newOAuthPasswordMarker() (string, error) {
	token, err := newCryptoID()
	if err != nil {
		return "", err
	}
	return "!oauth:" + token, nil
}

func isLinuxDOPlaceholderEmail(email string) bool {
	return strings.HasSuffix(normalizeEmail(email), "@oauth.invalid")
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
