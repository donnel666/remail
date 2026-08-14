package icloud

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ProcessICloudValidation creates one alias through every imported channel.
// A resource is usable when at least one created alias forwards to an approved
// auxiliary domain; channel failures remain isolated on their channel rows.
func (s *Service) ProcessICloudValidation(ctx context.Context, task iCloudValidationTask) error {
	if s == nil || s.db == nil || task.ResourceID == 0 || task.OwnerUserID == 0 ||
		task.ValidationGeneration == 0 || task.ExpectedCredentialRevision == 0 {
		return ErrICloudValidationTemp
	}
	resource, found, err := s.iCloudValidationResource(ctx, task)
	if err != nil || !found {
		return err
	}
	now := s.now().UTC()
	allowedDomains := iCloudForwardingDomains(runtimeconfig.String(runtimeconfig.ICloudForwardingSuffixesKey, ""))
	if len(allowedDomains) == 0 {
		return s.applyICloudChannelValidationResult(ctx, task, "", "No authorized iCloud forwarding domain is configured.", true, true, time.Time{})
	}
	if !resource.ExpireAt.After(now) {
		return s.applyICloudChannelValidationResult(ctx, task, "", "The iCloud resource has expired and cannot create a validation alias.", false, false, time.Time{})
	}
	if resource.AliasCount >= iCloudMaxAliases {
		return s.applyICloudChannelValidationResult(ctx, task, "", "The iCloud alias limit has been reached.", false, false, time.Time{})
	}
	var channels []iCloudResourceChannelModel
	if err := s.db.WithContext(ctx).Order("CASE kind WHEN 'apple_account' THEN 0 ELSE 1 END").
		Where("resource_id = ?", resource.ID).Find(&channels).Error; err != nil {
		return ErrICloudValidationTemp
	}
	selectedForwardTo := ""
	nextValidationAt := time.Time{}
	countFailure := len(channels) == 0
	retryValidation := len(channels) == 0
	transientValidation := false
	var failures []error
	for _, original := range channels {
		currentResource, currentChannel, loadErr := s.loadICloudValidationProvisionScope(ctx, *resource, original.ID)
		if loadErr != nil {
			if errors.Is(loadErr, errICloudValidationStale) {
				return nil
			}
			retryValidation = true
			transientValidation = true
			nextValidationAt = earlierICloudProvisionAt(nextValidationAt, now.Add(iCloudValidationRetryInterval))
			failures = append(failures, loadErr)
			continue
		}
		if !currentResource.ExpireAt.After(now) || currentResource.AliasCount >= iCloudMaxAliases {
			failures = append(failures, fmt.Errorf("iCloud resource cannot create another alias"))
			continue
		}
		if currentChannel.SessionStatus == iCloudSessionInvalid {
			countFailure = true
			retryValidation = true
			failures = append(failures, fmt.Errorf("iCloud channel session is invalid"))
			continue
		}
		if currentChannel.CooldownUntil != nil && currentChannel.CooldownUntil.After(now) {
			retryValidation = true
			transientValidation = true
			nextValidationAt = earlierICloudProvisionAt(nextValidationAt, *currentChannel.CooldownUntil)
			continue
		}
		createAt, createAllowed := iCloudChannelWindow(currentChannel, now)
		if currentChannel.Kind == iCloudChannelWeb && strings.TrimSpace(currentResource.AliasProvisionCandidate) != "" {
			createAllowed = true
		}
		if !createAllowed {
			retryValidation = true
			transientValidation = true
			nextValidationAt = earlierICloudProvisionAt(nextValidationAt, createAt)
			continue
		}
		var alias *hmeAlias
		definiteAttemptFailure := false
		switch currentChannel.Kind {
		case iCloudChannelAppleAccount:
			alias, loadErr = s.provisionICloudAppleAccount(ctx, currentResource, currentChannel, true, now)
		case iCloudChannelWeb:
			var immediate bool
			alias, immediate, loadErr = s.provisionICloudWeb(ctx, currentResource, currentChannel, true, now)
			if loadErr == nil && immediate {
				currentResource, currentChannel, loadErr = s.loadICloudValidationProvisionScope(ctx, *resource, original.ID)
				if loadErr == nil {
					alias, _, loadErr = s.provisionICloudWeb(ctx, currentResource, currentChannel, true, now)
				}
			}
		default:
			definiteAttemptFailure = true
			countFailure = true
			retryValidation = true
			loadErr = fmt.Errorf("unsupported iCloud channel")
		}
		if errors.Is(loadErr, errICloudValidationStale) {
			return nil
		}
		if loadErr != nil {
			definiteFailure := definiteAttemptFailure || iCloudValidationErrorCountsFailure(loadErr)
			if definiteFailure {
				countFailure = true
				retryValidation = true
			}
			failures = append(failures, loadErr)
			if refreshedResource, refreshedChannel, refreshErr := s.loadICloudValidationProvisionScope(ctx, *resource, original.ID); refreshErr == nil {
				if retryAt := iCloudValidationChannelRetryAt(refreshedResource, refreshedChannel, now); !retryAt.IsZero() {
					retryValidation = true
					if !definiteFailure {
						transientValidation = true
					}
					nextValidationAt = earlierICloudProvisionAt(nextValidationAt, retryAt)
				}
			} else {
				retryValidation = true
				if !definiteFailure {
					transientValidation = true
				}
				nextValidationAt = earlierICloudProvisionAt(nextValidationAt, now.Add(iCloudValidationRetryInterval))
			}
			continue
		}
		if alias == nil {
			failures = append(failures, fmt.Errorf("iCloud channel did not create an alias"))
			if refreshedResource, refreshedChannel, refreshErr := s.loadICloudValidationProvisionScope(ctx, *resource, original.ID); refreshErr == nil {
				if retryAt := iCloudValidationChannelRetryAt(refreshedResource, refreshedChannel, now); !retryAt.IsZero() {
					retryValidation = true
					transientValidation = true
					nextValidationAt = earlierICloudProvisionAt(nextValidationAt, retryAt)
				}
			}
			continue
		}
		if !alias.Active || !iCloudForwardingDomainAllowed(alias.ForwardToEmail, allowedDomains) {
			countFailure = true
			retryValidation = true
			if alias.Active {
				_ = s.disableICloudAlias(ctx, resource.ID, alias.AnonymousID, now)
				failures = append(failures, fmt.Errorf("iCloud forwarding domain is not authorized"))
			} else {
				failures = append(failures, fmt.Errorf("iCloud alias is inactive"))
			}
			if refreshedResource, refreshedChannel, refreshErr := s.loadICloudValidationProvisionScope(ctx, *resource, original.ID); refreshErr == nil {
				nextValidationAt = earlierICloudProvisionAt(nextValidationAt, iCloudValidationChannelRetryAt(refreshedResource, refreshedChannel, now))
			}
			continue
		}
		if selectedForwardTo == "" {
			selectedForwardTo = strings.ToLower(strings.TrimSpace(alias.ForwardToEmail))
		}
	}
	message := ""
	if selectedForwardTo == "" {
		message = "No iCloud session created an alias for an authorized forwarding domain."
		if len(failures) == 0 {
			message = "No usable iCloud session is configured."
		}
	}
	if transientValidation {
		countFailure = false
	}
	return s.applyICloudChannelValidationResult(ctx, task, selectedForwardTo, message, countFailure, retryValidation, nextValidationAt)
}

