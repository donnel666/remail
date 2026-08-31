package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/domain"
	"gorm.io/gorm"
)

type SystemKeyModel struct {
	ID               uint       `gorm:"column:id;primaryKey;autoIncrement"`
	Name             string     `gorm:"column:name;type:varchar(120);not null"`
	Purpose          string     `gorm:"column:purpose;type:varchar(32);not null;default:icloud_forwarding"`
	Platform         *string    `gorm:"column:platform;type:varchar(32)"`
	SubjectNamespace *string    `gorm:"column:subject_namespace;type:varchar(50)"`
	AllowedGroupIDs  *string    `gorm:"column:allowed_group_ids;type:json"`
	KeyPrefix        string     `gorm:"column:key_prefix;type:varchar(20);not null"`
	KeyHash          string     `gorm:"column:key_hash;type:char(64);not null"`
	LastUsedAt       *time.Time `gorm:"column:last_used_at"`
	DeletedAt        *time.Time `gorm:"column:deleted_at"`
	CreatedAt        time.Time  `gorm:"column:created_at;not null;autoCreateTime"`
}

func (SystemKeyModel) TableName() string { return "system_keys" }

func (m SystemKeyModel) toDomain() domain.SystemKey {
	var groups []string
	if m.AllowedGroupIDs != nil {
		_ = json.Unmarshal([]byte(*m.AllowedGroupIDs), &groups)
	}
	return domain.SystemKey{
		ID: m.ID, Name: m.Name, Purpose: domain.SystemKeyPurpose(m.Purpose),
		Platform: stringValue(m.Platform), SubjectNamespace: stringValue(m.SubjectNamespace), KeyPrefix: m.KeyPrefix,
		AllowedGroupIDs: groups,
		LastUsedAt:      m.LastUsedAt, CreatedAt: m.CreatedAt,
	}
}

func (r *Repository) CreateSystemKey(ctx context.Context, key domain.SystemKey, keyHash string) (*domain.SystemKey, error) {
	model := SystemKeyModel{
		Name: key.Name, Purpose: string(key.Purpose), KeyPrefix: key.KeyPrefix, KeyHash: keyHash,
		CreatedAt: key.CreatedAt,
	}
	if key.Platform != "" {
		model.Platform = &key.Platform
	}
	if key.SubjectNamespace != "" {
		model.SubjectNamespace = &key.SubjectNamespace
	}
	if len(key.AllowedGroupIDs) > 0 {
		payload, err := json.Marshal(key.AllowedGroupIDs)
		if err != nil {
			return nil, fmt.Errorf("encode system key groups: %w", err)
		}
		value := string(payload)
		model.AllowedGroupIDs = &value
	}
	if err := r.dbFor(ctx).Create(&model).Error; err != nil {
		return nil, fmt.Errorf("create system key: %w", err)
	}
	created := model.toDomain()
	created.KeyPlain = key.KeyPlain
	return &created, nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (r *Repository) ListSystemKeys(ctx context.Context) ([]domain.SystemKey, error) {
	var models []SystemKeyModel
	if err := r.dbFor(ctx).Where("deleted_at IS NULL").Order("created_at DESC, id DESC").Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list system keys: %w", err)
	}
	keys := make([]domain.SystemKey, len(models))
	for i := range models {
		keys[i] = models[i].toDomain()
	}
	return keys, nil
}

func (r *Repository) FindSystemKeyByHash(ctx context.Context, keyHash string) (*domain.SystemKey, error) {
	var model SystemKeyModel
	if err := r.dbFor(ctx).Where("key_hash = ? AND deleted_at IS NULL", keyHash).First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrSystemKeyNotFound
		}
		return nil, fmt.Errorf("find system key: %w", err)
	}
	key := model.toDomain()
	return &key, nil
}

func (r *Repository) DeleteSystemKey(ctx context.Context, keyID uint, deletedAt time.Time) error {
	result := r.dbFor(ctx).Model(&SystemKeyModel{}).
		Where("id = ? AND deleted_at IS NULL", keyID).
		Update("deleted_at", deletedAt)
	if result.Error != nil {
		return fmt.Errorf("delete system key: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return domain.ErrSystemKeyNotFound
	}
	return nil
}

func (r *Repository) TouchSystemKey(ctx context.Context, keyID uint, usedAt time.Time) error {
	if err := r.dbFor(ctx).Model(&SystemKeyModel{}).
		Where("id = ? AND deleted_at IS NULL", keyID).
		Update("last_used_at", usedAt).Error; err != nil {
		return fmt.Errorf("touch system key: %w", err)
	}
	return nil
}
