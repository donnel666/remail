package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/iam/domain"
)

// LoginUseCase handles user authentication.
type LoginUseCase struct {
	repo     UserRepository
	hasher   Hasher
	sessions SessionStore
	captcha  CaptchaStore
}

// NewLoginUseCase creates a new LoginUseCase.
func NewLoginUseCase(repo UserRepository, hasher Hasher, sessions SessionStore, captcha CaptchaStore) *LoginUseCase {
	return &LoginUseCase{repo: repo, hasher: hasher, sessions: sessions, captcha: captcha}
}

// LoginResult contains the outcome of a successful login.
type LoginResult struct {
	Session *domain.Session
	User    *domain.User
}

// Login authenticates a user by email and password.
// Requires a valid captcha to prevent brute-force attacks.
// Returns ErrCaptchaIncorrect or ErrAccountOrPasswordIncorrect.
// Disabled accounts return the same error to prevent account enumeration
// (docs/8-iam.md:109 — only "Account or password is incorrect" is safe to expose).
func (uc *LoginUseCase) Login(ctx context.Context, email, password, captchaID, captchaAnswer string, sessionTTL int) (*LoginResult, error) {
	// Verify captcha first to avoid leaking user existence
	if err := uc.verifyCaptcha(ctx, captchaID, captchaAnswer); err != nil {
		return nil, err
	}

	user, err := uc.repo.FindByEmail(ctx, email)
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
	if !user.Enabled {
		return nil, domain.ErrAccountOrPasswordIncorrect
	}

	// Update last login time
	now := time.Now()
	user.LastLoginAt = &now
	if err := uc.repo.Update(ctx, user); err != nil {
		return nil, fmt.Errorf("login update last login: %w", err)
	}

	// Create session
	sessionID, err := newCryptoID()
	if err != nil {
		return nil, fmt.Errorf("login generate session id: %w", err)
	}
	session := &domain.Session{
		ID:           sessionID,
		UserID:       user.ID,
		RoleLevel:    user.RoleLevel,
		Email:        user.Email,
		TokenVersion: user.TokenVersion,
		CreatedAt:    now,
	}

	if err := uc.sessions.Create(ctx, session, sessionTTL); err != nil {
		return nil, fmt.Errorf("login create session: %w", err)
	}

	return &LoginResult{
		Session: session,
		User:    user,
	}, nil
}

// verifyCaptcha checks the captcha. Uses the shared verification from registration.
func (uc *LoginUseCase) verifyCaptcha(ctx context.Context, captchaID, answer string) error {
	if captchaID == "" {
		return domain.ErrCaptchaIncorrect
	}
	storedAnswer, err := uc.captcha.Get(ctx, captchaID)
	if err != nil {
		return fmt.Errorf("get captcha: %w", err)
	}
	if storedAnswer == "" {
		return domain.ErrCaptchaIncorrect
	}
	matched := strings.EqualFold(storedAnswer, strings.TrimSpace(answer))
	// Always delete after use; fail closed if replay prevention cannot be proven.
	if err := uc.captcha.Delete(ctx, captchaID); err != nil {
		return fmt.Errorf("delete captcha: %w", err)
	}
	if !matched {
		return domain.ErrCaptchaIncorrect
	}
	return nil
}
