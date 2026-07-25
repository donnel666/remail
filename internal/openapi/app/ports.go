package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/openapi/domain"
	"github.com/donnel666/remail/internal/platform"
)

const (
	apiKeyPrefix             = "rk-"
	orderTokenPrefix         = "st_"
	defaultAPIKeyConcurrency = 500
	maxAPIKeyConcurrency     = 500
)

type Repository interface {
	CreateAPIKey(ctx context.Context, cmd CreateAPIKeyCommand) (*domain.APIKey, bool, error)
	ListAPIKeys(ctx context.Context, userID uint, offset, limit int) ([]domain.APIKey, int64, error)
	GetAPIKeyUsage(ctx context.Context, userID uint) (*APIKeyUsage, error)
	FindAPIKey(ctx context.Context, userID uint, keyID uint) (*domain.APIKey, error)
	UpdateAPIKey(ctx context.Context, cmd UpdateAPIKeyCommand) (*domain.APIKey, error)
	DeleteAPIKey(ctx context.Context, userID uint, keyID uint, deletedAt time.Time) error
	FindAPIKeyByPlain(ctx context.Context, plain string) (*domain.APIKey, error)
	GetAPIKeyOwnerAccess(ctx context.Context, userID uint) (role string, active bool, groupConcurrencyLimit int64, err error)
	AddAPIKeyQuotaUsed(ctx context.Context, keyID uint, delta int64, lastUsedAt time.Time) error

	IssueOrderToken(ctx context.Context, cmd IssueOrderTokenCommand) (*domain.OrderToken, error)
	FindOrderTokenByOrder(ctx context.Context, orderNo string) (*domain.OrderToken, error)
	FindOrderTokenByPlain(ctx context.Context, tokenPlain string) (*domain.OrderToken, error)
	ExtendOrderToken(ctx context.Context, orderNo string, expireAt time.Time) error
	DisableOrderToken(ctx context.Context, orderNo string, reason string, disabledAt time.Time) error
}

type APIKeyConcurrencyGate interface {
	Acquire(ctx context.Context, keyID uint, limit int, leaseID string) (active int, acquired bool, err error)
	Release(ctx context.Context, keyID uint, leaseID string) error
}

type CreateAPIKeyRequest struct {
	UserID           uint
	Name             string
	ExpireAt         *time.Time
	ConcurrencyLimit *int
	QuotaLimit       *int64
	IdempotencyKey   string
	RequestID        string
}

type CreateAPIKeyCommand struct {
	UserID             uint
	Name               string
	KeyPlain           string
	KeyPrefix          string
	ExpireAt           *time.Time
	ConcurrencyLimit   *int
	QuotaLimit         *int64
	IdempotencyKey     string
	RequestFingerprint string
	RequestID          string
	Now                time.Time
}

type UpdateAPIKeyRequest struct {
	UserID           uint
	KeyID            uint
	Name             *string
	Enabled          *bool
	ExpireAt         *time.Time
	ExpireSet        bool
	ConcurrencyLimit *int
	ConcurrencySet   bool
	QuotaLimit       *int64
	QuotaSet         bool
}

type UpdateAPIKeyCommand struct {
	UserID           uint
	KeyID            uint
	Name             *string
	Enabled          *bool
	ExpireAt         *time.Time
	ExpireSet        bool
	ConcurrencyLimit *int
	ConcurrencySet   bool
	QuotaLimit       *int64
	QuotaSet         bool
}

type APIKeyAuthResult struct {
	UserID   uint
	APIKeyID uint
	Role     string
	LeaseID  string
}

type APIKeyUsage struct {
	RequestCount int64
	KeyCount     int64
}

type IssueOrderTokenCommand struct {
	OrderNo     string
	TokenPlain  string
	TokenPrefix string
	ExpireAt    *time.Time
	Now         time.Time
}

type UseCase struct {
	repo    Repository
	runtime *apiKeyRuntime
	now     func() time.Time
}

func NewUseCase(repo Repository, concurrencyGates ...APIKeyConcurrencyGate) *UseCase {
	uc := &UseCase{
		repo: repo,
		now:  func() time.Time { return time.Now().UTC() },
	}
	uc.runtime = newAPIKeyRuntime(repo, uc.now, concurrencyGates...)
	return uc
}

