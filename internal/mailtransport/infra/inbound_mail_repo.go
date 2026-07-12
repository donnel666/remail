package infra

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type InboundMailModel struct {
	ID               uint       `gorm:"primaryKey;autoIncrement"`
	EnvelopeFrom     string     `gorm:"type:varchar(320);not null;column:envelope_from"`
	HeaderFrom       string     `gorm:"type:varchar(320);not null;default:'';column:header_from"`
	Recipient        string     `gorm:"type:varchar(320);not null"`
	Subject          string     `gorm:"type:varchar(500);not null;default:''"`
	BodyPreview      string     `gorm:"type:varchar(1000);not null;default:'';column:body_preview"`
	VerificationCode string     `gorm:"type:varchar(64);not null;default:'';column:verification_code"`
	MessageIDHeader  string     `gorm:"type:varchar(500);not null;default:'';column:message_id_header"`
	ReceivedAt       *time.Time `gorm:"column:received_at"`
	ParsedAt         *time.Time `gorm:"column:parsed_at"`
	ResourceID       uint       `gorm:"not null;column:resource_id"`
	ResourceType     string     `gorm:"type:varchar(32);not null;column:resource_type"`
	OwnerUserID      uint       `gorm:"not null;column:owner_user_id"`
	SourceObjectKey  string     `gorm:"type:varchar(500);not null;column:source_object_key"`
	Status           string     `gorm:"type:varchar(32);not null;default:'pending'"`
	FailureReason    string     `gorm:"type:varchar(500);not null;default:'';column:failure_reason"`
	CreatedAt        time.Time  `gorm:"not null;autoCreateTime"`
	UpdatedAt        time.Time  `gorm:"not null;autoUpdateTime"`
}

func (InboundMailModel) TableName() string {
	return "inbound_mails"
}

func (m *InboundMailModel) toDomain() *domain.InboundMail {
	return &domain.InboundMail{
		ID:               m.ID,
		EnvelopeFrom:     m.EnvelopeFrom,
		HeaderFrom:       m.HeaderFrom,
		Recipient:        m.Recipient,
		Subject:          m.Subject,
		BodyPreview:      m.BodyPreview,
		VerificationCode: m.VerificationCode,
		MessageIDHeader:  m.MessageIDHeader,
		ReceivedAt:       m.ReceivedAt,
		ParsedAt:         m.ParsedAt,
		ResourceID:       m.ResourceID,
		ResourceType:     domain.InboundResourceType(m.ResourceType),
		OwnerUserID:      m.OwnerUserID,
		SourceObjectKey:  m.SourceObjectKey,
		Status:           domain.InboundStatus(m.Status),
		FailureReason:    m.FailureReason,
		CreatedAt:        m.CreatedAt,
		UpdatedAt:        m.UpdatedAt,
	}
}

func inboundMailFromDomain(mail domain.InboundMail) *InboundMailModel {
	return &InboundMailModel{
		ID:               mail.ID,
		EnvelopeFrom:     mail.EnvelopeFrom,
		HeaderFrom:       mail.HeaderFrom,
		Recipient:        mail.Recipient,
		Subject:          mail.Subject,
		BodyPreview:      mail.BodyPreview,
		VerificationCode: mail.VerificationCode,
		MessageIDHeader:  mail.MessageIDHeader,
		ReceivedAt:       mail.ReceivedAt,
		ParsedAt:         mail.ParsedAt,
		ResourceID:       mail.ResourceID,
		ResourceType:     string(mail.ResourceType),
		OwnerUserID:      mail.OwnerUserID,
		SourceObjectKey:  mail.SourceObjectKey,
		Status:           string(mail.Status),
		FailureReason:    mail.FailureReason,
		CreatedAt:        mail.CreatedAt,
		UpdatedAt:        mail.UpdatedAt,
	}
}

type InboundMailRepo struct {
	db *gorm.DB
}

func NewInboundMailRepo(db *gorm.DB) *InboundMailRepo {
	return &InboundMailRepo{db: db}
}

func (r *InboundMailRepo) CreateMany(ctx context.Context, mails []domain.InboundMail) error {
	if len(mails) == 0 {
		return nil
	}
	models := make([]InboundMailModel, len(mails))
	for i := range mails {
		models[i] = *inboundMailFromDomain(mails[i])
	}
	if err := r.db.WithContext(ctx).Create(&models).Error; err != nil {
		return fmt.Errorf("create inbound mails: %w", err)
	}
	for i := range models {
		mails[i].ID = models[i].ID
		mails[i].CreatedAt = models[i].CreatedAt
		mails[i].UpdatedAt = models[i].UpdatedAt
	}
	return nil
}

func (r *InboundMailRepo) FindByID(ctx context.Context, id uint) (*domain.InboundMail, error) {
	var model InboundMailModel
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find inbound mail: %w", err)
	}
	return model.toDomain(), nil
}

