package icloud

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	iCloudProvisionBatchLimit = 128
	iCloudProvisionLease      = 5 * time.Minute
	iCloudProvisionRetry      = 5 * time.Minute
	iCloudCookieKeepalive     = 8 * time.Minute
	iCloudCookieKeepaliveMax  = 12 * time.Minute
	iCloudSessionFailureLimit = 3
	iCloudSessionRetryBase    = 2 * time.Minute
)

type iCloudProvisionTask struct {
	ResourceID uint `json:"resourceId"`
}

type iCloudProvisionScope struct {
	Resource iCloudResourceModel
	Channels []iCloudResourceChannelModel
}

func (s *Service) DispatchICloudProvisions(ctx context.Context, limit int) error {
	if s == nil || s.db == nil || s.queue == nil {
		return ErrICloudValidationTemp
	}
	if limit <= 0 || limit > iCloudProvisionBatchLimit {
		limit = iCloudProvisionBatchLimit
	}
	now := s.now().UTC()
	if err := s.recoverStaleICloudProvisionRuns(ctx, now); err != nil {
		return err
	}
	var resourceIDs []uint
	err := s.db.WithContext(ctx).Table("icloud_resources AS ir").Distinct("ir.id").
		Joins("JOIN icloud_resource_channels AS ch ON ch.resource_id = ir.id").
		Where("ir.status = ? AND ir.next_provision_at IS NOT NULL AND ir.next_provision_at <= ?", iCloudResourceNormal, now).
		Order("ir.id ASC").Limit(limit).Pluck("ir.id", &resourceIDs).Error
	if err != nil {
		return ErrICloudValidationTemp
	}
	var joined error
	for _, resourceID := range resourceIDs {
		if err := s.ScheduleICloudProvision(ctx, resourceID, 0); err != nil {
			joined = errors.Join(joined, err)
		}
	}
	return joined
}

