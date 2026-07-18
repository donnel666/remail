package infra

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

type OutboundMailModel struct {
	ID             uint       `gorm:"primaryKey;autoIncrement"`
	IdempotencyKey string     `gorm:"type:char(64);not null;uniqueIndex:idx_outbound_mails_idempotency_key;column:idempotency_key"`
	RequestHash    string     `gorm:"type:char(64);not null;column:request_hash"`
	Purpose        string     `gorm:"type:varchar(64);not null"`
	Sender         string     `gorm:"type:varchar(320);not null"`
	Recipient      string     `gorm:"type:varchar(320);not null"`
	ReplyTo        string     `gorm:"type:varchar(320);not null;column:reply_to"`
	Subject        string     `gorm:"type:varchar(500);not null"`
	TextBody       string     `gorm:"type:mediumtext;not null;column:text_body"`
	HTMLBody       string     `gorm:"type:mediumtext;not null;column:html_body"`
	Status         string     `gorm:"type:varchar(32);not null"`
	SendGeneration uint64     `gorm:"not null;default:1;column:send_generation"`
	Retries        int        `gorm:"not null"`
	FailureReason  string     `gorm:"type:varchar(500);not null;column:failure_reason"`
	SentAt         *time.Time `gorm:"column:sent_at"`
	CreatedAt      time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt      time.Time  `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (OutboundMailModel) TableName() string {
	return "outbound_mails"
}

type OutboundMailStore struct {
	db *gorm.DB
}

func NewOutboundMailStore(db *gorm.DB) *OutboundMailStore {
	return &OutboundMailStore{db: db}
}

func (s *OutboundMailStore) Reserve(ctx context.Context, mail *domain.OutboundMail) (*domain.OutboundMail, bool, error) {
	model := outboundMailModel(mail)
	err := s.db.WithContext(ctx).Create(model).Error
	if err == nil {
		return outboundMailFromModel(*model), true, nil
	}
	if !isDuplicateKeyError(err) {
		return nil, false, fmt.Errorf("create outbound mail: %w", err)
	}
	existing, findErr := s.FindByIdempotencyKey(ctx, mail.IdempotencyKey)
	if findErr != nil {
		return nil, false, findErr
	}
	if existing == nil {
		return nil, false, fmt.Errorf("find outbound mail after duplicate key: not found")
	}
	if existing.RequestHash != mail.RequestHash {
		return nil, false, domain.ErrOutboundIdempotencyConflict
	}
	return existing, false, nil
}

func (s *OutboundMailStore) ActivateSending(ctx context.Context, idempotencyKey string, generation uint64, now time.Time) (bool, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || generation == 0 {
		return false, nil
	}
	result := s.db.WithContext(ctx).Model(&OutboundMailModel{}).
		Where("idempotency_key = ? AND status = ? AND send_generation = ?", idempotencyKey, string(domain.OutboundStatusPending), generation).
		Updates(map[string]any{
			"status":         string(domain.OutboundStatusSending),
			"failure_reason": "",
			"updated_at":     now,
		})
	if result.Error != nil {
		return false, fmt.Errorf("activate outbound mail sending: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}

func (s *OutboundMailStore) ListPending(ctx context.Context, limit int) ([]domain.OutboundMail, error) {
	if limit <= 0 {
		limit = 100
	}
	var models []OutboundMailModel
	if err := s.db.WithContext(ctx).
		Select("idempotency_key", "send_generation").
		Where("status = ?", string(domain.OutboundStatusPending)).
		Order("created_at ASC, id ASC").
		Limit(limit).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list pending outbound mails: %w", err)
	}
	mails := make([]domain.OutboundMail, 0, len(models))
	for _, model := range models {
		mails = append(mails, *outboundMailFromModel(model))
	}
	return mails, nil
}

func (s *OutboundMailStore) ReleasePending(ctx context.Context, idempotencyKey string, generation uint64, reason string) (bool, error) {
	return s.markPending(ctx, idempotencyKey, generation, reason, false)
}

func (s *OutboundMailStore) ResetPending(ctx context.Context, idempotencyKey string, generation uint64, reason string) (bool, error) {
	return s.markPending(ctx, idempotencyKey, generation, reason, true)
}

func (s *OutboundMailStore) markPending(ctx context.Context, idempotencyKey string, generation uint64, reason string, resetAttempts bool) (bool, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || generation == 0 {
		return false, nil
	}
	updates := map[string]any{
		"status":          string(domain.OutboundStatusPending),
		"send_generation": gorm.Expr("send_generation + 1"),
		"failure_reason":  safeDiagnostic(reason),
		"updated_at":      time.Now().UTC(),
	}
	allowedStatus := domain.OutboundStatusSending
	if resetAttempts {
		updates["retries"] = 0
		allowedStatus = domain.OutboundStatusFailed
	}
	result := s.db.WithContext(ctx).Model(&OutboundMailModel{}).
		Where("idempotency_key = ? AND send_generation = ? AND status = ?", idempotencyKey, generation, string(allowedStatus)).
		Updates(updates)
	if result.Error != nil {
		return false, fmt.Errorf("release outbound mail pending: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}

func (s *OutboundMailStore) RecordSendFailure(ctx context.Context, idempotencyKey string, generation uint64, reason string, retryable bool) (bool, bool, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || generation == 0 {
		return false, false, nil
	}
	current := func() *gorm.DB {
		return s.db.WithContext(ctx).Model(&OutboundMailModel{}).
			Where("idempotency_key = ? AND status = ? AND send_generation = ?", idempotencyKey, string(domain.OutboundStatusSending), generation)
	}
	now := time.Now().UTC()
	if !retryable {
		terminal := current().Updates(map[string]any{
			"status":         string(domain.OutboundStatusFailed),
			"retries":        gorm.Expr("LEAST(retries + 1, 3)"),
			"failure_reason": safeDiagnostic(reason),
			"updated_at":     now,
		})
		if terminal.Error != nil {
			return false, false, fmt.Errorf("record terminal outbound mail failure: %w", terminal.Error)
		}
		return true, terminal.RowsAffected > 0, nil
	}
	terminal := current().Where("retries >= 2").Updates(map[string]any{
		"status":         string(domain.OutboundStatusFailed),
		"retries":        3,
		"failure_reason": safeDiagnostic(reason),
		"updated_at":     now,
	})
	if terminal.Error != nil {
		return false, false, fmt.Errorf("record terminal outbound mail failure: %w", terminal.Error)
	}
	if terminal.RowsAffected > 0 {
		return true, true, nil
	}
	retry := current().Where("retries < 2").Updates(map[string]any{
		"status":          string(domain.OutboundStatusPending),
		"retries":         gorm.Expr("retries + 1"),
		"send_generation": gorm.Expr("send_generation + 1"),
		"failure_reason":  safeDiagnostic(reason),
		"updated_at":      now,
	})
	if retry.Error != nil {
		return false, false, fmt.Errorf("record retryable outbound mail failure: %w", retry.Error)
	}
	return false, retry.RowsAffected > 0, nil
}

func (s *OutboundMailStore) MarkSent(ctx context.Context, idempotencyKey string, generation uint64, now time.Time) (bool, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || generation == 0 {
		return false, nil
	}
	result := s.db.WithContext(ctx).Model(&OutboundMailModel{}).
		Where("idempotency_key = ? AND status = ? AND send_generation = ?", idempotencyKey, string(domain.OutboundStatusSending), generation).
		Updates(map[string]any{
			"status":         string(domain.OutboundStatusSent),
			"retries":        0,
			"failure_reason": "",
			"sent_at":        now.UTC(),
			"updated_at":     now.UTC(),
		})
	if result.Error != nil {
		return false, fmt.Errorf("mark outbound mail sent: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}

func (s *OutboundMailStore) FindByIdempotencyKey(ctx context.Context, idempotencyKey string) (*domain.OutboundMail, error) {
	var model OutboundMailModel
	err := s.db.WithContext(ctx).Where("idempotency_key = ?", idempotencyKey).First(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find outbound mail: %w", err)
	}
	return outboundMailFromModel(model), nil
}

func outboundMailModel(mail *domain.OutboundMail) *OutboundMailModel {
	return &OutboundMailModel{
		ID:             mail.ID,
		IdempotencyKey: mail.IdempotencyKey,
		RequestHash:    mail.RequestHash,
		Purpose:        string(mail.Purpose),
		Sender:         mail.Sender,
		Recipient:      mail.Recipient,
		ReplyTo:        mail.ReplyTo,
		Subject:        mail.Subject,
		TextBody:       mail.TextBody,
		HTMLBody:       mail.HTMLBody,
		Status:         string(mail.Status),
		SendGeneration: mail.SendGeneration,
		Retries:        mail.Retries,
		FailureReason:  mail.FailureReason,
		SentAt:         mail.SentAt,
		CreatedAt:      mail.CreatedAt,
		UpdatedAt:      mail.UpdatedAt,
	}
}

func outboundMailFromModel(model OutboundMailModel) *domain.OutboundMail {
	return &domain.OutboundMail{
		ID:             model.ID,
		IdempotencyKey: model.IdempotencyKey,
		RequestHash:    model.RequestHash,
		Purpose:        domain.OutboundPurpose(model.Purpose),
		Sender:         model.Sender,
		Recipient:      model.Recipient,
		ReplyTo:        model.ReplyTo,
		Subject:        model.Subject,
		TextBody:       model.TextBody,
		HTMLBody:       model.HTMLBody,
		Status:         domain.OutboundStatus(model.Status),
		SendGeneration: model.SendGeneration,
		Retries:        model.Retries,
		FailureReason:  model.FailureReason,
		SentAt:         model.SentAt,
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