func (r *InboundMailRepo) ClaimProcessing(ctx context.Context, id uint) (bool, error) {
	result := r.db.WithContext(ctx).Model(&InboundMailModel{}).
		Where("id = ? AND status = ?", id, string(domain.InboundStatusPending)).
		Updates(map[string]any{
			"status":         string(domain.InboundStatusProcessing),
			"failure_reason": "",
			"updated_at":     time.Now().UTC(),
		})
	if result.Error != nil {
		return false, fmt.Errorf("claim inbound mail processing: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}

func (r *InboundMailRepo) ClaimDispatchable(ctx context.Context, limit int, staleBefore time.Time) ([]domain.InboundMail, error) {
	if limit <= 0 {
		limit = 100
	}
	var models []InboundMailModel
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where(
				"status = ? OR (status = ? AND updated_at < ?)",
				string(domain.InboundStatusPending),
				string(domain.InboundStatusProcessing),
				staleBefore,
			).
			Order("created_at ASC, id ASC").
			Limit(limit).
			Find(&models).Error; err != nil {
			return fmt.Errorf("claim dispatchable inbound mails: %w", err)
		}
		if len(models) == 0 {
			return nil
		}
		ids := make([]uint, 0, len(models))
		for i := range models {
			ids = append(ids, models[i].ID)
			models[i].Status = string(domain.InboundStatusPending)
			models[i].FailureReason = ""
			models[i].UpdatedAt = now
		}
		result := tx.Model(&InboundMailModel{}).
			Where("id IN ?", ids).
			Updates(map[string]any{
				"status":         string(domain.InboundStatusPending),
				"failure_reason": "",
				"updated_at":     now,
			})
		if result.Error != nil {
			return fmt.Errorf("mark dispatchable inbound mails pending: %w", result.Error)
		}
		if result.RowsAffected != int64(len(ids)) {
			return fmt.Errorf("claim dispatchable inbound mails: claimed %d of %d", result.RowsAffected, len(ids))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	mails := make([]domain.InboundMail, 0, len(models))
	for _, model := range models {
		mails = append(mails, *model.toDomain())
	}
	return mails, nil
}

func (r *InboundMailRepo) MarkPending(ctx context.Context, id uint, safeError string) error {
	return r.updateStatus(ctx, id, domain.InboundStatusPending, []domain.InboundStatus{domain.InboundStatusPending, domain.InboundStatusProcessing}, safeDiagnostic(safeError))
}

func (r *InboundMailRepo) SaveParsedSummary(ctx context.Context, id uint, summary domain.InboundMailSummary) error {
	if id == 0 || summary.ParsedAt.IsZero() {
		return fmt.Errorf("save inbound mail summary: invalid summary")
	}
	receivedAt := summary.ReceivedAt.UTC()
	if summary.ReceivedAt.IsZero() {
		receivedAt = summary.ParsedAt.UTC()
	}
	result := r.db.WithContext(ctx).Model(&InboundMailModel{}).
		Where("id = ? AND status = ?", id, string(domain.InboundStatusProcessing)).
		Updates(map[string]any{
			"header_from":       summary.HeaderFrom,
			"subject":           summary.Subject,
			"body_preview":      summary.BodyPreview,
			"verification_code": summary.VerificationCode,
			"message_id_header": summary.MessageIDHeader,
			"received_at":       receivedAt,
			"parsed_at":         summary.ParsedAt.UTC(),
			"updated_at":        time.Now().UTC(),
		})
	if result.Error != nil {
		return fmt.Errorf("save inbound mail summary: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("save inbound mail summary: inbound mail not found")
	}
	return nil
}

func (r *InboundMailRepo) MarkStored(ctx context.Context, id uint) error {
	return r.updateStatus(ctx, id, domain.InboundStatusStored, []domain.InboundStatus{domain.InboundStatusProcessing}, "")
}

func (r *InboundMailRepo) MarkFailed(ctx context.Context, id uint, safeError string) error {
	return r.updateStatus(ctx, id, domain.InboundStatusFailed, []domain.InboundStatus{domain.InboundStatusPending, domain.InboundStatusProcessing}, safeDiagnostic(safeError))
}

func (r *InboundMailRepo) updateStatus(ctx context.Context, id uint, status domain.InboundStatus, allowed []domain.InboundStatus, safeError string) error {
	safeError = strings.TrimSpace(safeError)
	allowedValues := make([]string, 0, len(allowed))
	for _, value := range allowed {
		allowedValues = append(allowedValues, string(value))
	}
	result := r.db.WithContext(ctx).Model(&InboundMailModel{}).
		Where("id = ? AND status IN ?", id, allowedValues).
		Updates(map[string]any{
			"status":         string(status),
			"failure_reason": safeError,
			"updated_at":     time.Now().UTC(),
		})
	if result.Error != nil {
		return fmt.Errorf("update inbound mail status: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("inbound mail not found")
	}
	return nil
}
