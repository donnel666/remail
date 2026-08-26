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

// ProcessICloudValidation checks every imported channel and creates an alias
// when no existing alias forwards to an approved auxiliary domain. Channel
// failures remain isolated on their channel rows.
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
	if task.PreserveResourceStatus && resource.AliasCount >= iCloudMaxAliases {
		return s.clearICloudMaintenanceAtAliasLimit(ctx, resource.ID, now)
	}
	var channels []iCloudResourceChannelModel
	if err := s.db.WithContext(ctx).Order("CASE kind WHEN 'apple_account' THEN 0 ELSE 1 END").
		Where("resource_id = ?", resource.ID).Find(&channels).Error; err != nil {
		return ErrICloudValidationTemp
	}
	if task.PreserveResourceStatus {
		return s.processICloudCredentialCheck(ctx, task, *resource, channels, now)
	}
	allowedDomains := iCloudForwardingDomains(runtimeconfig.String(runtimeconfig.ICloudForwardingSuffixesKey, ""))
	if len(allowedDomains) == 0 {
		return s.applyICloudChannelValidationResult(ctx, task, "", "No authorized iCloud forwarding domain is configured.", true, true, time.Time{})
	}
	if !resource.ExpireAt.After(now) {
		return s.applyICloudChannelValidationResult(ctx, task, "", "The iCloud resource has expired and cannot create a validation alias.", false, false, time.Time{})
	}
	if resource.AliasCount >= iCloudMaxAliases && findICloudProvisionChannel(channels, iCloudChannelAppleAccount) == nil {
		return s.applyICloudChannelValidationResult(ctx, task, "", "The iCloud alias limit has been reached.", false, false, time.Time{})
	}
	selectedForwardTo := ""
	expectedForwardTo := strings.ToLower(strings.TrimSpace(resource.RequiredForwardTo))
	nextValidationAt := time.Time{}
	countFailure := len(channels) == 0
	retryValidation := len(channels) == 0
	transientValidation := false
	proxyRetryExhaustedFailure := false
	var failures []error
	lastRequestSafeError := ""
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
		if !currentResource.ExpireAt.After(now) ||
			(currentResource.AliasCount >= iCloudMaxAliases && currentChannel.Kind != iCloudChannelAppleAccount) {
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
		if !createAllowed && currentChannel.Kind != iCloudChannelAppleAccount {
			retryValidation = true
			transientValidation = true
			nextValidationAt = earlierICloudProvisionAt(nextValidationAt, createAt)
			continue
		}
		var alias *hmeAlias
		definiteAttemptFailure := false
		switch currentChannel.Kind {
		case iCloudChannelAppleAccount:
			var list hmeListResult
			var refreshed iCloudResourceChannelModel
			list, refreshed, loadErr = s.syncICloudAppleAccount(ctx, currentResource, currentChannel, now)
			if loadErr == nil {
				alias = findICloudValidationAlias(list.Aliases, allowedDomains, expectedForwardTo)
				if alias == nil && (list.MaxLimitReached || len(list.Aliases) >= iCloudMaxAliases) {
					failures = append(failures, fmt.Errorf("iCloud alias limit has been reached"))
					continue
				}
				if alias == nil && !createAllowed {
					retryValidation = true
					transientValidation = true
					nextValidationAt = earlierICloudProvisionAt(nextValidationAt, createAt)
					continue
				}
				if alias == nil {
					alias, loadErr = s.createICloudAppleAccountAlias(ctx, currentResource, refreshed, now)
				}
			}
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
			if safeError := safeICloudProvisionRequestError(loadErr); safeError != "" {
				lastRequestSafeError = safeError
			}
			proxyRetryExhausted := iCloudProxyRetryExhausted(loadErr)
			definiteFailure := definiteAttemptFailure || proxyRetryExhausted || iCloudValidationErrorCountsFailure(loadErr)
			if definiteFailure {
				countFailure = true
				retryValidation = true
			}
			failures = append(failures, loadErr)
			if proxyRetryExhausted {
				proxyRetryExhaustedFailure = true
				retryValidation = false
				continue
			}
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
		actualForwardTo := strings.ToLower(strings.TrimSpace(alias.ForwardToEmail))
		if expectedForwardTo != "" && actualForwardTo != expectedForwardTo {
			countFailure = true
			retryValidation = true
			_ = s.disableICloudAlias(ctx, resource.ID, alias.AnonymousID, now)
			failures = append(failures, fmt.Errorf("iCloud forwarding mailbox does not match the prepared address"))
			if refreshedResource, refreshedChannel, refreshErr := s.loadICloudValidationProvisionScope(ctx, *resource, original.ID); refreshErr == nil {
				nextValidationAt = earlierICloudProvisionAt(nextValidationAt, iCloudValidationChannelRetryAt(refreshedResource, refreshedChannel, now))
			}
			continue
		}
		if selectedForwardTo == "" {
			selectedForwardTo = actualForwardTo
		}
	}
	message := ""
	if selectedForwardTo == "" {
		fallback := "No iCloud session created an alias for an authorized forwarding domain."
		if expectedForwardTo != "" {
			fallback = "No iCloud session created an alias for the prepared forwarding mailbox."
		}
		if len(failures) == 0 {
			fallback = "No usable iCloud session is configured."
		}
		message = firstNonEmptyICloudSafeError(lastRequestSafeError, fallback)
	}
	if transientValidation && !proxyRetryExhaustedFailure {
		countFailure = false
	}
	return s.applyICloudChannelValidationResult(ctx, task, selectedForwardTo, message, countFailure, retryValidation, nextValidationAt)
}