func (s *Service) ProcessICloudProvision(ctx context.Context, task iCloudProvisionTask) (resultErr error) {
	if s == nil || s.db == nil || task.ResourceID == 0 {
		return ErrICloudValidationTemp
	}
	scope, run, claimed, err := s.claimICloudProvision(ctx, task.ResourceID)
	if err != nil || !claimed {
		return err
	}
	now := s.now().UTC()
	runStatus := iCloudMaintenanceCanceled
	runSafeError := "No alias creation was due."
	defer func() {
		if resultErr != nil {
			runStatus = iCloudMaintenanceFailed
			runSafeError = "Provision task did not finish."
		}
		if err := s.finishICloudProvisionRun(ctx, run.ID, runStatus, runSafeError, s.now().UTC()); resultErr == nil && err != nil {
			resultErr = err
		}
	}()
	aliasCount := scope.Resource.AliasCount
	nextAt := time.Time{}
	pendingWebCandidate := strings.TrimSpace(scope.Resource.AliasProvisionCandidate) != ""
	providerLimitReached := false
	attemptCount, failureCount := 0, 0
	createAttempted, creationProgress := false, false
	lastAttemptSafeError := ""
	for _, kind := range []string{iCloudChannelAppleAccount, iCloudChannelWeb} {
		channel := findICloudProvisionChannel(scope.Channels, kind)
		if channel == nil {
			continue
		}
		primaryWeb := scope.Resource.AccountRole == "primary" && channel.Kind == iCloudChannelWeb
		familyDue := primaryWeb &&
			(scope.Resource.FamilyNextSyncAt != nil && !scope.Resource.FamilyNextSyncAt.After(now) ||
				scope.Resource.FamilyNextSyncAt == nil && scope.Resource.FamilySyncStatus != iCloudFamilySyncFailed)
		if primaryWeb && !familyDue && scope.Resource.FamilyNextSyncAt != nil {
			nextAt = earlierICloudProvisionAt(nextAt, *scope.Resource.FamilyNextSyncAt)
		}
		if channel.SessionStatus == iCloudSessionInvalid {
			if familyDue {
				familyNextAt, familyErr := s.syncICloudPrimaryFamilyScheduled(ctx, scope.Resource, *channel, now)
				if familyErr != nil {
					return familyErr
				}
				nextAt = earlierICloudProvisionAt(nextAt, familyNextAt)
			}
			continue
		}
		coolingDown := channel.CooldownUntil != nil && channel.CooldownUntil.After(now)
		if coolingDown {
			nextAt = earlierICloudProvisionAt(nextAt, *channel.CooldownUntil)
		}
		resourceCanCreate := scope.Resource.ExpireAt.After(now) && aliasCount < iCloudMaxAliases && !providerLimitReached
		createAt, createAllowed := iCloudChannelWindow(*channel, now)
		createAllowed = createAllowed && resourceCanCreate && !coolingDown
		if kind == iCloudChannelWeb && pendingWebCandidate && resourceCanCreate {
			createAllowed = !coolingDown
			createAt = time.Time{}
		}
		keepaliveAt := now
		if channel.NextKeepaliveAt != nil {
			keepaliveAt = *channel.NextKeepaliveAt
		}
		keepaliveDue := !coolingDown && !keepaliveAt.After(now)
		hmeDue := createAllowed || keepaliveDue
		if !hmeDue && !familyDue {
			if resourceCanCreate {
				nextAt = earlierICloudProvisionAt(nextAt, createAt)
			}
			nextAt = earlierICloudProvisionAt(nextAt, keepaliveAt)
			continue
		}

		var alias *hmeAlias
		var immediate bool
		var attemptErr error
		attemptedCreate := false
		madeCreationProgress := false
		updated := *channel
		if hmeDue {
			attemptedCreate = createAllowed
			madeCreationProgress = createAllowed
			attemptCount++
			switch channel.Kind {
			case iCloudChannelAppleAccount:
				var list hmeListResult
				alias, list, attemptedCreate, attemptErr = s.provisionICloudAppleAccount(ctx, scope.Resource, *channel, createAllowed, now)
				madeCreationProgress = alias != nil
				if list.Complete {
					aliasCount = uint(len(list.Aliases))
					providerLimitReached = providerLimitReached || list.MaxLimitReached
				}
			case iCloudChannelWeb:
				alias, immediate, attemptErr = s.provisionICloudWeb(ctx, scope.Resource, *channel, createAllowed, now)
			}
			if attemptedCreate {
				createAttempted = true
			}
			if errors.Is(attemptErr, errICloudValidationStale) {
				runStatus = iCloudMaintenanceCanceled
				runSafeError = "Resource changed while provisioning was running."
				return nil
			}
			if attemptErr != nil {
				failureCount++
				lastAttemptSafeError = safeICloudProvisionRequestError(attemptErr)
			} else if madeCreationProgress {
				creationProgress = true
			}
			if alias != nil {
				aliasCount++
				if kind == iCloudChannelWeb {
					pendingWebCandidate = false
				}
			}
			resourceCanCreate = scope.Resource.ExpireAt.After(now) && aliasCount < iCloudMaxAliases && !providerLimitReached
			if immediate {
				pendingWebCandidate = true
				nextAt = earlierICloudProvisionAt(nextAt, now.Add(time.Second))
			}
			var loadErr error
			updated, loadErr = s.loadICloudProvisionChannel(ctx, channel.ID)
			if loadErr != nil {
				nextAt = earlierICloudProvisionAt(nextAt, now.Add(iCloudProvisionRetry))
				continue
			}
		}
		*channel = updated
		if familyDue {
			familyNextAt, familyErr := s.syncICloudPrimaryFamilyScheduled(ctx, scope.Resource, updated, now)
			if familyErr != nil {
				return familyErr
			}
			nextAt = earlierICloudProvisionAt(nextAt, familyNextAt)
			var loadErr error
			updated, loadErr = s.loadICloudProvisionChannel(ctx, channel.ID)
			if loadErr != nil {
				nextAt = earlierICloudProvisionAt(nextAt, now.Add(iCloudProvisionRetry))
				continue
			}
			*channel = updated
		}
		if !hmeDue {
			if updated.SessionStatus == iCloudSessionInvalid {
				continue
			}
			if updated.CooldownUntil != nil && updated.CooldownUntil.After(now) {
				nextAt = earlierICloudProvisionAt(nextAt, *updated.CooldownUntil)
			} else {
				if resourceCanCreate {
					nextAt = earlierICloudProvisionAt(nextAt, createAt)
				}
				nextAt = earlierICloudProvisionAt(nextAt, keepaliveAt)
			}
			continue
		}
		if attemptErr != nil {
			nextAt = earlierICloudProvisionAt(nextAt, iCloudProvisionRequestRetryAt(attemptErr, updated, now))
			continue
		}
		if updated.SessionStatus == iCloudSessionInvalid {
			continue
		}
		if updated.CooldownUntil != nil && updated.CooldownUntil.After(now) {
			nextAt = earlierICloudProvisionAt(nextAt, *updated.CooldownUntil)
			continue
		}
		if resourceCanCreate {
			if updated.Kind == iCloudChannelWeb && pendingWebCandidate {
				nextAt = earlierICloudProvisionAt(nextAt, now.Add(time.Second))
			} else if ready, allowed := iCloudChannelWindow(updated, now); allowed {
				nextAt = earlierICloudProvisionAt(nextAt, now)
			} else {
				nextAt = earlierICloudProvisionAt(nextAt, ready)
			}
		}
		if updated.NextKeepaliveAt != nil {
			nextAt = earlierICloudProvisionAt(nextAt, *updated.NextKeepaliveAt)
		}
	}
	switch {
	case creationProgress:
		runStatus, runSafeError = iCloudMaintenanceSucceeded, ""
	case createAttempted:
		runStatus, runSafeError = iCloudMaintenanceFailed, firstNonEmptyICloudSafeError(lastAttemptSafeError, "No channel completed alias creation.")
	case attemptCount > 0 && failureCount == attemptCount:
		runStatus, runSafeError = iCloudMaintenanceFailed, firstNonEmptyICloudSafeError(lastAttemptSafeError, "All channel maintenance attempts failed.")
	case attemptCount > 0:
		runStatus, runSafeError = iCloudMaintenanceCanceled, "Channel maintenance completed without alias creation."
	default:
		runStatus, runSafeError = iCloudMaintenanceCanceled, "No eligible provisioning channel was available."
	}
	refreshCreated, err := s.finishICloudProvision(ctx, scope.Resource, nextAt, now)
	if err != nil {
		return err
	}
	if refreshCreated {
		_ = s.ScheduleICloudOnboardingDispatcher(context.WithoutCancel(ctx), 0)
	}
	return nil
}

