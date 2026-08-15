package icloud

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"math/big"
	stdmail "net/mail"
	"strings"
	"time"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	iCloudImportPreparationTTL          = 30 * time.Minute
	iCloudImportPreparationRetention    = 24 * time.Hour
	iCloudImportPreparationMailLimit    = 50
	iCloudImportPreparationCleanupBatch = 100
)

func (s *Service) CreateAdminICloudImportPreparation(ctx context.Context, operatorUserID uint) (*ICloudImportPreparationView, error) {
	if s == nil || s.db == nil || operatorUserID == 0 {
		return nil, ErrICloudImportDependency
	}
	now := s.now().UTC()
	if err := s.cleanupICloudImportPreparations(ctx, now.Add(-iCloudImportPreparationRetention)); err != nil {
		return nil, ErrICloudImportTemporary
	}
	configured := runtimeconfig.ICloudForwardingSuffixes(
		runtimeconfig.String(runtimeconfig.ICloudForwardingSuffixesKey, ""),
	)
	if len(configured) == 0 {
		return nil, ErrICloudForwardingUnavailable
	}
	var domains []struct {
		ID     uint   `gorm:"column:id"`
		Domain string `gorm:"column:domain"`
	}
	if err := s.db.WithContext(ctx).Table("domain_resources").
		Select("id, LOWER(domain) AS domain").
		Where("purpose = ? AND status = ? AND allow_new_bindings = ? AND LOWER(domain) IN ?", "binding", "normal", true, configured).
		Find(&domains).Error; err != nil {
		return nil, ErrICloudImportTemporary
	}
	if len(domains) == 0 {
		return nil, ErrICloudForwardingUnavailable
	}

	for attempt := 0; attempt < 4; attempt++ {
		index, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(len(domains))))
		if err != nil {
			return nil, ErrICloudImportTemporary
		}
		local := make([]byte, 16)
		if _, err := cryptorand.Read(local); err != nil {
			return nil, ErrICloudImportTemporary
		}
		domain := domains[index.Int64()]
		model := iCloudImportPreparationModel{
			OperatorUserID:   operatorUserID,
			DomainResourceID: domain.ID,
			ForwardToEmail:   "icloud_" + hex.EncodeToString(local) + "@" + domain.Domain,
			ExpiresAt:        now.Add(iCloudImportPreparationTTL),
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		if err := s.db.WithContext(ctx).Create(&model).Error; err != nil {
			if isICloudDuplicateError(err) {
				continue
			}
			return nil, ErrICloudImportTemporary
		}
		return model.preparationView(now), nil
	}
	return nil, ErrICloudImportTemporary
}

func (s *Service) cleanupICloudImportPreparations(ctx context.Context, before time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ids []uint
		if err := tx.Table("icloud_import_preparations AS p").
			Select("p.id").
			Where("p.expires_at < ? AND NOT EXISTS (SELECT 1 FROM icloud_resource_imports AS i WHERE i.preparation_id = p.id)", before).
			Order("p.id ASC").
			Limit(iCloudImportPreparationCleanupBatch).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Pluck("p.id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		return tx.Where("id IN ?", ids).Delete(&iCloudImportPreparationModel{}).Error
	})
}

