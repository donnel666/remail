package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	emailCodeTTL      = 600 // 10 minutes
	emailCodeDigitLen = 6
)

// EmailCodeUseCase handles email verification code creation and delivery.
type EmailCodeUseCase struct {
	store  EmailCodeStore
	sender EmailCodeSender
}

// NewEmailCodeUseCase creates an EmailCodeUseCase.
func NewEmailCodeUseCase(store EmailCodeStore, sender EmailCodeSender) *EmailCodeUseCase {
	return &EmailCodeUseCase{store: store, sender: sender}
}

// Send creates or reuses an unexpired email verification code and sends it.
func (uc *EmailCodeUseCase) Send(ctx context.Context, email string) error {
	normalized := strings.ToLower(strings.TrimSpace(email))
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