func (s *Service) claimICloudProvision(ctx context.Context, resourceID uint) (*iCloudProvisionScope, *iCloudMaintenanceRunModel, bool, error) {
	var scope iCloudProvisionScope
	var run *iCloudMaintenanceRunModel
	claimed := false
	now := s.now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&scope.Resource, resourceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if scope.Resource.Status != iCloudResourceNormal || scope.Resource.NextProvisionAt == nil || scope.Resource.NextProvisionAt.After(now) {
			return nil
		}
		if err := tx.Order("CASE kind WHEN 'apple_account' THEN 0 ELSE 1 END").
			Where("resource_id = ?", resourceID).Find(&scope.Channels).Error; err != nil {
			return err
		}
		if len(scope.Channels) == 0 {
			return nil
		}
		if err := recoverStaleICloudProvisionRunsTx(ctx, tx, resourceID, now); err != nil {
			return err
		}
		createdRun, err := startICloudProvisionRunTx(ctx, tx, scope.Resource, now)
		if err != nil {
			return err
		}
		leaseUntil := now.Add(iCloudProvisionLease)
		result := tx.Model(&iCloudResourceModel{}).Where("id = ?", resourceID).
			Updates(map[string]any{"next_provision_at": leaseUntil, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errICloudValidationStale
		}
		run = createdRun
		claimed = true
		return nil
	})
	if err != nil {
		return nil, nil, false, ErrICloudValidationTemp
	}
	return &scope, run, claimed, nil
}

func (s *Service) provisionICloudAppleAccount(ctx context.Context, resource iCloudResourceModel, channel iCloudResourceChannelModel, createAllowed bool, now time.Time) (*hmeAlias, hmeListResult, bool, error) {
	list, refreshed, err := s.syncICloudAppleAccount(ctx, resource, channel, now)
	if err != nil {
		return nil, hmeListResult{}, false, err
	}
	if !createAllowed || list.MaxLimitReached || len(list.Aliases) >= iCloudMaxAliases {
		return nil, list, false, nil
	}
	alias, err := s.createICloudAppleAccountAlias(ctx, resource, refreshed, now)
	return alias, list, true, err
}

