package icloud

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	errICloudRefreshStale             = errors.New("icloud: account refresh is stale")
	ErrICloudCookieRefreshUnavailable = errors.New("icloud: cookie refresh is unavailable")
)

// EnsureICloudCookieRefresh creates at most one active refresh task for an
// automated resource whose imported Apple session is known to be invalid.
func (s *Service) EnsureICloudCookieRefresh(ctx context.Context, resourceID uint) error {
	if s == nil || s.db == nil || resourceID == 0 {
		return ErrICloudOnboardingTemporary
	}
	created := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		created, err = s.ensureICloudCookieRefreshTx(ctx, tx, resourceID, false)
		return err
	})
	if err != nil {
		return ErrICloudOnboardingTemporary
	}
	if created {
		_ = s.ScheduleICloudOnboardingDispatcher(context.WithoutCancel(ctx), 0)
	}
	return nil
}

func (s *Service) ensureICloudCookieRefreshTx(ctx context.Context, tx *gorm.DB, resourceID uint, forceOldCookie bool) (bool, error) {
	if s == nil || tx == nil || resourceID == 0 {
		return false, ErrICloudOnboardingTemporary
	}
	var resource iCloudResourceModel
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&resource, resourceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if resource.Status == iCloudResourceDeleted || resource.Status == iCloudResourceDisabled || strings.TrimSpace(resource.BoundPhoneNumber) == "" || resource.KitesimPhoneID == nil {
		return false, nil
	}
	if !forceOldCookie {
		var invalidChannels int64
		if err := tx.Model(&iCloudResourceChannelModel{}).
			Where("resource_id = ? AND session_status = ?", resourceID, iCloudSessionInvalid).
			Count(&invalidChannels).Error; err != nil || invalidChannels == 0 {
			return false, err
		}
	}
	var active int64
	if err := tx.Model(&iCloudResourceModel{}).
		Where("id = ? AND onboarding_status IN ?", resourceID, []string{iCloudOnboardingProcessing, iCloudOnboardingWaiting}).
		Count(&active).Error; err != nil || active > 0 {
		return false, err
	}
	var credential iCloudResourceCredentialModel
	if err := tx.First(&credential, resourceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	var answers [3]iCloudSecurityAnswer
	if err := json.Unmarshal(credential.SecurityAnswers, &answers); err != nil || strings.TrimSpace(credential.ApplePassword) == "" || credential.Birthday.IsZero() {
		return false, nil
	}
	for _, answer := range answers {
		if strings.TrimSpace(answer.Question) == "" || strings.TrimSpace(answer.Answer) == "" {
			return false, nil
		}
	}
	secret, err := json.Marshal(iCloudOnboardingSecret{
		Password: credential.ApplePassword, SecurityAnswers: answers, Birthday: credential.Birthday.Format("2006-01-02"),
	})
	if err != nil {
		return false, err
	}
	icloudOpened := resource.ICloudOpened || forceOldCookie
	stage := "manage_prepare"
	pendingSMSPurpose := ""
	if forceOldCookie {
		stage = "old_cookie_prepare"
		pendingSMSPurpose = appleSMSOldCookieLogin
	} else {
		var invalidWeb int64
		if err := tx.Model(&iCloudResourceChannelModel{}).
			Where("resource_id = ? AND kind = ? AND session_status = ?", resourceID, iCloudChannelWeb, iCloudSessionInvalid).
			Count(&invalidWeb).Error; err != nil {
			return false, err
		}
		if icloudOpened || invalidWeb > 0 {
			stage = "icloud_prepare"
		}
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	generation := resource.WorkflowGeneration + 1
	if generation == 0 {
		generation = 1
	}
	updates := map[string]any{
		// Keep the original onboarding receipt projection while the current
		// workflow switches to refresh. This preserves idempotent replay and
		// governance history without a second batch table.
		"import_id": resource.WorkflowImportID, "resource_id": resource.ID, "task_kind": "refresh", "line_number": resource.WorkflowLineNumber,
		"family_reservation_confirmed": false, "secret_payload": iCloudJSON(secret), "session_payload": nil,
		"manual_verification_code": "", "pending_sms_purpose": pendingSMSPurpose, "sms_sent_at": nil, "sms_poll_deadline": nil,
		"forward_preparation_id": nil, "onboarding_status": iCloudOnboardingProcessing, "stage": stage,
		"dispatch_status": "pending", "generation": generation, "expected_credential_revision": resource.CredentialRevision,
		"claim_token": "", "attempts": 0, "max_attempts": iCloudConfiguredOnboardingMaxAttempts(), "stage_attempts": 0,
		"next_attempt_at": now, "last_error_category": "", "last_safe_error": "", "started_at": nil, "finished_at": nil,
		"icloud_activation_confirmed_at": nil, "onboarding_operator_user_id": resource.WorkflowOperatorUserID, "onboarding_request_id": resource.WorkflowRequestID,
		"onboarding_idempotency_key": resource.WorkflowIdempotencyKey, "onboarding_request_fingerprint": resource.WorkflowRequestFingerprint, "updated_at": now,
	}
	if forceOldCookie {
		delete(updates, "last_safe_error")
	}
	result := tx.Model(&iCloudResourceModel{}).
		Where("id = ? AND onboarding_status NOT IN ?", resourceID, []string{iCloudOnboardingProcessing, iCloudOnboardingWaiting}).
		Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected != 1 {
		return false, nil
	}
	return true, nil
}

func isICloudOldCookieBackfill(task *iCloudOnboardingTaskModel) bool {
	return task != nil && task.TaskKind == "refresh" && task.PendingSMSPurpose == appleSMSOldCookieLogin
}

// Workflow and resource state share a row after the resource-first migration.
// Old-cookie maintenance must not replace the resource's canonical safe error
// with a transient workflow message.
func omitICloudOldCookieSafeError(task *iCloudOnboardingTaskModel, updates map[string]any) {
	if isICloudOldCookieBackfill(task) {
		delete(updates, "last_safe_error")
	}
}

func iCloudRefreshSnapshotMatches(resource iCloudResourceModel, task *iCloudOnboardingTaskModel) bool {
	if task == nil || task.ResourceID == nil || resource.ID != *task.ResourceID ||
		resource.Status == iCloudResourceDeleted || resource.Status == iCloudResourceDisabled ||
		resource.CredentialRevision != task.ExpectedCredentialRevision || resource.KitesimPhoneID == nil || task.KitesimPhoneID == nil ||
		*resource.KitesimPhoneID != *task.KitesimPhoneID {
		return false
	}
	resourcePhone, taskPhone := onboardingPhoneDigits(resource.BoundPhoneNumber), onboardingPhoneDigits(task.BoundPhoneNumber)
	return resourcePhone != "" && taskPhone != "" && sameICloudPhoneNumber(resourcePhone, taskPhone)
}

func (s *Service) activateAdminICloudResourceTx(
	ctx context.Context,
	tx *gorm.DB,
	resourceID uint,
	expectedVersion uint64,
	now time.Time,
) (*AdminICloudMutationResult, bool, error) {
	var root iCloudRootModel
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND type = ?", resourceID, "icloud").Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, ErrICloudResourceNotFound
		}
		return nil, false, err
	}
	if root.Version != expectedVersion {
		return nil, false, ErrICloudResourceVersion
	}
	var resource iCloudResourceModel
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&resource, resourceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, ErrICloudResourceNotFound
		}
		return nil, false, err
	}
	if resource.Status == iCloudResourceDeleted {
		return nil, false, ErrICloudResourceNotFound
	}
	if resource.Status == iCloudResourceDisabled {
		return nil, false, ErrICloudResourceStatus
	}
	created, err := s.ensureICloudCookieRefreshTx(ctx, tx, resourceID, true)
	if err != nil {
		return nil, false, err
	}
	if !created {
		return nil, false, ErrICloudCookieRefreshUnavailable
	}
	updated := tx.WithContext(ctx).Model(&iCloudRootModel{}).
		Where("id = ? AND type = ? AND version = ?", resourceID, "icloud", root.Version).
		Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now})
	if updated.Error != nil {
		return nil, false, updated.Error
	}
	if updated.RowsAffected != 1 {
		return nil, false, ErrICloudResourceVersion
	}
	root.Version++
	return adminICloudMutationResult(root, resource), true, nil
}

