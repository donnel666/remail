package icloud

import (
	"context"
	"encoding/json"
	"errors"
	"net/mail"
	"strconv"
	"strings"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// StandaloneValidatedAccount is the result of the interactive validator. It
// is deliberately the same credential/channel shape used by the normal
// iCloud import path, so the resource workers can take over after the commit.
type StandaloneValidatedAccount struct {
	Email                string
	Region               string
	CountryCode          string
	AccountRole          string
	ICloudOpened         bool
	FamilyInviteURL      string
	PhoneNumber          string
	PhoneCountryCode     string
	PhoneSource          string
	KitesimPhoneID       *uint
	ForwardToEmail       string
	ForwardPreparationID uint
	ExpireAt             time.Time
	Secret               AppleOnboardingSecret
	OldChannel           *AppleOnboardingChannel
	NewChannel           *AppleOnboardingChannel
}

type StandaloneCommitResult struct {
	ResourceID           uint
	ValidationGeneration uint64
	CredentialRevision   uint64
	Created              bool
	ValidationScheduled  bool
}

// CommitStandaloneValidatedAccount stores a completed CMD run in the normal
// resource tables and queues the existing validation worker. No onboarding
// task or batch record is created.
func (s *Service) CommitStandaloneValidatedAccount(
	ctx context.Context,
	ownerUserID uint,
	account StandaloneValidatedAccount,
	requestID string,
) (StandaloneCommitResult, error) {
	if s == nil || s.db == nil || ownerUserID == 0 || s.operationLogs == nil {
		return StandaloneCommitResult{}, ErrICloudImportDependency
	}
	if ctx == nil {
		ctx = context.Background()
	}
	email := strings.ToLower(strings.TrimSpace(account.Email))
	if !validStandaloneICloudEmail(email) || !validStandaloneICloudRole(account.AccountRole) || strings.TrimSpace(account.Secret.Password) == "" {
		return StandaloneCommitResult{}, ErrICloudImportInvalid
	}
	expireAt := accountExpireAt(account)
	if !validICloudResourceExpireAt(expireAt, s.now().UTC()) {
		return StandaloneCommitResult{}, ErrICloudImportInvalid
	}
	birthday, err := time.Parse("2006-01-02", strings.TrimSpace(account.Secret.Birthday))
	if err != nil {
		return StandaloneCommitResult{}, ErrICloudImportInvalid
	}
	if account.NewChannel == nil || strings.TrimSpace(account.NewChannel.Cookie) == "" {
		return StandaloneCommitResult{}, errors.New("icloud: new Apple Account channel is missing")
	}
	newChannel := standaloneChannel(*account.NewChannel, iCloudChannelAppleAccount)
	if newChannel == nil {
		return StandaloneCommitResult{}, errors.New("icloud: new Apple Account channel is invalid")
	}
	var oldChannel *iCloudImportChannel
	if account.OldChannel != nil {
		oldChannel = standaloneChannel(*account.OldChannel, iCloudChannelWeb)
		if oldChannel == nil {
			return StandaloneCommitResult{}, errors.New("icloud: old iCloud channel is invalid")
		}
	}
	if account.ICloudOpened && oldChannel == nil {
		return StandaloneCommitResult{}, errors.New("icloud: old iCloud channel is missing")
	}
	answers, err := json.Marshal(account.Secret.SecurityAnswers)
	if err != nil {
		return StandaloneCommitResult{}, ErrICloudImportInvalid
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		requestID = platform.NewUUIDV7String()
	}
	if len(requestID) > 64 {
		requestID = requestID[:64]
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	forwardTo := strings.ToLower(strings.TrimSpace(account.ForwardToEmail))
	if forwardTo != "" && !validICloudHMEEmail(forwardTo) {
		return StandaloneCommitResult{}, ErrICloudImportInvalid
	}

	var result StandaloneCommitResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := assertAdminICloudOwnerEligibleTx(ctx, tx, ownerUserID); err != nil {
			return err
		}
		var resource iCloudResourceModel
		findErr := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("LOWER(primary_email) = ?", email).Take(&resource).Error
		created := errors.Is(findErr, gorm.ErrRecordNotFound)
		if findErr != nil && !created {
			return findErr
		}
		// New standalone imports carry their own invitation URL. Do not resolve
		// or create a synthetic primary account; retain a historical pointer only
		// when an existing resource already has one.
		var familyPrimaryID *uint
		if !created && account.AccountRole == "child" && resource.FamilyPrimaryResourceID != nil {
			value := *resource.FamilyPrimaryResourceID
			familyPrimaryID = &value
		}
		consumePreparation := false
		if account.ForwardPreparationID != 0 {
			var preparation iCloudImportPreparationModel
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND operator_user_id = ?", account.ForwardPreparationID, ownerUserID).
				First(&preparation).Error; err != nil {
				return ErrICloudImportPreparationConflict
			}
			matches := strings.EqualFold(strings.TrimSpace(preparation.ForwardToEmail), forwardTo)
			if !matches {
				return ErrICloudImportPreparationConflict
			}
			if preparation.ConsumedAt != nil {
				if created || !strings.EqualFold(strings.TrimSpace(resource.SelectedForwardTo), forwardTo) {
					return ErrICloudImportPreparationConflict
				}
			} else {
				consumePreparation = true
			}
		}
		var root iCloudRootModel
		if created {
			root = iCloudRootModel{Type: "icloud", OwnerUserID: ownerUserID, Version: 1, CreatedAt: now, UpdatedAt: now}
			if err := tx.WithContext(ctx).Create(&root).Error; err != nil {
				if isICloudDuplicateError(err) {
					return ErrICloudResourceIdentity
				}
				return err
			}
			resource = iCloudResourceModel{ID: root.ID, ResourceType: "icloud", PrimaryEmail: email,
				AccountRole: account.AccountRole, Region: strings.TrimSpace(account.Region),
				FamilyPrimaryResourceID: familyPrimaryID, FamilyInviteURL: strings.TrimSpace(account.FamilyInviteURL),
				CountryCode: strings.ToUpper(strings.TrimSpace(account.CountryCode)), ICloudOpened: account.ICloudOpened,
				BoundPhoneNumber: strings.TrimSpace(account.PhoneNumber), BoundPhoneCountryCode: strings.ToUpper(strings.TrimSpace(account.PhoneCountryCode)),
				BoundPhoneSource: standalonePhoneSource(account.PhoneSource), KitesimPhoneID: account.KitesimPhoneID,
				FamilySyncStatus: iCloudFamilySyncUnknown, SelectedForwardTo: forwardTo, RequiredForwardTo: forwardTo,
				ExpireAt: expireAt, ForSale: false, Status: iCloudResourcePending,
				CredentialRevision: 1, CredentialUpdatedAt: now, ValidationGeneration: 1,
				NextValidationAt: &now, CreatedAt: now, UpdatedAt: now}
			if err := tx.WithContext(ctx).Create(&resource).Error; err != nil {
				if isICloudDuplicateError(err) {
					return ErrICloudResourceIdentity
				}
				return err
			}
			result = StandaloneCommitResult{ResourceID: resource.ID, ValidationGeneration: 1, CredentialRevision: 1, Created: true}
		} else {
			if resource.Status == iCloudResourceDeleted || resource.Status == iCloudResourceDisabled {
				return ErrICloudResourceStatus
			}
			if rootErr := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND type = ?", resource.ID, "icloud").Take(&root).Error; rootErr != nil {
				return rootErr
			}
			if root.OwnerUserID != ownerUserID {
				return ErrICloudResourceOwner
			}
			if resource.AccountRole != "" && resource.AccountRole != "unknown" && resource.AccountRole != account.AccountRole {
				return ErrICloudResourceIdentity
			}
			if resource.BoundPhoneNumber != "" && account.PhoneNumber != "" && !sameICloudPhoneNumber(resource.BoundPhoneNumber, account.PhoneNumber) {
				return ErrICloudResourceIdentity
			}
			generation := resource.ValidationGeneration + 1
			if generation == 0 {
				generation = 1
			}
			revision := resource.CredentialRevision + 1
			if revision == 1 {
				revision = 2
			}
			status := iCloudResourcePending
			// A Cookie refresh is maintenance, not an unpublish operation. Keep a
			// usable resource normal so allocation and sale state stay unchanged;
			// the validation worker will use its preserve-status path.
			if resource.Status == iCloudResourceNormal {
				status = resource.Status
			}
			effectiveForward := forwardTo
			if effectiveForward == "" {
				effectiveForward = strings.ToLower(strings.TrimSpace(resource.SelectedForwardTo))
			}
			region := firstNonEmpty(strings.TrimSpace(account.Region), resource.Region)
			countryCode := firstNonEmpty(strings.ToUpper(strings.TrimSpace(account.CountryCode)), resource.CountryCode)
			phoneNumber := firstNonEmpty(strings.TrimSpace(account.PhoneNumber), resource.BoundPhoneNumber)
			phoneCountryCode := firstNonEmpty(strings.ToUpper(strings.TrimSpace(account.PhoneCountryCode)), resource.BoundPhoneCountryCode)
			phoneSource := standalonePhoneSource(firstNonEmpty(strings.TrimSpace(account.PhoneSource), resource.BoundPhoneSource))
			phoneID := account.KitesimPhoneID
			if phoneID == nil {
				phoneID = resource.KitesimPhoneID
			}
			updates := map[string]any{
				"account_role": account.AccountRole, "region": region,
				"family_primary_resource_id": familyPrimaryID,
				"country_code":               countryCode, "icloud_opened": resource.ICloudOpened || account.ICloudOpened,
				"bound_phone_number": phoneNumber, "bound_phone_country_code": phoneCountryCode,
				"bound_phone_source": phoneSource, "kitesim_phone_id": phoneID,
				"expire_at": expireAt, "status": status,
				"credential_revision": revision, "credential_updated_at": now, "validation_generation": generation,
				"validation_failures": 0, "next_validation_at": now, "next_provision_at": nil,
				"last_checked_at": nil, "last_valid_at": nil, "last_safe_error": "", "updated_at": now,
				"selected_forward_to": effectiveForward, "required_forward_to": effectiveForward,
				"task_kind": "", "onboarding_status": "", "stage": "", "dispatch_status": "",
				"import_id": nil, "resource_id": nil, "secret_payload": nil, "session_payload": nil,
				"manual_verification_code": "", "pending_sms_purpose": "", "sms_sent_at": nil,
				"sms_poll_deadline": nil, "forward_preparation_id": nil, "claim_token": "",
				"generation": 0, "expected_credential_revision": 0, "attempts": 0, "max_attempts": 0, "stage_attempts": 0,
				"next_attempt_at": nil, "last_error_category": "", "started_at": nil, "finished_at": nil,
				"icloud_activation_confirmed_at": nil, "onboarding_operator_user_id": 0, "onboarding_request_id": "",
				"onboarding_idempotency_key": "", "onboarding_request_fingerprint": "",
			}
			if invite := strings.TrimSpace(account.FamilyInviteURL); invite != "" {
				updates["family_invite_url"] = invite
			}
			if err := tx.WithContext(ctx).Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).Updates(updates).Error; err != nil {
				return err
			}
			if err := tx.WithContext(ctx).Model(&iCloudRootModel{}).Where("id = ? AND version = ?", root.ID, root.Version).
				Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
				return err
			}
			result = StandaloneCommitResult{ResourceID: resource.ID, ValidationGeneration: generation, CredentialRevision: revision}
		}

		if err := tx.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "resource_id"}}, DoUpdates: clause.AssignmentColumns([]string{
			"apple_password", "security_answers", "birthday", "updated_at",
		})}).Create(&iCloudResourceCredentialModel{ResourceID: result.ResourceID, ApplePassword: account.Secret.Password,
			SecurityAnswers: iCloudJSON(answers), Birthday: birthday, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			return err
		}
		channels := []iCloudImportChannel{*newChannel}
		if oldChannel != nil {
			channels = append(channels, *oldChannel)
		}
		if err := upsertICloudImportChannelsTx(tx, result.ResourceID, channels, result.Created, now); err != nil {
			return err
		}
		if _, err := ensureICloudValidationRunTx(ctx, tx, result.ResourceID, result.ValidationGeneration, result.CredentialRevision, now); err != nil {
			return err
		}
		if consumePreparation {
			consumed := tx.Model(&iCloudImportPreparationModel{}).
				Where("id = ? AND operator_user_id = ? AND consumed_at IS NULL", account.ForwardPreparationID, ownerUserID).
				Updates(map[string]any{"consumed_at": now, "updated_at": now})
			if consumed.Error != nil || consumed.RowsAffected != 1 {
				return ErrICloudImportPreparationConflict
			}
		}
		return s.operationLogs.CreateInTx(ctx, tx, &governancedomain.OperationLog{
			OperatorUserID: ownerUserID, OperationType: "icloud.standalone.validate",
			ResourceType: "icloud_resource", ResourceID: strconv.FormatUint(uint64(result.ResourceID), 10),
			Path: "cmd/icloudvalidate", Result: "success", SafeSummary: "Standalone iCloud validation credentials committed.", RequestID: requestID,
		})
	})
	if err != nil {
		return StandaloneCommitResult{}, err
	}
	if s.queue != nil {
		if err := s.ScheduleICloudValidationDispatcher(context.WithoutCancel(ctx), 0); err == nil {
			result.ValidationScheduled = true
		}
	}
	return result, nil
}

