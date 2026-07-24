package infra

import (
	"context"
	"errors"
	"fmt"
	"time"

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
	return domain.Setting{Key: m.Key, Value: m.Value, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
}

type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

func (r *Repository) List(ctx context.Context) ([]domain.Setting, error) {
	var models []SettingModel
	if err := r.db.WithContext(ctx).Order("`key` ASC").Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list system settings: %w", err)
	}
	settings := make([]domain.Setting, len(models))
	for i := range models {
		settings[i] = models[i].toDomain()
	}
	return settings, nil
}

func (r *Repository) Get(ctx context.Context, key string) (*domain.Setting, error) {
	var model SettingModel
	if err := r.db.WithContext(ctx).Where("`key` = ?", key).First(&model).Error; err != nil {
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
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&model).Error; err != nil {
		return nil, fmt.Errorf("upsert system setting: %w", err)
	}
	return r.Get(ctx, key)
}

func (r *Repository) Delete(ctx context.Context, key string) error {
	result := r.db.WithContext(ctx).Where("`key` = ?", key).Delete(&SettingModel{})
	if result.Error != nil {
		return fmt.Errorf("delete system setting: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return domain.ErrSettingNotFound
	}
	return nil
}