// processICloudCredentialCheck only checks channels that are not already
// valid. It deliberately does not create aliases: cookie maintenance must not
// let a healthy channel hide a newly submitted, unchecked channel.
func (s *Service) processICloudCredentialCheck(ctx context.Context, task iCloudValidationTask, resource iCloudResourceModel, channels []iCloudResourceChannelModel, now time.Time) error {
	if len(channels) == 0 {
		return s.applyICloudChannelValidationResult(ctx, task, "", "No usable iCloud session is configured.", true, false, time.Time{})
	}
	retryValidation := false
	countFailure := false
	nextValidationAt := time.Time{}
	lastSafeError := ""
	checked := false
	proxyRetryExhaustedFailure := false
	for _, original := range channels {
		if original.SessionStatus == iCloudSessionValid {
			continue
		}
		checked = true
		currentResource, currentChannel, err := s.loadICloudValidationProvisionScope(ctx, resource, original.ID)
		if err != nil {
			if errors.Is(err, errICloudValidationStale) {
				return nil
			}
			retryValidation = true
			nextValidationAt = earlierICloudProvisionAt(nextValidationAt, now.Add(iCloudValidationRetryInterval))
			continue
		}
		if currentResource.AliasCount >= iCloudMaxAliases {
			return s.clearICloudMaintenanceAtAliasLimit(ctx, resource.ID, now)
		}
		if currentChannel.SessionStatus == iCloudSessionInvalid {
			countFailure = true
			lastSafeError = "iCloud channel session is invalid."
			continue
		}
		if currentChannel.CooldownUntil != nil && currentChannel.CooldownUntil.After(now) {
			retryValidation = true
			nextValidationAt = earlierICloudProvisionAt(nextValidationAt, *currentChannel.CooldownUntil)
			continue
		}
		var requestErr error
		switch currentChannel.Kind {
		case iCloudChannelAppleAccount:
			_, _, requestErr = s.syncICloudAppleAccount(ctx, currentResource, currentChannel, now)
		case iCloudChannelWeb:
			_, _, requestErr = s.provisionICloudWeb(ctx, currentResource, currentChannel, false, now)
		default:
			requestErr = fmt.Errorf("unsupported iCloud channel")
		}
		if errors.Is(requestErr, errICloudValidationStale) {
			return nil
		}
		if requestErr == nil {
			continue
		}
		if safeError := safeICloudProvisionRequestError(requestErr); safeError != "" {
			lastSafeError = safeError
		}
		checked = true
		if iCloudProxyRetryExhausted(requestErr) {
			countFailure = true
			proxyRetryExhaustedFailure = true
			continue
		}
		refreshedResource, refreshedChannel, refreshErr := s.loadICloudValidationProvisionScope(ctx, resource, original.ID)
		if refreshErr == nil && refreshedChannel.SessionStatus == iCloudSessionInvalid {
			countFailure = true
			continue
		}
		if iCloudValidationErrorCountsFailure(requestErr) {
			countFailure = true
			continue
		}
		retryValidation = true
		if refreshErr == nil {
			nextValidationAt = earlierICloudProvisionAt(nextValidationAt, iCloudValidationChannelRetryAt(refreshedResource, refreshedChannel, now))
		}
		if nextValidationAt.IsZero() {
			nextValidationAt = now.Add(iCloudValidationRetryInterval)
		}
	}
	if !checked {
		return s.applyICloudChannelValidationResult(ctx, task, resource.SelectedForwardTo, "", false, false, time.Time{})
	}
	// A transient channel failure gets another maintenance attempt; do not
	// consume a health failure while a retry is still possible.
	if retryValidation && !proxyRetryExhaustedFailure {
		countFailure = false
	}
	message := firstNonEmptyICloudSafeError(lastSafeError, "iCloud credential check failed.")
	return s.applyICloudChannelValidationResult(ctx, task, resource.SelectedForwardTo, message, countFailure, retryValidation, nextValidationAt)
}

