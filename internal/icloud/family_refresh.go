package icloud

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errFamilyAccountBusy = errors.New("icloud: family refresh account is busy")
var errFamilyDeviceRequired = errors.New("icloud: family refresh requires a cookie or device")
var errFamilyCredentials = errors.New("icloud: family refresh credentials are missing")

var releaseFamilyLoginScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end
return 0`)

var renewFamilyJobScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
return redis.call('PEXPIRE', KEYS[1], ARGV[2])`)

func (s *Service) familyLoginActive(ctx context.Context, id uint) (bool, error) {
	if s.deviceRedis == nil {
		return false, nil
	}
	count, err := s.deviceRedis.Exists(ctx, familyRedisKey(id, "login")).Result()
	return count > 0, err
}

func (s *Service) processFamilyRefresh(ctx context.Context, task iCloudFamilyRefreshTask) (runErr error) {
	ctx, cancel := context.WithTimeout(ctx, familyRefreshTimeout)
	defer cancel()
	finished := false
	defer func() {
		if finished {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		cause := runErr
		if cause == nil {
			cause = ErrICloudValidationTemp
		}
		if err := s.failFamilyRefresh(cleanupCtx, task, cause); err != nil && !errors.Is(err, errICloudRefreshStale) {
			runErr = errors.Join(runErr, err)
		}
	}()
	// The queue wait must not consume the worker's execution lease. Renewal
	// is fenced so an expired task cannot recreate or extend another job.
	renewed, err := renewFamilyJobScript.Run(ctx, s.deviceRedis, []string{familyRedisKey(task.ResourceID, "job")}, task.Token, familyRefreshLease.Milliseconds()).Int()
	if err != nil {
		return ErrICloudValidationTemp
	}
	if renewed == 0 {
		finished = true
		return nil
	}
	resource, err := s.familyResource(ctx, task.ResourceID)
	if err != nil {
		return err
	}
	cache, err := s.loadFamilyCache(ctx, resource)
	if err != nil {
		return err
	}
	progress := func(state string) error {
		cache.State, cache.LastError = state, ""
		return s.writeFamilyCache(ctx, task, cache, false)
	}
	snapshot, err := s.refreshFamilySnapshot(ctx, task, resource, progress)
	if err != nil {
		cache.State, cache.LastError = "failed", familyRefreshError(err)
	} else {
		now := s.now().UTC()
		cache.State, cache.LastError, cache.Snapshot, cache.SyncedAt = "ready", "", &snapshot, &now
	}
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	runErr = s.writeFamilyCache(finishCtx, task, cache, true)
	finished = runErr == nil
	return runErr
}

func (s *Service) failFamilyRefresh(ctx context.Context, task iCloudFamilyRefreshTask, cause error) error {
	// Even if the view cannot be read or written, release only our own job so
	// a later GET can expose its failed state and permit a fresh request.
	defer func() {
		_ = releaseFamilyLoginScript.Run(ctx, s.deviceRedis, []string{familyRedisKey(task.ResourceID, "job")}, task.Token).Err()
	}()
	var cache iCloudFamilyCache
	data, err := s.deviceRedis.Get(ctx, familyRedisKey(task.ResourceID, "view")).Bytes()
	if err != nil && !errors.Is(err, redis.Nil) {
		return err
	}
	if err == nil {
		if err := json.Unmarshal(data, &cache); err != nil {
			return err
		}
	}
	cache.State, cache.LastError = "failed", familyRefreshError(cause)
	return s.writeFamilyCache(ctx, task, cache, true)
}

func familyRefreshError(err error) string {
	switch {
	case errors.Is(err, errFamilyAccountBusy):
		return "Family refresh is waiting for an account task."
	case errors.Is(err, errFamilyDeviceRequired):
		return "Update Cookie or bind a device to refresh the family."
	case errors.Is(err, errFamilyCredentials):
		return "Apple account credentials are required to refresh the family."
	}
	if errors.Is(err, errICloudRefreshStale) {
		return "The account changed during family refresh. Please retry."
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "Family refresh timed out. Please retry."
	}
	var familyErr *iCloudFamilyError
	if errors.As(err, &familyErr) && familyErr.Category == "rate_limited" {
		return "Family refresh is rate limited. Please retry later."
	}
	var appleErr *AppleOnboardingError
	if errors.As(err, &appleErr) {
		return "Device login failed. Check the account credentials and device status."
	}
	if errors.Is(err, errDeviceNoCode) || errors.Is(err, errDeviceUnauthorized) || errors.Is(err, errDeviceUnavailable) {
		return "Unable to obtain a device verification code."
	}
	return "Family refresh failed. Please retry."
}

func (s *Service) refreshFamilySnapshot(ctx context.Context, task iCloudFamilyRefreshTask, resource iCloudResourceModel, progress func(string) error) (iCloudFamilySnapshot, error) {
	if resource.Status == iCloudResourceDisabled {
		return iCloudFamilySnapshot{}, ErrICloudResourceStatus
	}
	if err := progress("querying"); err != nil {
		return iCloudFamilySnapshot{}, err
	}
	triedCookies := make(map[string]bool)
	tryCookies := func() (iCloudFamilySnapshot, bool, error) {
		channels, err := s.familyChannels(ctx, resource.ID)
		if err != nil {
			return iCloudFamilySnapshot{}, false, err
		}
		var deferredErr error
		for _, channel := range channels {
			cookie := iCloudFamilyCookie(channel)
			if !usableFamilyCookie(channel) || triedCookies[cookie] {
				continue
			}
			triedCookies[cookie] = true
			snapshot, err := s.fetchResourceFamily(ctx, resource, channel)
			if err == nil {
				return snapshot, true, nil
			}
			var familyErr *iCloudFamilyError
			if !errors.As(err, &familyErr) || familyErr.Category != "session_invalid" {
				deferredErr = errors.Join(deferredErr, err)
				continue
			}
			if channel.Kind == iCloudChannelAppleAccount {
				// FamilyWS can reject an expired family session while the new
				// management Cookie can still renew it without a device challenge.
				refreshed, refreshErr := s.apple.refresh(withAppleRouteEmail(ctx, resource.PrimaryEmail), channel, s.now())
				if refreshErr != nil {
					var appleErr *appleAccountError
					if errors.As(refreshErr, &appleErr) && (appleErr.Category == "session_invalid" || appleErr.Category == "invalid_context") {
						continue
					}
					deferredErr = errors.Join(deferredErr, refreshErr)
					continue
				}
				if err := s.saveFamilyCookieRefresh(ctx, resource, channel, refreshed); err != nil {
					return iCloudFamilySnapshot{}, false, err
				}
				// This save advances the shared credential fence used by all
				// channel writers. Carry its version through the remaining flow.
				resource.CredentialRevision++
				resource.ValidationGeneration++
				if resource.Status == iCloudResourceValidating {
					resource.Status = iCloudResourcePending
				}
				triedCookies[iCloudFamilyCookie(refreshed)] = true
				snapshot, err = s.fetchResourceFamily(ctx, resource, refreshed)
				if err == nil {
					return snapshot, true, nil
				}
				if !errors.As(err, &familyErr) || familyErr.Category != "session_invalid" {
					deferredErr = errors.Join(deferredErr, err)
				}
			}
		}
		return iCloudFamilySnapshot{}, false, deferredErr
	}
	if snapshot, ready, err := tryCookies(); ready || err != nil {
		return snapshot, err
	}
	if resource.DeviceCodeAPI == "" {
		return iCloudFamilySnapshot{}, errFamilyDeviceRequired
	}
	// The resource row serializes this lease with normal Cookie task creation.
	var credential iCloudResourceCredentialModel
	expectedEmail := resource.PrimaryEmail
	var err error
	for {
		busy, wakeRecovery := false, false
		err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&resource, task.ResourceID).Error; err != nil {
				return err
			}
			if resource.Status == iCloudResourceDeleted || resource.Status == iCloudResourceDisabled {
				return ErrICloudResourceStatus
			}
			if resource.PrimaryEmail != expectedEmail {
				return errICloudRefreshStale
			}
			if resource.DeviceCodeAPI == "" {
				return errFamilyDeviceRequired
			}
			if iCloudCookieMaintenanceWorkflowActive(resource) {
				busy = true
				// Reuse a queued device recovery without waiting its initial
				// background jitter. Never bypass an upstream retry backoff.
				if resource.WorkflowTaskKind == iCloudCookieRecoveryTaskKind && resource.WorkflowStage == "manage_prepare" &&
					resource.WorkflowDispatchStatus == "pending" && resource.WorkflowStartedAt == nil && resource.WorkflowAttempts == 0 &&
					resource.WorkflowNextAttemptAt != nil && resource.WorkflowNextAttemptAt.After(s.now()) {
					if err := tx.Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).Update("next_attempt_at", s.now().UTC()).Error; err != nil {
						return err
					}
					wakeRecovery = true
				}
				return nil
			}
			if err := tx.First(&credential, resource.ID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return errFamilyCredentials
				}
				return err
			}
			if strings.TrimSpace(credential.ApplePassword) == "" {
				return errFamilyCredentials
			}
			claimed, err := s.deviceRedis.SetNX(ctx, familyRedisKey(resource.ID, "login"), task.Token, familyRefreshTimeout).Result()
			if err != nil {
				return err
			}
			if !claimed {
				return errFamilyAccountBusy
			}
			return nil
		})
		if err == nil && busy {
			if wakeRecovery {
				_ = s.ScheduleICloudOnboardingDispatcher(ctx, 0)
			}
			err = errFamilyAccountBusy
		}
		if !errors.Is(err, errFamilyAccountBusy) {
			break
		}
		if err := progress("waiting"); err != nil {
			return iCloudFamilySnapshot{}, err
		}
		if err := waitFamilyRefresh(ctx, 2*time.Second); err != nil {
			return iCloudFamilySnapshot{}, err
		}
		// A completed normal Cookie task can satisfy this request without another login.
		if snapshot, ready, err := tryCookies(); ready || err != nil {
			return snapshot, err
		}
	}
	if err != nil {
		return iCloudFamilySnapshot{}, err
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = releaseFamilyLoginScript.Run(releaseCtx, s.deviceRedis, []string{familyRedisKey(resource.ID, "login")}, task.Token).Err()
	}()
	// A normal Cookie task may have finished between the initial read and
	// taking the login lease. Reuse its newly persisted Cookie first.
	if snapshot, ready, err := tryCookies(); ready || err != nil {
		return snapshot, err
	}
	if err := progress("logging_in"); err != nil {
		return iCloudFamilySnapshot{}, err
	}
	channel, err := s.loginFamilyDevice(ctx, resource, credential)
	if err != nil {
		return iCloudFamilySnapshot{}, err
	}
	if err := s.saveFamilyCookie(ctx, resource, channel); err != nil {
		return iCloudFamilySnapshot{}, err
	}
	if err := progress("querying"); err != nil {
		return iCloudFamilySnapshot{}, err
	}
	return s.fetchResourceFamily(ctx, resource, iCloudResourceChannelModel{Kind: channel.Kind, Cookie: channel.Cookie, UserAgent: channel.UserAgent})
}