func (s *Service) GetAdminICloudImportPreparation(ctx context.Context, operatorUserID, preparationID uint) (*ICloudImportPreparationView, error) {
	if s == nil || s.db == nil || operatorUserID == 0 || preparationID == 0 {
		return nil, ErrICloudImportPreparationNotFound
	}
	var model iCloudImportPreparationModel
	err := s.db.WithContext(ctx).
		Where("id = ? AND operator_user_id = ?", preparationID, operatorUserID).
		First(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrICloudImportPreparationNotFound
	}
	if err != nil {
		return nil, ErrICloudImportTemporary
	}
	now := s.now().UTC()
	if model.ConsumedAt != nil || !model.ExpiresAt.After(now) || strings.TrimSpace(model.VerificationCode) != "" {
		return model.preparationView(now), nil
	}
	var rows []iCloudPreparationInboundMail
	if err := s.db.WithContext(ctx).Table("inbound_mails").
		Select("id, header_from, verification_code, source_object_key, parsed_at, created_at").
		Where("recipient = ? AND created_at >= ?", model.ForwardToEmail, model.CreatedAt).
		Order("COALESCE(received_at, created_at) DESC, id DESC").
		Limit(iCloudImportPreparationMailLimit).
		Find(&rows).Error; err != nil {
		return nil, ErrICloudImportTemporary
	}

	readFailed := false
	for _, row := range rows {
		sender := normalizeICloudPreparationSender(row.HeaderFrom)
		code := strings.TrimSpace(row.VerificationCode)
		if (sender == "" || code == "") && row.ParsedAt == nil {
			if s.files == nil {
				return nil, ErrICloudImportDependency
			}
			stored, err := s.files.ReadPrivate(ctx, row.SourceObjectKey)
			if err != nil || stored == nil {
				readFailed = true
				continue
			}
			summary := mailapp.ParseInboundMessageSummary(stored.ContentBytes, row.CreatedAt)
			sender = normalizeICloudPreparationSender(summary.HeaderFrom)
			code = strings.TrimSpace(summary.VerificationCode)
			if err := s.db.WithContext(ctx).Table("inbound_mails").Where("id = ? AND parsed_at IS NULL", row.ID).Updates(map[string]any{
				"header_from": summary.HeaderFrom, "subject": summary.Subject,
				"body_preview": summary.BodyPreview, "verification_code": summary.VerificationCode,
				"message_id_header": summary.MessageIDHeader, "received_at": summary.ReceivedAt,
				"parsed_at": summary.ParsedAt, "updated_at": now,
			}).Error; err != nil {
				return nil, ErrICloudImportTemporary
			}
		}
		if sender != "noreply@apple.com" || code == "" {
			continue
		}
		verifiedAt := now
		result := s.db.WithContext(ctx).Model(&iCloudImportPreparationModel{}).
			Where("id = ? AND operator_user_id = ? AND verification_message_id IS NULL AND consumed_at IS NULL AND expires_at > ?", model.ID, operatorUserID, now).
			Updates(map[string]any{
				"verification_message_id": row.ID,
				"verification_code":       code,
				"verified_at":             verifiedAt,
				"updated_at":              now,
			})
		if result.Error != nil {
			return nil, ErrICloudImportTemporary
		}
		if result.RowsAffected == 1 {
			messageID := row.ID
			model.VerificationMessageID = &messageID
			model.VerificationCode = code
			model.VerifiedAt = &verifiedAt
			model.UpdatedAt = now
			return model.preparationView(now), nil
		}
		return s.GetAdminICloudImportPreparation(ctx, operatorUserID, preparationID)
	}
	if readFailed {
		return nil, ErrICloudImportTemporary
	}
	return model.preparationView(now), nil
}

type iCloudPreparationInboundMail struct {
	ID               uint       `gorm:"column:id"`
	HeaderFrom       string     `gorm:"column:header_from"`
	VerificationCode string     `gorm:"column:verification_code"`
	SourceObjectKey  string     `gorm:"column:source_object_key"`
	ParsedAt         *time.Time `gorm:"column:parsed_at"`
	CreatedAt        time.Time  `gorm:"column:created_at"`
}

func (m iCloudImportPreparationModel) preparationView(now time.Time) *ICloudImportPreparationView {
	status := "waiting"
	if m.ConsumedAt != nil {
		status = "consumed"
	} else if !m.ExpiresAt.After(now) {
		status = "expired"
	} else if strings.TrimSpace(m.VerificationCode) != "" {
		status = "code_received"
	}
	return &ICloudImportPreparationView{
		ID: m.ID, ForwardToEmail: m.ForwardToEmail, Status: status,
		VerificationCode: strings.TrimSpace(m.VerificationCode),
		ExpiresAt:        m.ExpiresAt, CreatedAt: m.CreatedAt,
	}
}

func normalizeICloudPreparationSender(value string) string {
	value = strings.TrimSpace(value)
	if address, err := stdmail.ParseAddress(value); err == nil {
		value = address.Address
	}
	return strings.ToLower(strings.TrimSpace(value))
}

func (s *Service) usableICloudImportPreparation(ctx context.Context, operatorUserID, preparationID uint, now time.Time) (*iCloudImportPreparationModel, error) {
	var model iCloudImportPreparationModel
	err := s.db.WithContext(ctx).
		Where("id = ? AND operator_user_id = ?", preparationID, operatorUserID).
		First(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrICloudImportPreparationConflict
	}
	if err != nil {
		return nil, ErrICloudImportTemporary
	}
	if model.ConsumedAt != nil || !model.ExpiresAt.After(now) || model.VerifiedAt == nil || strings.TrimSpace(model.VerificationCode) == "" {
		return nil, ErrICloudImportPreparationConflict
	}
	return &model, nil
}
