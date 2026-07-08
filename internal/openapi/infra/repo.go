package infra

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	openapiapp "github.com/donnel666/remail/internal/openapi/app"
	"github.com/donnel666/remail/internal/openapi/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type APIKeyModel struct {
	ID                 uint       `gorm:"primaryKey;autoIncrement"`
	UserID             uint       `gorm:"not null;column:user_id"`
	Name               string     `gorm:"type:varchar(120);not null;default:''"`
	KeyPrefix          string     `gorm:"type:varchar(32);not null;column:key_prefix"`
	KeyPlain           string     `gorm:"type:varchar(255);not null;column:key_plain"`
	Enabled            bool       `gorm:"not null;default:true"`
	RateLimitPerMinute int        `gorm:"not null;column:rate_limit_per_minute"`
	ConcurrencyLimit   int        `gorm:"not null;column:concurrency_limit"`
	ActiveRequests     int        `gorm:"not null;column:active_requests"`
	WindowStartedAt    *time.Time `gorm:"column:window_started_at"`
	WindowRequestCount int        `gorm:"not null;column:window_request_count"`
	ExpireAt           *time.Time `gorm:"column:expire_at"`
	LastUsedAt         *time.Time `gorm:"column:last_used_at"`
	CreatedAt          time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt          time.Time  `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (APIKeyModel) TableName() string { return "api_keys" }

type OrderTokenModel struct {
	ID             uint       `gorm:"primaryKey;autoIncrement"`
	TokenPrefix    string     `gorm:"type:varchar(32);not null;column:token_prefix"`
	TokenPlain     string     `gorm:"type:varchar(255);not null;column:token_plain"`
	OrderNo        string     `gorm:"type:varchar(64);not null;column:order_no"`
	Enabled        bool       `gorm:"not null;default:true"`
	ExpireAt       *time.Time `gorm:"column:expire_at"`
	DisabledAt     *time.Time `gorm:"column:disabled_at"`
	DisabledReason string     `gorm:"type:varchar(500);not null;default:'';column:disabled_reason"`
	CreatedAt      time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt      time.Time  `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (OrderTokenModel) TableName() string { return "order_tokens" }

type APILogModel struct {
	ID             uint      `gorm:"primaryKey;autoIncrement"`
	PrincipalType  string    `gorm:"type:varchar(32);not null;column:principal_type"`
	PrincipalID    uint      `gorm:"not null;column:principal_id"`
	UserID         *uint     `gorm:"column:user_id"`
	Path           string    `gorm:"type:varchar(255);not null"`
	Method         string    `gorm:"type:varchar(16);not null"`
	IdempotencyKey string    `gorm:"type:varchar(128);not null;default:'';column:idempotency_key"`
	HTTPStatus     int       `gorm:"not null;column:http_status"`
	DurationMs     int       `gorm:"not null;column:duration_ms"`
	RequestID      string    `gorm:"type:varchar(64);not null;default:'';column:request_id"`
	CreatedAt      time.Time `gorm:"not null;autoCreateTime;column:created_at"`
}

func (APILogModel) TableName() string { return "api_logs" }

type idempotencyKeyModel struct {
	ID                 uint           `gorm:"primaryKey;autoIncrement"`
	OwnerUserID        uint           `gorm:"not null;column:owner_user_id"`
	IdempotencyKey     string         `gorm:"type:varchar(128);not null;column:idempotency_key"`
	Operation          string         `gorm:"type:varchar(64);not null"`
	RequestFingerprint string         `gorm:"type:char(64);not null;column:request_fingerprint"`
	Status             string         `gorm:"type:varchar(32);not null;default:'succeeded'"`
	ResponseJSON       sql.NullString `gorm:"type:json;column:response_json"`
	CreatedAt          time.Time      `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt          time.Time      `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (idempotencyKeyModel) TableName() string { return "idempotency_keys" }

type Repo struct {
	db *gorm.DB
}

func NewRepo(db *gorm.DB) *Repo {
	return &Repo{db: db}
}

func (r *Repo) withTx(ctx context.Context, fn func(context.Context, *gorm.DB) error) error {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		db := tx.WithContext(ctx)
		name := "openapi_sp_" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
		if err := db.SavePoint(name).Error; err != nil {
			return fmt.Errorf("create openapi savepoint: %w", err)
		}
		if err := fn(ctx, db); err != nil {
			if rollbackErr := db.RollbackTo(name).Error; rollbackErr != nil {
				return fmt.Errorf("rollback openapi savepoint: %w: %v", err, rollbackErr)
			}
			return err
		}
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(platform.WithGormTx(ctx, tx), tx)
	})
}

func (r *Repo) dbFor(ctx context.Context) *gorm.DB {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return tx.WithContext(ctx)
	}
	return r.db.WithContext(ctx)
}

