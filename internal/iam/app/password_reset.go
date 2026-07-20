package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

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

func (uc *PasswordResetUseCase) Request(ctx context.Context, email string) (bool, error) {
	normalized := normalizeEmail(email)

	// Acquire the resend cooldown before the user lookup so a registered and an
	// unknown email throttle identically and existence cannot be probed by
	// comparing responses to a repeated request.
	started, retryAfter, err := uc.codeStore.StartCooldown(ctx, emailCodeKey(normalized), emailCodeResendGap)
	if err != nil {
		return false, fmt.Errorf("password reset cooldown: %w", err)
	}
	if !started {
		return false, &domain.EmailCodeThrottledError{RetryAfterSeconds: retryAfter}
	}

	user, err := uc.repo.FindByEmail(ctx, normalized)
	if err != nil {
		return false, fmt.Errorf("password reset find user: %w", err)
	}
	if user == nil || !user.IsActive() {
		return uc.emailCode.createDummy(ctx, normalized)
	}
	return uc.emailCode.deliver(ctx, normalized)
}

func (uc *PasswordResetUseCase) Reset(ctx context.Context, email, code, newPassword string) error {
	normalized := normalizeEmail(email)
	key := emailCodeKey(normalized)
	code = strings.TrimSpace(code)
	claimToken, err := newCryptoID()
	if err != nil {
		return fmt.Errorf("password reset generate claim: %w", err)
	}
	restore := func(cause error) error {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if _, restoreErr := uc.codeStore.Restore(restoreCtx, key, claimToken, code); restoreErr != nil {
			return fmt.Errorf("restore password reset code after %v: %w", cause, restoreErr)
		}
		return cause
	}
	claimed, err := uc.codeStore.Claim(ctx, key, code, claimToken)
	if err != nil {
		return restore(fmt.Errorf("password reset claim code: %w", err))
	}
	if !claimed {
		return domain.ErrVerificationCodeIncorrect
	}

	user, err := uc.repo.FindByEmail(ctx, normalized)
	if err != nil {
		return restore(fmt.Errorf("password reset find user: %w", err))
	}
	if user == nil || !user.IsActive() {
		return restore(domain.ErrVerificationCodeIncorrect)
	}

	hash, err := uc.hasher.Hash(newPassword)
	if err != nil {
		return restore(fmt.Errorf("password reset hash: %w", err))
	}

	updated, err := uc.repo.UpdatePassword(ctx, user.ID, user.PasswordHash, hash)
	if err != nil {
		return restore(fmt.Errorf("password reset update: %w", err))
	}
	if !updated {
		return restore(domain.ErrVerificationCodeIncorrect)
	}
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if committed, commitErr := uc.codeStore.Commit(commitCtx, key, claimToken); commitErr != nil || !committed {
		slog.Warn("commit password reset code", "error", commitErr, "committed", committed)
	}

	// TokenVersion is the authoritative invalidation fact. Session cleanup is
	// best-effort so a committed password reset is not reported as failed.
	_ = uc.sessions.DeleteByUserID(ctx, user.ID)
	return nil
}
