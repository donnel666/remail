package icloud

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const iCloudCookieRecoveryTaskKind = "cookie_recovery"

// ensureICloudCookieMaintenanceTx keeps the two session repairs independent:
// Apple Account recovery only replaces the management cookie, while an
// already-opened iCloud account uses the existing old-cookie backfill path.
func (s *Service) ensureICloudCookieMaintenanceTx(ctx context.Context, tx *gorm.DB, resourceID uint, forceOldCookie bool) (bool, error) {
	if forceOldCookie {
		return s.ensureICloudCookieRefreshTx(ctx, tx, resourceID, true)
	}
	var resource iCloudResourceModel
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&resource, resourceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if resource.Status == iCloudResourceDeleted || resource.Status == iCloudResourceDisabled || resource.AliasCount >= iCloudMaxAliases {
		return false, nil
	}
	if iCloudCookieMaintenanceWorkflowActive(resource) {
		return false, nil
	}
	var invalidApple int64
	if err := tx.Model(&iCloudResourceChannelModel{}).
		Where("resource_id = ? AND kind = ? AND session_status = ?", resourceID, iCloudChannelAppleAccount, iCloudSessionInvalid).
		Count(&invalidApple).Error; err != nil {
		return false, err
	}
	if invalidApple > 0 && !iCloudCookieRecoveryTerminallyFailed(resource) && !iCloudCookiePhoneBlacklistedTerminallyFailed(resource) {
		created, err := s.ensureICloudCookieRecoveryTx(ctx, tx, resourceID)
		if err != nil {
			return false, err
		}
		if created {
			return true, nil
		}
		// Missing recovery credentials must not prevent an already-opened
		// account from repairing its independent iCloud Web channel.
	}
	if resource.ICloudOpened {
		var invalidWeb int64
		if err := tx.Model(&iCloudResourceChannelModel{}).
			Where("resource_id = ? AND kind = ? AND session_status = ?", resourceID, iCloudChannelWeb, iCloudSessionInvalid).
			Count(&invalidWeb).Error; err != nil {
			return false, err
		}
		if invalidWeb > 0 && !iCloudCookiePhoneBlacklistedTerminallyFailed(resource) {
			return s.ensureICloudCookieRefreshTx(ctx, tx, resourceID, true)
		}
	}
	// No generic refresh fallback: each invalid channel has its own recovery
	// path. A missing Apple recovery credential must not turn into a combined
	// refresh that touches the healthy iCloud Web session.
	return false, nil
}

// A terminal recovery failure must not be recreated on every validation or
// provision pass. A changed credential revision is an explicit new recovery
// signal and clears this fence when the next task is created.
func iCloudCookieRecoveryTerminallyFailed(resource iCloudResourceModel) bool {
	return resource.WorkflowTaskKind == iCloudCookieRecoveryTaskKind &&
		resource.OnboardingStatus == iCloudOnboardingFailed &&
		resource.WorkflowExpectedCredential == resource.CredentialRevision &&
		strings.TrimSpace(resource.WorkflowLastErrorCategory) != ""
}

