package icloud

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrICloudResourceUpdate   = errors.New("icloud: invalid resource update")
	ErrICloudResourceIdentity = errors.New("icloud: duplicate resource identity")
)

type AdminICloudEditCommand struct {
	ResourceID     uint
	Version        uint64
	ImportLine     *string
	OwnerUserID    *uint
	ForSale        *bool
	ExpireAt       *time.Time
	OperatorUserID uint
	IdempotencyKey string
	RequestID      string
	Path           string
}

func (s *Service) EditAdminICloudResource(ctx context.Context, command AdminICloudEditCommand) (*AdminICloudMutationResult, error) {
	if s == nil || s.db == nil || s.operationLogs == nil || command.ResourceID == 0 || command.Version == 0 ||
		command.OperatorUserID == 0 || (command.ImportLine == nil && command.OwnerUserID == nil && command.ForSale == nil && command.ExpireAt == nil) {
		return nil, ErrICloudResourceUpdate
	}

	var imported *iCloudImportLine
	if command.ImportLine != nil {
		line, failure := parseICloudCurlImportLine(1, *command.ImportLine)
		if failure != nil || line == nil {
			return nil, ErrICloudResourceUpdate
		}
		imported = line
		value := strings.TrimSpace(*command.ImportLine)
		command.ImportLine = &value
	}
	if command.OwnerUserID != nil {
		if *command.OwnerUserID == 0 {
			return nil, ErrICloudResourceUpdate
		}
		if err := s.validateICloudImportOwner(ctx, *command.OwnerUserID); err != nil {
			if errors.Is(err, ErrICloudImportInvalidOwner) {
				return nil, ErrICloudResourceOwner
			}
			return nil, err
		}
	}
	if command.ExpireAt != nil {
		value := normalizeICloudResourceExpireAt(*command.ExpireAt)
		if !validICloudResourceExpireAt(value, s.now().UTC()) {
			return nil, ErrICloudResourceUpdate
		}
		command.ExpireAt = &value
	}
	idempotencyKey, err := normalizeAdminICloudIdempotencyKey(command.IdempotencyKey)
	if err != nil {
		return nil, ErrICloudResourceUpdate
	}
	fingerprint, err := adminICloudCommandFingerprint(struct {
		Version    uint64     `json:"version"`
		ImportLine *string    `json:"importLine,omitempty"`
		OwnerID    *uint      `json:"ownerId,omitempty"`
		ForSale    *bool      `json:"forSale,omitempty"`
		ExpireAt   *time.Time `json:"expireAt,omitempty"`
	}{command.Version, command.ImportLine, command.OwnerUserID, command.ForSale, command.ExpireAt})
	if err != nil {
		return nil, ErrICloudResourceQueryTemporary
	}
	receipt := coreapp.AdminResourceCommandReceipt{
		OperatorUserID: command.OperatorUserID, IdempotencyKey: idempotencyKey,
		Operation: "icloud.admin_resource.edit", Subject: "icloud_resource:" + strconv.FormatUint(uint64(command.ResourceID), 10),
		RequestFingerprint: fingerprint,
	}

	result := &AdminICloudMutationResult{}
	replayed, queuedValidation, queuedProvision := false, false, false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		wasReplayed, reserveErr := s.reserveAdminICloudCommand(ctx, tx, receipt, result)
		if reserveErr != nil || wasReplayed {
			replayed = wasReplayed
			return reserveErr
		}
		now := s.now().UTC()

		var root iCloudRootModel
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND type = ?", command.ResourceID, "icloud").Take(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrICloudResourceNotFound
			}
			return err
		}
		if root.Version != command.Version {
			return ErrICloudResourceVersion
		}
		var resource iCloudResourceModel
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&resource, command.ResourceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrICloudResourceNotFound
			}
			return err
		}
		if resource.Status == iCloudResourceDeleted {
			return ErrICloudResourceNotFound
		}

		var existingChannels []iCloudResourceChannelModel
		if imported != nil {
			if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("resource_id = ?", command.ResourceID).Find(&existingChannels).Error; err != nil {
				return err
			}
		}
		emailChanged := imported != nil && imported.PrimaryEmail != resource.PrimaryEmail
		appPasswordChanged := imported != nil && imported.AppPassword != resource.IMAPAppPassword
		channelsChanged := imported != nil && (emailChanged || !sameICloudChannels(existingChannels, imported.Channels))
		ownerChanged := command.OwnerUserID != nil && *command.OwnerUserID != root.OwnerUserID
		expireAtChanged := command.ExpireAt != nil && !command.ExpireAt.Equal(resource.ExpireAt)
		if (emailChanged || ownerChanged) && assertNoActiveAdminICloudAllocationTx(ctx, tx, command.ResourceID) != nil {
			return ErrICloudResourceAllocation
		}
		if emailChanged {
			if err := assertUniqueAdminICloudEmailTx(ctx, tx, command.ResourceID, imported.PrimaryEmail); err != nil {
				return err
			}
		}

		nextOwnerID := root.OwnerUserID
		if command.OwnerUserID != nil {
			nextOwnerID = *command.OwnerUserID
		}
		nextForSale := resource.ForSale
		if command.ForSale != nil {
			nextForSale = *command.ForSale
		}
		if nextForSale {
			if err := assertAdminICloudOwnerEligibleTx(ctx, tx, nextOwnerID); err != nil {
				return err
			}
		}

		updates := make(map[string]any)
		credentialChanged := emailChanged || appPasswordChanged || channelsChanged
		nextCredentialRevision := resource.CredentialRevision
		if credentialChanged {
			nextCredentialRevision++
			if nextCredentialRevision == 0 {
				nextCredentialRevision = 1
			}
			updates["credential_revision"] = nextCredentialRevision
			updates["credential_updated_at"] = now
		}
		if imported != nil {
			if emailChanged {
				updates["primary_email"] = imported.PrimaryEmail
				updates["imap_uid_validity"] = ""
				updates["imap_last_uid"] = 0
				updates["imap_last_sync_at"] = nil
				updates["alias_count"] = 0
				updates["alias_provision_candidate"] = ""
				updates["alias_provision_reconcile"] = false
				updates["last_alias_sync_at"] = nil
			}
			if appPasswordChanged {
				updates["imap_app_password"] = imported.AppPassword
			}
			if channelsChanged {
				updates["alias_provision_candidate"] = ""
				updates["alias_provision_reconcile"] = false
			}
		}
		if nextForSale != resource.ForSale {
			updates["for_sale"] = nextForSale
		}
		if expireAtChanged {
			updates["expire_at"] = *command.ExpireAt
		}

		imapChanged := emailChanged || appPasswordChanged
		queuedGeneration := uint64(0)
		if imapChanged {
			if resource.Status != iCloudResourceDisabled {
				queuedGeneration = queueAdminICloudValidation(updates, resource, now)
				queuedValidation = true
			} else {
				updates["next_validation_at"] = nil
			}
		}
		provisionExpireAt := resource.ExpireAt
		if command.ExpireAt != nil {
			provisionExpireAt = *command.ExpireAt
		}
		if channelsChanged || expireAtChanged {
			if !imapChanged && resource.Status == iCloudResourceNormal && provisionExpireAt.After(now) && resource.AliasCount < iCloudMaxAliases {
				updates["next_provision_at"] = now
				queuedProvision = true
			} else if !provisionExpireAt.After(now) || resource.AliasCount >= iCloudMaxAliases {
				updates["next_provision_at"] = nil
			}
		}

		changed := len(updates) > 0 || ownerChanged || channelsChanged
		if len(updates) > 0 {
			updates["updated_at"] = now
			updated := tx.WithContext(ctx).Model(&iCloudResourceModel{}).Where("id = ?", command.ResourceID).Updates(updates)
			if updated.Error != nil {
				if isICloudDuplicateError(updated.Error) {
					return ErrICloudResourceIdentity
				}
				return updated.Error
			}
		}
		if emailChanged {
			if err := tx.WithContext(ctx).Model(&iCloudAliasModel{}).
				Where("resource_id = ? AND status <> ?", command.ResourceID, iCloudResourceDeleted).
				Updates(map[string]any{"status": "missing", "updated_at": now}).Error; err != nil {
				return err
			}
		}
		if channelsChanged {
			if err := upsertICloudImportChannelsTx(tx, command.ResourceID, imported.Channels, now); err != nil {
				return err
			}
		}
		if queuedValidation {
			if _, err := ensureICloudMaintenanceRunTx(ctx, tx, command.ResourceID, queuedGeneration, nextCredentialRevision, 0, now); err != nil {
				return err
			}
		}
		if changed {
			rootUpdates := map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}
			if ownerChanged {
				rootUpdates["owner_user_id"] = nextOwnerID
			}
			updated := tx.WithContext(ctx).Model(&iCloudRootModel{}).
				Where("id = ? AND type = ? AND version = ?", command.ResourceID, "icloud", root.Version).Updates(rootUpdates)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrICloudResourceVersion
			}
			root.Version++
		}
		if status, ok := updates["status"].(string); ok {
			resource.Status = status
		}
		if forSale, ok := updates["for_sale"].(bool); ok {
			resource.ForSale = forSale
		}
		if queuedGeneration > 0 {
			resource.ValidationGeneration = queuedGeneration
		}
		*result = *adminICloudMutationResult(root, resource)
		result.Changed = changed

		summary := "iCloud resource already matched the requested edit."
		if changed {
			summary = "iCloud resource edit applied."
		}
		if err := s.operationLogs.CreateInTx(ctx, tx, &governancedomain.OperationLog{
			OperatorUserID: command.OperatorUserID, OperationType: "icloud.admin_resource.edit",
			ResourceType: "icloud_resource", ResourceID: strconv.FormatUint(uint64(command.ResourceID), 10),
			Path: strings.TrimSpace(command.Path), Result: "success", SafeSummary: summary,
			RequestID: strings.TrimSpace(command.RequestID),
		}); err != nil {
			return err
		}
		return s.completeAdminICloudCommand(ctx, tx, command.OperatorUserID, idempotencyKey, result)
	})
	if err != nil {
		return nil, normalizeAdminICloudCommandError(err)
	}
	if !replayed && queuedValidation {
		_ = s.ScheduleICloudValidationDispatcher(context.WithoutCancel(ctx), 0)
	}
	if !replayed && queuedProvision {
		_ = s.ScheduleICloudProvisionDispatcher(context.WithoutCancel(ctx), 0)
	}
	return result, nil
}