func (s *Service) preflightICloudRefreshTask(ctx context.Context, task *iCloudOnboardingTaskModel) (bool, error) {
	if task == nil || task.TaskKind != "refresh" {
		return true, nil
	}
	if task.ResourceID == nil {
		return false, s.failICloudOnboardingTask(ctx, task, "invalid_refresh_state", "Apple session refresh state is invalid.")
	}
	var locked iCloudOnboardingTaskModel
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND generation = ? AND claim_token = ? AND dispatch_status = ? AND task_kind = ?", task.ID, task.Generation, task.ClaimToken, "running", "refresh").
			First(&locked).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errICloudRefreshStale
			}
			return err
		}
		if locked.ResourceID == nil || *locked.ResourceID != *task.ResourceID {
			return errICloudRefreshStale
		}
		var resource iCloudResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&resource, *locked.ResourceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errICloudRefreshStale
			}
			return err
		}
		if !iCloudRefreshSnapshotMatches(resource, &locked) {
			return errICloudRefreshStale
		}
		return nil
	})
	if errors.Is(err, errICloudRefreshStale) {
		return false, s.failICloudRefreshTask(ctx, task, "refresh_stale", "Apple session refresh was canceled because the resource changed.")
	}
	if err != nil {
		return false, ErrICloudOnboardingTemporary
	}
	*task = locked
	return true, nil
}

