package app

import (
	"context"
	"fmt"

	"github.com/donnel666/remail/internal/iam/domain"
)

// ActivationUseCase handles the first-activation flow (INV-I8).
type ActivationUseCase struct {
	repo   UserRepository
	hasher Hasher
}

// NewActivationUseCase creates a new ActivationUseCase.
func NewActivationUseCase(repo UserRepository, hasher Hasher) *ActivationUseCase {
	return &ActivationUseCase{repo: repo, hasher: hasher}
}

// ActivationStatus represents whether the system needs first activation.
type ActivationStatus struct {
	Needed bool `json:"needed"`
}

// Check returns whether the system needs first activation.
func (uc *ActivationUseCase) Check(ctx context.Context) (*ActivationStatus, error) {
	count, err := uc.repo.Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("check activation: %w", err)
	}
	return &ActivationStatus{Needed: domain.IsActivationNeeded(count)}, nil
}

// Activate creates the first super_admin user.
// Uses a DB-level serialized transaction to guarantee only one super_admin
// is created even under concurrent requests (docs/8-iam.md:88, INV-I8).
// Returns ErrActivationAlreadyDone if any user already exists.
func (uc *ActivationUseCase) Activate(ctx context.Context, email, password, nickname string) (*domain.User, error) {
	hash, err := uc.hasher.Hash(password)
	if err != nil {
		return nil, fmt.Errorf("activate hash: %w", err)
	}

	user := &domain.User{
		Email:        email,
		PasswordHash: hash,
		Nickname:     nickname,
		Enabled:      true,
		RoleLevel:    domain.RoleSuperAdmin,
		TokenVersion: 0,
	}

	// CreateFirstUser runs inside a GORM transaction with FOR UPDATE,
	// guaranteeing serialized access for the first-activation invariant.
	if err := uc.repo.CreateFirstUser(ctx, user); err != nil {
		return nil, err
	}

	return user, nil
}
