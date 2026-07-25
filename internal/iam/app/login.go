package app

import (
	"context"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

// LoginUseCase handles user authentication.
type LoginUseCase struct {
	repo     UserRepository
	hasher   Hasher
	sessions SessionStore
}

// NewLoginUseCase creates a new LoginUseCase.
func NewLoginUseCase(repo UserRepository, hasher Hasher, sessions SessionStore) *LoginUseCase {
	return &LoginUseCase{repo: repo, hasher: hasher, sessions: sessions}
}

// LoginResult contains the outcome of a successful login.
type LoginResult struct {
	Session       *domain.Session
	User          *domain.User
	SessionMaxAge int
}

// Login authenticates a user by email and password after the API boundary has
// validated the request's Turnstile token.
// Disabled accounts return the same error to prevent account enumeration
// (docs/8-iam.md:109 — only "Account or password is incorrect" is safe to expose).
func (uc *LoginUseCase) Login(ctx context.Context, email, password string) (*LoginResult, error) {
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

	// Create session
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

	return &LoginResult{
		Session:       session,
		User:          user,
		SessionMaxAge: sessionMaxAge,
	}, nil
}