func (s *Service) fetchResourceFamily(ctx context.Context, resource iCloudResourceModel, channel iCloudResourceChannelModel) (iCloudFamilySnapshot, error) {
	snapshot, err := s.family.fetch(withAppleRouteEmail(ctx, resource.PrimaryEmail), channel)
	if err != nil {
		return snapshot, err
	}
	if snapshot.Member && (!strings.EqualFold(snapshot.CurrentUserAppleID, resource.PrimaryEmail) || !snapshot.Linked) {
		return iCloudFamilySnapshot{}, invalidICloudFamilyResponse()
	}
	return snapshot, nil
}

func (s *Service) loginFamilyDevice(ctx context.Context, resource iCloudResourceModel, credential iCloudResourceCredentialModel) (*AppleOnboardingChannel, error) {
	secret := AppleOnboardingSecret{Password: credential.ApplePassword, Birthday: credential.Birthday.Format(time.DateOnly)}
	if len(credential.SecurityAnswers) > 0 {
		_ = json.Unmarshal(credential.SecurityAnswers, &secret.SecurityAnswers)
	}
	var session json.RawMessage
	execute := func(operation, code string) (AppleOnboardingResponse, error) {
		response, err := s.onboardingApple.Execute(ctx, AppleOnboardingRequest{Operation: operation, Email: resource.PrimaryEmail, Secret: secret, Session: session,
			PhoneNumber: resource.BoundPhoneNumber, PhoneCountryCode: resource.BoundPhoneCountryCode, Code: code, SMSPurpose: appleSMSManageLogin,
			UseDeviceCode: true, SkipPhoneEnrollment: true, SkipPrivateAlias: true})
		if err == nil {
			session = response.Session
		}
		return response, err
	}
	prepared, err := execute(appleOnboardingPrepareManage, "")
	if err != nil {
		return nil, err
	}
	if prepared.Next != "ready" {
		if prepared.Next != appleSMSManageLogin {
			return nil, ErrICloudOnboardingProvider
		}
		codeCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		rejected := ""
		for {
			code, err := s.FetchDeviceCode(codeCtx, resource.DeviceCodeAPI)
			if err == nil && code != rejected {
				_, err = execute(appleOnboardingVerifySMS, code)
				if err == nil {
					break
				}
				var appleErr *AppleOnboardingError
				if !errors.As(err, &appleErr) || !appleErr.CodeRejected {
					return nil, err
				}
				rejected = code
			} else if err != nil && !errors.Is(err, errDeviceNoCode) && !errors.Is(err, errDeviceUnavailable) {
				return nil, err
			}
			if err := waitFamilyRefresh(codeCtx, 4*time.Second); err != nil {
				return nil, errDeviceNoCode
			}
		}
	}
	if _, err := execute(appleOnboardingFetchManage, ""); err != nil {
		return nil, err
	}
	response, err := execute(appleOnboardingExportSession, "")
	if err != nil {
		return nil, err
	}
	if response.NewChannel == nil || response.NewChannel.Kind != iCloudChannelAppleAccount || !validICloudFamilyCookie(response.NewChannel.Cookie) {
		return nil, ErrICloudOnboardingProvider
	}
	return response.NewChannel, nil
}