func (uc *UseCase) CreateAPIKey(ctx context.Context, req CreateAPIKeyRequest) (*domain.APIKey, error) {
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if req.UserID == 0 {
		return nil, domain.ErrInvalidAPIKey
	}
	if idempotencyKey == "" {
		return nil, domain.ErrIdempotencyRequired
	}
	if req.ConcurrencyLimit != nil && !validAPIKeyConcurrency(*req.ConcurrencyLimit) {
		return nil, domain.ErrInvalidAPIKey
	}
	plain := nextCredential(apiKeyPrefix)
	keyPrefix := credentialPrefix(plain)
	name := domain.NormalizeAPIKeyName(req.Name)
	if req.QuotaLimit != nil && *req.QuotaLimit <= 0 {
		return nil, domain.ErrInvalidAPIKey
	}
	fingerprint := fingerprint("apikey.create", req.UserID, name, timeFingerprint(req.ExpireAt), "", intFingerprint(req.ConcurrencyLimit), int64Fingerprint(req.QuotaLimit))
	key, _, err := uc.repo.CreateAPIKey(ctx, CreateAPIKeyCommand{
		UserID:             req.UserID,
		Name:               name,
		KeyPlain:           plain,
		KeyPrefix:          keyPrefix,
		ExpireAt:           req.ExpireAt,
		ConcurrencyLimit:   req.ConcurrencyLimit,
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
	items, total, err := uc.repo.ListAPIKeys(ctx, userID, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	uc.runtime.overlayKeys(items)
	return items, total, nil
}

func (uc *UseCase) GetAPIKeyUsage(ctx context.Context, userID uint) (*APIKeyUsage, error) {
	if userID == 0 {
		return nil, domain.ErrInvalidCredentialFilter
	}
	usage, err := uc.repo.GetAPIKeyUsage(ctx, userID)
	if err != nil {
		return nil, err
	}
	usage.RequestCount += uc.runtime.quotaDeltaForUser(userID)
	return usage, nil
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
	if req.ConcurrencyLimit != nil {
		req.ConcurrencySet = true
	}
	if req.ConcurrencySet && req.ConcurrencyLimit != nil && !validAPIKeyConcurrency(*req.ConcurrencyLimit) {
		return nil, domain.ErrInvalidAPIKey
	}
	if req.QuotaSet && req.QuotaLimit != nil && *req.QuotaLimit <= 0 {
		return nil, domain.ErrInvalidAPIKey
	}
	if err := uc.runtime.flush(ctx); err != nil {
		return nil, err
	}
	key, err := uc.repo.UpdateAPIKey(ctx, UpdateAPIKeyCommand(req))
	if err == nil {
		uc.runtime.updateKey(*key)
	}
	return key, err
}

func (uc *UseCase) DeleteAPIKey(ctx context.Context, userID uint, keyID uint) error {
	if userID == 0 || keyID == 0 {
		return domain.ErrInvalidAPIKey
	}
	if err := uc.runtime.flush(ctx); err != nil {
		return err
	}
	err := uc.repo.DeleteAPIKey(ctx, userID, keyID, uc.now())
	if err == nil {
		uc.runtime.invalidateKey(keyID)
	}
	return err
}

func (uc *UseCase) FlushRuntime(ctx context.Context) error {
	return uc.runtime.flush(ctx)
}

func (uc *UseCase) Close(ctx context.Context) error {
	return uc.runtime.close(ctx)
}

func (uc *UseCase) BeginAPIKeyRequest(ctx context.Context, plain string) (*APIKeyAuthResult, error) {
	plain = strings.TrimSpace(plain)
	if plain == "" {
		return nil, domain.ErrInvalidAPIKey
	}
	leaseID := platform.NewUUIDV4String()
	key, err := uc.runtime.begin(ctx, plain, leaseID)
	if err != nil {
		return nil, err
	}
	return &APIKeyAuthResult{UserID: key.UserID, APIKeyID: key.ID, Role: key.OwnerRole, LeaseID: leaseID}, nil
}

func (uc *UseCase) FinishAPIKeyRequest(ctx context.Context, keyID uint, leaseIDs ...string) error {
	if keyID == 0 {
		return nil
	}
	leaseID := ""
	if len(leaseIDs) > 0 {
		leaseID = leaseIDs[0]
	}
	return uc.runtime.finishRequest(ctx, keyID, leaseID)
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

func nextCredential(prefix string) string {
	return prefix + platform.NewUUIDV4String()
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

func effectiveAPIKeyConcurrency(value *int, groupLimit int64) int {
	limit := defaultAPIKeyConcurrency
	if value != nil {
		limit = *value
	}
	if groupLimit > 0 && groupLimit < int64(limit) {
		return int(groupLimit)
	}
	return limit
}

func validAPIKeyConcurrency(value int) bool {
	return value > 0 && value <= maxAPIKeyConcurrency
}
