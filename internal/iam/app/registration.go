package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

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
	if err := validateRegistrationEmail(normalizedEmail); err != nil {
		return nil, err
	}
	key := emailCodeKey(normalizedEmail)
	code = strings.TrimSpace(code)
	claimToken, err := newCryptoID()
	if err != nil {
		return nil, fmt.Errorf("register generate claim: %w", err)
	}
	restore := func(cause error) error {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if _, restoreErr := uc.codeStore.Restore(restoreCtx, key, claimToken, code); restoreErr != nil {
			return fmt.Errorf("restore registration code after %v: %w", cause, restoreErr)
		}
		return cause
	}
	claimed, err := uc.codeStore.Claim(ctx, key, code, claimToken)
	if err != nil {
		return nil, restore(fmt.Errorf("register claim email code: %w", err))
	}
	if !claimed {
		return nil, domain.ErrVerificationCodeIncorrect
	}

	// Check email uniqueness
	existing, err := uc.repo.FindByEmail(ctx, normalizedEmail)
	if err != nil {
		return nil, restore(fmt.Errorf("register check email: %w", err))
	}
	if existing != nil {
		return nil, restore(domain.ErrVerificationCodeIncorrect)
	}

	hash, err := uc.hasher.Hash(password)
	if err != nil {
		return nil, restore(fmt.Errorf("register hash: %w", err))
	}

	user := &domain.User{
		Email:        normalizedEmail,
		PasswordHash: hash,
		Nickname:     strings.TrimSpace(nickname),
		Status:       domain.UserStatusActive,
		Role:         domain.RoleUser,
		UserGroupID:  1,
		TokenVersion: 0,
	}

	if strings.TrimSpace(inviteCode) != "" {
		if err := uc.repo.CreateWithInvite(ctx, user, strings.TrimSpace(inviteCode)); err != nil {
			if errors.Is(err, domain.ErrEmailAlreadyExists) {
				err = domain.ErrVerificationCodeIncorrect
			}
			return nil, restore(err)
		}
	} else if err := uc.repo.Create(ctx, user); err != nil {
		if errors.Is(err, domain.ErrEmailAlreadyExists) {
			err = domain.ErrVerificationCodeIncorrect
		}
		return nil, restore(err)
	}

	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if committed, commitErr := uc.codeStore.Commit(commitCtx, key, claimToken); commitErr != nil || !committed {
		slog.Warn("commit registration code", "error", commitErr, "committed", committed)
	}

	return user, nil
}