func (s *Service) saveFamilyCookieRefresh(ctx context.Context, resource iCloudResourceModel, previous, refreshed iCloudResourceChannelModel) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root iCloudRootModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&root, resource.ID).Error; err != nil {
			return err
		}
		locked, err := lockICloudProvisionResourceTx(ctx, tx, resource)
		if err != nil {
			return err
		}
		if iCloudCookieMaintenanceWorkflowActive(*locked) {
			return errFamilyAccountBusy
		}
		var current iCloudResourceChannelModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND resource_id = ?", previous.ID, resource.ID).Take(&current).Error; err != nil {
			return err
		}
		if current.Cookie != previous.Cookie || current.SetupCookie != previous.SetupCookie || !current.UpdatedAt.Equal(previous.UpdatedAt) {
			return errICloudRefreshStale
		}
		now := s.now().UTC().Truncate(time.Millisecond)
		if err := tx.Model(&current).Updates(map[string]any{
			"cookie": refreshed.Cookie, "scnt": refreshed.Scnt, "session_id": refreshed.SessionID,
			"api_key": refreshed.APIKey, "data_access_token": refreshed.DataAccessToken,
			"manage_expires_at": refreshed.ManageExpiresAt, "updated_at": now,
			"session_status": iCloudSessionUnchecked, "session_failures": 0,
		}).Error; err != nil {
			return err
		}
		updates := map[string]any{
			"credential_revision": locked.CredentialRevision + 1, "credential_updated_at": now,
			"validation_generation": locked.ValidationGeneration + 1, "next_validation_at": now, "updated_at": now,
		}
		if locked.Status == iCloudResourceValidating {
			updates["status"] = iCloudResourcePending
		}
		if err := tx.Model(locked).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Model(&root).Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}).Error
	})
}

