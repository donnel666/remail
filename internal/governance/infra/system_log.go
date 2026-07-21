package infra

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/governance/domain"
	"gorm.io/gorm"
)

// SystemLogModel is the GORM model for governance system logs.
type SystemLogModel struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement"`
	Level     string    `gorm:"type:varchar(32);not null"`
	Module    string    `gorm:"type:varchar(100);not null"`
	EventType string    `gorm:"type:varchar(100);not null;column:event_type"`
	RequestID string    `gorm:"type:varchar(64);not null;column:request_id"`
	BizType   string    `gorm:"type:varchar(100);not null;column:biz_type"`
	BizID     string    `gorm:"type:varchar(100);not null;column:biz_id"`
	Message   string    `gorm:"type:varchar(500);not null"`
	Detail    string    `gorm:"type:varchar(1000);not null"`
	CreatedAt time.Time `gorm:"not null;autoCreateTime;column:created_at"`
}

func (SystemLogModel) TableName() string {
	return "system_logs"
}

// SystemLogRepo persists safe system diagnostics.
type SystemLogRepo struct {
	db *gorm.DB
}

func NewSystemLogRepo(db *gorm.DB) *SystemLogRepo {
	return &SystemLogRepo{db: db}
}

func (r *SystemLogRepo) Create(ctx context.Context, log *domain.SystemLog) error {
	if err := r.db.WithContext(ctx).Create(systemLogModel(log)).Error; err != nil {
		return fmt.Errorf("create system log: %w", err)
	}
	return nil
}

func (r *SystemLogRepo) CreateInTx(ctx context.Context, tx *gorm.DB, log *domain.SystemLog) error {
	if err := tx.WithContext(ctx).Create(systemLogModel(log)).Error; err != nil {
		return fmt.Errorf("create system log: %w", err)
	}
	return nil
}

func systemLogModel(log *domain.SystemLog) *SystemLogModel {
	return &SystemLogModel{
		Level:     truncateSystemLogText(log.Level, 32),
		Module:    truncateSystemLogText(log.Module, 100),
		EventType: truncateSystemLogText(log.EventType, 100),
		RequestID: truncateSystemLogText(log.RequestID, 64),
		BizType:   truncateSystemLogText(log.BizType, 100),
		BizID:     truncateSystemLogText(log.BizID, 100),
		Message:   sanitizeSystemLogText(log.Message, 500),
		Detail:    sanitizeSystemLogText(log.Detail, 1000),
	}
}

var (
	systemLogSecretPattern    = regexp.MustCompile(`(?i)(["']?(?:password|passwd|pwd|token|access[_-]?token|refresh[_-]?token|id[_-]?token|client[_-]?secret|client[_-]?id|cookie|set-cookie|ppft|canary|flowtoken)["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^,\s;&}]+)`)
	systemLogBearerPattern    = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	systemLogURLUserPattern   = regexp.MustCompile(`(?i)\b((?:https?|socks5h?)://)[^/@\s]+@`)
	systemLogDSNPattern       = regexp.MustCompile(`(?i)\b([a-z0-9._-]+):[^@\s]+@(tcp|unix)\(`)
	systemLogObjectKeyPattern = regexp.MustCompile(`\bmailtransport/inbound/[^\s,;]+`)
)

func sanitizeSystemLogText(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	value = systemLogURLUserPattern.ReplaceAllString(value, `${1}***@`)
	value = systemLogDSNPattern.ReplaceAllString(value, `${1}:***@${2}(`)
	value = systemLogSecretPattern.ReplaceAllString(value, `${1}***`)
	value = systemLogBearerPattern.ReplaceAllString(value, "Bearer ***")
	value = systemLogObjectKeyPattern.ReplaceAllString(value, "mailtransport/inbound/***")
	return truncateSystemLogText(value, maxRunes)
}

func truncateSystemLogText(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return value
}