func (r *Repo) CreateAPIKey(ctx context.Context, cmd openapiapp.CreateAPIKeyCommand) (*domain.APIKey, bool, error) {
	var result domain.APIKey
	var replayed bool
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		response, wasReplayed, err := withIdempotencyInTx(ctx, tx, cmd.UserID, "apikey.create", cmd.IdempotencyKey, cmd.RequestFingerprint, func(writeTx *gorm.DB) ([]byte, error) {
			model := APIKeyModel{
				UserID:             cmd.UserID,
				Name:               cmd.Name,
				KeyPrefix:          cmd.KeyPrefix,
				KeyPlain:           cmd.KeyPlain,
				Enabled:            true,
				RateLimitPerMinute: cmd.RateLimitPerMinute,
				ConcurrencyLimit:   cmd.ConcurrencyLimit,
				ExpireAt:           cmd.ExpireAt,
			}
			if err := writeTx.WithContext(ctx).Create(&model).Error; err != nil {
				if isDuplicateKeyError(err) {
					return nil, domain.ErrInvalidAPIKey
				}
				return nil, fmt.Errorf("create api key: %w", err)
			}
			result = apiKeyModelToDomain(model)
			return json.Marshal(result)
		})
		if err != nil {
			return err
		}
		replayed = wasReplayed
		if replayed {
			if err := json.Unmarshal(response, &result); err != nil {
				return fmt.Errorf("decode idempotent api key: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return &result, replayed, nil
}

func (r *Repo) ListAPIKeys(ctx context.Context, userID uint, offset, limit int) ([]domain.APIKey, int64, error) {
	var total int64
	if err := r.db.WithContext(ctx).Model(&APIKeyModel{}).Where("user_id = ?", userID).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count api keys: %w", err)
	}
	var models []APIKeyModel
	if err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC, id DESC").
		Offset(offset).
		Limit(limit).
		Find(&models).Error; err != nil {
		return nil, 0, fmt.Errorf("list api keys: %w", err)
	}
	items := make([]domain.APIKey, len(models))
	for i := range models {
		items[i] = apiKeyModelToDomain(models[i])
	}
	return items, total, nil
}

func (r *Repo) FindAPIKey(ctx context.Context, userID uint, keyID uint) (*domain.APIKey, error) {
	var model APIKeyModel
	if err := r.db.WithContext(ctx).First(&model, "id = ? AND user_id = ?", keyID, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrAPIKeyNotFound
		}
		return nil, fmt.Errorf("find api key: %w", err)
	}
	item := apiKeyModelToDomain(model)
	return &item, nil
}

func (r *Repo) UpdateAPIKey(ctx context.Context, cmd openapiapp.UpdateAPIKeyCommand) (*domain.APIKey, error) {
	updates := map[string]any{}
	if cmd.Name != nil {
		updates["name"] = *cmd.Name
	}
	if cmd.Enabled != nil {
		updates["enabled"] = *cmd.Enabled
	}
	if cmd.ExpireSet {
		updates["expire_at"] = cmd.ExpireAt
	}
	if cmd.RateLimitPerMinute != nil {
		updates["rate_limit_per_minute"] = *cmd.RateLimitPerMinute
	}
	if cmd.ConcurrencyLimit != nil {
		updates["concurrency_limit"] = *cmd.ConcurrencyLimit
	}
	if len(updates) > 0 {
		query := r.db.WithContext(ctx).Model(&APIKeyModel{}).Where("id = ? AND user_id = ?", cmd.KeyID, cmd.UserID)
		if cmd.ConcurrencyLimit != nil {
			query = query.Where("active_requests <= ?", *cmd.ConcurrencyLimit)
		}
		result := query.Updates(updates)
		if result.Error != nil {
			return nil, fmt.Errorf("update api key: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			existing, err := r.FindAPIKey(ctx, cmd.UserID, cmd.KeyID)
			if err != nil {
				return nil, err
			}
			if cmd.ConcurrencyLimit != nil && existing.ActiveRequests > *cmd.ConcurrencyLimit {
				return nil, domain.ErrAPIKeyConcurrencyLimit
			}
			return existing, nil
		}
	}
	return r.FindAPIKey(ctx, cmd.UserID, cmd.KeyID)
}

func (r *Repo) AcquireAPIKeyRequest(ctx context.Context, plain string, now time.Time) (*domain.APIKey, error) {
	var model APIKeyModel
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&model, "key_plain = ?", strings.TrimSpace(plain)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrAPIKeyNotFound
			}
			return fmt.Errorf("lock api key for request: %w", err)
		}
		if !model.Enabled {
			return domain.ErrAPIKeyDisabled
		}
		var owner struct {
			Enabled bool
		}
		if err := tx.WithContext(ctx).
			Table("users").
			Select("enabled").
			Where("id = ?", model.UserID).
			Take(&owner).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrAPIKeyDisabled
			}
			return fmt.Errorf("load api key owner: %w", err)
		}
		if !owner.Enabled {
			return domain.ErrAPIKeyDisabled
		}
		if model.ExpireAt != nil && !model.ExpireAt.After(now) {
			return domain.ErrAPIKeyExpired
		}
		if model.ActiveRequests >= model.ConcurrencyLimit {
			return domain.ErrAPIKeyConcurrencyLimit
		}
		windowStartedAt := model.WindowStartedAt
		windowRequestCount := model.WindowRequestCount
		if shouldResetRateWindow(windowStartedAt, now) {
			windowStartedAt = &now
			windowRequestCount = 0
		}
		if windowRequestCount >= model.RateLimitPerMinute {
			return domain.ErrAPIKeyRateLimited
		}
		result := tx.WithContext(ctx).Model(&APIKeyModel{}).
			Where("id = ?", model.ID).
			Updates(map[string]any{
				"active_requests":      model.ActiveRequests + 1,
				"window_started_at":    windowStartedAt,
				"window_request_count": windowRequestCount + 1,
				"last_used_at":         now,
			})
		if result.Error != nil {
			return fmt.Errorf("acquire api key request: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return domain.ErrAPIKeyNotFound
		}
		model.ActiveRequests++
		model.WindowStartedAt = windowStartedAt
		model.WindowRequestCount = windowRequestCount + 1
		model.LastUsedAt = &now
		return nil
	})
	if err != nil {
		return nil, err
	}
	item := apiKeyModelToDomain(model)
	return &item, nil
}

