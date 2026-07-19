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
	DeletedAt          *time.Time `gorm:"column:deleted_at"`
	RateLimitPerMinute *int       `gorm:"column:rate_limit_per_minute"`
	ConcurrencyLimit   int        `gorm:"not null;column:concurrency_limit"`
	QuotaLimit         *int64     `gorm:"column:quota_limit"`
	QuotaUsed          int64      `gorm:"not null;column:quota_used"`
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
				QuotaLimit:         cmd.QuotaLimit,
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
	if err := r.db.WithContext(ctx).Model(&APIKeyModel{}).Where("user_id = ? AND deleted_at IS NULL", userID).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count api keys: %w", err)
	}
	var models []APIKeyModel
	if err := r.db.WithContext(ctx).
		Where("user_id = ? AND deleted_at IS NULL", userID).
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

func (r *Repo) GetAPIKeyUsage(ctx context.Context, userID uint) (*openapiapp.APIKeyUsage, error) {
	var usage struct {
		RequestCount int64
		KeyCount     int64
	}
	if err := r.db.WithContext(ctx).
		Model(&APIKeyModel{}).
		Select("COALESCE(SUM(quota_used), 0) AS request_count, COUNT(*) AS key_count").
		Where("user_id = ? AND deleted_at IS NULL", userID).
		Scan(&usage).Error; err != nil {
		return nil, fmt.Errorf("sum api key usage: %w", err)
	}
	return &openapiapp.APIKeyUsage{
		RequestCount: usage.RequestCount,
		KeyCount:     usage.KeyCount,
	}, nil
}

func (r *Repo) FindAPIKey(ctx context.Context, userID uint, keyID uint) (*domain.APIKey, error) {
	var model APIKeyModel
	if err := r.db.WithContext(ctx).First(&model, "id = ? AND user_id = ? AND deleted_at IS NULL", keyID, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrAPIKeyNotFound
		}
		return nil, fmt.Errorf("find api key: %w", err)
	}
	item := apiKeyModelToDomain(model)
	return &item, nil
}

func (r *Repo) FindAPIKeyByPlain(ctx context.Context, plain string) (*domain.APIKey, error) {
	var model APIKeyModel
	if err := r.db.WithContext(ctx).First(&model, "key_plain = ? AND deleted_at IS NULL", strings.TrimSpace(plain)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrAPIKeyNotFound
		}
		return nil, fmt.Errorf("find api key by plain: %w", err)
	}
	ownerRole, active, err := r.GetAPIKeyOwnerAccess(ctx, model.UserID)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, domain.ErrAPIKeyDisabled
	}
	item := apiKeyModelToDomain(model)
	item.OwnerRole = ownerRole
	return &item, nil
}

func (r *Repo) GetAPIKeyOwnerAccess(ctx context.Context, userID uint) (string, bool, error) {
	var owner struct {
		Status string
		Role   string
	}
	if err := r.dbFor(ctx).
		Table("users").
		Select("status, role").
		Where("id = ?", userID).
		Take(&owner).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("load api key owner access: %w", err)
	}
	return owner.Role, owner.Status == "active", nil
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
	if cmd.RateLimitSet {
		if cmd.RateLimitPerMinute == nil {
			updates["rate_limit_per_minute"] = nil
		} else {
			updates["rate_limit_per_minute"] = *cmd.RateLimitPerMinute
		}
	}
	if cmd.ConcurrencyLimit != nil {
		updates["concurrency_limit"] = *cmd.ConcurrencyLimit
	}
	if cmd.QuotaSet {
		updates["quota_limit"] = cmd.QuotaLimit
	}
	if len(updates) > 0 {
		query := r.db.WithContext(ctx).Model(&APIKeyModel{}).Where("id = ? AND user_id = ? AND deleted_at IS NULL", cmd.KeyID, cmd.UserID)
		if cmd.QuotaSet && cmd.QuotaLimit != nil {
			query = query.Where("quota_used <= ?", *cmd.QuotaLimit)
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
			if cmd.QuotaSet && cmd.QuotaLimit != nil && existing.QuotaUsed > *cmd.QuotaLimit {
				return nil, domain.ErrAPIKeyQuotaExceeded
			}
			return existing, nil
		}
	}
	return r.FindAPIKey(ctx, cmd.UserID, cmd.KeyID)
}

func (r *Repo) DeleteAPIKey(ctx context.Context, userID uint, keyID uint, deletedAt time.Time) error {
	result := r.db.WithContext(ctx).Model(&APIKeyModel{}).
		Where("id = ? AND user_id = ? AND deleted_at IS NULL", keyID, userID).
		Updates(map[string]any{
			"enabled":    false,
			"deleted_at": deletedAt,
		})
	if result.Error != nil {
		return fmt.Errorf("delete api key: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return domain.ErrAPIKeyNotFound
	}
	return nil
}

func (r *Repo) AddAPIKeyQuotaUsed(ctx context.Context, keyID uint, delta int64, lastUsedAt time.Time) error {
	if keyID == 0 || delta <= 0 {
		return nil
	}
	result := r.db.WithContext(ctx).Model(&APIKeyModel{}).
		Where("id = ? AND deleted_at IS NULL", keyID).
		Updates(map[string]any{
			"quota_used":   gorm.Expr("quota_used + ?", delta),
			"last_used_at": gorm.Expr("CASE WHEN last_used_at IS NULL OR last_used_at < ? THEN ? ELSE last_used_at END", lastUsedAt, lastUsedAt),
		})
	if result.Error != nil {
		return fmt.Errorf("add api key quota used: %w", result.Error)
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
		QuotaLimit:         model.QuotaLimit,
		QuotaUsed:          model.QuotaUsed,
		ActiveRequests:     0,
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
