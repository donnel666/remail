package infra

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
	"gorm.io/gorm"
)

type InboundMailModel struct {
	ID                uint       `gorm:"primaryKey;autoIncrement"`
	EnvelopeFrom      string     `gorm:"type:varchar(320);not null;column:envelope_from"`
	HeaderFrom        string     `gorm:"type:varchar(320);not null;default:'';column:header_from"`
	Recipient         string     `gorm:"type:varchar(320);not null"`
	Subject           string     `gorm:"type:varchar(500);not null;default:''"`
	BodyPreview       string     `gorm:"type:varchar(1000);not null;default:'';column:body_preview"`
	VerificationCode  string     `gorm:"type:varchar(64);not null;default:'';column:verification_code"`
	MessageIDHeader   string     `gorm:"type:varchar(500);not null;default:'';column:message_id_header"`
	ReceivedAt        *time.Time `gorm:"column:received_at"`
	ParsedAt          *time.Time `gorm:"column:parsed_at"`
	ResourceID        uint       `gorm:"not null;column:resource_id"`
	ResourceType      string     `gorm:"type:varchar(32);not null;column:resource_type"`
	OwnerUserID       uint       `gorm:"not null;column:owner_user_id"`
	SourceObjectKey   string     `gorm:"type:varchar(500);not null;column:source_object_key"`
	Status            string     `gorm:"type:varchar(32);not null;default:'pending'"`
	ProcessGeneration uint64     `gorm:"not null;default:1;column:process_generation"`
	ProcessAttempts   int        `gorm:"not null;default:0;column:process_attempts"`
	FailureReason     string     `gorm:"type:varchar(500);not null;default:'';column:failure_reason"`
	CreatedAt         time.Time  `gorm:"not null;autoCreateTime"`
	UpdatedAt         time.Time  `gorm:"not null;autoUpdateTime"`
}

func (InboundMailModel) TableName() string {
	return "inbound_mails"
}

func (m *InboundMailModel) toDomain() *domain.InboundMail {
	return &domain.InboundMail{
		ID:                m.ID,
		EnvelopeFrom:      m.EnvelopeFrom,
		HeaderFrom:        m.HeaderFrom,
		Recipient:         m.Recipient,
		Subject:           m.Subject,
		BodyPreview:       m.BodyPreview,
		VerificationCode:  m.VerificationCode,
		MessageIDHeader:   m.MessageIDHeader,
		ReceivedAt:        m.ReceivedAt,
		ParsedAt:          m.ParsedAt,
		ResourceID:        m.ResourceID,
		ResourceType:      domain.InboundResourceType(m.ResourceType),
		OwnerUserID:       m.OwnerUserID,
		SourceObjectKey:   m.SourceObjectKey,
		Status:            domain.InboundStatus(m.Status),
		ProcessGeneration: m.ProcessGeneration,
		ProcessAttempts:   m.ProcessAttempts,
		FailureReason:     m.FailureReason,
		CreatedAt:         m.CreatedAt,
		UpdatedAt:         m.UpdatedAt,
	}
}

