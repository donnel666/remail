package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/openapi/domain"
)

const (
	apiKeyPrefix             = "ak_"
	orderTokenPrefix         = "st_"
	defaultAPIKeyConcurrency = 5
	maxRateLimitPerMinute    = 10000
	maxAPIKeyConcurrency     = 100
)

type Repository interface {
	CreateAPIKey(ctx context.Context, cmd CreateAPIKeyCommand) (*domain.APIKey, bool, error)
	ListAPIKeys(ctx context.Context, userID uint, offset, limit int) ([]domain.APIKey, int64, error)
	FindAPIKey(ctx context.Context, userID uint, keyID uint) (*domain.APIKey, error)
	UpdateAPIKey(ctx context.Context, cmd UpdateAPIKeyCommand) (*domain.APIKey, error)
	DeleteAPIKey(ctx context.Context, userID uint, keyID uint, deletedAt time.Time) error
	AcquireAPIKeyRequest(ctx context.Context, plain string, now time.Time) (*domain.APIKey, error)
	ReleaseAPIKeyRequest(ctx context.Context, keyID uint) error

	IssueOrderToken(ctx context.Context, cmd IssueOrderTokenCommand) (*domain.OrderToken, error)
	FindOrderTokenByOrder(ctx context.Context, orderNo string) (*domain.OrderToken, error)
	FindOrderTokenByPlain(ctx context.Context, tokenPlain string) (*domain.OrderToken, error)
	ExtendOrderToken(ctx context.Context, orderNo string, expireAt time.Time) error
	DisableOrderToken(ctx context.Context, orderNo string, reason string, disabledAt time.Time) error

	CreateAPILog(ctx context.Context, cmd CreateAPILogCommand) error
}

type CreateAPIKeyRequest struct {
	UserID             uint
	Name               string
	ExpireAt           *time.Time
	RateLimitPerMinute *int
	ConcurrencyLimit   int
	QuotaLimit         *int64
	IdempotencyKey     string
	RequestID          string
}

type CreateAPIKeyCommand struct {
	UserID             uint
	Name               string
	KeyPlain           string
	KeyPrefix          string
	ExpireAt           *time.Time
	RateLimitPerMinute *int
	ConcurrencyLimit   int
	QuotaLimit         *int64
	IdempotencyKey     string
	RequestFingerprint string
	RequestID          string
	Now                time.Time
}

type UpdateAPIKeyRequest struct {
	UserID             uint
	KeyID              uint
	Name               *string
	Enabled            *bool
	ExpireAt           *time.Time
	ExpireSet          bool
	RateLimitPerMinute *int
	RateLimitSet       bool
	ConcurrencyLimit   *int
	QuotaLimit         *int64
	QuotaSet           bool
}

type UpdateAPIKeyCommand struct {
	UserID             uint
	KeyID              uint
	Name               *string
	Enabled            *bool
	ExpireAt           *time.Time
	ExpireSet          bool
	RateLimitPerMinute *int
	RateLimitSet       bool
	ConcurrencyLimit   *int
	QuotaLimit         *int64
	QuotaSet           bool
}

type APIKeyAuthResult struct {
	UserID   uint
	APIKeyID uint
}

type CreateAPILogCommand struct {
	PrincipalType  string
	PrincipalID    uint
	UserID         uint
	Path           string
	Method         string
	IdempotencyKey string
	HTTPStatus     int
	DurationMs     int
	RequestID      string
	Now            time.Time
}

type LogAPIRequestRequest struct {
	PrincipalType  string
	PrincipalID    uint
	UserID         uint
	Path           string
	Method         string
	IdempotencyKey string
	HTTPStatus     int
	DurationMs     int
	RequestID      string
}

type IssueOrderTokenCommand struct {
	OrderNo     string
	TokenPlain  string
	TokenPrefix string
	ExpireAt    *time.Time
	Now         time.Time
}

type UseCase struct {
	repo Repository
	now  func() time.Time
}

func NewUseCase(repo Repository) *UseCase {
	return &UseCase{
		repo: repo,
		now:  func() time.Time { return time.Now().UTC() },
	}
}