func (s *Service) syncICloudAppleAccount(ctx context.Context, resource iCloudResourceModel, channel iCloudResourceChannelModel, now time.Time) (hmeListResult, iCloudResourceChannelModel, error) {
	ctx = withAppleRouteEmail(ctx, resource.PrimaryEmail)
	client := s.apple
	if client == nil {
		client = newRoutedAppleAccountClient(s.appleRoutes)
	}
	current := channel
	if strings.TrimSpace(current.APIKey) == "" ||
		(current.NextKeepaliveAt != nil && !current.NextKeepaliveAt.After(now)) ||
		(current.ManageExpiresAt != nil && !current.ManageExpiresAt.After(now)) {
		refreshed, err := client.refresh(ctx, current, now)
		if err != nil {
			return hmeListResult{}, current, s.applyICloudProvisionError(ctx, resource, current, err, now)
		}
		current = refreshed
	}
	list, updated, err := client.list(ctx, current, now)
	updated.NextKeepaliveAt = appleAccountNextKeepalive(updated, now)
	if err != nil {
		_ = s.persistICloudProvisionChannel(ctx, resource, updated, false, false, now)
		return hmeListResult{}, updated, s.applyICloudProvisionError(ctx, resource, updated, err, now)
	}
	if err := s.persistICloudProvisionSnapshot(ctx, resource, updated, list, now); err != nil {
		return hmeListResult{}, updated, err
	}
	return list, updated, nil
}

func (s *Service) createICloudAppleAccountAlias(ctx context.Context, resource iCloudResourceModel, channel iCloudResourceChannelModel, now time.Time) (*hmeAlias, error) {
	ctx = withAppleRouteEmail(ctx, resource.PrimaryEmail)
	client := s.apple
	if client == nil {
		client = newRoutedAppleAccountClient(s.appleRoutes)
	}
	alias, updated, err := client.create(ctx, channel, now)
	if err != nil {
		_ = s.persistICloudProvisionChannel(ctx, resource, updated, false, false, now)
		return nil, s.applyICloudProvisionError(ctx, resource, updated, err, now)
	}
	updated.NextKeepaliveAt = appleAccountNextKeepalive(updated, now)
	if err := s.persistICloudCreatedAlias(ctx, resource, updated, alias, true, now); err != nil {
		return nil, err
	}
	return &alias, nil
}

func (s *Service) provisionICloudWeb(ctx context.Context, resource iCloudResourceModel, channel iCloudResourceChannelModel, createAllowed bool, now time.Time) (*hmeAlias, bool, error) {
	ctx = withAppleRouteEmail(ctx, resource.PrimaryEmail)
	channel.UserAgent = defaultICloudHMEUserAgent
	client := s.hme
	if client == nil {
		client = newRoutedHMEClient(s.appleRoutes)
	}
	var err error
	if channel.NextKeepaliveAt != nil && !channel.NextKeepaliveAt.After(now) {
		channel, err = s.refreshICloudWebChannel(ctx, resource, channel, client, now)
		if err != nil {
			return nil, false, err
		}
	}
	list, err := client.list(ctx, channel.hmeConfig())
	if isICloudHMEHTTPStatus(err, 421) {
		var movedErr *hmeError
		if errors.As(err, &movedErr) && movedErr.UpdatedCookie != "" {
			channel.Cookie = movedErr.UpdatedCookie
		}
		channel, err = s.refreshICloudWebChannel(ctx, resource, channel, client, now)
		if err != nil {
			return nil, false, err
		}
		list, err = client.list(ctx, channel.hmeConfig())
	}
	if err != nil {
		return nil, false, s.applyICloudProvisionError(ctx, resource, channel, err, now)
	}
	channel.Cookie = list.UpdatedCookie
	if channel.NextKeepaliveAt == nil {
		channel.NextKeepaliveAt = iCloudTimePointer(now.Add(iCloudCookieKeepaliveInterval()))
	}
	if err := s.persistICloudProvisionSnapshot(ctx, resource, channel, list, now); err != nil {
		return nil, false, err
	}
	if !createAllowed || len(list.Aliases) >= iCloudMaxAliases {
		return nil, false, nil
	}
	candidate := strings.TrimSpace(resource.AliasProvisionCandidate)
	if candidate != "" {
		if reconciled := findICloudAlias(list.Aliases, candidate); reconciled != nil {
			if err := s.persistICloudProvisionCandidate(ctx, resource, "", false, now); err != nil {
				return nil, false, err
			}
			return reconciled, false, nil
		}
		if err := s.persistICloudProvisionCandidate(ctx, resource, candidate, true, now); err != nil {
			return nil, false, err
		}
		alias, updatedCookie, reserveErr := client.reserve(ctx, channel.hmeConfig(), candidate, "ReMail", "")
		if updatedCookie != "" {
			channel.Cookie = updatedCookie
		}
		if reserveErr != nil {
			if providerErr, ok := reserveErr.(*hmeError); ok && providerErr.Category == "invalid_candidate" {
				_ = s.persistICloudProvisionCandidate(ctx, resource, "", false, now)
			}
			return nil, false, s.applyICloudProvisionError(ctx, resource, channel, reserveErr, now)
		}
		if err := s.persistICloudCreatedAlias(ctx, resource, channel, alias, false, now); err != nil {
			return nil, false, err
		}
		return &alias, false, nil
	}
	candidate, updatedCookie, err := client.Generate(ctx, channel.hmeConfig())
	if updatedCookie != "" {
		channel.Cookie = updatedCookie
	}
	if err != nil {
		return nil, false, s.applyICloudProvisionError(ctx, resource, channel, err, now)
	}
	if err := s.persistICloudProvisionChannel(ctx, resource, channel, true, true, now); err != nil {
		return nil, false, err
	}
	return nil, true, s.persistICloudProvisionCandidate(ctx, resource, candidate, false, now)
}