func iCloudValidationErrorCountsFailure(requestErr error) bool {
	var hmeErr *hmeError
	if errors.As(requestErr, &hmeErr) {
		return hmeErr.Category == "session_invalid" || hmeErr.Category == "invalid_context"
	}
	var appleErr *appleAccountError
	if errors.As(requestErr, &appleErr) {
		return appleErr.Category == "session_invalid" || appleErr.Category == "invalid_context"
	}
	return false
}

func iCloudValidationChannelRetryAt(resource iCloudResourceModel, channel iCloudResourceChannelModel, now time.Time) time.Time {
	if channel.SessionStatus == iCloudSessionInvalid || !resource.ExpireAt.After(now) || resource.AliasCount >= iCloudMaxAliases {
		return time.Time{}
	}
	if channel.CooldownUntil != nil && channel.CooldownUntil.After(now) {
		return *channel.CooldownUntil
	}
	if channel.Kind != iCloudChannelWeb || strings.TrimSpace(resource.AliasProvisionCandidate) == "" {
		if ready, allowed := iCloudChannelWindow(channel, now); !allowed {
			return ready
		}
	}
	return now.Add(iCloudValidationRetryInterval)
}

func (s *Service) loadICloudValidationProvisionScope(ctx context.Context, expected iCloudResourceModel, channelID uint) (iCloudResourceModel, iCloudResourceChannelModel, error) {
	var resource iCloudResourceModel
	if err := s.db.WithContext(ctx).First(&resource, expected.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return iCloudResourceModel{}, iCloudResourceChannelModel{}, errICloudValidationStale
		}
		return iCloudResourceModel{}, iCloudResourceChannelModel{}, err
	}
	if resource.CredentialRevision != expected.CredentialRevision || resource.Status != iCloudResourceValidating {
		return iCloudResourceModel{}, iCloudResourceChannelModel{}, errICloudValidationStale
	}
	var channel iCloudResourceChannelModel
	if err := s.db.WithContext(ctx).Where("id = ? AND resource_id = ?", channelID, expected.ID).First(&channel).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return iCloudResourceModel{}, iCloudResourceChannelModel{}, errICloudValidationStale
		}
		return iCloudResourceModel{}, iCloudResourceChannelModel{}, err
	}
	return resource, channel, nil
}

func (s *Service) disableICloudAlias(ctx context.Context, resourceID uint, anonymousID string, now time.Time) error {
	return s.db.WithContext(ctx).Model(&iCloudAliasModel{}).
		Where("resource_id = ? AND anonymous_id = ?", resourceID, strings.TrimSpace(anonymousID)).
		Updates(map[string]any{"status": "disabled", "updated_at": now}).Error
}