func findICloudValidationAlias(aliases []hmeAlias, allowedDomains map[string]struct{}, expectedForwardTo string) *hmeAlias {
	expectedForwardTo = strings.ToLower(strings.TrimSpace(expectedForwardTo))
	for index := range aliases {
		forwardTo := strings.ToLower(strings.TrimSpace(aliases[index].ForwardToEmail))
		if !aliases[index].Active || !iCloudForwardingDomainAllowed(forwardTo, allowedDomains) ||
			(expectedForwardTo != "" && forwardTo != expectedForwardTo) {
			continue
		}
		return &aliases[index]
	}
	return nil
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
	if resource.CredentialRevision != expected.CredentialRevision ||
		(expected.Status == iCloudResourceNormal && resource.Status != iCloudResourceNormal) ||
		(expected.Status != iCloudResourceNormal && resource.Status != iCloudResourceValidating) ||
		iCloudCookieMaintenanceBlocksValidation(&resource) {
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
	if selectedForwardTo != "" && !task.PreserveResourceStatus && !iCloudForwardingDomainAllowed(selectedForwardTo, allowedDomains) {
		selectedForwardTo = ""
		message = "The iCloud forwarding domain is no longer authorized."
		countFailure = true
	}
	refreshCreated := false
	credentialCheckSucceeded := false
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
		maintenanceBlocked := iCloudCookieMaintenanceBlocksValidation(&resource)
		if resource.ValidationGeneration != task.ValidationGeneration ||
			resource.CredentialRevision != task.ExpectedCredentialRevision ||
			(task.PreserveResourceStatus && resource.Status != iCloudResourceNormal) ||
			(!task.PreserveResourceStatus && resource.Status != iCloudResourceValidating) ||
			maintenanceBlocked {
			if maintenanceBlocked {
				if resource.ValidationGeneration == task.ValidationGeneration && resource.CredentialRevision == task.ExpectedCredentialRevision {
					if err := releaseICloudValidatingResourceForCookieMaintenanceTx(ctx, tx, &resource, now); err != nil {
						return err
					}
				}
				run, findErr := findICloudMaintenanceRunTx(ctx, tx, task)
				if findErr != nil {
					return findErr
				}
				if run != nil {
					return finishICloudMaintenanceRunTx(ctx, tx, run.ID, iCloudMaintenanceCanceled, "Validation canceled because Cookie maintenance owns the session.", now)
				}
				return nil
			}
			return errICloudValidationStale
		}
		if task.PreserveResourceStatus && resource.AliasCount >= iCloudMaxAliases {
			return clearICloudMaintenanceAtAliasLimitTx(ctx, tx, resource.ID, now)
		}
		status := iCloudResourceNormal
		if task.PreserveResourceStatus {
			status = resource.Status
		}
		failures := uint8(0)
		storedSelectedForwardTo := selectedForwardTo
		requiredForwardTo := strings.ToLower(strings.TrimSpace(resource.RequiredForwardTo))
		if storedSelectedForwardTo == "" && requiredForwardTo != "" && iCloudForwardingDomainAllowed(requiredForwardTo, allowedDomains) {
			storedSelectedForwardTo = requiredForwardTo
		} else if storedSelectedForwardTo == "" && iCloudForwardingDomainAllowed(resource.SelectedForwardTo, allowedDomains) {
			storedSelectedForwardTo = strings.ToLower(strings.TrimSpace(resource.SelectedForwardTo))
		}
		credentialCheckSucceeded = task.PreserveResourceStatus && !countFailure && !retryValidation
		var nextValidationAt *time.Time
		if task.PreserveResourceStatus {
			failures = resource.ValidationFailures
			if credentialCheckSucceeded {
				failures = 0
			} else if countFailure {
				failures = min(failures+1, uint8(iCloudValidationMaxFailures))
			}
			if !retryValidation {
				storedSelectedForwardTo = resource.SelectedForwardTo
			}
		} else if selectedForwardTo == "" {
			status = iCloudResourcePending
			failures = resource.ValidationFailures
			if !countFailure && !retryValidation && resource.LastValidAt != nil {
				status = iCloudResourceNormal
				storedSelectedForwardTo = resource.SelectedForwardTo
			} else if countFailure {
				failures = min(failures+1, uint8(iCloudValidationMaxFailures))
			}
		}
		if failures >= iCloudValidationMaxFailures && !task.PreserveResourceStatus {
			status = iCloudResourceAbnormal
		} else if retryValidation && (task.PreserveResourceStatus || selectedForwardTo == "") {
			next := now.Add(iCloudValidationRetryInterval)
			if retryAt.After(now) {
				next = retryAt
			}
			nextValidationAt = &next
		}
		if !task.PreserveResourceStatus {
			if err := disableUnauthorizedICloudAliasesTx(tx, resource.ID, allowedDomains, now); err != nil {
				return err
			}
		}
		updates := map[string]any{
			"status": status, "validation_failures": failures, "selected_forward_to": storedSelectedForwardTo,
			"last_safe_error": safeICloudImportMessage(message), "last_checked_at": now,
			"next_validation_at": nextValidationAt, "next_provision_at": nil, "updated_at": now,
		}
		if status == iCloudResourceNormal {
			if selectedForwardTo != "" || credentialCheckSucceeded {
				updates["last_valid_at"] = now
			}
			if resource.ExpireAt.After(now) && resource.AliasCount < iCloudMaxAliases && (!task.PreserveResourceStatus || credentialCheckSucceeded) && len(allowedDomains) > 0 {
				updates["next_provision_at"] = now
			}
			if resource.AccountRole == "primary" && (!task.PreserveResourceStatus || credentialCheckSucceeded) {
				updates["family_next_sync_at"] = now
			}
		}
		whereStatus := iCloudResourceValidating
		if task.PreserveResourceStatus {
			whereStatus = iCloudResourceNormal
		}
		result := tx.Model(&iCloudResourceModel{}).
			Where("id = ? AND status = ? AND validation_generation = ? AND credential_revision = ?",
				task.ResourceID, whereStatus, task.ValidationGeneration, task.ExpectedCredentialRevision).
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
		var ensureErr error
		refreshCreated, ensureErr = s.ensureICloudCookieMaintenanceTx(ctx, tx, resource.ID, false, false)
		if ensureErr != nil {
			return ensureErr
		}
		if run, err := findICloudMaintenanceRunTx(ctx, tx, task); err != nil {
			return err
		} else if run != nil {
			runStatus := iCloudMaintenanceSucceeded
			if (task.PreserveResourceStatus && !credentialCheckSucceeded) || (!task.PreserveResourceStatus && selectedForwardTo == "") {
				runStatus = iCloudMaintenanceFailed
				if nextValidationAt != nil {
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
	if refreshCreated {
		_ = s.ScheduleICloudOnboardingDispatcher(context.WithoutCancel(ctx), 0)
	}
	if (!task.PreserveResourceStatus && selectedForwardTo != "") || (credentialCheckSucceeded && len(allowedDomains) > 0) {
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