func (s *Service) refreshICloudOnboardingResource(ctx context.Context, task *iCloudOnboardingTaskModel, secret iCloudOnboardingSecret) error {
	if task == nil {
		return ErrICloudOnboardingTemporary
	}
	if task.TaskKind != "refresh" || task.ResourceID == nil {
		return s.failICloudOnboardingTask(ctx, task, "invalid_refresh_state", "Apple session refresh state is invalid.")
	}
	if task.KitesimPhoneID == nil || strings.TrimSpace(task.BoundPhoneNumber) == "" {
		return s.failICloudOnboardingTask(ctx, task, "phone_binding_missing", "Cookie refresh requires the permanently bound eSIM phone.")
	}
	response, err := s.executeICloudOnboardingApple(ctx, task, secret, AppleOnboardingRequest{Operation: appleOnboardingExport})
	if err != nil {
		return s.handleICloudOnboardingAppleError(ctx, task, err)
	}
	if response.NewChannel == nil || strings.TrimSpace(response.NewChannel.Cookie) == "" {
		return s.failICloudOnboardingTask(ctx, task, "new_cookie_missing", "Apple Account did not return a usable refreshed session cookie.")
	}
	if task.ICloudOpened && (response.OldChannel == nil || strings.TrimSpace(response.OldChannel.Cookie) == "") {
		return s.failICloudOnboardingTask(ctx, task, "old_cookie_missing", "iCloud did not return a usable refreshed V2 session cookie.")
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked iCloudOnboardingTaskModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND generation = ? AND claim_token = ? AND dispatch_status = ? AND task_kind = ?", task.ID, task.Generation, task.ClaimToken, "running", "refresh").
			First(&locked).Error; err != nil {
			return errICloudRefreshStale
		}
		if locked.ResourceID == nil || *locked.ResourceID != *task.ResourceID {
			return errICloudRefreshStale
		}
		var resource iCloudResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&resource, *locked.ResourceID).Error; err != nil {
			return errICloudRefreshStale
		}
		if !iCloudRefreshSnapshotMatches(resource, task) {
			return errICloudRefreshStale
		}
		channels := []iCloudImportChannel{appleOnboardingImportChannel(*response.NewChannel)}
		if locked.ICloudOpened && response.OldChannel != nil {
			channels = append(channels, appleOnboardingImportChannel(*response.OldChannel))
		}
		if err := upsertICloudImportChannelsTx(tx, resource.ID, channels, true, now); err != nil {
			return err
		}
		countryCode := resource.CountryCode
		if value := strings.ToUpper(strings.TrimSpace(response.CountryCode)); value != "" {
			countryCode = value
		}
		generation := resource.ValidationGeneration + 1
		if generation == 0 {
			generation = 1
		}
		updates := map[string]any{
			"country_code": countryCode, "icloud_opened": locked.ICloudOpened,
			// Cookie maintenance is independent from the resource's sale and
			// provisioning state. Validation may be queued, but it must not make
			// an otherwise usable resource unavailable to normal operations.
			"validation_generation": generation, "validation_failures": 0,
			"next_validation_at": now, "last_safe_error": "", "updated_at": now,
		}
		if resource.AccountRole == "primary" {
			updates["family_next_sync_at"] = now
		}
		updated := tx.Model(&iCloudResourceModel{}).
			Where("id = ? AND credential_revision = ?", resource.ID, locked.ExpectedCredentialRevision).
			Updates(updates)
		if updated.Error != nil || updated.RowsAffected != 1 {
			return errICloudRefreshStale
		}
		if err := tx.Model(&iCloudRootModel{}).Where("id = ?", resource.ID).
			Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
			return err
		}
		finishedUpdates := map[string]any{
			"onboarding_status": iCloudOnboardingCompleted, "stage": "completed", "dispatch_status": "succeeded",
			"secret_payload": nil, "session_payload": nil, "manual_verification_code": "", "pending_sms_purpose": "",
			"sms_sent_at": nil, "sms_poll_deadline": nil, "forward_preparation_id": nil,
			"claim_token": "", "next_attempt_at": nil,
			"last_error_category": "", "last_safe_error": "", "finished_at": now, "updated_at": now,
		}
		omitICloudOldCookieSafeError(&locked, finishedUpdates)
		finished := tx.Model(&iCloudOnboardingTaskModel{}).
			Where("id = ? AND generation = ? AND claim_token = ?", locked.ID, locked.Generation, locked.ClaimToken).
			Updates(finishedUpdates)
		if finished.Error != nil || finished.RowsAffected != 1 {
			return errICloudRefreshStale
		}
		return nil
	})
	if errors.Is(err, errICloudRefreshStale) {
		return s.failICloudOnboardingTask(ctx, task, "refresh_stale", "Apple session refresh was canceled because the resource changed.")
	}
	if err != nil {
		return ErrICloudOnboardingTemporary
	}
	_ = s.ScheduleICloudValidationDispatcher(context.WithoutCancel(ctx), 0)
	return nil
}