func (s *Service) refreshICloudWebChannel(ctx context.Context, resource iCloudResourceModel, channel iCloudResourceChannelModel, client *HMEClient, now time.Time) (iCloudResourceChannelModel, error) {
	refreshed, err := client.refreshSession(ctx, channel.hmeConfig())
	if err != nil {
		return channel, s.applyICloudProvisionError(ctx, resource, channel, err, now)
	}
	channel.Host = refreshed.Host
	channel.Cookie = refreshed.Cookie
	channel.SetupCookie = refreshed.SetupCookie
	channel.NextKeepaliveAt = iCloudTimePointer(now.Add(iCloudCookieKeepaliveInterval()))
	if err := s.persistICloudProvisionChannel(ctx, resource, channel, false, false, now); err != nil {
		return channel, err
	}
	return channel, nil
}

func isICloudHMEHTTPStatus(err error, status int) bool {
	var providerErr *hmeError
	return errors.As(err, &providerErr) && providerErr.HTTPStatus == status
}

func safeICloudProvisionRequestError(err error) string {
	var hmeErr *hmeError
	if errors.As(err, &hmeErr) {
		return hmeErr.Error()
	}
	var appleErr *appleAccountError
	if errors.As(err, &appleErr) {
		return appleErr.Error()
	}
	return ""
}

func firstNonEmptyICloudSafeError(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (s *Service) persistICloudProvisionSnapshot(ctx context.Context, resource iCloudResourceModel, channel iCloudResourceChannelModel, list hmeListResult, now time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockICloudProvisionResourceTx(ctx, tx, resource)
		if err != nil {
			return err
		}
		if err := syncICloudAliasesTx(tx, locked.ID, list.Aliases, list.Complete, now); err != nil {
			return err
		}
		updates := map[string]any{"alias_count": len(list.Aliases), "last_alias_sync_at": now, "updated_at": now}
		if selectedForwardTo := strings.ToLower(strings.TrimSpace(list.SelectedForwardTo)); selectedForwardTo != "" &&
			(strings.TrimSpace(locked.RequiredForwardTo) == "" || strings.EqualFold(selectedForwardTo, strings.TrimSpace(locked.RequiredForwardTo))) {
			updates["selected_forward_to"] = selectedForwardTo
		}
		if err := tx.Model(&iCloudResourceModel{}).Where("id = ?", locked.ID).Updates(updates).Error; err != nil {
			return err
		}
		return updateICloudProvisionChannelTx(tx, channel, true, false, now)
	})
}

