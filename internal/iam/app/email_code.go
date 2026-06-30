package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

const (
	emailCodeTTL      = 600 // 10 minutes
	emailCodeDigitLen = 6
)

// EmailCodeUseCase handles email verification code creation and delivery.
type EmailCodeUseCase struct {
	store   EmailCodeStore
	sender  EmailCodeSender
	captcha CaptchaStore
}

// NewEmailCodeUseCase creates an EmailCodeUseCase.
func NewEmailCodeUseCase(store EmailCodeStore, sender EmailCodeSender, captcha CaptchaStore) *EmailCodeUseCase {
	return &EmailCodeUseCase{store: store, sender: sender, captcha: captcha}
}

// VerifyCaptcha validates the image captcha attached to an email-code request.
func (uc *EmailCodeUseCase) VerifyCaptcha(ctx context.Context, captchaID, captchaAnswer string) error {
	return VerifyCaptcha(ctx, uc.captcha, captchaID, captchaAnswer)
}

// SendWithCaptcha validates the image captcha before sending an email code.
func (uc *EmailCodeUseCase) SendWithCaptcha(ctx context.Context, email, captchaID, captchaAnswer string) error {
	if err := uc.VerifyCaptcha(ctx, captchaID, captchaAnswer); err != nil {
		return err
	}
	return uc.Send(ctx, email)
}

// Send creates or reuses an unexpired email verification code and sends it.
func (uc *EmailCodeUseCase) Send(ctx context.Context, email string) error {
	normalized := normalizeEmail(email)
	code, err := generateRandomDigits(emailCodeDigitLen)
	if err != nil {
		return fmt.Errorf("generate email code: %w", err)
	}

	key := emailCodeKey(normalized)
	storedCode, reused, err := uc.store.CreateIfAbsent(ctx, key, code, emailCodeTTL)
	if err != nil {
		return fmt.Errorf("store email code: %w", err)
	}
	if reused {
		return nil
	}

	if err := uc.sender.SendEmailCode(ctx, normalized, storedCode); err != nil {
		if deleteErr := uc.store.Delete(ctx, key); deleteErr != nil {
			return fmt.Errorf("send email code: %w; cleanup email code: %v", err, deleteErr)
		}
		return fmt.Errorf("send email code: %w", err)
	}
	return nil
}

func emailCodeKey(email string) string {
	sum := sha256.Sum256([]byte(email))
	return hex.EncodeToString(sum[:])
}