func sameICloudChannels(existing []iCloudResourceChannelModel, imported []iCloudImportChannel) bool {
	if len(existing) != len(imported) {
		return false
	}
	byKind := make(map[string]iCloudResourceChannelModel, len(existing))
	for _, channel := range existing {
		byKind[channel.Kind] = channel
	}
	for _, channel := range imported {
		current, ok := byKind[channel.Kind]
		if !ok || current.Host != strings.TrimSpace(channel.Host) || current.Cookie != strings.TrimSpace(channel.Cookie) ||
			current.Origin != strings.TrimSpace(channel.Origin) || current.Referer != strings.TrimSpace(channel.Referer) ||
			current.UserAgent != strings.TrimSpace(channel.UserAgent) || current.DSID != strings.TrimSpace(channel.DSID) ||
			current.ClientID != strings.TrimSpace(channel.ClientID) || current.ClientBuildNumber != strings.TrimSpace(channel.ClientBuildNumber) ||
			current.ClientMasteringNumber != strings.TrimSpace(channel.ClientMasteringNumber) || current.Scnt != strings.TrimSpace(channel.Scnt) {
			return false
		}
	}
	return true
}

func assertUniqueAdminICloudEmailTx(ctx context.Context, tx *gorm.DB, resourceID uint, primaryEmail string) error {
	var count int64
	if err := tx.WithContext(ctx).Model(&iCloudResourceModel{}).
		Where("id <> ? AND LOWER(primary_email) = ?", resourceID, strings.ToLower(strings.TrimSpace(primaryEmail))).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrICloudResourceIdentity
	}
	return nil
}
