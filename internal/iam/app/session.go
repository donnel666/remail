package app

import (
	"context"
	"fmt"

	"github.com/donnel666/remail/internal/iam/domain"
)

// SessionUseCase handles session queries and logout.
type SessionUseCase struct {
	sessions SessionStore
	repo     UserRepository
}

// NewSessionUseCase creates a new SessionUseCase.
func NewSessionUseCase(sessions SessionStore, repo UserRepository) *SessionUseCase {
	return &SessionUseCase{sessions: sessions, repo: repo}
}

// GetCurrent returns the user associated with a session.
// Verifies that the session's TokenVersion matches the current user's TokenVersion
// (INV-I3: password change / disable invalidates all sessions by bumping TokenVersion).
// Returns nil, nil if the session is invalid or expired.
func (uc *SessionUseCase) GetCurrent(ctx context.Context, sessionID string) (*domain.User, error) {
	if sessionID == "" {
		return nil, nil
	}

	sess, err := uc.sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	if sess == nil {
		return nil, nil
	}

	// Fetch fresh user data
	user, err := uc.repo.FindByID(ctx, sess.UserID)
	if err != nil {
		return nil, fmt.Errorf("get current user: %w", err)
	}
	if user == nil {
		return nil, nil
	}
	if !user.Enabled {
		return nil, nil
	}

	// TokenVersion check: if the user's TokenVersion has been bumped
	// (via password change, disable, or force logout), this session is stale.
	if user.TokenVersion != sess.TokenVersion {
		return nil, nil
	}

	return user, nil
}

// Logout deletes the current session.
func (uc *SessionUseCase) Logout(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	return uc.sessions.Delete(ctx, sessionID)
}