func (s *Service) ensureICloudCookieRecoveryTx(ctx context.Context, tx *gorm.DB, resourceID uint) (bool, error) {
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
	if resource.Status == iCloudResourceDeleted || resource.Status == iCloudResourceDisabled || resource.AliasCount >= iCloudMaxAliases ||
		strings.TrimSpace(resource.BoundPhoneNumber) == "" || resource.KitesimPhoneID == nil {
		return false, nil
	}
	var invalidApple int64
	if err := tx.Model(&iCloudResourceChannelModel{}).
		Where("resource_id = ? AND kind = ? AND session_status = ?", resourceID, iCloudChannelAppleAccount, iCloudSessionInvalid).
		Count(&invalidApple).Error; err != nil || invalidApple == 0 {
		return false, err
	}
	if iCloudCookieMaintenanceWorkflowActive(resource) {
		return false, nil
	}
	var credential iCloudResourceCredentialModel
	if err := tx.First(&credential, resourceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	var answers [3]iCloudSecurityAnswer
	if err := json.Unmarshal(credential.SecurityAnswers, &answers); err != nil ||
		strings.TrimSpace(credential.ApplePassword) == "" || credential.Birthday.IsZero() {
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
	now := s.now().UTC().Truncate(time.Millisecond)
	generation := resource.WorkflowGeneration + 1
	if generation == 0 {
		generation = 1
	}
	// Persist the random delay in the resource workflow row so a worker restart
	// cannot turn a delayed recovery into an immediate SMS request.
	nextAttempt := now.Add(time.Duration(1+rand.Intn(60)) * time.Minute)
	updates := map[string]any{
		"import_id": resource.WorkflowImportID, "resource_id": resource.ID, "task_kind": iCloudCookieRecoveryTaskKind,
		"line_number": resource.WorkflowLineNumber, "family_reservation_confirmed": false,
		"secret_payload": iCloudJSON(secret), "session_payload": nil, "manual_verification_code": "",
		"pending_sms_purpose": "", "sms_sent_at": nil, "sms_poll_deadline": nil, "forward_preparation_id": nil,
		"onboarding_status": iCloudOnboardingProcessing, "stage": "manage_prepare", "dispatch_status": "pending",
		"generation": generation, "expected_credential_revision": resource.CredentialRevision, "claim_token": "",
		"attempts": 0, "max_attempts": iCloudConfiguredOnboardingMaxAttempts(), "stage_attempts": 0,
		"next_attempt_at": nextAttempt, "last_error_category": "", "last_safe_error": "", "started_at": nil,
		"finished_at": nil, "icloud_activation_confirmed_at": nil,
		"onboarding_operator_user_id": resource.WorkflowOperatorUserID, "onboarding_request_id": resource.WorkflowRequestID,
		"onboarding_idempotency_key": resource.WorkflowIdempotencyKey, "onboarding_request_fingerprint": resource.WorkflowRequestFingerprint,
		"updated_at": now,
	}
	// Starting maintenance must not hide the resource's last validation error;
	// only a successful channel replacement may clear it.
	delete(updates, "last_safe_error")
	prepareICloudCookieMaintenanceResource(resource, updates, now)
	result := tx.Model(&iCloudResourceModel{}).
		Where("id = ? AND (onboarding_status NOT IN ? OR (task_kind IN ? AND expected_credential_revision <> credential_revision))",
			resourceID, []string{iCloudOnboardingProcessing, iCloudOnboardingWaiting}, []string{"refresh", iCloudCookieRecoveryTaskKind}).
		Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func isICloudCookieRecoveryTask(task *iCloudOnboardingTaskModel) bool {
	return task != nil && task.TaskKind == iCloudCookieRecoveryTaskKind
}

// A recovery may sit in the queue for up to an hour. Recheck the credential
// and phone snapshot immediately before the first Apple request so a queued
// task cannot use secrets that an admin/import operation has replaced.
func (s *Service) preflightICloudCookieRecoveryTask(ctx context.Context, task *iCloudOnboardingTaskModel) (bool, error) {
	if task == nil || !isICloudCookieRecoveryTask(task) {
		return true, nil
	}
	if task.ResourceID == nil {
		return false, s.failICloudOnboardingTask(ctx, task, "invalid_cookie_recovery_state", "Apple Account cookie recovery state is invalid.")
	}
	var locked iCloudOnboardingTaskModel
	stoppedAtAliasLimit := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND generation = ? AND claim_token = ? AND dispatch_status = ? AND task_kind = ?", task.ID, task.Generation, task.ClaimToken, "running", iCloudCookieRecoveryTaskKind).
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
		if resource.AliasCount >= iCloudMaxAliases {
			if err := completeICloudCookieMaintenanceAtAliasLimitTx(ctx, tx, &locked, s.now().UTC().Truncate(time.Millisecond)); err != nil {
				return err
			}
			stoppedAtAliasLimit = true
		}
		return nil
	})
	if errors.Is(err, errICloudRefreshStale) {
		return false, s.failICloudOnboardingTask(ctx, task, "cookie_recovery_stale", "Apple Account cookie recovery was canceled because the resource changed.")
	}
	if err != nil {
		return false, ErrICloudOnboardingTemporary
	}
	if stoppedAtAliasLimit {
		s.cancelICloudOnboardingSMSChallenge(context.WithoutCancel(ctx), &locked)
		return false, nil
	}
	*task = locked
	return true, nil
}

func (s *Service) recoverICloudAppleCookie(ctx context.Context, task *iCloudOnboardingTaskModel, secret iCloudOnboardingSecret) error {
	if task == nil {
		return ErrICloudOnboardingTemporary
	}
	if !isICloudCookieRecoveryTask(task) || task.ResourceID == nil ||
		task.KitesimPhoneID == nil || strings.TrimSpace(task.BoundPhoneNumber) == "" {
		return s.failICloudOnboardingTask(ctx, task, "invalid_cookie_recovery_state", "Apple Account cookie recovery state is invalid.")
	}
	response, err := s.executeICloudOnboardingApple(ctx, task, secret, AppleOnboardingRequest{Operation: appleOnboardingExport, SkipPrivateAlias: true})
	if err != nil {
		return s.handleICloudOnboardingAppleError(ctx, task, err)
	}
	if response.NewChannel == nil || response.NewChannel.Kind != iCloudChannelAppleAccount || strings.TrimSpace(response.NewChannel.Cookie) == "" {
		return s.retryICloudOnboardingTask(ctx, task, "manage_prepare", nil, "new_cookie_missing", "Apple Account did not return a usable replacement cookie.", map[string]any{"session_payload": nil})
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked iCloudOnboardingTaskModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND generation = ? AND claim_token = ? AND dispatch_status = ? AND task_kind = ?", task.ID, task.Generation, task.ClaimToken, "running", iCloudCookieRecoveryTaskKind).
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
		if resource.Status == iCloudResourceDeleted || resource.Status == iCloudResourceDisabled ||
			resource.CredentialRevision != locked.ExpectedCredentialRevision || resource.KitesimPhoneID == nil ||
			*resource.KitesimPhoneID != *locked.KitesimPhoneID || !sameICloudPhoneNumber(resource.BoundPhoneNumber, locked.BoundPhoneNumber) {
			return errICloudRefreshStale
		}
		if err := upsertICloudImportChannelsTx(tx, resource.ID, []iCloudImportChannel{appleOnboardingImportChannel(*response.NewChannel)}, false, now); err != nil {
			return err
		}
		validationGeneration := resource.ValidationGeneration + 1
		if validationGeneration == 0 {
			validationGeneration = 1
		}
		credentialRevision := resource.CredentialRevision + 1
		if credentialRevision == 0 {
			credentialRevision = 1
		}
		countryCode := resource.CountryCode
		if value := strings.ToUpper(strings.TrimSpace(response.CountryCode)); value != "" {
			countryCode = value
		}
		updated := tx.Model(&iCloudResourceModel{}).
			Where("id = ? AND credential_revision = ?", resource.ID, locked.ExpectedCredentialRevision).
			Updates(map[string]any{
				"country_code": countryCode, "credential_revision": credentialRevision, "credential_updated_at": now,
				"validation_generation": validationGeneration, "validation_failures": 0, "next_validation_at": now,
				"next_provision_at": nil, "last_safe_error": "", "updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errICloudRefreshStale
		}
		rootUpdated := tx.Model(&iCloudRootModel{}).Where("id = ?", resource.ID).
			Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now})
		if rootUpdated.Error != nil {
			return rootUpdated.Error
		}
		if rootUpdated.RowsAffected != 1 {
			return errICloudRefreshStale
		}
		finished := tx.Model(&iCloudOnboardingTaskModel{}).
			Where("id = ? AND generation = ? AND claim_token = ?", locked.ID, locked.Generation, locked.ClaimToken).
			Updates(map[string]any{
				"onboarding_status": iCloudOnboardingCompleted, "stage": "completed", "dispatch_status": "succeeded",
				"secret_payload": nil, "session_payload": nil, "manual_verification_code": "", "pending_sms_purpose": "",
				"sms_sent_at": nil, "sms_poll_deadline": nil, "forward_preparation_id": nil, "claim_token": "",
				"next_attempt_at": nil, "last_error_category": "", "last_safe_error": "", "finished_at": now, "updated_at": now,
			})
		if finished.Error != nil {
			return finished.Error
		}
		if finished.RowsAffected != 1 {
			return errICloudRefreshStale
		}
		return nil
	})
	if errors.Is(err, errICloudRefreshStale) {
		return s.failICloudOnboardingTask(ctx, task, "cookie_recovery_stale", "Apple Account cookie recovery was canceled because the resource changed.")
	}
	if err != nil {
		return ErrICloudOnboardingTemporary
	}
	_ = s.ScheduleICloudValidationDispatcher(context.WithoutCancel(ctx), 0)
	s.scheduleICloudCookieMaintenanceAfter(context.WithoutCancel(ctx), task.ResourceID, iCloudCookieRecoveryTaskKind)
	return nil
}

// The resource-first workflow row can execute one maintenance task at a time.
// Once one channel is repaired, immediately enqueue the other invalid channel
// instead of waiting for a later validation/provision sweep.
func (s *Service) scheduleICloudCookieMaintenanceAfter(ctx context.Context, resourceID *uint, completedKind string) {
	if s == nil || s.db == nil || resourceID == nil || *resourceID == 0 {
		return
	}
	if completedKind != iCloudCookieRecoveryTaskKind && completedKind != "refresh" {
		return
	}
	var created bool
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		created, err = s.ensureICloudCookieMaintenanceTx(ctx, tx, *resourceID, false)
		return err
	})
	if err == nil && created {
		_ = s.ScheduleICloudOnboardingDispatcher(context.WithoutCancel(ctx), 0)
	}
}
