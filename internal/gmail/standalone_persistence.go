package gmail

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type StandaloneCommitResult struct {
	ResourceID           uint
	ValidationGeneration uint64
	CredentialRevision   uint64
	HistoryQueued        bool
}

// CommitStandaloneValidatedCredentials applies a browser-validated account to
// the same Gmail resource state machine used by asynchronous validation.
func (s *Service) CommitStandaloneValidatedCredentials(
	ctx context.Context,
	ownerUserID uint,
	credential StandaloneCredential,
	rotation StandaloneRotationResult,
	requestID string,
) (StandaloneCommitResult, error) {
	if s == nil || s.db == nil || ownerUserID == 0 {
		return StandaloneCommitResult{}, ErrLocalValidationDependency
	}
	if ctx == nil {
		ctx = context.Background()
	}
	email := strings.ToLower(strings.TrimSpace(credential.Email))
	identity, ok := localGmailIdentity(email)
	if !ok || strings.TrimSpace(credential.Password) == "" {
		return StandaloneCommitResult{}, ErrInvalidLocalResource
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		requestID = platform.NewUUIDV7String()
	}
	if len(requestID) > 64 {
		requestID = requestID[:64]
	}

	twoFactor := strings.ToUpper(removeWhitespace(rotation.TwoFactorSecret))
	appPassword := removeWhitespace(rotation.AppPassword)
	complete := rotation.Err == nil
	if complete && !validLocalGmailRotatedCredentials(twoFactor, appPassword) {
		return StandaloneCommitResult{}, errors.New("gmail returned incomplete replacement credentials")
	}
	if rotation.TwoFactorAuthoritative && !validLocalGmailTOTPSecret(twoFactor) {
		return StandaloneCommitResult{}, errors.New("gmail returned an invalid replacement 2FA secret")
	}
	if rotation.AppPasswordAuthoritative && !validLocalGmailAppPassword(appPassword) {
		return StandaloneCommitResult{}, errors.New("gmail returned an invalid replacement app password")
	}
	if !complete && !rotation.TwoFactorAuthoritative && !rotation.AppPasswordAuthoritative {
		return StandaloneCommitResult{}, rotation.Err
	}

	bindingEmail := strings.ToLower(strings.TrimSpace(credential.BindingEmail))
	if bindingEmail != "" && !validLocalGmailEmailAddress(bindingEmail) {
		return StandaloneCommitResult{}, ErrInvalidLocalResource
	}
	now := s.now().UTC()
	var committed StandaloneCommitResult
	var historyTask localGmailHistoryTask
	err := withLocalGmailValidationTransaction(ctx, s.dbFor(ctx), func(tx *gorm.DB) error {
		var owner struct {
			ID uint `gorm:"column:id"`
		}
		if err := tx.Table("users").Select("id").Where(
			"id = ? AND status = ? AND role IN ?", ownerUserID, "active", []string{"supplier", "admin", "super_admin"},
		).Take(&owner).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrInvalidLocalResource
			}
			return fmt.Errorf("validate Gmail standalone owner: %w", err)
		}

		var resource localResourceModel
		findErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("identity = ?", identity).Take(&resource).Error
		isNew := errors.Is(findErr, gorm.ErrRecordNotFound)
		if findErr != nil && !isNew {
			return fmt.Errorf("find Gmail standalone resource: %w", findErr)
		}
		if isNew {
			root := resourceRootModel{Type: "gmail", OwnerUserID: ownerUserID, Version: 1, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&root).Error; err != nil {
				return fmt.Errorf("create Gmail standalone resource root: %w", err)
			}
			resource = localResourceModel{
				ID: root.ID, ResourceType: "gmail", OwnerUserID: ownerUserID,
				Email: email, Identity: identity, Password: credential.Password, BindingEmail: bindingEmail,
				TwoFactorSecret: twoFactor, AppPassword: appPassword,
				CredentialRevision: 1, CredentialUpdatedAt: now, ForSale: false,
				Status: standaloneCommitStatus(complete, rotation.Temporary), ValidationGeneration: 1,
				ValidationRequestID: requestID, ValidationFailures: standaloneCommitFailures(complete, rotation.Temporary),
				LastSafeError: standaloneCommitSafeError(complete, rotation), LastCheckedAt: &now,
				AllocBucket: uint16(root.ID % localGmailAllocationBucketCount), CreatedAt: now, UpdatedAt: now,
			}
			if err := tx.Create(&resource).Error; err != nil {
				return fmt.Errorf("create Gmail standalone resource: %w", err)
			}
			committed = StandaloneCommitResult{ResourceID: resource.ID, ValidationGeneration: resource.ValidationGeneration, CredentialRevision: resource.CredentialRevision}
		} else {
			if resource.Status == LocalResourceDeleted || resource.Status == LocalResourceDisabled {
				return ErrInvalidLocalResource
			}
			if resource.OwnerUserID != ownerUserID {
				return ErrLocalResourceVersion
			}
			var activeAllocations int64
			if err := tx.Model(&allocationModel{}).Where("resource_id = ? AND status = ?", resource.ID, AllocationStatusAllocated).Count(&activeAllocations).Error; err != nil {
				return fmt.Errorf("check Gmail standalone allocations: %w", err)
			}
			if activeAllocations != 0 {
				return ErrLocalResourceBusy
			}
			var root resourceRootModel
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND type = ?", resource.ID, "gmail").Take(&root).Error; err != nil {
				return fmt.Errorf("lock Gmail standalone resource root: %w", err)
			}
			generation := resource.ValidationGeneration
			if generation == 0 {
				generation = 1
			} else {
				generation++
			}
			updates := map[string]any{
				"password": credential.Password, "validation_request_id": requestID,
				"status":                standaloneCommitStatus(complete, rotation.Temporary),
				"validation_failures":   standaloneCommitFailures(complete, rotation.Temporary),
				"validation_generation": generation,
				"last_safe_error":       standaloneCommitSafeError(complete, rotation), "last_checked_at": &now, "updated_at": now,
			}
			if bindingEmail != "" {
				updates["binding_email"] = bindingEmail
			}
			credentialChanged := resource.Password != credential.Password
			if complete || rotation.TwoFactorAuthoritative {
				updates["two_factor_secret"] = twoFactor
				credentialChanged = credentialChanged || resource.TwoFactorSecret != twoFactor
			}
			if complete || rotation.AppPasswordAuthoritative {
				updates["app_password"] = appPassword
				credentialChanged = credentialChanged || resource.AppPassword != appPassword
			}
			newRevision := resource.CredentialRevision
			if newRevision == 0 {
				newRevision = 1
			}
			if credentialChanged {
				newRevision++
				updates["credential_revision"] = newRevision
				updates["credential_updated_at"] = now
			}
			if err := tx.Model(&localResourceModel{}).Where("id = ?", resource.ID).Updates(updates).Error; err != nil {
				return fmt.Errorf("update Gmail standalone resource: %w", err)
			}
			if err := tx.Model(&resourceRootModel{}).Where("id = ? AND version = ?", root.ID, root.Version).
				Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
				return fmt.Errorf("bump Gmail standalone resource version: %w", err)
			}
			resource.ValidationGeneration = generation
			resource.CredentialRevision = newRevision
			committed = StandaloneCommitResult{ResourceID: resource.ID, ValidationGeneration: resource.ValidationGeneration, CredentialRevision: newRevision}
		}
		if complete {
			historyTask = localGmailHistoryTask{
				ResourceID: committed.ResourceID, OwnerUserID: ownerUserID,
				ValidationGeneration: committed.ValidationGeneration, ExpectedCredentialRevision: committed.CredentialRevision,
				RequestID: requestID,
			}
		}
		if s.logs != nil {
			return s.logs.CreateInTx(ctx, tx, &governancedomain.OperationLog{
				OperatorUserID: ownerUserID, OperationType: "gmail.standalone.validate",
				ResourceType: "gmail_resource", ResourceID: strconv.FormatUint(uint64(committed.ResourceID), 10),
				Path: "cmd/gmailvalidate", Result: "success",
				SafeSummary: "Gmail standalone validation credentials committed.", RequestID: requestID,
			})
		}
		return nil
	})
	if err != nil {
		return StandaloneCommitResult{}, err
	}
	if historyTask.ResourceID != 0 && s.queue != nil {
		enqueueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err := s.enqueueValidatedLocalGmailHistory(enqueueCtx, historyTask)
		cancel()
		if err != nil {
			_ = s.scheduleLocalResourceValidationDispatcher(context.WithoutCancel(ctx), time.Second)
			return committed, fmt.Errorf("enqueue Gmail standalone history: %w", err)
		}
		committed.HistoryQueued = true
	}
	return committed, nil
}

func standaloneCommitStatus(complete, temporary bool) string {
	if complete {
		return LocalResourceIdentifying
	}
	if temporary {
		return LocalResourcePending
	}
	return LocalResourceAbnormal
}

func standaloneCommitFailures(complete, temporary bool) int {
	if complete || temporary {
		return 0
	}
	return 1
}

func standaloneCommitSafeError(complete bool, rotation StandaloneRotationResult) string {
	if complete {
		return ""
	}
	if safe := strings.TrimSpace(rotation.SafeError); safe != "" {
		return safe
	}
	return "Gmail standalone validation failed."
}
