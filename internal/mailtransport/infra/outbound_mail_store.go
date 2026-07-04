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
	"gorm.io/gorm/clause"
)

type OutboundMailModel struct {
	ID             uint       `gorm:"primaryKey;autoIncrement"`
	IdempotencyKey string     `gorm:"type:char(64);not null;uniqueIndex:idx_outbound_mails_idempotency_key;column:idempotency_key"`
	RequestHash    string     `gorm:"type:char(64);not null;column:request_hash"`
	Purpose        string     `gorm:"type:varchar(64);not null"`
	Sender         string     `gorm:"type:varchar(320);not null"`
	Recipient      string     `gorm:"type:varchar(320);not null"`
	Subject        string     `gorm:"type:varchar(500);not null"`
	TextBody       string     `gorm:"type:mediumtext;not null;column:text_body"`
	HTMLBody       string     `gorm:"type:mediumtext;not null;column:html_body"`
	Status         string     `gorm:"type:varchar(32);not null"`
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
	existing, findErr := s.findByIdempotencyKey(ctx, mail.IdempotencyKey)
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

func (s *OutboundMailStore) ClaimSending(ctx context.Context, idempotencyKey string, staleBefore time.Time, now time.Time) (*domain.OutboundMail, bool, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return nil, false, nil
	}
	var model OutboundMailModel
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where(
				"idempotency_key = ? AND (status = ? OR (status = ? AND updated_at < ?))",
				idempotencyKey,
				string(domain.OutboundStatusPending),
				string(domain.OutboundStatusSending),
				staleBefore,
			).
			First(&model).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return fmt.Errorf("lock outbound mail for claim: %w", err)
		}
		next := outboundMailFromModel(model)
		next.MarkSending(now)
		result := tx.Model(&OutboundMailModel{}).
			Where("id = ? AND status IN ?", model.ID, []string{string(domain.OutboundStatusPending), string(domain.OutboundStatusSending)}).
			Updates(map[string]any{
				"status":         string(next.Status),
				"retries":        next.Retries,
				"failure_reason": "",
				"updated_at":     now,
			})
		if result.Error != nil {
			return fmt.Errorf("claim outbound mail: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			model.ID = 0
			return nil
		}
		model.Status = string(next.Status)
		model.Retries = next.Retries
		model.FailureReason = ""
		model.UpdatedAt = now
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if model.ID == 0 {
		return nil, false, nil
	}
	return outboundMailFromModel(model), true, nil
}

func (s *OutboundMailStore) ClaimDispatchable(ctx context.Context, limit int, staleBefore time.Time) ([]domain.OutboundMail, error) {
	if limit <= 0 {
		limit = 100
	}
	var models []OutboundMailModel
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status = ? OR (status = ? AND updated_at < ?)", string(domain.OutboundStatusPending), string(domain.OutboundStatusSending), staleBefore).
			Order("created_at ASC, id ASC").
			Limit(limit).
			Find(&models).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("claim dispatchable outbound mails: %w", err)
	}
	mails := make([]domain.OutboundMail, 0, len(models))
	for _, model := range models {
		mails = append(mails, *outboundMailFromModel(model))
	}
	return mails, nil
}

func (s *OutboundMailStore) MarkPending(ctx context.Context, idempotencyKey string, reason string) error {
	return s.updateStatus(ctx, idempotencyKey, domain.OutboundStatusPending, safeDiagnostic(reason), nil)
}

func (s *OutboundMailStore) MarkSent(ctx context.Context, idempotencyKey string, now time.Time) error {
	return s.updateStatus(ctx, idempotencyKey, domain.OutboundStatusSent, "", &now)
}

func (s *OutboundMailStore) MarkFailed(ctx context.Context, idempotencyKey string, reason string) error {
	return s.updateStatus(ctx, idempotencyKey, domain.OutboundStatusFailed, safeDiagnostic(reason), nil)
}

func (s *OutboundMailStore) updateStatus(ctx context.Context, idempotencyKey string, status domain.OutboundStatus, reason string, sentAt *time.Time) error {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return nil
	}
	updates := map[string]any{
		"status":         string(status),
		"failure_reason": reason,
		"updated_at":     time.Now().UTC(),
	}
	if sentAt != nil {
		updates["sent_at"] = sentAt.UTC()
	}
	result := s.db.WithContext(ctx).
		Model(&OutboundMailModel{}).
		Where("idempotency_key = ?", idempotencyKey).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("update outbound mail status: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("outbound mail not found")
	}
	return nil
}

func (s *OutboundMailStore) findByIdempotencyKey(ctx context.Context, idempotencyKey string) (*domain.OutboundMail, error) {
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
		Subject:        mail.Subject,
		TextBody:       mail.TextBody,
		HTMLBody:       mail.HTMLBody,
		Status:         string(mail.Status),
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
		Subject:        model.Subject,
		TextBody:       model.TextBody,
		HTMLBody:       model.HTMLBody,
		Status:         domain.OutboundStatus(model.Status),
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
