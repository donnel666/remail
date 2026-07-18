package app

import (
	"context"
	"fmt"

	"github.com/donnel666/remail/internal/iam/domain"
)

// ChangePasswordUseCase handles password changes.
// On success, increments tokenVersion and deletes all sessions for the user (INV-I3).
type ChangePasswordUseCase struct {
	repo     UserRepository
	hasher   Hasher
	sessions SessionStore
}

// NewChangePasswordUseCase creates a new ChangePasswordUseCase.
func NewChangePasswordUseCase(repo UserRepository, hasher Hasher, sessions SessionStore) *ChangePasswordUseCase {
	return &ChangePasswordUseCase{repo: repo, hasher: hasher, sessions: sessions}
}

// Change verifies the old password, updates to the new password,
// increments tokenVersion, and deletes all existing sessions.
func (uc *ChangePasswordUseCase) Change(ctx context.Context, userID uint, oldPassword, newPassword string) error {
	user, err := uc.repo.FindByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("change password find user: %w", err)
	}
	if user == nil {
		return domain.ErrAuthenticationRequired
	}

	// Verify old password
	if !uc.hasher.Verify(oldPassword, user.PasswordHash) {
		return domain.ErrInvalidPassword
	}

	// Hash new password
	hash, err := uc.hasher.Hash(newPassword)
	if err != nil {
		return fmt.Errorf("change password hash: %w", err)
	}

	updated, err := uc.repo.UpdatePassword(ctx, user.ID, user.PasswordHash, hash)
	if err != nil {
		return fmt.Errorf("change password update: %w", err)
	}
	if !updated {
		return domain.ErrInvalidPassword
	}

	// TokenVersion is the authoritative invalidation fact. Redis cleanup is
	// best-effort so a committed password change is not reported as failed.
	_ = uc.sessions.DeleteByUserID(ctx, userID)

	return nil
}