func inboundMailFromDomain(mail domain.InboundMail) *InboundMailModel {
	return &InboundMailModel{
		ID:                mail.ID,
		EnvelopeFrom:      mail.EnvelopeFrom,
		HeaderFrom:        mail.HeaderFrom,
		Recipient:         mail.Recipient,
		Subject:           mail.Subject,
		BodyPreview:       mail.BodyPreview,
		VerificationCode:  mail.VerificationCode,
		MessageIDHeader:   mail.MessageIDHeader,
		ReceivedAt:        mail.ReceivedAt,
		ParsedAt:          mail.ParsedAt,
		ResourceID:        mail.ResourceID,
		ResourceType:      string(mail.ResourceType),
		OwnerUserID:       mail.OwnerUserID,
		SourceObjectKey:   mail.SourceObjectKey,
		Status:            string(mail.Status),
		ProcessGeneration: mail.ProcessGeneration,
		ProcessAttempts:   mail.ProcessAttempts,
		FailureReason:     mail.FailureReason,
		CreatedAt:         mail.CreatedAt,
		UpdatedAt:         mail.UpdatedAt,
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

func (r *InboundMailRepo) ActivateProcessing(ctx context.Context, id uint, generation uint64) (bool, error) {
	result := r.db.WithContext(ctx).Model(&InboundMailModel{}).
		Where("id = ? AND status = ? AND process_generation = ?", id, string(domain.InboundStatusPending), generation).
		Updates(map[string]any{
			"status":         string(domain.InboundStatusProcessing),
			"failure_reason": "",
			"updated_at":     time.Now().UTC(),
		})
	if result.Error != nil {
		return false, fmt.Errorf("activate inbound mail processing: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}

func (r *InboundMailRepo) ListPending(ctx context.Context, limit int) ([]domain.InboundMail, error) {
	if limit <= 0 {
		limit = 100
	}
	var models []InboundMailModel
	if err := r.db.WithContext(ctx).
		Select("id", "process_generation").
		Where("status = ?", string(domain.InboundStatusPending)).
		Order("created_at ASC, id ASC").
		Limit(limit).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list pending inbound mails: %w", err)
	}
	mails := make([]domain.InboundMail, 0, len(models))
	for _, model := range models {
		mails = append(mails, *model.toDomain())
	}
	return mails, nil
}

func (r *InboundMailRepo) ReleasePending(ctx context.Context, id uint, generation uint64, safeError string) (bool, error) {
	result := r.db.WithContext(ctx).Model(&InboundMailModel{}).
		Where("id = ? AND status = ? AND process_generation = ?", id, string(domain.InboundStatusProcessing), generation).
		Updates(map[string]any{
			"status":             string(domain.InboundStatusPending),
			"process_generation": gorm.Expr("process_generation + 1"),
			"failure_reason":     safeDiagnostic(safeError),
			"updated_at":         time.Now().UTC(),
		})
	if result.Error != nil {
		return false, fmt.Errorf("release inbound mail pending: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}

func (r *InboundMailRepo) SaveParsedSummary(ctx context.Context, id uint, generation uint64, summary domain.InboundMailSummary) (bool, error) {
	if id == 0 || summary.ParsedAt.IsZero() {
		return false, fmt.Errorf("save inbound mail summary: invalid summary")
	}
	receivedAt := summary.ReceivedAt.UTC()
	if summary.ReceivedAt.IsZero() {
		receivedAt = summary.ParsedAt.UTC()
	}
	result := r.db.WithContext(ctx).Model(&InboundMailModel{}).
		Where("id = ? AND status = ? AND process_generation = ?", id, string(domain.InboundStatusProcessing), generation).
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
		return false, fmt.Errorf("save inbound mail summary: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}

func (r *InboundMailRepo) RecordProcessFailure(ctx context.Context, id uint, generation uint64, safeError string, retryable bool) (bool, bool, error) {
	current := func() *gorm.DB {
		return r.db.WithContext(ctx).Model(&InboundMailModel{}).
			Where("id = ? AND status = ? AND process_generation = ?", id, string(domain.InboundStatusProcessing), generation)
	}
	now := time.Now().UTC()
	if !retryable {
		terminal := current().Updates(map[string]any{
			"status":           string(domain.InboundStatusFailed),
			"process_attempts": gorm.Expr("LEAST(process_attempts + 1, 3)"),
			"failure_reason":   safeDiagnostic(safeError),
			"updated_at":       now,
		})
		if terminal.Error != nil {
			return false, false, fmt.Errorf("record terminal inbound mail failure: %w", terminal.Error)
		}
		return true, terminal.RowsAffected > 0, nil
	}
	terminal := current().Where("process_attempts >= 2").Updates(map[string]any{
		"status":           string(domain.InboundStatusFailed),
		"process_attempts": 3,
		"failure_reason":   safeDiagnostic(safeError),
		"updated_at":       now,
	})
	if terminal.Error != nil {
		return false, false, fmt.Errorf("record terminal inbound mail failure: %w", terminal.Error)
	}
	if terminal.RowsAffected > 0 {
		return true, true, nil
	}
	retry := current().Where("process_attempts < 2").Updates(map[string]any{
		"status":             string(domain.InboundStatusPending),
		"process_attempts":   gorm.Expr("process_attempts + 1"),
		"process_generation": gorm.Expr("process_generation + 1"),
		"failure_reason":     safeDiagnostic(safeError),
		"updated_at":         now,
	})
	if retry.Error != nil {
		return false, false, fmt.Errorf("record retryable inbound mail failure: %w", retry.Error)
	}
	return false, retry.RowsAffected > 0, nil
}

func (r *InboundMailRepo) MarkStored(ctx context.Context, id uint, generation uint64) (bool, error) {
	result := r.db.WithContext(ctx).Model(&InboundMailModel{}).
		Where("id = ? AND status = ? AND process_generation = ?", id, string(domain.InboundStatusProcessing), generation).
		Updates(map[string]any{
			"status":           string(domain.InboundStatusStored),
			"process_attempts": 0,
			"failure_reason":   "",
			"updated_at":       time.Now().UTC(),
		})
	if result.Error != nil {
		return false, fmt.Errorf("mark inbound mail stored: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}

func (r *InboundMailRepo) MarkFailed(ctx context.Context, id uint, safeError string) error {
	return r.updateStatus(ctx, id, domain.InboundStatusFailed, []domain.InboundStatus{domain.InboundStatusPending}, safeDiagnostic(safeError))
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