func (s *Service) applyICloudChannelValidationResult(ctx context.Context, task iCloudValidationTask, selectedForwardTo, message string, countFailure, retryValidation bool, retryAt time.Time) error {
	now := s.now().UTC()
	allowedDomains := iCloudForwardingDomains(runtimeconfig.String(runtimeconfig.ICloudForwardingSuffixesKey, ""))
	selectedForwardTo = strings.ToLower(strings.TrimSpace(selectedForwardTo))
	if selectedForwardTo != "" && !iCloudForwardingDomainAllowed(selectedForwardTo, allowedDomains) {
		selectedForwardTo = ""
		message = "The iCloud forwarding domain is no longer authorized."
		countFailure = true
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root iCloudRootModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND type = ? AND owner_user_id = ?", task.ResourceID, "icloud", task.OwnerUserID).First(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		var resource iCloudResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&resource, task.ResourceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if resource.ValidationGeneration != task.ValidationGeneration ||
			resource.CredentialRevision != task.ExpectedCredentialRevision || resource.Status != iCloudResourceValidating {
			return errICloudValidationStale
		}
		status := iCloudResourceNormal
		failures := uint8(0)
		storedSelectedForwardTo := selectedForwardTo
		if storedSelectedForwardTo == "" && iCloudForwardingDomainAllowed(resource.SelectedForwardTo, allowedDomains) {
			storedSelectedForwardTo = strings.ToLower(strings.TrimSpace(resource.SelectedForwardTo))
		}
		var nextValidationAt *time.Time
		if selectedForwardTo == "" {
			status = iCloudResourcePending
			failures = resource.ValidationFailures
			if !countFailure && !retryValidation && resource.LastValidAt != nil {
				status = iCloudResourceNormal
				storedSelectedForwardTo = resource.SelectedForwardTo
			} else {
				if countFailure {
					failures = min(failures+1, uint8(iCloudValidationMaxFailures))
				}
				if failures >= iCloudValidationMaxFailures {
					status = iCloudResourceAbnormal
				} else if retryValidation {
					next := now.Add(iCloudValidationRetryInterval)
					if retryAt.After(now) {
						next = retryAt
					}
					nextValidationAt = &next
				}
			}
		}
		if err := disableUnauthorizedICloudAliasesTx(tx, resource.ID, allowedDomains, now); err != nil {
			return err
		}
		updates := map[string]any{
			"status": status, "validation_failures": failures, "selected_forward_to": storedSelectedForwardTo,
			"last_safe_error": safeICloudImportMessage(message), "last_checked_at": now,
			"next_validation_at": nextValidationAt, "next_provision_at": nil, "updated_at": now,
		}
		if status == iCloudResourceNormal {
			if selectedForwardTo != "" {
				updates["last_valid_at"] = now
			}
			if resource.ExpireAt.After(now) && resource.AliasCount < iCloudMaxAliases {
				updates["next_provision_at"] = now
			}
		}
		result := tx.Model(&iCloudResourceModel{}).
			Where("id = ? AND status = ? AND validation_generation = ? AND credential_revision = ?",
				task.ResourceID, iCloudResourceValidating, task.ValidationGeneration, task.ExpectedCredentialRevision).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errICloudValidationStale
		}
		rootResult := tx.Model(&iCloudRootModel{}).Where("id = ? AND version = ?", root.ID, root.Version).
			Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now})
		if rootResult.Error != nil {
			return rootResult.Error
		}
		if rootResult.RowsAffected != 1 {
			return errICloudValidationStale
		}
		if run, err := findICloudMaintenanceRunTx(ctx, tx, task); err != nil {
			return err
		} else if run != nil {
			runStatus := iCloudMaintenanceSucceeded
			if status != iCloudResourceNormal {
				runStatus = iCloudMaintenanceFailed
				if status == iCloudResourcePending && nextValidationAt != nil {
					runStatus = iCloudMaintenanceQueued
				}
			}
			return finishICloudMaintenanceRunTx(ctx, tx, run.ID, runStatus, message, now)
		}
		return nil
	})
	if errors.Is(err, errICloudValidationStale) {
		return nil
	}
	if err != nil {
		return ErrICloudValidationTemp
	}
	if selectedForwardTo != "" {
		_ = s.ScheduleICloudProvisionDispatcher(context.WithoutCancel(ctx), 0)
	}
	return nil
}

func iCloudForwardingDomains(value string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, item := range strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '，' || unicode.IsSpace(r)
	}) {
		if domain := strings.ToLower(strings.TrimSpace(item)); domain != "" {
			result[domain] = struct{}{}
		}
	}
	return result
}

func iCloudForwardingDomainAllowed(email string, allowed map[string]struct{}) bool {
	_, ok := allowed[iCloudEmailDomain(email)]
	return ok
}

func iCloudEmailDomain(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndexByte(email, '@')
	if at <= 0 || at == len(email)-1 {
		return ""
	}
	return email[at+1:]
}