func (r *Repo) ReleaseAPIKeyRequest(ctx context.Context, keyID uint) error {
	if keyID == 0 {
		return nil
	}
	result := r.db.WithContext(ctx).Model(&APIKeyModel{}).
		Where("id = ?", keyID).
		Update("active_requests", gorm.Expr("CASE WHEN active_requests > 0 THEN active_requests - 1 ELSE 0 END"))
	if result.Error != nil {
		return fmt.Errorf("release api key request: %w", result.Error)
	}
	return nil
}

func (r *Repo) IssueOrderToken(ctx context.Context, cmd openapiapp.IssueOrderTokenCommand) (*domain.OrderToken, error) {
	var model OrderTokenModel
	err := r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		candidate := OrderTokenModel{
			TokenPrefix: cmd.TokenPrefix,
			TokenPlain:  cmd.TokenPlain,
			OrderNo:     cmd.OrderNo,
			Enabled:     true,
			ExpireAt:    cmd.ExpireAt,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate).Error; err != nil {
			if isDuplicateKeyError(err) {
				return domain.ErrInvalidOrderToken
			}
			return fmt.Errorf("issue order token: %w", err)
		}
		if err := tx.WithContext(txCtx).First(&model, "order_no = ?", cmd.OrderNo).Error; err != nil {
			return fmt.Errorf("find issued order token: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	token := orderTokenModelToDomain(model)
	return &token, nil
}

func (r *Repo) FindOrderTokenByOrder(ctx context.Context, orderNo string) (*domain.OrderToken, error) {
	var model OrderTokenModel
	if err := r.dbFor(ctx).First(&model, "order_no = ? AND enabled = ?", orderNo, true).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find order token: %w", err)
	}
	token := orderTokenModelToDomain(model)
	return &token, nil
}

func (r *Repo) FindOrderTokenByPlain(ctx context.Context, tokenPlain string) (*domain.OrderToken, error) {
	var model OrderTokenModel
	if err := r.dbFor(ctx).First(&model, "token_plain = ?", strings.TrimSpace(tokenPlain)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find order token by plain: %w", err)
	}
	token := orderTokenModelToDomain(model)
	return &token, nil
}

func (r *Repo) DisableOrderToken(ctx context.Context, orderNo string, reason string, disabledAt time.Time) error {
	if strings.TrimSpace(reason) == "" {
		reason = "Order service disabled."
	}
	result := r.dbFor(ctx).
		Model(&OrderTokenModel{}).
		Where("order_no = ? AND enabled = ?", orderNo, true).
		Updates(map[string]any{
			"enabled":         false,
			"disabled_at":     disabledAt,
			"disabled_reason": reason,
		})
	if result.Error != nil {
		return fmt.Errorf("disable order token: %w", result.Error)
	}
	return nil
}

func (r *Repo) ExtendOrderToken(ctx context.Context, orderNo string, expireAt time.Time) error {
	result := r.dbFor(ctx).
		Model(&OrderTokenModel{}).
		Where("order_no = ? AND enabled = ?", strings.TrimSpace(orderNo), true).
		Updates(map[string]any{
			"expire_at": gorm.Expr("CASE WHEN expire_at IS NULL OR expire_at < ? THEN ? ELSE expire_at END", expireAt.UTC(), expireAt.UTC()),
		})
	if result.Error != nil {
		return fmt.Errorf("extend order token: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return domain.ErrInvalidOrderToken
	}
	return nil
}

func (r *Repo) CreateAPILog(ctx context.Context, cmd openapiapp.CreateAPILogCommand) error {
	var userID *uint
	if cmd.UserID > 0 {
		userID = &cmd.UserID
	}
	model := APILogModel{
		PrincipalType:  strings.TrimSpace(cmd.PrincipalType),
		PrincipalID:    cmd.PrincipalID,
		UserID:         userID,
		Path:           strings.TrimSpace(cmd.Path),
		Method:         strings.TrimSpace(cmd.Method),
		IdempotencyKey: strings.TrimSpace(cmd.IdempotencyKey),
		HTTPStatus:     cmd.HTTPStatus,
		DurationMs:     cmd.DurationMs,
		RequestID:      strings.TrimSpace(cmd.RequestID),
		CreatedAt:      cmd.Now,
	}
	if err := r.db.WithContext(ctx).Create(&model).Error; err != nil {
		return fmt.Errorf("create api log: %w", err)
	}
	return nil
}

func withIdempotencyInTx(ctx context.Context, tx *gorm.DB, ownerUserID uint, operation string, idempotencyKey string, fingerprint string, run func(*gorm.DB) ([]byte, error)) ([]byte, bool, error) {
	if strings.TrimSpace(idempotencyKey) == "" || strings.TrimSpace(fingerprint) == "" {
		return nil, false, domain.ErrIdempotencyRequired
	}
	model := idempotencyKeyModel{
		OwnerUserID:        ownerUserID,
		IdempotencyKey:     idempotencyKey,
		Operation:          operation,
		RequestFingerprint: fingerprint,
		Status:             "processing",
	}
	if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&model).Error; err != nil {
		return nil, false, fmt.Errorf("create idempotency key: %w", err)
	}
	var stored idempotencyKeyModel
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("owner_user_id = ? AND idempotency_key = ? AND operation = ?", ownerUserID, idempotencyKey, operation).
		First(&stored).Error; err != nil {
		return nil, false, fmt.Errorf("lock idempotency key: %w", err)
	}
	if stored.RequestFingerprint != fingerprint {
		return nil, false, domain.ErrIdempotencyConflict
	}
	if stored.Status == "succeeded" && stored.ResponseJSON.Valid && strings.TrimSpace(stored.ResponseJSON.String) != "" {
		return []byte(stored.ResponseJSON.String), true, nil
	}
	response, err := run(tx)
	if err != nil {
		return nil, false, err
	}
	if err := tx.WithContext(ctx).Model(&idempotencyKeyModel{}).Where("id = ?", stored.ID).Updates(map[string]any{
		"status":        "succeeded",
		"response_json": string(response),
	}).Error; err != nil {
		return nil, false, fmt.Errorf("finish idempotency key: %w", err)
	}
	return response, false, nil
}

func apiKeyModelToDomain(model APIKeyModel) domain.APIKey {
	return domain.APIKey{
		ID:                 model.ID,
		UserID:             model.UserID,
		Name:               model.Name,
		KeyPrefix:          model.KeyPrefix,
		KeyPlain:           model.KeyPlain,
		Enabled:            model.Enabled,
		RateLimitPerMinute: model.RateLimitPerMinute,
		ConcurrencyLimit:   model.ConcurrencyLimit,
		ActiveRequests:     model.ActiveRequests,
		ExpireAt:           model.ExpireAt,
		LastUsedAt:         model.LastUsedAt,
		CreatedAt:          model.CreatedAt,
		UpdatedAt:          model.UpdatedAt,
	}
}

func orderTokenModelToDomain(model OrderTokenModel) domain.OrderToken {
	return domain.OrderToken{
		ID:             model.ID,
		TokenPrefix:    model.TokenPrefix,
		TokenPlain:     model.TokenPlain,
		OrderNo:        model.OrderNo,
		Enabled:        model.Enabled,
		ExpireAt:       model.ExpireAt,
		DisabledAt:     model.DisabledAt,
		DisabledReason: model.DisabledReason,
		CreatedAt:      model.CreatedAt,
		UpdatedAt:      model.UpdatedAt,
	}
}

func isDuplicateKeyError(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func shouldResetRateWindow(startedAt *time.Time, now time.Time) bool {
	if startedAt == nil {
		return true
	}
	return now.Sub(*startedAt) >= time.Minute || startedAt.After(now.Add(5*time.Second))
}
