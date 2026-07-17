package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/donnel666/remail/internal/iam/domain"
)

// PasswordResetUseCase handles password reset through email verification code.
type PasswordResetUseCase struct {
	repo      UserRepository
	hasher    Hasher
	sessions  SessionStore
	codeStore EmailCodeStore
	emailCode *EmailCodeUseCase
}

func NewPasswordResetUseCase(repo UserRepository, hasher Hasher, sessions SessionStore, codeStore EmailCodeStore, emailCode *EmailCodeUseCase) *PasswordResetUseCase {
	return &PasswordResetUseCase{repo: repo, hasher: hasher, sessions: sessions, codeStore: codeStore, emailCode: emailCode}
}

func (uc *PasswordResetUseCase) Request(ctx context.Context, email, captchaID, captchaAnswer string) error {
	if err := uc.emailCode.VerifyCaptcha(ctx, captchaID, captchaAnswer); err != nil {
		return err
	}

	normalized := normalizeEmail(email)

	// Acquire the resend cooldown before the user lookup so a registered and an
	// unknown email throttle identically and existence cannot be probed by
	// comparing responses to a repeated request.
	started, retryAfter, err := uc.codeStore.StartCooldown(ctx, emailCodeKey(normalized), emailCodeResendGap)
	if err != nil {
		return fmt.Errorf("password reset cooldown: %w", err)
	}
	if !started {
		return &domain.EmailCodeThrottledError{RetryAfterSeconds: retryAfter}
	}

	user, err := uc.repo.FindByEmail(ctx, normalized)
	if err != nil {
		return fmt.Errorf("password reset find user: %w", err)
	}
	if user == nil || !user.Enabled {
		return nil
	}
	return uc.emailCode.deliver(ctx, normalized)
}

func (uc *PasswordResetUseCase) Reset(ctx context.Context, email, code, newPassword string) error {
	normalized := normalizeEmail(email)
	storedCode, err := uc.codeStore.Get(ctx, emailCodeKey(normalized))
	if err != nil {
		return fmt.Errorf("password reset get code: %w", err)
	}
	if storedCode == "" || storedCode != strings.TrimSpace(code) {
		return domain.ErrVerificationCodeIncorrect
	}

	user, err := uc.repo.FindByEmail(ctx, normalized)
	if err != nil {
		return fmt.Errorf("password reset find user: %w", err)
	}
	if user == nil || !user.Enabled {
		return domain.ErrVerificationCodeIncorrect
	}

	hash, err := uc.hasher.Hash(newPassword)
	if err != nil {
		return fmt.Errorf("password reset hash: %w", err)
	}

	user.PasswordHash = hash
	user.TokenVersion++
	if err := uc.repo.Update(ctx, user); err != nil {
		return fmt.Errorf("password reset update: %w", err)
	}

	// TokenVersion is the authoritative invalidation fact. Redis cleanup is
	// best-effort so a committed password reset is not reported as failed.
	_ = uc.codeStore.Delete(ctx, emailCodeKey(normalized))
	_ = uc.sessions.DeleteByUserID(ctx, user.ID)
	return nil
}
