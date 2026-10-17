package infra

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/daum/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ExplicitAlias struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	ResourceID   uint      `gorm:"uniqueIndex:uq_daum_alias_resource_email" json:"resourceId"`
	OwnerUserID  uint      `json:"ownerUserId"`
	EmailAddress string    `gorm:"uniqueIndex:uq_daum_alias_resource_email" json:"email"`
	EmailDomain  string    `json:"suffix"`
	Status       string    `json:"status"`
	Version      uint64    `json:"version"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

func (ExplicitAlias) TableName() string { return "daum_explicit_aliases" }

type BindingMailbox struct {
	ID                uint `gorm:"primaryKey"`
	ResourceID        uint `gorm:"uniqueIndex"`
	ResourceType      string
	OwnerUserID       uint
	AccountEmail      string
	BindingAddress    string
	PickupURL         string `gorm:"column:pickup_url" json:"-"`
	DomainResourceID  *uint
	MailboxResourceID *uint
	OrderNo           string
	Status            string
	LastSafeError     string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (BindingMailbox) TableName() string { return "daum_binding_mailboxes" }

type BindingInput struct {
	OwnerUserID    uint
	BindingAddress string
	PickupURL      string
}
type BindingResolution struct {
	Status            string
	DomainResourceID  *uint
	MailboxResourceID *uint
	OrderNo           string
	LastSafeError     string
}

func ensureFixedAliasesTx(tx *gorm.DB, row Resource, now time.Time) error {
	local, _, _ := strings.Cut(row.EmailAddress, "@")
	for _, suffix := range []string{"hanmail.net", "hanmail2.net"} {
		alias := ExplicitAlias{ResourceID: row.ID, OwnerUserID: 1, EmailAddress: local + "@" + suffix, EmailDomain: suffix, Status: "normal", Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&alias).Error; err != nil {
			return err
		}
		var existing ExplicitAlias
		if err := tx.Where("resource_id = ? AND email_address = ?", row.ID, alias.EmailAddress).Take(&existing).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrResourceConflict
			}
			return err
		}
		if existing.ResourceID != row.ID {
			return domain.ErrResourceConflict
		}
		if existing.Status != "normal" {
			if err := tx.Model(&ExplicitAlias{}).Where("id = ?", existing.ID).Updates(map[string]any{"status": "normal", "version": existing.Version + 1, "updated_at": now}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func correctFixedAliasesTx(tx *gorm.DB, row Resource, now time.Time) error {
	var historical int64
	if err := tx.Table("daum_allocations").Where("resource_id = ?", row.ID).Count(&historical).Error; err != nil {
		return err
	}
	if historical > 0 {
		// Historical allocations keep their original alias IDs and delivery addresses.
		if err := tx.Model(&ExplicitAlias{}).Where("resource_id = ? AND status = 'normal'", row.ID).Updates(map[string]any{"status": "disabled", "version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
			return err
		}
	} else {
		local, _, _ := strings.Cut(row.EmailAddress, "@")
		for _, suffix := range []string{"hanmail.net", "hanmail2.net"} {
			if err := tx.Model(&ExplicitAlias{}).Where("resource_id = ? AND email_domain = ? AND status = 'normal'", row.ID, suffix).Updates(map[string]any{"email_address": local + "@" + suffix, "version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
				return err
			}
		}
	}
	return ensureFixedAliasesTx(tx, row, now)
}

func (s *Service) ListAliases(ctx context.Context, id uint) ([]ExplicitAlias, error) {
	rows := make([]ExplicitAlias, 0, 2)
	err := s.dbFor(ctx).Where("resource_id = ? AND status = 'normal'", id).Order("id ASC").Find(&rows).Error
	return rows, err
}

func (s *Service) AliasesByResource(ctx context.Context, ids []uint) (map[uint][]ExplicitAlias, error) {
	result := make(map[uint][]ExplicitAlias, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var rows []ExplicitAlias
	if err := s.dbFor(ctx).Where("resource_id IN ? AND status = 'normal'", ids).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ResourceID] = append(result[row.ResourceID], row)
	}
	return result, nil
}

func (s *Service) upsertBindingTx(ctx context.Context, tx *gorm.DB, resource Resource, address, pickupURL string, now time.Time) error {
	address = strings.ToLower(strings.TrimSpace(address))
	if pickupURL != "" {
		var err error
		address, _, err = ParsePickupURL(pickupURL)
		if err != nil {
			return err
		}
	}
	if !validEmail(address) {
		return domain.ErrInvalidResource
	}
	resolution := BindingResolution{Status: "unlinked", LastSafeError: "The system mailbox has not been linked."}
	if resolution.Status != "conflict" && s.ResolveBinding != nil {
		var err error
		resolution, err = s.ResolveBinding(ctx, BindingInput{OwnerUserID: resource.OwnerUserID, BindingAddress: address, PickupURL: pickupURL})
		if err != nil {
			return err
		}
	}
	switch resolution.Status {
	case "linked", "unlinked", "unavailable", "conflict":
	default:
		return domain.ErrInvalidResource
	}
	if resolution.Status == "linked" && resolution.DomainResourceID == nil && resolution.MailboxResourceID == nil {
		return domain.ErrInvalidResource
	}
	row := BindingMailbox{ResourceID: resource.ID, ResourceType: domain.ResourceType, OwnerUserID: resource.OwnerUserID, AccountEmail: resource.EmailAddress, BindingAddress: address, PickupURL: pickupURL, DomainResourceID: resolution.DomainResourceID, MailboxResourceID: resolution.MailboxResourceID, OrderNo: resolution.OrderNo, Status: resolution.Status, LastSafeError: safeSessionError(resolution.LastSafeError), CreatedAt: now, UpdatedAt: now}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "resource_id"}}, DoUpdates: clause.AssignmentColumns([]string{"owner_user_id", "account_email", "binding_address", "pickup_url", "domain_resource_id", "mailbox_resource_id", "order_no", "status", "last_safe_error", "updated_at"})}).Create(&row).Error
}

func (s *Service) UpdateBinding(ctx context.Context, id uint, address, pickupURL *string) error {
	return s.updateNullableBinding(ctx, id, address, address != nil, pickupURL, pickupURL != nil)
}

func (s *Service) updateNullableBinding(ctx context.Context, id uint, address *string, addressSet bool, pickupURL *string, pickupSet bool) error {
	clearBinding := (addressSet && address == nil) || (pickupSet && pickupURL == nil)
	if clearBinding && ((address != nil && strings.TrimSpace(*address) != "") || (pickupURL != nil && strings.TrimSpace(*pickupURL) != "")) {
		return domain.ErrInvalidResource
	}
	return s.transaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
		row, err := lockResource(tx, id, nil)
		if err != nil {
			return err
		}
		if row.Status == domain.StatusDeleted {
			return domain.ErrResourceMissing
		}
		var existing BindingMailbox
		result := tx.Where("resource_id = ?", id).Find(&existing)
		if result.Error != nil {
			return result.Error
		}
		if clearBinding {
			if result.RowsAffected == 0 {
				return nil
			}
			if err := tx.Where("resource_id = ?", id).Delete(&BindingMailbox{}).Error; err != nil {
				return err
			}
			return bumpRoot(tx, id, s.Now().UTC())
		}
		before := existing
		if address != nil {
			existing.BindingAddress = *address
		}
		if pickupURL != nil {
			existing.PickupURL = *pickupURL
		}
		if existing.PickupURL != "" && address != nil {
			email, _, err := ParsePickupURL(existing.PickupURL)
			if err != nil || email != strings.ToLower(strings.TrimSpace(*address)) {
				return domain.ErrInvalidResource
			}
		}
		if result.RowsAffected > 0 && before.BindingAddress == existing.BindingAddress && before.PickupURL == existing.PickupURL {
			return nil
		}
		now := s.Now().UTC()
		if err := s.upsertBindingTx(ctx, tx, *row, existing.BindingAddress, existing.PickupURL, now); err != nil {
			return err
		}
		return bumpRoot(tx, id, now)
	})
}