func (s *Service) completeICloudOldCookieBackfill(ctx context.Context, task *iCloudOnboardingTaskModel, channel AppleOnboardingChannel) error {
	if task == nil || task.ResourceID == nil || !isICloudOldCookieBackfill(task) || channel.Kind != iCloudChannelWeb || strings.TrimSpace(channel.Cookie) == "" {
		return ErrICloudOnboardingTemporary
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked iCloudOnboardingTaskModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND generation = ? AND claim_token = ? AND dispatch_status = ? AND task_kind = ?", task.ID, task.Generation, task.ClaimToken, "running", "refresh").
			First(&locked).Error; err != nil || !isICloudOldCookieBackfill(&locked) || locked.ResourceID == nil || *locked.ResourceID != *task.ResourceID {
			return errICloudRefreshStale
		}
		var resource iCloudResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&resource, *locked.ResourceID).Error; err != nil {
			return errICloudRefreshStale
		}
		if !iCloudRefreshSnapshotMatches(resource, task) {
			return errICloudRefreshStale
		}
		if err := upsertICloudImportChannelsTx(tx, resource.ID, []iCloudImportChannel{appleOnboardingImportChannel(channel)}, false, now); err != nil {
			return err
		}
		if err := tx.Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).
			Updates(map[string]any{"icloud_opened": true, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&iCloudRootModel{}).Where("id = ?", resource.ID).
			Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
			return err
		}
		finishedUpdates := map[string]any{
			"onboarding_status": iCloudOnboardingCompleted, "stage": "completed", "dispatch_status": "succeeded",
			"secret_payload": nil, "session_payload": nil, "manual_verification_code": "", "pending_sms_purpose": "",
			"sms_sent_at": nil, "sms_poll_deadline": nil, "forward_preparation_id": nil,
			"claim_token": "", "next_attempt_at": nil,
			"last_error_category": "", "last_safe_error": "", "finished_at": now, "updated_at": now,
		}
		omitICloudOldCookieSafeError(&locked, finishedUpdates)
		finished := tx.Model(&iCloudOnboardingTaskModel{}).
			Where("id = ? AND generation = ? AND claim_token = ?", locked.ID, locked.Generation, locked.ClaimToken).
			Updates(finishedUpdates)
		if finished.Error != nil || finished.RowsAffected != 1 {
			return errICloudRefreshStale
		}
		return nil
	})
	if errors.Is(err, errICloudRefreshStale) {
		return s.failICloudOnboardingTask(ctx, task, "refresh_stale", "Old Cookie backfill was canceled because the resource changed.")
	}
	if err != nil {
		return ErrICloudOnboardingTemporary
	}
	return nil
}

func (s *Service) failICloudRefreshTask(ctx context.Context, task *iCloudOnboardingTaskModel, category, message string) error {
	if task == nil || task.ResourceID == nil {
		return ErrICloudOnboardingTemporary
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		taskUpdates := map[string]any{
			"onboarding_status": iCloudOnboardingFailed, "dispatch_status": "failed", "claim_token": "", "next_attempt_at": nil,
			"attempts": min(task.Attempts+1, task.MaxAttempts), "last_error_category": safeICloudImportMessage(category),
			"last_safe_error": safeICloudImportMessage(message), "secret_payload": nil, "session_payload": nil,
			"manual_verification_code": "", "pending_sms_purpose": "", "sms_sent_at": nil, "sms_poll_deadline": nil,
			"forward_preparation_id": nil, "finished_at": now, "updated_at": now,
		}
		omitICloudOldCookieSafeError(task, taskUpdates)
		result := tx.Model(&iCloudOnboardingTaskModel{}).
			Where("id = ? AND generation = ? AND claim_token = ? AND dispatch_status = ? AND task_kind = ?", task.ID, task.Generation, task.ClaimToken, "running", "refresh").
			Updates(taskUpdates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errICloudRefreshStale
		}
		return nil
	})
	if err != nil {
		return ErrICloudOnboardingTemporary
	}
	return nil
}
