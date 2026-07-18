package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/donnel666/remail/internal/iam/domain"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
)

const (
	emailCodeTTL       = 600 // 10 minutes: how long a delivered code stays valid
	emailCodeResendGap = 60  // seconds: minimum interval between sends to one address
	emailCodeDigitLen  = 6
)

// EmailCodeResendGapSeconds is the per-address resend cooldown, surfaced to
// clients via the Retry-After header when a request is throttled.
const EmailCodeResendGapSeconds = emailCodeResendGap

// EmailCodeUseCase handles email verification code creation and delivery.
type EmailCodeUseCase struct {
	store    EmailCodeStore
	delivery mailapp.DeliveryPort
	captcha  CaptchaStore
}

// NewEmailCodeUseCase creates an EmailCodeUseCase.
func NewEmailCodeUseCase(store EmailCodeStore, delivery mailapp.DeliveryPort, captcha CaptchaStore) *EmailCodeUseCase {
	return &EmailCodeUseCase{store: store, delivery: delivery, captcha: captcha}
}

// VerifyCaptcha validates the image captcha attached to an email-code request.
func (uc *EmailCodeUseCase) VerifyCaptcha(ctx context.Context, captchaID, captchaAnswer string) error {
	return VerifyCaptcha(ctx, uc.captcha, captchaID, captchaAnswer)
}

// SendWithCaptcha validates the image captcha before sending an email code.
func (uc *EmailCodeUseCase) SendWithCaptcha(ctx context.Context, email, captchaID, captchaAnswer string) (bool, error) {
	if err := uc.VerifyCaptcha(ctx, captchaID, captchaAnswer); err != nil {
		return false, err
	}
	return uc.send(ctx, email)
}

// Send delivers an email verification code, enforcing a per-address resend
// cooldown. Within the cooldown window it returns ErrEmailCodeThrottled instead
// of silently dropping the mail; once it lapses, a still-valid code is
// re-delivered so a lost first email can be resent.
func (uc *EmailCodeUseCase) Send(ctx context.Context, email string) error {
	_, err := uc.send(ctx, email)
	return err
}

func (uc *EmailCodeUseCase) send(ctx context.Context, email string) (bool, error) {
	normalized := normalizeEmail(email)

	started, retryAfter, err := uc.store.StartCooldown(ctx, emailCodeKey(normalized), emailCodeResendGap)
	if err != nil {
		return false, fmt.Errorf("email code cooldown: %w", err)
	}
	if !started {
		return false, &domain.EmailCodeThrottledError{RetryAfterSeconds: retryAfter}
	}

	return uc.deliver(ctx, normalized)
}

// deliver stores and sends a code without touching the resend cooldown. Callers
// that have already acquired the cooldown (e.g. password reset, which must
// throttle registered and unknown emails identically) use this directly.
func (uc *EmailCodeUseCase) deliver(ctx context.Context, normalizedEmail string) (bool, error) {
	code, err := generateRandomDigits(emailCodeDigitLen)
	if err != nil {
		return false, fmt.Errorf("generate email code: %w", err)
	}

	key := emailCodeKey(normalizedEmail)
	// Reuse a still-valid code so a resend re-delivers the same digits.
	storedCode, reused, err := uc.store.CreateIfAbsent(ctx, key, code, emailCodeTTL)
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
	code, err := generateRandomDigits(emailCodeDigitLen)
	if err != nil {
		return false, fmt.Errorf("generate email code: %w", err)
	}
	_, reused, err := uc.store.CreateIfAbsent(ctx, emailCodeKey(normalizedEmail), code, emailCodeTTL)
	if err != nil {
		return false, fmt.Errorf("store email code: %w", err)
	}
	return !reused, nil
}

func emailCodeKey(email string) string {
	sum := sha256.Sum256([]byte(email))
	return hex.EncodeToString(sum[:])
}