func (uc *UseCase) CreateAPIKey(ctx context.Context, req CreateAPIKeyRequest) (*domain.APIKey, error) {
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if req.UserID == 0 {
		return nil, domain.ErrInvalidAPIKey
	}
	if idempotencyKey == "" {
		return nil, domain.ErrIdempotencyRequired
	}
	rateLimit, concurrency, err := normalizeAPIKeyLimits(req.RateLimitPerMinute, req.ConcurrencyLimit)
	if err != nil {
		return nil, err
	}
	plain := nextCredential(apiKeyPrefix)
	keyPrefix := credentialPrefix(plain)
	name := domain.NormalizeAPIKeyName(req.Name)
	if req.QuotaLimit != nil && *req.QuotaLimit <= 0 {
		return nil, domain.ErrInvalidAPIKey
	}
	fingerprint := fingerprint("apikey.create", req.UserID, name, timeFingerprint(req.ExpireAt), intFingerprint(rateLimit), concurrency, int64Fingerprint(req.QuotaLimit))
	key, _, err := uc.repo.CreateAPIKey(ctx, CreateAPIKeyCommand{
		UserID:             req.UserID,
		Name:               name,
		KeyPlain:           plain,
		KeyPrefix:          keyPrefix,
		ExpireAt:           req.ExpireAt,
		RateLimitPerMinute: rateLimit,
		ConcurrencyLimit:   concurrency,
		QuotaLimit:         req.QuotaLimit,
		IdempotencyKey:     idempotencyKey,
		RequestFingerprint: fingerprint,
		RequestID:          strings.TrimSpace(req.RequestID),
		Now:                uc.now(),
	})
	return key, err
}

func (uc *UseCase) ListAPIKeys(ctx context.Context, userID uint, offset, limit int) ([]domain.APIKey, int64, error) {
	if userID == 0 {
		return nil, 0, domain.ErrInvalidCredentialFilter
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	return uc.repo.ListAPIKeys(ctx, userID, offset, limit)
}

func (uc *UseCase) GetAPIKey(ctx context.Context, userID uint, keyID uint) (*domain.APIKey, error) {
	if userID == 0 || keyID == 0 {
		return nil, domain.ErrInvalidAPIKey
	}
	return uc.repo.FindAPIKey(ctx, userID, keyID)
}

func (uc *UseCase) UpdateAPIKey(ctx context.Context, req UpdateAPIKeyRequest) (*domain.APIKey, error) {
	if req.UserID == 0 || req.KeyID == 0 {
		return nil, domain.ErrInvalidAPIKey
	}
	if req.Name != nil {
		normalized := domain.NormalizeAPIKeyName(*req.Name)
		req.Name = &normalized
	}
	if req.RateLimitSet && req.RateLimitPerMinute != nil && !validRateLimitPerMinute(*req.RateLimitPerMinute) {
		return nil, domain.ErrInvalidAPIKey
	}
	if req.ConcurrencyLimit != nil && !validAPIKeyConcurrency(*req.ConcurrencyLimit) {
		return nil, domain.ErrInvalidAPIKey
	}
	if req.QuotaSet && req.QuotaLimit != nil && *req.QuotaLimit <= 0 {
		return nil, domain.ErrInvalidAPIKey
	}
	return uc.repo.UpdateAPIKey(ctx, UpdateAPIKeyCommand(req))
}

func (uc *UseCase) DeleteAPIKey(ctx context.Context, userID uint, keyID uint) error {
	if userID == 0 || keyID == 0 {
		return domain.ErrInvalidAPIKey
	}
	return uc.repo.DeleteAPIKey(ctx, userID, keyID, uc.now())
}

func (uc *UseCase) BeginAPIKeyRequest(ctx context.Context, plain string) (*APIKeyAuthResult, error) {
	plain = strings.TrimSpace(plain)
	if !strings.HasPrefix(plain, apiKeyPrefix) {
		return nil, domain.ErrInvalidAPIKey
	}
	key, err := uc.repo.AcquireAPIKeyRequest(ctx, plain, uc.now())
	if err != nil {
		return nil, err
	}
	return &APIKeyAuthResult{UserID: key.UserID, APIKeyID: key.ID}, nil
}

func (uc *UseCase) FinishAPIKeyRequest(ctx context.Context, keyID uint) error {
	if keyID == 0 {
		return nil
	}
	return uc.repo.ReleaseAPIKeyRequest(ctx, keyID)
}

func (uc *UseCase) IssueOrderToken(ctx context.Context, orderNo string, expireAt *time.Time) (*domain.OrderToken, error) {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return nil, domain.ErrInvalidOrderToken
	}
	plain := nextCredential(orderTokenPrefix)
	return uc.repo.IssueOrderToken(ctx, IssueOrderTokenCommand{
		OrderNo:     orderNo,
		TokenPlain:  plain,
		TokenPrefix: credentialPrefix(plain),
		ExpireAt:    expireAt,
		Now:         uc.now(),
	})
}