func (s *Service) persistICloudCreatedAlias(ctx context.Context, resource iCloudResourceModel, channel iCloudResourceChannelModel, alias hmeAlias, consumeSlot bool, now time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockICloudProvisionResourceTx(ctx, tx, resource)
		if err != nil {
			return err
		}
		if !locked.ExpireAt.After(now) || locked.AliasCount >= iCloudMaxAliases {
			return errICloudValidationStale
		}
		if err := syncICloudAliasesTx(tx, locked.ID, []hmeAlias{alias}, false, now); err != nil {
			return err
		}
		var aliasCount int64
		if err := tx.Model(&iCloudAliasModel{}).Where("resource_id = ? AND status NOT IN ?", locked.ID, []string{"missing", iCloudResourceDeleted}).Count(&aliasCount).Error; err != nil {
			return err
		}
		resourceUpdates := map[string]any{"alias_count": aliasCount, "updated_at": now}
		if forwardToEmail := strings.ToLower(strings.TrimSpace(alias.ForwardToEmail)); forwardToEmail != "" &&
			(strings.TrimSpace(locked.RequiredForwardTo) == "" || strings.EqualFold(forwardToEmail, strings.TrimSpace(locked.RequiredForwardTo))) {
			resourceUpdates["selected_forward_to"] = forwardToEmail
		}
		if channel.Kind == iCloudChannelWeb {
			resourceUpdates["alias_provision_candidate"] = ""
			resourceUpdates["alias_provision_reconcile"] = false
		}
		if err := tx.Model(&iCloudResourceModel{}).Where("id = ?", locked.ID).
			Updates(resourceUpdates).Error; err != nil {
			return err
		}
		if err := updateICloudProvisionChannelTx(tx, channel, true, consumeSlot, now); err != nil {
			return err
		}
		return tx.Model(&iCloudResourceChannelModel{}).Where("id = ? AND resource_id = ?", channel.ID, channel.ResourceID).
			Updates(map[string]any{"cooldown_until": nil, "cooldown_stage": 0}).Error
	})
}

func (s *Service) persistICloudProvisionChannel(ctx context.Context, resource iCloudResourceModel, channel iCloudResourceChannelModel, valid, consumeSlot bool, now time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockICloudProvisionResourceTx(ctx, tx, resource); err != nil {
			return err
		}
		return updateICloudProvisionChannelTx(tx, channel, valid, consumeSlot, now)
	})
}

func updateICloudProvisionChannelTx(tx *gorm.DB, channel iCloudResourceChannelModel, valid, consumeSlot bool, now time.Time) error {
	windowAt, windowCount := channel.ProvisionWindowAt, channel.ProvisionWindowCount
	if consumeSlot {
		if windowAt == nil || !now.Before(windowAt.Add(time.Hour)) ||
			windowAt.Add(time.Duration(windowCount)*iCloudChannelInterval(channel.Kind)).Before(now) {
			value := now
			windowAt, windowCount = &value, 0
		}
		if windowCount < 255 {
			windowCount++
		}
	}
	updates := map[string]any{
		"host": channel.Host, "cookie": channel.Cookie, "setup_cookie": channel.SetupCookie,
		"fd_client_info": channel.FDClientInfo, "scnt": channel.Scnt, "session_id": channel.SessionID,
		"api_key": channel.APIKey, "data_access_token": channel.DataAccessToken,
		"manage_expires_at": channel.ManageExpiresAt, "next_keepalive_at": channel.NextKeepaliveAt,
		"last_checked_at": now, "updated_at": now, "provision_window_at": windowAt,
		"provision_window_count": windowCount,
	}
	if valid {
		updates["session_status"] = iCloudSessionValid
		updates["session_failures"] = 0
		updates["last_valid_at"] = now
	}
	result := tx.Model(&iCloudResourceChannelModel{}).Where("id = ? AND resource_id = ?", channel.ID, channel.ResourceID).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		var existing iCloudResourceChannelModel
		err := tx.Select("id").Where("id = ? AND resource_id = ?", channel.ID, channel.ResourceID).Take(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errICloudValidationStale
		}
		return err
	}
	return nil
}

