package icloud

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func deviceRequired(task *iCloudOnboardingTaskModel) bool {
	return task.DeviceCodeAPI != "" || task.TaskKind == "onboarding" && task.DeviceBindStatus != ""
}

func (s *Service) ensureOnboardingDevice(ctx context.Context, task *iCloudOnboardingTaskModel, secret iCloudOnboardingSecret) (bool, error) {
	if task.DeviceCodeAPI != "" {
		return true, nil
	}
	if task.KitesimPhoneID == nil {
		return false, s.waitICloudOnboardingTask(ctx, task, nil, "waiting", "Device enrollment requires the initially bound phone.")
	}
	binding, err := s.EnsureDeviceBinding(ctx, task.PrimaryEmail, secret.Password, *task.KitesimPhoneID, task.BoundPhoneNumber, &task.ID, time.Time{})
	if err != nil {
		next := s.now().Add(deviceBindingPoll)
		return false, s.waitICloudOnboardingTask(ctx, task, &next, "pending", "Device enrollment is unavailable; check the device platform settings.")
	}
	if binding.Status == "success" && binding.CodeAPI != "" {
		next := s.now().Add(iCloudOnboardingStageDelay())
		return false, s.advanceICloudOnboardingTask(ctx, task, task.Stage, &next, map[string]any{"device_code_api": binding.CodeAPI, "device_bind_status": "success", "device_account_id": binding.RemoteID})
	}
	message := "Waiting for device binding; subsequent verification will use the device API."
	if binding.LastError != "" {
		message = binding.LastError
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	var resumeAt *time.Time
	changed := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current iCloudOnboardingTaskModel
		query := tx.Model(&iCloudOnboardingTaskModel{}).
			Where("id = ? AND generation = ? AND claim_token = ? AND dispatch_status = ?", task.ID, task.Generation, task.ClaimToken, "running")
		if err := query.Clauses(clause.Locking{Strength: "UPDATE"}).Select("device_code_api").Take(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		// The same resource lock serializes waiting with the binding worker's
		// success update. A ready API must never be replaced by an indefinite wait.
		updates := map[string]any{
			"onboarding_status": iCloudOnboardingWaiting, "dispatch_status": "waiting",
			"claim_token": "", "next_attempt_at": nil, "last_safe_error": safeICloudImportMessage(message), "updated_at": now,
		}
		if current.DeviceCodeAPI != "" {
			next := now.Add(iCloudOnboardingStageDelay())
			resumeAt = &next
			updates["onboarding_status"] = iCloudOnboardingProcessing
			updates["dispatch_status"] = "pending"
			updates["next_attempt_at"] = next
			updates["last_safe_error"] = ""
			updates["last_error_category"] = ""
			updates["stage_attempts"] = 0
		}
		omitICloudOldCookieSafeError(task, updates)
		result := tx.Model(&iCloudOnboardingTaskModel{}).
			Where("id = ? AND generation = ? AND claim_token = ? AND dispatch_status = ?", task.ID, task.Generation, task.ClaimToken, "running").Updates(updates)
		changed = result.RowsAffected == 1
		return result.Error
	})
	if err != nil {
		return false, ErrICloudOnboardingTemporary
	}
	if changed {
		_ = s.refreshICloudOnboardingImport(context.WithoutCancel(ctx), iCloudOnboardingImportID(task))
		if resumeAt != nil {
			_ = s.ScheduleICloudOnboardingDispatcher(context.WithoutCancel(ctx), resumeAt.Sub(s.now()))
		}
	}
	return false, nil
}

func (s *Service) startOnboardingDevice(ctx context.Context, task *iCloudOnboardingTaskModel, secret iCloudOnboardingSecret) error {
	if !DeviceCodeConfigured() || task.TaskKind != "onboarding" || task.KitesimPhoneID == nil || task.DeviceBindStatus != "" || task.DeviceCodeAPI != "" {
		return nil
	}
	_, err := s.EnsureDeviceBinding(ctx, task.PrimaryEmail, secret.Password, *task.KitesimPhoneID, task.BoundPhoneNumber, &task.ID, s.now())
	return err
}

func (s *Service) waitOnboardingDeviceCode(ctx context.Context, task *iCloudOnboardingTaskModel) error {
	if task.SMSPollDeadline == nil || !s.now().Before(*task.SMSPollDeadline) {
		return s.retryICloudOnboardingSMSRound(ctx, task, "The device code was not available before the verification deadline.")
	}
	code, err := s.FetchDeviceCode(ctx, task.DeviceCodeAPI)
	if err == nil {
		return s.advanceICloudOnboardingTask(ctx, task, "sms_verify", nil, map[string]any{"manual_verification_code": code, "stage_attempts": task.StageAttempts})
	}
	message := "Waiting for the Apple device verification code."
	if !errors.Is(err, errDeviceNoCode) {
		message = err.Error()
	}
	next := s.now().Add(iCloudOnboardingSMSPoll)
	return s.waitICloudOnboardingTask(ctx, task, &next, "pending", message)
}
