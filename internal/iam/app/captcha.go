package app

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/iam/infra"
)

const (
	captchaTTL = 300 // 5 minutes
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
	expression, err := generateCaptchaExpression()
	if err != nil {
		return nil, fmt.Errorf("generate captcha expression: %w", err)
	}

	captchaID, err := newCryptoID()
	if err != nil {
		return nil, fmt.Errorf("generate captcha id: %w", err)
	}

	if err := uc.store.Create(ctx, captchaID, strconv.Itoa(expression.answer), captchaTTL); err != nil {
		return nil, fmt.Errorf("create captcha store: %w", err)
	}

	image, err := infra.GenerateCaptchaImage(expression.question + "=?")
	if err != nil {
		return nil, fmt.Errorf("create captcha image: %w", err)
	}

	return &CaptchaResult{
		CaptchaID: captchaID,
		Image:     image,
	}, nil
}

// VerifyCaptcha validates a captcha answer and deletes the challenge after use.
// Deleting on both match and mismatch prevents replay of a known captcha ID.
func VerifyCaptcha(ctx context.Context, store CaptchaStore, captchaID, answer string) error {
	if captchaID == "" {
		return domain.ErrCaptchaIncorrect
	}

	storedAnswer, err := store.GetDel(ctx, captchaID)
	if err != nil {
		return fmt.Errorf("get captcha: %w", err)
	}
	if storedAnswer == "" {
		return domain.ErrCaptchaIncorrect
	}

	matched := strings.EqualFold(storedAnswer, strings.TrimSpace(answer))
	if !matched {
		return domain.ErrCaptchaIncorrect
	}

	return nil
}

type captchaExpression struct {
	question string
	answer   int
}

func generateCaptchaExpression() (captchaExpression, error) {
	operator, err := cryptoRandInt(4)
	if err != nil {
		return captchaExpression{}, err
	}

	switch operator {
	case 0:
		return additionExpression()
	case 1:
		return subtractionExpression()
	case 2:
		return multiplicationExpression()
	default:
		return divisionExpression()
	}
}

func additionExpression() (captchaExpression, error) {
	left, err := cryptoRandRange(1, 9)
	if err != nil {
		return captchaExpression{}, err
	}
	right, err := cryptoRandRange(1, 9)
	if err != nil {
		return captchaExpression{}, err
	}
	return captchaExpression{question: fmt.Sprintf("%d+%d", left, right), answer: left + right}, nil
}

func subtractionExpression() (captchaExpression, error) {
	left, err := cryptoRandRange(1, 9)
	if err != nil {
		return captchaExpression{}, err
	}
	right, err := cryptoRandRange(1, left)
	if err != nil {
		return captchaExpression{}, err
	}
	return captchaExpression{question: fmt.Sprintf("%d−%d", left, right), answer: left - right}, nil
}

func multiplicationExpression() (captchaExpression, error) {
	left, err := cryptoRandRange(1, 9)
	if err != nil {
		return captchaExpression{}, err
	}
	right, err := cryptoRandRange(1, 9)
	if err != nil {
		return captchaExpression{}, err
	}
	return captchaExpression{question: fmt.Sprintf("%d×%d", left, right), answer: left * right}, nil
}

func divisionExpression() (captchaExpression, error) {
	divisor, err := cryptoRandRange(1, 9)
	if err != nil {
		return captchaExpression{}, err
	}
	answer, err := cryptoRandRange(1, 9)
	if err != nil {
		return captchaExpression{}, err
	}
	dividend := divisor * answer
	return captchaExpression{question: fmt.Sprintf("%d÷%d", dividend, divisor), answer: answer}, nil
}

func cryptoRandRange(minValue, maxValue int) (int, error) {
	if maxValue < minValue {
		return 0, fmt.Errorf("invalid random range %d..%d", minValue, maxValue)
	}
	value, err := cryptoRandInt(maxValue - minValue + 1)
	if err != nil {
		return 0, err
	}
	return minValue + value, nil
}

func cryptoRandInt(maxExclusive int) (int, error) {
	if maxExclusive <= 0 {
		return 0, fmt.Errorf("invalid random max %d", maxExclusive)
	}
	num, err := rand.Int(rand.Reader, big.NewInt(int64(maxExclusive)))
	if err != nil {
		return 0, fmt.Errorf("crypto/rand: %w", err)
	}
	return int(num.Int64()), nil
}
