package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/donnel666/remail/internal/iam/domain"
)

// RegistrationUseCase handles user self-registration.
type RegistrationUseCase struct {
	repo    UserRepository
	hasher  Hasher
	captcha CaptchaStore
}

// NewRegistrationUseCase creates a new RegistrationUseCase.
func NewRegistrationUseCase(repo UserRepository, hasher Hasher, captcha CaptchaStore) *RegistrationUseCase {
	return &RegistrationUseCase{repo: repo, hasher: hasher, captcha: captcha}
}

// Register creates a new user with the "user" role (level 10).
// It requires a valid captcha to prevent automated registration.
func (uc *RegistrationUseCase) Register(ctx context.Context, email, password, nickname, captchaID, captchaAnswer, inviteCode string) (*domain.User, error) {
	// Validate captcha
	if err := uc.verifyCaptcha(ctx, captchaID, captchaAnswer); err != nil {
		return nil, err
	}

	// Check email uniqueness
	existing, err := uc.repo.FindByEmail(ctx, email)
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
		Email:        email,
		PasswordHash: hash,
		Nickname:     nickname,
		Enabled:      true,
		RoleLevel:    domain.RoleUser,
		TokenVersion: 0,
	}

	if strings.TrimSpace(inviteCode) != "" {
		if err := uc.repo.CreateWithInvite(ctx, user, strings.TrimSpace(inviteCode)); err != nil {
			return nil, err
		}
	} else if err := uc.repo.Create(ctx, user); err != nil {
		return nil, err
	}

	return user, nil
}

func (uc *RegistrationUseCase) verifyCaptcha(ctx context.Context, captchaID, answer string) error {
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
