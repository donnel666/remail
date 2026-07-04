package infra

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MicrosoftBindingMailboxModel struct {
	ID             uint       `gorm:"primaryKey;autoIncrement"`
	ResourceID     uint       `gorm:"not null;column:resource_id;uniqueIndex:idx_microsoft_binding_resource"`
	ResourceType   string     `gorm:"type:varchar(32);not null;default:'microsoft';column:resource_type"`
	OwnerUserID    uint       `gorm:"not null;column:owner_user_id"`
	AccountEmail   string     `gorm:"type:varchar(255);not null;column:account_email"`
	BindingAddress string     `gorm:"type:varchar(320);not null;column:binding_address"`
	Purpose        string     `gorm:"type:varchar(64);not null;default:'validation'"`
	Status         string     `gorm:"type:varchar(32);not null;default:'pending'"`
	CodeMessageID  string     `gorm:"type:varchar(255);not null;default:'';column:code_msg_id"`
	BoundDisplay   string     `gorm:"type:varchar(255);not null;default:'';column:bound_display"`
	Category       string     `gorm:"type:varchar(64);not null;default:''"`
	LastSafeError  string     `gorm:"type:varchar(500);not null;default:'';column:last_safe_error"`
	SelectedAt     *time.Time `gorm:"column:selected_at"`
	CodeSentAt     *time.Time `gorm:"column:code_sent_at"`
	VerifiedAt     *time.Time `gorm:"column:verified_at"`
	ExpiresAt      *time.Time `gorm:"column:expires_at"`
	CreatedAt      time.Time  `gorm:"not null;autoCreateTime"`
	UpdatedAt      time.Time  `gorm:"not null;autoUpdateTime"`
}

func (MicrosoftBindingMailboxModel) TableName() string {
	return "microsoft_binding_mailboxes"
}

type MicrosoftBindingImportInput struct {
	OwnerUserID    uint
	AccountEmail   string
	BindingAddress string
}

type MicrosoftBindingRepo struct {
	db *gorm.DB
}

func NewMicrosoftBindingRepo(db *gorm.DB) *MicrosoftBindingRepo {
	return &MicrosoftBindingRepo{db: db}
}

func (r *MicrosoftBindingRepo) UpsertByEmail(ctx context.Context, inputs []MicrosoftBindingImportInput) error {
	if len(inputs) == 0 {
		return nil
	}
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return r.upsertByEmailTx(tx.WithContext(ctx), inputs)
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return r.upsertByEmailTx(tx, inputs)
	})
}

