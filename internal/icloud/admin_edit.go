package icloud

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/kitesim"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrICloudResourceUpdate   = errors.New("icloud: invalid resource update")
	ErrICloudResourceIdentity = errors.New("icloud: duplicate resource identity")
)

type AdminICloudEditCommand struct {
	ResourceID      uint
	Version         uint64
	ImportLine      *string
	FamilyInviteURL *string
	PhoneID         *uint
	PhoneNumber     *string
	OwnerUserID     *uint
	ForSale         *bool
	ExpireAt        *time.Time
	OperatorUserID  uint
	IdempotencyKey  string
	RequestID       string
	Path            string
}

func (s *Service) EditAdminICloudResource(ctx context.Context, command AdminICloudEditCommand) (*AdminICloudMutationResult, error) {
	if s == nil || s.db == nil || s.operationLogs == nil || command.ResourceID == 0 || command.Version == 0 ||
		command.OperatorUserID == 0 || (command.ImportLine == nil && command.FamilyInviteURL == nil && command.PhoneID == nil && command.PhoneNumber == nil && command.OwnerUserID == nil && command.ForSale == nil && command.ExpireAt == nil) {
		return nil, ErrICloudResourceUpdate
	}
	if command.PhoneID != nil && (*command.PhoneID == 0 || command.PhoneNumber == nil) {
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
	if command.FamilyInviteURL != nil {
		value := strings.TrimSpace(*command.FamilyInviteURL)
		if value != "" && !validICloudFamilyInvite(value) {
			return nil, ErrICloudResourceUpdate
		}
		command.FamilyInviteURL = &value
	}
	if command.PhoneNumber != nil {
		value := strings.TrimSpace(*command.PhoneNumber)
		if value == "" {
			return nil, ErrICloudResourceUpdate
		}
		command.PhoneNumber = &value
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
		Version         uint64     `json:"version"`
		ImportLine      *string    `json:"importLine,omitempty"`
		FamilyInviteURL *string    `json:"familyInviteUrl,omitempty"`
		PhoneID         *uint      `json:"phoneId,omitempty"`
		PhoneNumber     *string    `json:"phoneNumber,omitempty"`
		OwnerID         *uint      `json:"ownerId,omitempty"`
		ForSale         *bool      `json:"forSale,omitempty"`
		ExpireAt        *time.Time `json:"expireAt,omitempty"`
	}{command.Version, command.ImportLine, command.FamilyInviteURL, command.PhoneID, command.PhoneNumber, command.OwnerUserID, command.ForSale, command.ExpireAt})
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
		emailChanged := imported != nil && !strings.EqualFold(strings.TrimSpace(imported.PrimaryEmail), strings.TrimSpace(resource.PrimaryEmail))
		credentialsSubmitted := imported != nil
		accountIdentityChanged := emailChanged
		if accountIdentityChanged && (firstNonEmpty(strings.TrimSpace(resource.AccountRole), "unknown") != "unknown" || resource.KitesimPhoneID != nil) {
			return ErrICloudResourceUpdate
		}
		silentCredentialRefresh := credentialsSubmitted && !accountIdentityChanged && resource.Status == iCloudResourceNormal
		webChannelSubmitted := false
		if imported != nil {
			for _, channel := range imported.Channels {
				if channel.Kind == iCloudChannelWeb {
					webChannelSubmitted = true
					break
				}
			}
		}
		ownerChanged := command.OwnerUserID != nil && *command.OwnerUserID != root.OwnerUserID
		expireAtChanged := command.ExpireAt != nil && !command.ExpireAt.Equal(resource.ExpireAt)
		familyInviteChanged := command.FamilyInviteURL != nil && *command.FamilyInviteURL != strings.TrimSpace(resource.FamilyInviteURL)
		if (emailChanged || ownerChanged) && assertNoActiveAdminICloudAllocationTx(ctx, tx, command.ResourceID) != nil {
			return ErrICloudResourceAllocation
		}
		if emailChanged {
			if err := assertUniqueAdminICloudEmailTx(ctx, tx, command.ResourceID, imported.PrimaryEmail); err != nil {
				return err
			}
		}
		phoneChanged := false
		if command.PhoneID != nil {
			phoneChanged = resource.KitesimPhoneID == nil || *resource.KitesimPhoneID != *command.PhoneID ||
				!sameICloudPhoneNumber(*command.PhoneNumber, resource.BoundPhoneNumber)
		} else if command.PhoneNumber != nil {
			phoneChanged = resource.KitesimPhoneID == nil || !sameICloudPhoneNumber(*command.PhoneNumber, resource.BoundPhoneNumber)
		}
		var phoneBinding *kitesim.SMSPhoneBinding
		if phoneChanged {
			existing := iCloudOnboardingExistingResource{
				ID: resource.ID, PrimaryEmail: resource.PrimaryEmail,
				BoundPhoneNumber: resource.BoundPhoneNumber, KitesimPhoneID: resource.KitesimPhoneID,
			}
			if err := s.validateICloudOnboardingPhoneExclusivityTx(
				tx,
				[]iCloudOnboardingLine{{PrimaryEmail: resource.PrimaryEmail, PhoneNumber: *command.PhoneNumber}},
				map[string]iCloudOnboardingExistingResource{iCloudImportEmailKey(resource.PrimaryEmail): existing},
			); err != nil {
				return err
			}
			bindingEmail := resource.PrimaryEmail
			if emailChanged {
				bindingEmail = imported.PrimaryEmail
			}
			var binding kitesim.SMSPhoneBinding
			var err error
			if command.PhoneID != nil {
				rebinder, ok := s.smsPhones.(interface {
					RebindICloudSMSPhoneByIDTx(context.Context, *gorm.DB, string, uint, string) (kitesim.SMSPhoneBinding, error)
				})
				if !ok {
					return ErrICloudResourceUpdate
				}
				binding, err = rebinder.RebindICloudSMSPhoneByIDTx(ctx, tx, bindingEmail, *command.PhoneID, *command.PhoneNumber)
			} else {
				rebinder, ok := s.smsPhones.(interface {
					RebindICloudSMSPhoneTx(context.Context, *gorm.DB, string, string) (kitesim.SMSPhoneBinding, error)
				})
				if !ok {
					return ErrICloudResourceUpdate
				}
				binding, err = rebinder.RebindICloudSMSPhoneTx(ctx, tx, bindingEmail, *command.PhoneNumber)
			}
			if err != nil {
				if errors.Is(err, kitesim.ErrPhoneMissing) || errors.Is(err, kitesim.ErrInvalidInput) || errors.Is(err, kitesim.ErrSMSPhoneNumberAmbiguous) {
					return ErrICloudResourceUpdate
				}
				return err
			}
			phoneBinding = &binding
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
		nextCredentialRevision := resource.CredentialRevision
		if credentialsSubmitted {
			nextCredentialRevision++
			if nextCredentialRevision == 0 {
				nextCredentialRevision = 1
			}
			updates["credential_revision"] = nextCredentialRevision
			updates["credential_updated_at"] = now
		}
		if imported != nil {
			if accountIdentityChanged {
				updates["selected_forward_to"] = ""
				updates["required_forward_to"] = ""
				updates["alias_count"] = 0
				updates["last_alias_sync_at"] = nil
			}
			if accountIdentityChanged || webChannelSubmitted {
				updates["alias_provision_candidate"] = ""
				updates["alias_provision_reconcile"] = false
			}
			if emailChanged {
				updates["primary_email"] = imported.PrimaryEmail
			}
		}
		if nextForSale != resource.ForSale {
			updates["for_sale"] = nextForSale
		}
		if expireAtChanged {
			updates["expire_at"] = *command.ExpireAt
		}
		if familyInviteChanged {
			updates["family_invite_url"] = *command.FamilyInviteURL
			if isICloudFamilyInviteFailure(resource.FamilySyncErrorCategory) {
				updates["family_sync_error_category"] = ""
			}
		}
		if phoneBinding != nil {
			phoneID := phoneBinding.PhoneID
			updates["bound_phone_number"] = phoneBinding.PhoneNumber
			countryCode := strings.ToUpper(strings.TrimSpace(phoneBinding.CountryCode))
			if countryCode == "" {
				countryCode = strings.ToUpper(strings.TrimSpace(resource.CountryCode))
			}
			if countryCode == "" {
				countryCode = CountryCodeFromICloudRegion(resource.Region)
			}
			if countryCode == "" {
				countryCode = strings.ToUpper(strings.TrimSpace(resource.BoundPhoneCountryCode))
			}
			updates["bound_phone_country_code"] = countryCode
			updates["bound_phone_source"] = "manual"
			updates["kitesim_phone_id"] = phoneID
		}

		queuedGeneration := uint64(0)
		if credentialsSubmitted {
			switch {
			case resource.Status == iCloudResourceDisabled:
				updates["next_validation_at"] = nil
			case silentCredentialRefresh:
				queuedGeneration = queueAdminICloudCredentialCheck(updates, resource, now)
				queuedValidation = true
			default:
				queuedGeneration = queueAdminICloudValidation(updates, resource, now)
				queuedValidation = true
			}
		}
		provisionExpireAt := resource.ExpireAt
		if command.ExpireAt != nil {
			provisionExpireAt = *command.ExpireAt
		}
		if expireAtChanged && !credentialsSubmitted {
			if resource.Status == iCloudResourceNormal && provisionExpireAt.After(now) && resource.AliasCount < iCloudMaxAliases {
				updates["next_provision_at"] = now
				queuedProvision = true
			} else if !provisionExpireAt.After(now) || resource.AliasCount >= iCloudMaxAliases {
				updates["next_provision_at"] = nil
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
		if accountIdentityChanged {
			if err := tx.WithContext(ctx).Model(&iCloudAliasModel{}).
				Where("resource_id = ? AND status <> ?", command.ResourceID, iCloudResourceDeleted).
				Updates(map[string]any{"status": "missing", "updated_at": now}).Error; err != nil {
				return err
			}
		}
		if credentialsSubmitted {
			if err := upsertICloudImportChannelsTx(tx, command.ResourceID, imported.Channels, accountIdentityChanged, now); err != nil {
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