func (s *Service) applyICloudProvisionError(ctx context.Context, resource iCloudResourceModel, channel iCloudResourceChannelModel, requestErr error, now time.Time) error {
	category, retryAfter := "provider_unavailable", time.Duration(0)
	permanent := iCloudProvisionErrorPermanent(requestErr)
	var hmeErr *hmeError
	var appleErr *appleAccountError
	if errors.As(requestErr, &hmeErr) {
		category, retryAfter = hmeErr.Category, hmeErr.RetryAfter
		if hmeErr.UpdatedCookie != "" {
			channel.Cookie = hmeErr.UpdatedCookie
		}
		if hmeErr.UpdatedSetupCookie != "" {
			channel.SetupCookie = hmeErr.UpdatedSetupCookie
		}
	} else if errors.As(requestErr, &appleErr) {
		category, retryAfter = appleErr.Category, appleErr.RetryAfter
	}
	persistErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockICloudProvisionResourceTx(ctx, tx, resource); err != nil {
			return err
		}
		var locked iCloudResourceChannelModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, channel.ID).Error; err != nil {
			return errICloudValidationStale
		}
		updates := map[string]any{
			"host": channel.Host, "cookie": channel.Cookie, "setup_cookie": channel.SetupCookie,
			"last_checked_at": now, "updated_at": now,
		}
		switch category {
		case "session_invalid":
			failures := max(locked.SessionFailures, channel.SessionFailures)
			if failures < 255 {
				failures++
			}
			updates["session_failures"] = failures
			if failures >= iCloudSessionFailureLimit {
				updates["session_status"] = iCloudSessionInvalid
				updates["cooldown_until"] = nil
			} else {
				updates["cooldown_until"] = now.Add(iCloudSessionRetryDelay(failures))
			}
		case "rate_limited":
			delay := retryAfter
			if delay <= 0 {
				delay = iCloudRateLimitDelay(locked.CooldownStage)
			}
			updates["session_status"] = iCloudSessionValid
			updates["session_failures"] = 0
			updates["cooldown_until"] = now.Add(delay)
			updates["cooldown_stage"] = min(locked.CooldownStage+1, uint8(3))
		default:
			if permanent {
				updates["session_status"] = iCloudSessionInvalid
				updates["next_keepalive_at"] = nil
				updates["cooldown_until"] = nil
			}
		}
		return tx.Model(&iCloudResourceChannelModel{}).Where("id = ?", locked.ID).Updates(updates).Error
	})
	if persistErr != nil {
		return persistErr
	}
	return requestErr
}

func iCloudProvisionRequestRetryAt(requestErr error, channel iCloudResourceChannelModel, now time.Time) time.Time {
	if channel.SessionStatus == iCloudSessionInvalid {
		return time.Time{}
	}
	if channel.CooldownUntil != nil && channel.CooldownUntil.After(now) {
		return *channel.CooldownUntil
	}
	if iCloudProvisionErrorRetryable(requestErr) {
		return now.Add(iCloudProvisionRetry)
	}
	return time.Time{}
}

func iCloudProvisionErrorRetryable(requestErr error) bool {
	var hmeErr *hmeError
	if errors.As(requestErr, &hmeErr) {
		return hmeErr.Retryable || hmeErr.Category == "session_invalid" || hmeErr.Category == "rate_limited"
	}
	var appleErr *appleAccountError
	if errors.As(requestErr, &appleErr) {
		switch appleErr.Category {
		case "session_invalid", "rate_limited", "provider_unavailable":
			return true
		default:
			return false
		}
	}
	return true
}

func iCloudProvisionErrorPermanent(requestErr error) bool {
	var hmeErr *hmeError
	if errors.As(requestErr, &hmeErr) {
		return !hmeErr.Retryable && hmeErr.Category != "session_invalid" && hmeErr.Category != "rate_limited"
	}
	var appleErr *appleAccountError
	if errors.As(requestErr, &appleErr) {
		return appleErr.Category == "invalid_context" || appleErr.Category == "provider_rejected" || appleErr.Category == "provider_response"
	}
	return false
}

func (s *Service) persistICloudProvisionCandidate(ctx context.Context, resource iCloudResourceModel, candidate string, reconcile bool, now time.Time) error {
	candidate = strings.TrimSpace(candidate)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockICloudProvisionResourceTx(ctx, tx, resource)
		if err != nil {
			return err
		}
		if candidate != "" && (!locked.ExpireAt.After(now) || locked.AliasCount >= iCloudMaxAliases) {
			return errICloudValidationStale
		}
		result := tx.Model(&iCloudResourceModel{}).
			Where("id = ? AND credential_revision = ?", locked.ID, resource.CredentialRevision).
			Updates(map[string]any{"alias_provision_candidate": candidate, "alias_provision_reconcile": reconcile, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errICloudValidationStale
		}
		return nil
	})
}