func validStandaloneICloudEmail(value string) bool {
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address == value && isICloudImportEmail(value)
}

func validStandaloneICloudRole(value string) bool {
	return value == "unknown" || value == "primary" || value == "child"
}

func accountExpireAt(account StandaloneValidatedAccount) time.Time {
	if !account.ExpireAt.IsZero() {
		return account.ExpireAt.UTC()
	}
	return time.Now().UTC().AddDate(0, 0, 30)
}

func standalonePhoneSource(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "matched":
		return "manual"
	case "automatic":
		return "kitesim"
	default:
		return strings.TrimSpace(value)
	}
}

func standaloneChannel(channel AppleOnboardingChannel, expectedKind string) *iCloudImportChannel {
	if strings.TrimSpace(channel.Cookie) == "" {
		return nil
	}
	if strings.TrimSpace(channel.Kind) != "" && strings.TrimSpace(channel.Kind) != expectedKind {
		return nil
	}
	channel.Kind = expectedKind
	return &iCloudImportChannel{Kind: expectedKind, Host: channel.Host, Cookie: channel.Cookie, Origin: channel.Origin,
		Referer: channel.Referer, UserAgent: channel.UserAgent, FDClientInfo: channel.FDClientInfo, APIKey: channel.APIKey,
		DSID: channel.DSID, ClientID: channel.ClientID, ClientBuildNumber: channel.ClientBuildNumber,
		ClientMasteringNumber: channel.ClientMasteringNumber, Scnt: channel.Scnt}
}
