package icloud

import (
	"context"
	"errors"
	"strconv"
	"strings"

	coreapp "github.com/donnel666/remail/internal/core/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrICloudResourceUpdate   = errors.New("icloud: invalid resource update")
	ErrICloudResourceIdentity = errors.New("icloud: duplicate resource identity")
)

type AdminICloudCredentialsInput struct {
	Host                  string `json:"host"`
	DSID                  string `json:"dsid"`
	ClientID              string `json:"clientId"`
	ClientBuildNumber     string `json:"clientBuildNumber"`
	ClientMasteringNumber string `json:"clientMasteringNumber"`
	Cookie                string `json:"cookie"`
}

type AdminICloudEditCommand struct {
	ResourceID     uint
	Version        uint64
	PrimaryEmail   *string
	OwnerUserID    *uint
	ForSale        *bool
	Credentials    *AdminICloudCredentialsInput
	OperatorUserID uint
	IdempotencyKey string
	RequestID      string
	Path           string
}

func (s *Service) EditAdminICloudResource(ctx context.Context, command AdminICloudEditCommand) (*AdminICloudMutationResult, error) {
	if s == nil || s.db == nil || s.operationLogs == nil || command.ResourceID == 0 || command.Version == 0 ||
		command.OperatorUserID == 0 || (command.PrimaryEmail == nil && command.OwnerUserID == nil && command.ForSale == nil && command.Credentials == nil) {
		return nil, ErrICloudResourceUpdate
	}

	if command.PrimaryEmail != nil {
		value := strings.ToLower(strings.TrimSpace(*command.PrimaryEmail))
		if !isICloudImportEmail(value) {
			return nil, ErrICloudResourceUpdate
		}
		command.PrimaryEmail = &value
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
	if command.Credentials != nil {
		credentials, err := normalizeAdminICloudCredentials(*command.Credentials)
		if err != nil {
			return nil, err
		}
		command.Credentials = &credentials
	}
	idempotencyKey, err := normalizeAdminICloudIdempotencyKey(command.IdempotencyKey)
	if err != nil {
		return nil, ErrICloudResourceUpdate
	}
	fingerprint, err := adminICloudCommandFingerprint(struct {
		Version      uint64                       `json:"version"`
		PrimaryEmail *string                      `json:"primaryEmail,omitempty"`
		OwnerUserID  *uint                        `json:"ownerId,omitempty"`
		ForSale      *bool                        `json:"forSale,omitempty"`
		Credentials  *AdminICloudCredentialsInput `json:"credentials,omitempty"`
	}{command.Version, command.PrimaryEmail, command.OwnerUserID, command.ForSale, command.Credentials})
	if err != nil {
		return nil, ErrICloudResourceQueryTemporary
	}
	receipt := coreapp.AdminResourceCommandReceipt{
		OperatorUserID:     command.OperatorUserID,
		IdempotencyKey:     idempotencyKey,
		Operation:          "icloud.admin_resource.edit",
		Subject:            "icloud_resource:" + strconv.FormatUint(uint64(command.ResourceID), 10),
		RequestFingerprint: fingerprint,
	}

	result := &AdminICloudMutationResult{}
	replayed := false
	queuedValidation := false
	queuedGeneration := uint64(0)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		wasReplayed, reserveErr := s.reserveAdminICloudCommand(ctx, tx, receipt, result)
		if reserveErr != nil || wasReplayed {
			replayed = wasReplayed
			return reserveErr
		}

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

		emailChanged := command.PrimaryEmail != nil && *command.PrimaryEmail != resource.PrimaryEmail
		ownerChanged := command.OwnerUserID != nil && *command.OwnerUserID != root.OwnerUserID
		credentialsChanged := command.Credentials != nil &&
			(emailChanged || !sameAdminICloudCredentials(resource, *command.Credentials))
		identityChanged := emailChanged || ownerChanged || credentialsChanged
		if identityChanged {
			if err := assertNoActiveAdminICloudAllocationTx(ctx, tx, command.ResourceID); err != nil {
				return err
			}
		}
		if emailChanged && command.Credentials == nil {
			return ErrICloudResourceUpdate
		}
		if emailChanged {
			if err := assertUniqueAdminICloudIdentityTx(ctx, tx, command.ResourceID, *command.PrimaryEmail, ""); err != nil {
				return err
			}
		}
		if credentialsChanged {
			if err := assertUniqueAdminICloudIdentityTx(ctx, tx, command.ResourceID, "", command.Credentials.DSID); err != nil {
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
		if identityChanged {
			nextForSale = false
		}
		if nextForSale {
			if err := assertAdminICloudOwnerEligibleTx(ctx, tx, nextOwnerID); err != nil {
				return err
			}
		}

		now := s.now().UTC()
		updates := make(map[string]any)
		nextCredentialRevision := resource.CredentialRevision
		if emailChanged {
			updates["primary_email"] = *command.PrimaryEmail
		}
		if nextForSale != resource.ForSale {
			updates["for_sale"] = nextForSale
		}
		if credentialsChanged {
			credentials := *command.Credentials
			langCode, origin, referer := defaultICloudHMEContext(credentials.Host)
			updates["host"] = credentials.Host
			updates["dsid"] = credentials.DSID
			updates["client_id"] = credentials.ClientID
			updates["client_build_number"] = credentials.ClientBuildNumber
			updates["client_mastering_number"] = credentials.ClientMasteringNumber
			updates["cookie"] = credentials.Cookie
			updates["lang_code"] = langCode
			updates["origin"] = origin
			updates["referer"] = referer
			updates["user_agent"] = defaultICloudHMEUserAgent
			nextCredentialRevision = max(resource.CredentialRevision+1, 1)
			updates["credential_revision"] = nextCredentialRevision
			updates["credential_updated_at"] = now
			updates["alias_provision_candidate"] = ""
			updates["alias_provision_reconcile"] = false
			updates["session_status"] = iCloudSessionUnchecked
			updates["session_failures"] = 0
			updates["next_keepalive_at"] = nil
			updates["last_valid_at"] = nil
			updates["last_safe_error"] = ""
		}
		if identityChanged {
			queuedGeneration = queueAdminICloudValidation(updates, resource, now, emailChanged || credentialsChanged)
			if resource.Status == iCloudResourceDisabled {
				updates["status"] = iCloudResourceDisabled
				updates["next_validation_at"] = nil
				queuedGeneration = 0
			} else {
				queuedValidation = true
			}
		}

		changed := len(updates) > 0 || ownerChanged
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
		if queuedValidation {
			if _, err := ensureICloudMaintenanceRunTx(
				ctx, tx, command.ResourceID, queuedGeneration, iCloudMaintenanceValidation,
				nextCredentialRevision, 0, now,
			); err != nil {
				return err
			}
		} else if identityChanged {
			if err := cancelActiveICloudMaintenanceRunsTx(ctx, tx, command.ResourceID, 0, now); err != nil {
				return err
			}
		}
		if changed {
			rootUpdates := map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}
			if ownerChanged {
				rootUpdates["owner_user_id"] = nextOwnerID
			}
			updated := tx.WithContext(ctx).Model(&iCloudRootModel{}).
				Where("id = ? AND type = ? AND version = ?", command.ResourceID, "icloud", root.Version).
				Updates(rootUpdates)
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
			OperatorUserID: command.OperatorUserID,
			OperationType:  "icloud.admin_resource.edit",
			ResourceType:   "icloud_resource",
			ResourceID:     strconv.FormatUint(uint64(command.ResourceID), 10),
			Path:           strings.TrimSpace(command.Path),
			Result:         "success",
			SafeSummary:    summary,
			RequestID:      strings.TrimSpace(command.RequestID),
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
	return result, nil
}

func normalizeAdminICloudCredentials(input AdminICloudCredentialsInput) (AdminICloudCredentialsInput, error) {
	input.Host = strings.ToLower(strings.TrimSpace(input.Host))
	input.DSID = strings.TrimSpace(input.DSID)
	input.ClientID = strings.TrimSpace(input.ClientID)
	input.ClientBuildNumber = strings.TrimSpace(input.ClientBuildNumber)
	input.ClientMasteringNumber = strings.TrimSpace(input.ClientMasteringNumber)
	input.Cookie = strings.TrimSpace(input.Cookie)
	if !validICloudHMEHost(input.Host) ||
		!validICloudImportValue(input.DSID, iCloudImportDSIDMaxLength) ||
		!validICloudImportValue(input.ClientID, iCloudImportClientMaxLength) ||
		!validICloudImportValue(input.ClientBuildNumber, iCloudImportBuildMaxLength) ||
		!validICloudImportValue(input.ClientMasteringNumber, iCloudImportBuildMaxLength) ||
		!validICloudImportCookie(input.Cookie) {
		return AdminICloudCredentialsInput{}, ErrICloudResourceUpdate
	}
	return input, nil
}

func sameAdminICloudCredentials(resource iCloudResourceModel, credentials AdminICloudCredentialsInput) bool {
	return resource.Host == credentials.Host && resource.DSID == credentials.DSID &&
		resource.ClientID == credentials.ClientID && resource.ClientBuildNumber == credentials.ClientBuildNumber &&
		resource.ClientMasteringNumber == credentials.ClientMasteringNumber && resource.Cookie == credentials.Cookie
}

func assertUniqueAdminICloudIdentityTx(ctx context.Context, tx *gorm.DB, resourceID uint, primaryEmail, dsid string) error {
	for column, value := range map[string]string{"primary_email": primaryEmail, "dsid": dsid} {
		if value == "" {
			continue
		}
		var count int64
		if err := tx.WithContext(ctx).Model(&iCloudResourceModel{}).
			Where("id <> ? AND LOWER("+column+") = ?", resourceID, strings.ToLower(value)).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrICloudResourceIdentity
		}
	}
	return nil
}