func (s *Service) finishICloudProvision(ctx context.Context, resource iCloudResourceModel, nextAt, now time.Time) (bool, error) {
	refreshCreated := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockICloudProvisionResourceTx(ctx, tx, resource)
		if err != nil {
			return err
		}
		refreshCreated, err = s.ensureICloudCookieRefreshTx(ctx, tx, locked.ID)
		if err != nil {
			return err
		}
		if locked.Status != iCloudResourceNormal {
			nextAt = time.Time{}
		}
		updates := map[string]any{"next_provision_at": nil, "updated_at": now}
		if !nextAt.IsZero() {
			updates["next_provision_at"] = nextAt
		}
		result := tx.Model(&iCloudResourceModel{}).
			Where("id = ? AND credential_revision = ?", locked.ID, resource.CredentialRevision).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errICloudValidationStale
		}
		return nil
	})
	if errors.Is(err, errICloudValidationStale) {
		return false, nil
	}
	if err != nil {
		return false, ErrICloudValidationTemp
	}
	return refreshCreated, nil
}

func lockICloudProvisionResourceTx(ctx context.Context, tx *gorm.DB, expected iCloudResourceModel) (*iCloudResourceModel, error) {
	var resource iCloudResourceModel
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&resource, expected.ID).Error; err != nil {
		return nil, errICloudValidationStale
	}
	if resource.CredentialRevision != expected.CredentialRevision || resource.Status != expected.Status ||
		resource.Status == iCloudResourceDeleted || resource.Status == iCloudResourceDisabled {
		return nil, errICloudValidationStale
	}
	return &resource, nil
}

func (s *Service) loadICloudProvisionChannel(ctx context.Context, channelID uint) (iCloudResourceChannelModel, error) {
	var channel iCloudResourceChannelModel
	err := s.db.WithContext(ctx).First(&channel, channelID).Error
	return channel, err
}

func findICloudProvisionChannel(channels []iCloudResourceChannelModel, kind string) *iCloudResourceChannelModel {
	for index := range channels {
		if channels[index].Kind == kind {
			return &channels[index]
		}
	}
	return nil
}

func iCloudChannelWindow(channel iCloudResourceChannelModel, now time.Time) (time.Time, bool) {
	if channel.ProvisionWindowAt == nil || !now.Before(channel.ProvisionWindowAt.Add(time.Hour)) {
		return time.Time{}, true
	}
	if int(channel.ProvisionWindowCount) >= iCloudChannelHourlyLimit(channel.Kind) {
		return channel.ProvisionWindowAt.Add(time.Hour), false
	}
	next := channel.ProvisionWindowAt.Add(time.Duration(channel.ProvisionWindowCount) * iCloudChannelInterval(channel.Kind))
	if now.Before(next) {
		return next, false
	}
	return time.Time{}, true
}

func iCloudChannelHourlyLimit(kind string) int {
	if kind == iCloudChannelAppleAccount {
		return 20
	}
	return 5
}

func iCloudChannelInterval(kind string) time.Duration {
	if kind == iCloudChannelAppleAccount {
		return 3 * time.Minute
	}
	return 12 * time.Minute
}

func iCloudRateLimitDelay(stage uint8) time.Duration {
	switch stage {
	case 0:
		return 30 * time.Minute
	case 1:
		return time.Hour
	default:
		return 2 * time.Hour
	}
}

func iCloudSessionRetryDelay(failures uint8) time.Duration {
	if failures <= 1 {
		return iCloudSessionRetryBase
	}
	return 2 * iCloudSessionRetryBase
}

func appleAccountNextKeepalive(channel iCloudResourceChannelModel, now time.Time) *time.Time {
	next := now.Add(iCloudCookieKeepaliveInterval())
	if channel.ManageExpiresAt != nil && channel.ManageExpiresAt.After(now) {
		ttl := channel.ManageExpiresAt.Sub(now)
		skew := ttl / 10
		if skew < 30*time.Second {
			skew = 30 * time.Second
		}
		if skew > 2*time.Minute {
			skew = 2 * time.Minute
		}
		if expiresAt := channel.ManageExpiresAt.Add(-skew); expiresAt.Before(next) {
			next = expiresAt
		}
	}
	return &next
}

func iCloudCookieKeepaliveInterval() time.Duration {
	return min(
		runtimeconfig.Duration(runtimeconfig.ICloudCookieKeepaliveMinutesKey, iCloudCookieKeepalive, time.Minute, 1),
		iCloudCookieKeepaliveMax,
	)
}

func earlierICloudProvisionAt(current, candidate time.Time) time.Time {
	if candidate.IsZero() {
		return current
	}
	if current.IsZero() || candidate.Before(current) {
		return candidate
	}
	return current
}