func (r *MicrosoftBindingRepo) upsertByEmailTx(tx *gorm.DB, inputs []MicrosoftBindingImportInput) error {
	for _, input := range inputs {
		accountEmail := normalizeBindingEmail(input.AccountEmail)
		bindingAddress := normalizeBindingEmail(input.BindingAddress)
		if input.OwnerUserID == 0 || accountEmail == "" || bindingAddress == "" {
			continue
		}

		var row struct {
			ResourceID  uint
			OwnerUserID uint
		}
		err := tx.Raw(`
SELECT er.id AS resource_id, er.owner_user_id AS owner_user_id
FROM email_resources AS er
JOIN microsoft_resources AS mr ON mr.id = er.id
WHERE er.owner_user_id = ?
  AND er.type = 'microsoft'
  AND mr.email_address = ?
  AND mr.status <> 'deleted'
LIMIT 1`, input.OwnerUserID, accountEmail).Scan(&row).Error
		if err != nil {
			return fmt.Errorf("resolve microsoft binding resource: %w", err)
		}
		if row.ResourceID == 0 {
			continue
		}
		if err := upsertMicrosoftBindingTx(tx, &MicrosoftBindingMailboxModel{
			ResourceID:     row.ResourceID,
			ResourceType:   "microsoft",
			OwnerUserID:    row.OwnerUserID,
			AccountEmail:   accountEmail,
			BindingAddress: bindingAddress,
			Purpose:        "validation",
			Status:         string(domain.MicrosoftBindingPending),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r *MicrosoftBindingRepo) UpsertForResource(ctx context.Context, resourceID uint, ownerUserID uint, accountEmail string, bindingAddress string) error {
	accountEmail = normalizeBindingEmail(accountEmail)
	bindingAddress = normalizeBindingEmail(bindingAddress)
	if resourceID == 0 || ownerUserID == 0 || accountEmail == "" || bindingAddress == "" {
		return nil
	}
	db := r.db.WithContext(ctx)
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		db = tx.WithContext(ctx)
	}
	return upsertMicrosoftBindingTx(db, &MicrosoftBindingMailboxModel{
		ResourceID:     resourceID,
		ResourceType:   "microsoft",
		OwnerUserID:    ownerUserID,
		AccountEmail:   accountEmail,
		BindingAddress: bindingAddress,
		Purpose:        "validation",
		Status:         string(domain.MicrosoftBindingPending),
	})
}

func (r *MicrosoftBindingRepo) PreferredAddress(ctx context.Context, resourceID uint) (string, error) {
	if resourceID == 0 {
		return "", nil
	}
	var model MicrosoftBindingMailboxModel
	err := r.db.WithContext(ctx).
		Where("resource_id = ? AND status <> ?", resourceID, string(domain.MicrosoftBindingExpired)).
		Order("updated_at DESC").
		First(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("find microsoft binding address: %w", err)
	}
	return model.BindingAddress, nil
}

func (r *MicrosoftBindingRepo) MarkStatus(ctx context.Context, resourceID uint, bindingAddress string, status domain.MicrosoftBindingStatus, safeError string) error {
	if resourceID == 0 {
		return nil
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"status":          string(status),
		"category":        bindingStatusCategory(status),
		"last_safe_error": strings.TrimSpace(safeError),
		"updated_at":      now,
	}
	switch status {
	case domain.MicrosoftBindingCodeSent:
		updates["code_sent_at"] = now
	case domain.MicrosoftBindingVerified:
		updates["verified_at"] = now
	}
	db := r.db.WithContext(ctx)
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		db = tx.WithContext(ctx)
	}
	query := db.Model(&MicrosoftBindingMailboxModel{}).Where("resource_id = ?", resourceID)
	if normalized := normalizeBindingEmail(bindingAddress); normalized != "" {
		query = query.Where("binding_address = ?", normalized)
	}
	result := query.Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("mark microsoft binding status: %w", result.Error)
	}
	return nil
}

func upsertMicrosoftBindingTx(tx *gorm.DB, model *MicrosoftBindingMailboxModel) error {
	if model == nil {
		return nil
	}
	now := time.Now().UTC()
	assignments := map[string]any{
		"owner_user_id":   model.OwnerUserID,
		"resource_type":   firstNonBlank(model.ResourceType, "microsoft"),
		"account_email":   model.AccountEmail,
		"binding_address": model.BindingAddress,
		"purpose":         firstNonBlank(model.Purpose, "validation"),
		"status":          model.Status,
		"category":        "",
		"last_safe_error": "",
		"selected_at":     now,
		"code_sent_at":    nil,
		"verified_at":     nil,
		"expires_at":      model.ExpiresAt,
		"updated_at":      now,
	}
	if model.Status == "" {
		model.Status = string(domain.MicrosoftBindingPending)
		assignments["status"] = model.Status
	}
	err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "resource_id"}},
		DoUpdates: clause.Assignments(assignments),
	}).Create(model).Error
	if err != nil {
		return fmt.Errorf("upsert microsoft binding mailbox: %w", err)
	}
	return nil
}

func bindingStatusCategory(status domain.MicrosoftBindingStatus) string {
	switch status {
	case domain.MicrosoftBindingTimeout:
		return "code_timeout"
	case domain.MicrosoftBindingFailed:
		return "binding_failed"
	case domain.MicrosoftBindingExpired:
		return "expired"
	default:
		return ""
	}
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func normalizeBindingEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
