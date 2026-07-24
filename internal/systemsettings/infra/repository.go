package infra

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SettingModel struct {
	Key       string    `gorm:"primaryKey;type:varchar(191)"`
	Value     string    `gorm:"type:longtext;not null"`
	CreatedAt time.Time `gorm:"not null;autoCreateTime"`
	UpdatedAt time.Time `gorm:"not null;autoUpdateTime"`
}

func (SettingModel) TableName() string { return "system_settings" }

func (m SettingModel) toDomain() domain.Setting {
	return domain.Setting{Key: strings.ToLower(strings.TrimSpace(m.Key)), Value: m.Value, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
}

type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

func (r *Repository) WithTx(ctx context.Context, fn func(context.Context) error) error {
	if _, ok := platform.GormTxFromContext(ctx); ok {
		return fn(ctx)
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(platform.WithGormTx(ctx, tx))
	})
}

func (r *Repository) dbFor(ctx context.Context) *gorm.DB {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return tx.WithContext(ctx)
	}
	return r.db.WithContext(ctx)
}

func (r *Repository) List(ctx context.Context) ([]domain.Setting, error) {
	var models []SettingModel
	if err := r.dbFor(ctx).Order("`key` ASC").Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list system settings: %w", err)
	}
	settings := make([]domain.Setting, len(models))
	for i := range models {
		settings[i] = models[i].toDomain()
	}
	return settings, nil
}

// InsertMissing seeds new settings without changing values already managed by
// an administrator. The primary key conflict is intentionally ignored so
// concurrent application instances can run startup initialization safely.
func (r *Repository) InsertMissing(ctx context.Context, settings []domain.Setting) error {
	if len(settings) == 0 {
		return nil
	}
	models := make([]SettingModel, len(settings))
	for i, setting := range settings {
		models[i] = SettingModel{Key: setting.Key, Value: setting.Value}
	}
	if err := r.dbFor(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoNothing: true,
	}).Create(&models).Error; err != nil {
		return fmt.Errorf("insert missing system settings: %w", err)
	}
	return nil
}

func (r *Repository) Get(ctx context.Context, key string) (*domain.Setting, error) {
	var model SettingModel
	if err := r.dbFor(ctx).Where("LOWER(`key`) = LOWER(?)", key).First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrSettingNotFound
		}
		return nil, fmt.Errorf("get system setting: %w", err)
	}
	setting := model.toDomain()
	return &setting, nil
}

func (r *Repository) Upsert(ctx context.Context, key, value string) (*domain.Setting, error) {
	model := SettingModel{Key: key, Value: value}
	if err := r.dbFor(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&model).Error; err != nil {
		return nil, fmt.Errorf("upsert system setting: %w", err)
	}
	return r.Get(ctx, key)
}

func (r *Repository) BulkUpsert(ctx context.Context, settings []domain.Setting) ([]domain.Setting, error) {
	if len(settings) == 0 {
		return []domain.Setting{}, nil
	}
	models := make([]SettingModel, len(settings))
	for i, setting := range settings {
		models[i] = SettingModel{Key: setting.Key, Value: setting.Value}
	}
	if err := r.dbFor(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&models).Error; err != nil {
		return nil, fmt.Errorf("bulk upsert system settings: %w", err)
	}
	keys := make([]string, len(settings))
	for i := range settings {
		keys[i] = settings[i].Key
	}
	var stored []SettingModel
	if err := r.dbFor(ctx).Where("LOWER(`key`) IN ?", keys).Find(&stored).Error; err != nil {
		return nil, fmt.Errorf("reload bulk system settings: %w", err)
	}
	byKey := make(map[string]domain.Setting, len(stored))
	for i := range stored {
		setting := stored[i].toDomain()
		byKey[setting.Key] = setting
	}
	result := make([]domain.Setting, len(settings))
	for i := range settings {
		setting, ok := byKey[settings[i].Key]
		if !ok {
			return nil, fmt.Errorf("reload bulk system setting %q: %w", settings[i].Key, domain.ErrSettingNotFound)
		}
		result[i] = setting
	}
	return result, nil
}

func (r *Repository) Delete(ctx context.Context, key string) error {
	result := r.dbFor(ctx).Where("LOWER(`key`) = LOWER(?)", key).Delete(&SettingModel{})
	if result.Error != nil {
		return fmt.Errorf("delete system setting: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return domain.ErrSettingNotFound
	}
	return nil
}
