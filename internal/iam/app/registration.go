package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/donnel666/remail/internal/iam/domain"
)

// RegistrationUseCase handles user self-registration.
type RegistrationUseCase struct {
	repo      UserRepository
	hasher    Hasher
	codeStore EmailCodeStore
}

// NewRegistrationUseCase creates a new RegistrationUseCase.
func NewRegistrationUseCase(repo UserRepository, hasher Hasher, codeStore EmailCodeStore) *RegistrationUseCase {
	return &RegistrationUseCase{repo: repo, hasher: hasher, codeStore: codeStore}
}

// Register creates a new user with the "user" RBAC role and default user group.
// It requires a valid email verification code for the submitted email.
func (uc *RegistrationUseCase) Register(ctx context.Context, email, password, nickname, code, inviteCode string) (*domain.User, error) {
	normalizedEmail := normalizeEmail(email)

	if err := uc.verifyEmailCode(ctx, normalizedEmail, code); err != nil {
		return nil, err
	}

	// Check email uniqueness
	existing, err := uc.repo.FindByEmail(ctx, normalizedEmail)
	if err != nil {
		return nil, fmt.Errorf("register check email: %w", err)
	}
	if existing != nil {
		return nil, domain.ErrEmailAlreadyExists
	}

	hash, err := uc.hasher.Hash(password)
	if err != nil {
		return nil, fmt.Errorf("register hash: %w", err)
	}

	user := &domain.User{
		Email:        normalizedEmail,
		PasswordHash: hash,
		Nickname:     strings.TrimSpace(nickname),
		Enabled:      true,
		Role:         domain.RoleUser,
		UserGroupID:  1,
		TokenVersion: 0,
	}

	if strings.TrimSpace(inviteCode) != "" {
		if err := uc.repo.CreateWithInvite(ctx, user, strings.TrimSpace(inviteCode)); err != nil {
			return nil, err
		}
	} else if err := uc.repo.Create(ctx, user); err != nil {
		return nil, err
	}

	_ = uc.codeStore.Delete(ctx, emailCodeKey(normalizedEmail))

	return user, nil
}

func (uc *RegistrationUseCase) verifyEmailCode(ctx context.Context, email, code string) error {
	storedCode, err := uc.codeStore.Get(ctx, emailCodeKey(email))
	if err != nil {
		return fmt.Errorf("register get email code: %w", err)
	}
	if storedCode == "" || storedCode != strings.TrimSpace(code) {
		return domain.ErrVerificationCodeIncorrect
	}

	return nil
}