func (uc *UseCase) FindOrderTokenByOrder(ctx context.Context, orderNo string) (*domain.OrderToken, error) {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return nil, domain.ErrInvalidOrderToken
	}
	return uc.repo.FindOrderTokenByOrder(ctx, orderNo)
}

func (uc *UseCase) FindOrderTokenByPlain(ctx context.Context, tokenPlain string) (*domain.OrderToken, error) {
	tokenPlain = strings.TrimSpace(tokenPlain)
	if tokenPlain == "" || !strings.HasPrefix(tokenPlain, orderTokenPrefix) {
		return nil, domain.ErrInvalidOrderToken
	}
	token, err := uc.repo.FindOrderTokenByPlain(ctx, tokenPlain)
	if err != nil {
		return nil, err
	}
	if token == nil {
		return nil, domain.ErrInvalidOrderToken
	}
	now := uc.now()
	if !token.Enabled {
		return nil, domain.ErrOrderTokenDisabled
	}
	if token.ExpireAt != nil && !token.ExpireAt.After(now) {
		return nil, domain.ErrOrderTokenExpired
	}
	return token, nil
}

func (uc *UseCase) DisableOrderToken(ctx context.Context, orderNo string, reason string) error {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return domain.ErrInvalidOrderToken
	}
	return uc.repo.DisableOrderToken(ctx, orderNo, strings.TrimSpace(reason), uc.now())
}

func (uc *UseCase) ExtendOrderToken(ctx context.Context, orderNo string, expireAt time.Time) error {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" || expireAt.IsZero() {
		return domain.ErrInvalidOrderToken
	}
	return uc.repo.ExtendOrderToken(ctx, orderNo, expireAt.UTC())
}

func (uc *UseCase) LogAPIRequest(ctx context.Context, req LogAPIRequestRequest) error {
	if req.PrincipalID == 0 || strings.TrimSpace(req.PrincipalType) == "" {
		return domain.ErrInvalidAPIKey
	}
	if req.HTTPStatus < 100 || req.HTTPStatus > 599 || req.DurationMs < 0 {
		return domain.ErrInvalidAPIKey
	}
	path := truncate(strings.TrimSpace(req.Path), 255)
	method := truncate(strings.ToUpper(strings.TrimSpace(req.Method)), 16)
	if path == "" || method == "" {
		return domain.ErrInvalidAPIKey
	}
	return uc.repo.CreateAPILog(ctx, CreateAPILogCommand{
		PrincipalType:  truncate(strings.TrimSpace(req.PrincipalType), 32),
		PrincipalID:    req.PrincipalID,
		UserID:         req.UserID,
		Path:           path,
		Method:         method,
		IdempotencyKey: truncate(strings.TrimSpace(req.IdempotencyKey), 128),
		HTTPStatus:     req.HTTPStatus,
		DurationMs:     req.DurationMs,
		RequestID:      truncate(strings.TrimSpace(req.RequestID), 64),
		Now:            uc.now(),
	})
}

func nextCredential(prefix string) string {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(fmt.Sprintf("credential entropy unavailable: %v", err))
	}
	return prefix + strings.ToLower(hex.EncodeToString(raw[:]))
}

func credentialPrefix(plain string) string {
	if len(plain) <= 14 {
		return plain
	}
	return plain[:14]
}

func fingerprint(parts ...any) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprint(hash, part)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func timeFingerprint(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func int64Fingerprint(value *int64) string {
	if value == nil {
		return ""
	}
	return fmt.Sprintf("%d", *value)
}

func intFingerprint(value *int) string {
	if value == nil {
		return ""
	}
	return fmt.Sprintf("%d", *value)
}

func normalizeAPIKeyLimits(rateLimitPerMinute *int, concurrencyLimit int) (*int, int, error) {
	if concurrencyLimit == 0 {
		concurrencyLimit = defaultAPIKeyConcurrency
	}
	if rateLimitPerMinute != nil && !validRateLimitPerMinute(*rateLimitPerMinute) {
		return nil, 0, domain.ErrInvalidAPIKey
	}
	if !validAPIKeyConcurrency(concurrencyLimit) {
		return nil, 0, domain.ErrInvalidAPIKey
	}
	return rateLimitPerMinute, concurrencyLimit, nil
}

func validRateLimitPerMinute(value int) bool {
	return value > 0 && value <= maxRateLimitPerMinute
}

func validAPIKeyConcurrency(value int) bool {
	return value > 0 && value <= maxAPIKeyConcurrency
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