func (s *Service) saveFamilyCookie(ctx context.Context, expected iCloudResourceModel, channel *AppleOnboardingChannel) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current iCloudResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, expected.ID).Error; err != nil {
			return err
		}
		if current.Status == iCloudResourceDeleted || current.Status == iCloudResourceDisabled || current.CredentialRevision != expected.CredentialRevision ||
			current.WorkflowGeneration != expected.WorkflowGeneration || current.PrimaryEmail != expected.PrimaryEmail || current.DeviceCodeAPI != expected.DeviceCodeAPI || iCloudCookieMaintenanceWorkflowActive(current) {
			return errICloudRefreshStale
		}
		now := s.now().UTC().Truncate(time.Millisecond)
		if err := upsertICloudImportChannelsTx(tx, current.ID, []iCloudImportChannel{appleOnboardingImportChannel(*channel)}, false, now); err != nil {
			return err
		}
		if err := tx.Model(&iCloudResourceModel{}).Where("id = ?", current.ID).Updates(map[string]any{
			"credential_revision": current.CredentialRevision + 1, "credential_updated_at": now,
			"validation_generation": current.ValidationGeneration + 1, "next_validation_at": now, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&iCloudRootModel{}).Where("id = ?", current.ID).Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}).Error
	})
}

func waitFamilyRefresh(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
