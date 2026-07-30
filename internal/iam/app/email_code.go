package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/donnel666/remail/internal/iam/domain"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

const (
	emailCodeTTL       = 600 // 10 minutes: how long a delivered code stays valid
	emailCodeResendGap = 60  // seconds: minimum interval between sends to one address
	emailCodeDigitLen  = 6
)

// EmailCodeResendGapSeconds is the per-address resend cooldown, surfaced to
// clients via the Retry-After header.
func EmailCodeResendGapSeconds() int {
	return runtimeconfig.Int("email_code_resend_gap_seconds", emailCodeResendGap, 1)
}

// EmailCodeUseCase handles email verification code creation and delivery.
type EmailCodeUseCase struct {
	store    EmailCodeStore
	delivery mailapp.DeliveryPort
}

// NewEmailCodeUseCase creates an EmailCodeUseCase.
func NewEmailCodeUseCase(store EmailCodeStore, delivery mailapp.DeliveryPort) *EmailCodeUseCase {
	return &EmailCodeUseCase{store: store, delivery: delivery}
}

// Send delivers an email verification code, enforcing a per-address resend
// cooldown. Within the cooldown window it returns ErrEmailCodeThrottled instead
// of silently dropping the mail; once it lapses, a still-valid code is
// re-delivered so a lost first email can be resent.
func (uc *EmailCodeUseCase) Send(ctx context.Context, email string) error {
	_, err := uc.Request(ctx, email)
	return err
}

// Request sends a code and reports whether a new code was generated. The API
// uses that fact to preserve the existing verification-failure budget.
func (uc *EmailCodeUseCase) Request(ctx context.Context, email string) (bool, error) {
	normalized := normalizeEmail(email)
	if err := validateRegistrationEmail(normalized); err != nil {
		return false, err
	}
	return uc.request(ctx, normalized, emailCodeKey(normalized))
}

func (uc *EmailCodeUseCase) RequestLinuxDO(ctx context.Context, email, providerEmail string, mode LinuxDOAccountMode, legacyAccount bool) (bool, error) {
	normalized := normalizeEmail(email)
	if err := validateLinuxDOEmail(normalized, providerEmail, mode); err != nil {
		return false, err
	}
	if mode == LinuxDOAccountExisting && legacyAccount {
		return false, domain.ErrLinuxDOLegacyMergeUnsupported
	}
	if mode == LinuxDOAccountNew && !legacyAccount && !runtimeconfig.Bool("register_enabled", true) {
		return false, domain.ErrRegistrationDisabled
	}
	return uc.request(ctx, normalized, linuxDOEmailCodeKey(normalized))
}

func (uc *EmailCodeUseCase) RequestGitHub(ctx context.Context, email, expectedEmail string) (bool, error) {
	normalized := normalizeEmail(email)
	if normalized != normalizeEmail(expectedEmail) || validateEmailAddress(normalized) != nil {
		return false, domain.ErrInvalidEmailAddress
	}
	return uc.request(ctx, normalized, githubEmailCodeKey(normalized))
}

func (uc *EmailCodeUseCase) request(ctx context.Context, normalized, key string) (bool, error) {
	started, retryAfter, err := uc.store.StartCooldown(ctx, key, EmailCodeResendGapSeconds())
	if err != nil {
		return false, fmt.Errorf("email code cooldown: %w", err)
	}
	if !started {
		return false, &domain.EmailCodeThrottledError{RetryAfterSeconds: retryAfter}
	}

	return uc.deliverKey(ctx, normalized, key)
}

// deliver stores and sends a code without touching the resend cooldown. Callers
// that have already acquired the cooldown (e.g. password reset, which must
// throttle registered and unknown emails identically) use this directly.
func (uc *EmailCodeUseCase) deliver(ctx context.Context, normalizedEmail string) (bool, error) {
	return uc.deliverKey(ctx, normalizedEmail, emailCodeKey(normalizedEmail))
}

func (uc *EmailCodeUseCase) deliverKey(ctx context.Context, normalizedEmail, key string) (bool, error) {
	code, err := generateRandomDigits(runtimeconfig.Int("email_code_digit_len", emailCodeDigitLen, 1))
	if err != nil {
		return false, fmt.Errorf("generate email code: %w", err)
	}

	// Reuse a still-valid code so a resend re-delivers the same digits.
	storedCode, reused, err := uc.store.CreateIfAbsent(ctx, key, code, runtimeconfig.Int("email_code_ttl_seconds", emailCodeTTL, 1))
	if err != nil {
		return false, fmt.Errorf("store email code: %w", err)
	}

	message := mailapp.VerificationCodeMessage(normalizedEmail, storedCode)
	if err := uc.delivery.Send(ctx, message); err != nil {
		// Roll back so the caller can retry immediately: release the cooldown and
		// drop only a code created by this send. A failed resend must not destroy
		// the previously delivered code.
		_ = uc.store.ClearCooldown(ctx, key)
		if !reused {
			if deleteErr := uc.store.Delete(ctx, key); deleteErr != nil {
				return false, fmt.Errorf("send email code: %w; cleanup email code: %v", err, deleteErr)
			}
		}
		return false, fmt.Errorf("send email code: %w", err)
	}
	return !reused, nil
}

func (uc *EmailCodeUseCase) createDummy(ctx context.Context, normalizedEmail string) (bool, error) {
	code, err := generateRandomDigits(runtimeconfig.Int("email_code_digit_len", emailCodeDigitLen, 1))
	if err != nil {
		return false, fmt.Errorf("generate email code: %w", err)
	}
	_, reused, err := uc.store.CreateIfAbsent(ctx, emailCodeKey(normalizedEmail), code, runtimeconfig.Int("email_code_ttl_seconds", emailCodeTTL, 1))
	if err != nil {
		return false, fmt.Errorf("store email code: %w", err)
	}
	return !reused, nil
}

func emailCodeKey(email string) string {
	sum := sha256.Sum256([]byte(email))
	return hex.EncodeToString(sum[:])
}

func linuxDOEmailCodeKey(email string) string {
	return "linuxdo:" + emailCodeKey(email)
}

func githubEmailCodeKey(email string) string {
	return "github:" + emailCodeKey(email)
}
