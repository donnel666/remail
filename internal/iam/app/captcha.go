package app

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"

	"github.com/donnel666/remail/internal/iam/infra"
)

const (
	captchaTTL      = 300 // 5 minutes
	captchaDigitLen = 4
)

// CaptchaUseCase handles captcha creation.
type CaptchaUseCase struct {
	store CaptchaStore
}

// NewCaptchaUseCase creates a new CaptchaUseCase.
func NewCaptchaUseCase(store CaptchaStore) *CaptchaUseCase {
	return &CaptchaUseCase{store: store}
}

// CaptchaResult contains the captcha ID and base64 image.
type CaptchaResult struct {
	CaptchaID string `json:"captchaId"`
	Image     string `json:"image"`
}

// Create generates a new captcha, stores the answer in Redis, and returns
// the captcha ID and a base64-encoded PNG image.
func (uc *CaptchaUseCase) Create(ctx context.Context) (*CaptchaResult, error) {
	// Generate random digits using crypto/rand
	digits, err := generateRandomDigits(captchaDigitLen)
	if err != nil {
		return nil, fmt.Errorf("generate captcha digits: %w", err)
	}

	// Generate captcha ID
	captchaID, err := newCryptoID()
	if err != nil {
		return nil, fmt.Errorf("generate captcha id: %w", err)
	}

	// Store in Redis
	if err := uc.store.Create(ctx, captchaID, digits, captchaTTL); err != nil {
		return nil, fmt.Errorf("create captcha store: %w", err)
	}

	// Generate image
	image, err := infra.GenerateCaptchaImage(digits)
	if err != nil {
		return nil, fmt.Errorf("create captcha image: %w", err)
	}

	return &CaptchaResult{
		CaptchaID: captchaID,
		Image:     image,
	}, nil
}

// generateRandomDigits returns a string of n random digits using crypto/rand.
func generateRandomDigits(n int) (string, error) {
	result := make([]byte, n)
	for i := range result {
		num, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", fmt.Errorf("crypto/rand: %w", err)
		}
		result[i] = byte('0') + byte(num.Int64())
	}
	return string(result), nil
}
