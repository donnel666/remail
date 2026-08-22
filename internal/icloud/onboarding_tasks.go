package icloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/kitesim"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	iCloudOnboardingQueueLease        = 2 * time.Minute
	iCloudOnboardingRunningLease      = 5 * time.Minute
	iCloudOnboardingSMSPoll           = 4 * time.Second
	iCloudOnboardingSMSDeadline       = 2 * time.Minute
	iCloudOnboardingFamilyRetry       = 5 * time.Minute
	iCloudOnboardingForwardRetry      = 4 * time.Second
	iCloudOnboardingStageDelayMinimum = 60 * time.Second
	iCloudOnboardingStageDelayMaximum = 200 * time.Second
	iCloudFamilyChildLimit            = 5
)

var appleSMSCodePattern = regexp.MustCompile(`(?:^|[^0-9])([0-9]{6})(?:[^0-9]|$)`)

type iCloudOnboardingTask struct {
	TaskID     uint   `json:"taskId"`
	Generation uint64 `json:"generation"`
}

func (s *Service) DispatchICloudOnboardingTasks(ctx context.Context, limit int) error {
	if s == nil || s.db == nil || s.queue == nil {
		return ErrICloudOnboardingTemporary
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	if err := s.recoverStaleICloudOnboardingTasks(ctx, now); err != nil {
		return err
	}
	var tasks []iCloudOnboardingTaskModel
	if err := s.db.WithContext(ctx).
		Where("task_kind IN ? AND onboarding_status IN ? AND dispatch_status = ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)", []string{"onboarding", "refresh"}, []string{iCloudOnboardingProcessing, iCloudOnboardingWaiting}, "pending", now).
		Order("id ASC").Limit(limit).Find(&tasks).Error; err != nil {
		return ErrICloudOnboardingTemporary
	}
	var result error
	for _, task := range tasks {
		accepted, err := s.enqueueICloudOnboardingTask(ctx, iCloudOnboardingTask{TaskID: task.ID, Generation: task.Generation})
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if !accepted {
			continue
		}
		updated := s.db.WithContext(ctx).Model(&iCloudOnboardingTaskModel{}).
			Where("id = ? AND generation = ? AND onboarding_status IN ? AND dispatch_status = ?", task.ID, task.Generation, []string{iCloudOnboardingProcessing, iCloudOnboardingWaiting}, "pending").
			Updates(map[string]any{"onboarding_status": iCloudOnboardingProcessing, "dispatch_status": "queued", "next_attempt_at": now, "updated_at": now})
		if updated.Error != nil {
			result = errors.Join(result, ErrICloudOnboardingTemporary)
		}
	}
	if err := s.reconcileICloudOnboardingImports(ctx, limit); err != nil {
		result = errors.Join(result, err)
	}
	return result
}

func (s *Service) reconcileICloudOnboardingImports(ctx context.Context, limit int) error {
	var importIDs []uint
	if err := s.db.WithContext(ctx).Raw(`
		SELECT DISTINCT import_id
		FROM icloud_resources
		WHERE import_id IS NOT NULL AND task_kind = 'onboarding'
		  AND onboarding_status IN (?, ?)
		ORDER BY import_id ASC
		LIMIT ?`, iCloudOnboardingProcessing, iCloudOnboardingWaiting, limit).Scan(&importIDs).Error; err != nil {
		return ErrICloudOnboardingTemporary
	}
	var result error
	for _, importID := range importIDs {
		result = errors.Join(result, s.refreshICloudOnboardingImport(ctx, importID))
	}
	return result
}

func (s *Service) recoverStaleICloudOnboardingTasks(ctx context.Context, now time.Time) error {
	var tasks []iCloudOnboardingTaskModel
	if err := s.db.WithContext(ctx).
		Where(`task_kind IN ? AND onboarding_status IN ? AND (
			(dispatch_status = ? AND next_attempt_at IS NOT NULL AND next_attempt_at <= ?) OR
			(dispatch_status = ? AND started_at IS NOT NULL AND started_at <= ?)
		)`, []string{"onboarding", "refresh"}, []string{iCloudOnboardingProcessing, iCloudOnboardingWaiting}, "queued", now.Add(-iCloudOnboardingQueueLease), "running", now.Add(-iCloudOnboardingRunningLease)).
		Order("id ASC").Find(&tasks).Error; err != nil {
		return ErrICloudOnboardingTemporary
	}
	for _, task := range tasks {
		if err := s.releaseICloudOnboardingTask(ctx, iCloudOnboardingTask{TaskID: task.ID, Generation: task.Generation}, "Onboarding worker lease expired; dispatcher will retry.", now, true); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ProcessICloudOnboardingTask(ctx context.Context, payload iCloudOnboardingTask) error {
	if s == nil || s.db == nil || payload.TaskID == 0 || payload.Generation == 0 {
		return ErrICloudOnboardingTemporary
	}
	claim := newICloudOnboardingClaimToken()
	now := s.now().UTC().Truncate(time.Millisecond)
	result := s.db.WithContext(ctx).Model(&iCloudOnboardingTaskModel{}).
		Where("id = ? AND generation = ? AND task_kind IN ? AND onboarding_status IN ? AND dispatch_status IN ?", payload.TaskID, payload.Generation, []string{"onboarding", "refresh"}, []string{iCloudOnboardingProcessing, iCloudOnboardingWaiting}, []string{"pending", "queued"}).
		Updates(map[string]any{
			"onboarding_status": iCloudOnboardingProcessing, "dispatch_status": "running", "claim_token": claim,
			// started_at is the current worker lease timestamp, not the
			// lifetime start of the resource workflow.
			"started_at": now, "next_attempt_at": nil, "updated_at": now,
		})
	if result.Error != nil {
		return ErrICloudOnboardingTemporary
	}
	if result.RowsAffected != 1 {
		var current iCloudOnboardingTaskModel
		if err := s.db.WithContext(ctx).Select("id", "generation", "onboarding_status", "dispatch_status").First(&current, payload.TaskID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return ErrICloudOnboardingTemporary
		}
		if current.Generation == payload.Generation && current.DispatchStatus == "running" &&
			(current.Status == iCloudOnboardingProcessing || current.Status == iCloudOnboardingWaiting) {
			return ErrICloudOnboardingTemporary
		}
		return nil
	}
	var task iCloudOnboardingTaskModel
	if err := s.db.WithContext(ctx).First(&task, payload.TaskID).Error; err != nil {
		return ErrICloudOnboardingTemporary
	}
	task.ClaimToken = claim
	if task.TaskKind == "refresh" {
		proceed, err := s.preflightICloudRefreshTask(ctx, &task)
		if err != nil {
			return err
		}
		if !proceed {
			return nil
		}
	}
	if task.TaskKind == "onboarding" {
		if err := s.ensureICloudOnboardingAppleIDReservation(ctx, &task); err != nil {
			if errors.Is(err, ErrICloudOnboardingInvalid) {
				return s.failICloudOnboardingTask(ctx, &task, "invalid_credentials", "Stored Apple credentials are invalid.")
			}
			if !errors.Is(err, ErrICloudResourceIdentity) {
				return ErrICloudOnboardingTemporary
			}
			return s.failICloudOnboardingTask(ctx, &task, "duplicate_resource", "The Apple ID already exists as an iCloud resource.")
		}
	}
	return s.processICloudOnboardingStage(ctx, &task)
}

func (s *Service) processICloudOnboardingStage(ctx context.Context, task *iCloudOnboardingTaskModel) error {
	if task == nil || task.ID == 0 || task.ClaimToken == "" {
		return ErrICloudOnboardingTemporary
	}
	var secret iCloudOnboardingSecret
	if err := json.Unmarshal(task.SecretPayload, &secret); err != nil || strings.TrimSpace(secret.Password) == "" {
		return s.failICloudOnboardingTask(ctx, task, "invalid_credentials", "Stored Apple credentials are invalid.")
	}
	switch task.Stage {
	case "icloud_prepare", "old_cookie_prepare", "icloud_cookie_prepare", "family_select", "family_prepare", "family_reconcile_prepare", "manage_prepare":
		ready, err := s.checkICloudOnboardingSMSPhone(ctx, task)
		if err != nil || !ready {
			return err
		}
	}
	switch task.Stage {
	case "accepted":
		return s.assignICloudOnboardingPhone(ctx, task)
	case "icloud_prepare":
		return s.prepareICloudOnboardingApple(ctx, task, secret, appleOnboardingPrepareICloud, "icloud_finish")
	case "old_cookie_prepare":
		return s.prepareICloudOnboardingApple(ctx, task, secret, appleOnboardingPrepareICloud, "old_cookie_finish")
	case "icloud_cookie_prepare":
		return s.prepareICloudOnboardingApple(ctx, task, secret, appleOnboardingPrepareICloudCookie, "icloud_cookie_finish")
	case "sms_send":
		return s.sendICloudOnboardingSMS(ctx, task, secret)
	case "sms_wait":
		return s.waitICloudOnboardingSMS(ctx, task)
	case "sms_verify":
		return s.verifyICloudOnboardingSMS(ctx, task, secret)
	case "sms_verify_recover":
		return s.recoverICloudOnboardingSMSVerification(ctx, task)
	case "icloud_finish":
		return s.finishICloudOnboardingICloud(ctx, task, secret, false)
	case "old_cookie_finish":
		return s.finishICloudOnboardingICloud(ctx, task, secret, false)
	case "icloud_cookie_finish":
		return s.finishICloudOnboardingICloud(ctx, task, secret, true)
	case "family_select":
		return s.selectICloudOnboardingFamily(ctx, task)
	case "family_prepare":
		invite, organizer, err := s.iCloudOnboardingFamilyInvite(ctx, task)
		if err != nil {
			return err
		}
		if invite == "" {
			return nil
		}
		return s.prepareICloudOnboardingAppleWithInvite(ctx, task, secret, appleOnboardingPrepareFamily, "family_join_intent", invite, organizer)
	case "family_reconcile_prepare":
		invite, organizer, err := s.iCloudOnboardingFamilyRecoveryInvite(ctx, task)
		if err != nil {
			return err
		}
		return s.prepareICloudOnboardingAppleWithInvite(ctx, task, secret, appleOnboardingPrepareFamilyReconcile, "family_join_apply", invite, organizer)
	case "family_join_intent":
		return s.advanceICloudOnboardingTask(ctx, task, "family_join_apply", nil, nil)
	case "family_join_apply":
		return s.joinICloudOnboardingFamily(ctx, task, secret)
	case "manage_prepare":
		return s.prepareICloudOnboardingApple(ctx, task, secret, appleOnboardingPrepareManage, "manage_profile")
	case "manage_profile":
		return s.fetchICloudOnboardingManage(ctx, task, secret)
	case "forwarding_prepare":
		return s.prepareICloudOnboardingForwarding(ctx, task)
	case "forwarding_add_intent":
		return s.advanceICloudOnboardingTask(ctx, task, "forwarding_add_apply", nil, nil)
	case "forwarding_add_apply":
		return s.sendICloudOnboardingForwarding(ctx, task, secret)
	case "forwarding_wait":
		return s.waitICloudOnboardingForwarding(ctx, task)
	case "forwarding_verify_intent":
		return s.advanceICloudOnboardingTask(ctx, task, "forwarding_verify_apply", nil, nil)
	case "forwarding_verify_apply":
		return s.verifyICloudOnboardingForwarding(ctx, task, secret)
	case "resource_import":
		return s.importICloudOnboardingResource(ctx, task, secret)
	case "resource_refresh":
		return s.refreshICloudOnboardingResource(ctx, task, secret)
	case "waiting_family_reset":
		return s.waitICloudOnboardingTask(ctx, task, nil, "waiting", "Waiting for the family organizer sharing reset.")
	case "waiting_family_sharing":
		return s.waitICloudOnboardingTask(ctx, task, nil, "waiting", "Waiting for manual family sharing setup.")
	case "waiting_icloud_activation":
		return s.waitICloudOnboardingTask(ctx, task, nil, "waiting", "Waiting for manual iCloud activation.")
	default:
		return s.failICloudOnboardingTask(ctx, task, "invalid_stage", "Apple account onboarding stage is invalid.")
	}
}

func (s *Service) ensureICloudOnboardingAppleIDReservation(ctx context.Context, task *iCloudOnboardingTaskModel) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.ensureICloudOnboardingAppleIDReservationTx(tx, task)
	})
}

func (s *Service) ensureICloudOnboardingAppleIDReservationTx(tx *gorm.DB, task *iCloudOnboardingTaskModel) error {
	if tx == nil || task == nil || task.ID == 0 || task.ResourceID == nil || *task.ResourceID != task.ID || task.ImportID == nil || *task.ImportID == 0 {
		return ErrICloudResourceIdentity
	}
	var duplicateCount int64
	if err := tx.Model(&iCloudResourceModel{}).
		Where("id <> ? AND LOWER(primary_email) = ?", task.ID, iCloudImportEmailKey(task.PrimaryEmail)).
		Count(&duplicateCount).Error; err != nil {
		return err
	}
	if duplicateCount != 0 {
		return ErrICloudResourceIdentity
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	if err := reserveICloudAppleIDsTx(tx, []iCloudAppleIDReservationModel{{
		EmailKey: iCloudImportEmailKey(task.PrimaryEmail), OwnerKind: iCloudAppleIDReservationOnboarding,
		OwnerID: *task.ImportID, CreatedAt: now,
	}}); err != nil {
		return err
	}
	var resource struct {
		ID          uint   `gorm:"column:id"`
		OwnerUserID uint   `gorm:"column:owner_user_id"`
		AccountRole string `gorm:"column:account_role"`
		Status      string `gorm:"column:status"`
	}
	err := tx.Table("icloud_resources AS ir").Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("ir.id, er.owner_user_id, ir.account_role, ir.status").
		Joins("JOIN email_resources AS er ON er.id = ir.id AND er.type = ?", "icloud").
		Where("ir.id = ? AND LOWER(ir.primary_email) = ?", *task.ResourceID, iCloudImportEmailKey(task.PrimaryEmail)).Take(&resource).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrICloudResourceIdentity
		}
		return err
	}
	validRole := resource.AccountRole == "unknown" || resource.AccountRole == task.AccountRole
	if resource.OwnerUserID == 0 || !validRole || resource.Status == iCloudResourceDeleted {
		return ErrICloudResourceIdentity
	}
	return nil
}

func (s *Service) assignICloudOnboardingPhone(ctx context.Context, task *iCloudOnboardingTaskModel) error {
	if s.smsPhones == nil {
		return ErrICloudOnboardingTemporary
	}
	requested := strings.TrimSpace(task.BoundPhoneNumber) != ""
	var binding kitesim.SMSPhoneBinding
	var err error
	if task.AccountRole == "primary" && !requested {
		binding, err = s.smsPhones.BindICloudSMSPhoneBySuffix(ctx, task.PrimaryEmail, "")
		if errors.Is(err, kitesim.ErrPhoneMissing) {
			return s.advanceICloudOnboardingTask(ctx, task, "icloud_prepare", nil, nil)
		}
	} else {
		binding, err = s.smsPhones.BindICloudSMSPhone(ctx, task.PrimaryEmail, task.BoundPhoneNumber)
	}
	if err != nil {
		if errors.Is(err, kitesim.ErrSMSPhoneBindingConflict) {
			return s.failICloudOnboardingTask(ctx, task, "phone_binding_conflict", "The requested phone does not match this Apple ID's permanent phone binding.")
		}
		if errors.Is(err, kitesim.ErrPhoneMissing) && requested {
			return s.failICloudOnboardingTask(ctx, task, "phone_not_in_pool", "The requested phone is not available in the eSIM phone pool.")
		}
		if errors.Is(err, kitesim.ErrSMSPhoneBoundUnavailable) {
			return s.failICloudOnboardingTask(ctx, task, "phone_binding_unavailable", "The permanently bound phone is disabled or unavailable in the eSIM phone pool.")
		}
		if retryAt, ok := kitesim.SMSRetryAt(err); ok {
			retryAt = iCloudOnboardingSMSRetryAt(retryAt, s.now().UTC())
			return s.waitICloudOnboardingTask(ctx, task, &retryAt, "pending", "No eSIM phone is currently available.")
		}
		return ErrICloudOnboardingTemporary
	}
	phoneID := binding.PhoneID
	countryCode := strings.ToUpper(strings.TrimSpace(binding.CountryCode))
	if countryCode == "" {
		countryCode = task.CountryCode
	}
	source := "kitesim"
	next := "icloud_prepare"
	if requested {
		source = "manual"
		// A manually selected phone does not imply that a direct family invite
		// should be skipped.  Keep the old no-invite shortcut for historical
		// imports, but let the new phone+invite format complete iCloud and
		// family onboarding in order.
		if task.AccountRole == "child" && !task.ICloudOpened && !hasICloudDirectFamilyInvite(task) {
			next = "manage_prepare"
		}
	}
	return s.advanceICloudOnboardingTask(ctx, task, next, nil, map[string]any{
		"bound_phone_number": binding.PhoneNumber, "bound_phone_country_code": countryCode,
		"bound_phone_source": source, "kitesim_phone_id": &phoneID,
	})
}

func (s *Service) prepareICloudOnboardingApple(ctx context.Context, task *iCloudOnboardingTaskModel, secret iCloudOnboardingSecret, operation, readyStage string) error {
	return s.prepareICloudOnboardingAppleWithInvite(ctx, task, secret, operation, readyStage, "", "")
}

func (s *Service) prepareICloudOnboardingAppleWithInvite(ctx context.Context, task *iCloudOnboardingTaskModel, secret iCloudOnboardingSecret, operation, readyStage, invite, organizer string) error {
	response, err := s.executeICloudOnboardingApple(ctx, task, secret, AppleOnboardingRequest{Operation: operation, FamilyInviteURL: invite, FamilyOrganizerEmail: organizer})
	if err != nil {
		return s.handleICloudOnboardingAppleError(ctx, task, err)
	}
	updates := map[string]any{}
	if len(response.Session) > 0 {
		updates["session_payload"] = iCloudJSON(response.Session)
	}
	if response.Next == "ready" || response.Next == "" {
		return s.advanceICloudOnboardingTask(ctx, task, readyStage, nil, updates)
	}
	if isICloudOldCookieBackfill(task) && response.Next == appleSMSICloudLogin {
		response.Next = appleSMSOldCookieLogin
	}
	if response.Next != appleSMSICloudLogin && response.Next != appleSMSOldCookieLogin && response.Next != appleSMSICloudCookieLogin && response.Next != appleSMSPhoneEnrollment && response.Next != appleSMSFamilyLogin && response.Next != appleSMSFamilyReconcileLogin && response.Next != appleSMSManageLogin {
		return s.failICloudOnboardingTask(ctx, task, "unsupported_challenge", "Apple returned an unsupported authentication challenge.")
	}
	if task.KitesimPhoneID == nil && response.Next != appleSMSPhoneEnrollment {
		binding, err := s.bindICloudOnboardingTrustedPhone(ctx, task, response.TrustedPhoneLastTwo)
		if err != nil || binding == nil {
			return err
		}
		phoneID := binding.PhoneID
		updates["bound_phone_number"] = binding.PhoneNumber
		updates["bound_phone_country_code"] = firstNonEmpty(strings.ToUpper(strings.TrimSpace(binding.CountryCode)), task.CountryCode)
		updates["bound_phone_source"] = firstNonEmpty(task.BoundPhoneSource, "kitesim")
		updates["kitesim_phone_id"] = &phoneID
	}
	updates["pending_sms_purpose"] = response.Next
	updates["stage_attempts"] = task.StageAttempts
	updates["sms_poll_deadline"] = s.now().UTC().Truncate(time.Millisecond).Add(iCloudOnboardingSMSDeadline)
	return s.advanceICloudOnboardingTask(ctx, task, "sms_send", nil, updates)
}

func (s *Service) checkICloudOnboardingSMSPhone(ctx context.Context, task *iCloudOnboardingTaskModel) (bool, error) {
	if task == nil || task.KitesimPhoneID == nil {
		return true, nil
	}
	if s.smsPhones == nil {
		return false, ErrICloudOnboardingTemporary
	}
	err := s.smsPhones.CheckSMSPhoneAvailable(ctx, *task.KitesimPhoneID)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, kitesim.ErrSMSPhoneBoundUnavailable):
		return false, s.failICloudOnboardingTask(ctx, task, "phone_binding_unavailable", "The permanently bound phone is disabled or unavailable in the eSIM phone pool.")
	case errors.Is(err, kitesim.ErrPhoneMissing):
		return false, s.failICloudOnboardingTask(ctx, task, "phone_not_in_pool", "The permanently bound phone is not available in the eSIM phone pool.")
	}
	if retryAt, ok := kitesim.SMSRetryAt(err); ok {
		retryAt = iCloudOnboardingSMSRetryAt(retryAt, s.now().UTC())
		return false, s.waitICloudOnboardingTask(ctx, task, &retryAt, "pending", safeICloudImportMessage(err.Error()))
	}
	return false, ErrICloudOnboardingTemporary
}

func (s *Service) bindICloudOnboardingTrustedPhone(ctx context.Context, task *iCloudOnboardingTaskModel, suffix string) (*kitesim.SMSPhoneBinding, error) {
	suffix = onboardingPhoneDigits(suffix)
	if suffix == "" {
		return nil, s.failICloudOnboardingTask(ctx, task, "phone_binding_missing", "Apple did not return the permanently bound trusted phone.")
	}
	if s.smsPhones == nil {
		return nil, ErrICloudOnboardingTemporary
	}
	boundNumber := onboardingPhoneDigits(task.BoundPhoneNumber)
	var binding kitesim.SMSPhoneBinding
	var err error
	if boundNumber != "" {
		if !strings.HasSuffix(boundNumber, suffix) {
			return nil, s.failICloudOnboardingTask(ctx, task, "phone_binding_mismatch", "The Apple trusted phone does not match the permanently bound phone number.")
		}
		binding, err = s.smsPhones.BindICloudSMSPhone(ctx, task.PrimaryEmail, task.BoundPhoneNumber)
	} else {
		binding, err = s.smsPhones.BindICloudSMSPhoneBySuffix(ctx, task.PrimaryEmail, suffix)
	}
	if err == nil {
		return &binding, nil
	}
	switch {
	case errors.Is(err, kitesim.ErrSMSPhoneSuffixAmbiguous):
		return nil, s.failICloudOnboardingTask(ctx, task, "phone_binding_ambiguous", "The Apple trusted phone suffix matches multiple eSIM pool numbers; import the explicit phone number.")
	case errors.Is(err, kitesim.ErrPhoneMissing):
		return nil, s.failICloudOnboardingTask(ctx, task, "phone_not_in_pool", "The Apple trusted phone is not available in the eSIM phone pool.")
	case errors.Is(err, kitesim.ErrSMSPhoneBindingConflict):
		return nil, s.failICloudOnboardingTask(ctx, task, "phone_binding_conflict", "The Apple trusted phone does not match this Apple ID's permanent phone binding.")
	case errors.Is(err, kitesim.ErrSMSPhoneBoundUnavailable):
		return nil, s.failICloudOnboardingTask(ctx, task, "phone_binding_unavailable", "The permanently bound phone is disabled or unavailable in the eSIM phone pool.")
	default:
		return nil, ErrICloudOnboardingTemporary
	}
}

func (s *Service) sendICloudOnboardingSMS(ctx context.Context, task *iCloudOnboardingTaskModel, secret iCloudOnboardingSecret) error {
	purpose := strings.TrimSpace(task.PendingSMSPurpose)
	if purpose == "" {
		return s.failICloudOnboardingTask(ctx, task, "invalid_sms_state", "Apple SMS verification state is invalid.")
	}
	if task.SMSPollDeadline == nil || !task.SMSPollDeadline.After(s.now().UTC()) {
		s.cancelICloudOnboardingSMSChallenge(context.WithoutCancel(ctx), task)
		updates := map[string]any{
			"session_payload": nil, "manual_verification_code": "", "sms_sent_at": nil, "sms_poll_deadline": nil,
			"stage_attempts": task.StageAttempts,
		}
		if purpose != appleSMSOldCookieLogin {
			updates["pending_sms_purpose"] = ""
		}
		return s.advanceICloudOnboardingTask(ctx, task, appleOnboardingRestartStage(purpose), nil, updates)
	}
	var reservation *kitesim.SMSReservation
	if task.KitesimPhoneID != nil {
		if s.smsPhones == nil {
			return ErrICloudOnboardingTemporary
		}
		binding, err := s.smsPhones.BindICloudSMSPhone(ctx, task.PrimaryEmail, task.BoundPhoneNumber)
		if err != nil {
			switch {
			case errors.Is(err, kitesim.ErrSMSPhoneBindingConflict):
				return s.failICloudOnboardingTask(ctx, task, "phone_binding_conflict", "The SMS phone does not match this Apple ID's permanent phone binding.")
			case errors.Is(err, kitesim.ErrPhoneMissing):
				return s.failICloudOnboardingTask(ctx, task, "phone_not_in_pool", "The permanently bound phone is not available in the eSIM phone pool.")
			case errors.Is(err, kitesim.ErrSMSPhoneBoundUnavailable):
				return s.failICloudOnboardingTask(ctx, task, "phone_binding_unavailable", "The permanently bound phone is disabled or unavailable in the eSIM phone pool.")
			default:
				return ErrICloudOnboardingTemporary
			}
		}
		if binding.PhoneID != *task.KitesimPhoneID {
			return s.failICloudOnboardingTask(ctx, task, "phone_binding_conflict", "The SMS phone does not match this Apple ID's permanent phone binding.")
		}
		deadline := s.now().UTC().Add(iCloudOnboardingSMSDeadline)
		reserved, err := s.smsPhones.ReserveSMSChallenge(ctx, binding.PhoneID, purpose, iCloudOnboardingSMSOwner(task), deadline)
		if err != nil {
			if errors.Is(err, kitesim.ErrSMSPhoneBoundUnavailable) {
				return s.failICloudOnboardingTask(ctx, task, "phone_binding_unavailable", "The permanently bound phone is disabled or unavailable in the eSIM phone pool.")
			}
			if retryAt, ok := kitesim.SMSRetryAt(err); ok {
				retryAt = iCloudOnboardingSMSRetryAt(retryAt, s.now().UTC())
				updates := map[string]any{
					"stage": appleOnboardingRestartStage(purpose), "stage_attempts": task.StageAttempts,
					"session_payload": nil, "manual_verification_code": "", "sms_sent_at": nil, "sms_poll_deadline": nil,
				}
				if purpose != appleSMSOldCookieLogin {
					updates["pending_sms_purpose"] = ""
				}
				return s.waitICloudOnboardingTaskWithUpdates(ctx, task, &retryAt, "pending", safeICloudImportMessage(err.Error()), updates)
			}
			return ErrICloudOnboardingTemporary
		}
		reservation = &reserved
		switch reserved.Status {
		case kitesim.SMSChallengeReserved:
			// Treat a crash after this point as may-have-sent: polling and expiry are safer than a duplicate Apple send.
			if err := s.smsPhones.MarkSMSAttemptSent(context.WithoutCancel(ctx), reserved.ID); err != nil {
				if errors.Is(err, kitesim.ErrSMSChallengeInactive) {
					return s.waitForICloudOnboardingSMSChallenge(ctx, task, reserved, nil)
				}
				return ErrICloudOnboardingTemporary
			}
		case kitesim.SMSChallengeSent:
			return s.waitForICloudOnboardingSMSChallenge(ctx, task, reserved, nil)
		default:
			return s.retryICloudOnboardingSMSRound(ctx, task, "The previous Apple SMS challenge ended before verification.")
		}
	}
	response, err := s.executeICloudOnboardingApple(ctx, task, secret, AppleOnboardingRequest{
		Operation: appleOnboardingSendSMS, SMSPurpose: purpose,
		PhoneNumber: task.BoundPhoneNumber, PhoneCountryCode: task.BoundPhoneCountryCode,
	})
	if err != nil {
		var appleErr *AppleOnboardingError
		if reservation != nil && s.smsPhones != nil && errors.As(err, &appleErr) && appleErr.SendRejected {
			if markErr := s.smsPhones.MarkSMSAttemptSendFailed(context.WithoutCancel(ctx), reservation.ID); markErr != nil {
				return ErrICloudOnboardingTemporary
			}
		}
		if errors.As(err, &appleErr) && appleErr.SendRejected {
			var retryAt *time.Time
			if reservation != nil && reservation.CooldownUntil.After(s.now().UTC()) {
				value := reservation.CooldownUntil
				retryAt = &value
			}
			return s.retryICloudOnboardingSMSRoundAt(ctx, task, appleErr.SafeMessage, retryAt)
		}
		return s.handleICloudOnboardingAppleError(ctx, task, err)
	}
	if reservation != nil && s.smsPhones != nil {
		if err := s.smsPhones.ConfirmSMSAttemptSent(context.WithoutCancel(ctx), reservation.ID); err != nil {
			return ErrICloudOnboardingTemporary
		}
	}
	if task.KitesimPhoneID == nil {
		now := s.now().UTC().Truncate(time.Millisecond)
		deadline := now.Add(iCloudOnboardingSMSDeadline)
		updates := map[string]any{"sms_sent_at": now, "sms_poll_deadline": deadline, "manual_verification_code": ""}
		if len(response.Session) > 0 {
			updates["session_payload"] = iCloudJSON(response.Session)
		}
		return s.waitICloudOnboardingTaskWithUpdates(ctx, task, nil, "waiting", "Waiting for a manually entered Apple verification code.", updatesWith(updates, "stage", "sms_wait"))
	}
	return s.waitForICloudOnboardingSMSChallenge(ctx, task, *reservation, response.Session)
}

func (s *Service) waitICloudOnboardingSMS(ctx context.Context, task *iCloudOnboardingTaskModel) error {
	if strings.TrimSpace(task.ManualVerificationCode) != "" {
		return s.advanceICloudOnboardingTask(ctx, task, "sms_verify", nil, map[string]any{"stage_attempts": task.StageAttempts})
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	if task.KitesimPhoneID == nil {
		return s.waitICloudOnboardingTask(ctx, task, nil, "waiting", "Waiting for a manually entered Apple verification code.")
	}
	if s.smsPhones == nil {
		return ErrICloudOnboardingTemporary
	}
	challenge, err := s.smsPhones.GetSMSChallengeByOwner(ctx, iCloudOnboardingSMSOwner(task))
	if errors.Is(err, kitesim.ErrSMSReservationNotFound) {
		return s.retryICloudOnboardingSMSRound(ctx, task, "The Apple SMS challenge no longer exists; authentication will restart.")
	}
	if err != nil {
		return ErrICloudOnboardingTemporary
	}
	if challenge.Status != kitesim.SMSChallengeSent {
		return s.retryICloudOnboardingSMSRound(ctx, task, "The Apple verification SMS was not received before the challenge expired.")
	}
	message, err := s.smsPhones.ClaimAppleSMSMessage(ctx, challenge.ID)
	if err == nil && message != nil {
		code := claimedAppleSMSCode(*message)
		if code == "" {
			return s.failICloudOnboardingTask(ctx, task, "invalid_sms_message", "The claimed Apple SMS did not contain a verification code.")
		}
		return s.advanceICloudOnboardingTask(ctx, task, "sms_verify", nil, map[string]any{"manual_verification_code": code, "stage_attempts": task.StageAttempts})
	}
	if err != nil && !errors.Is(err, kitesim.ErrAppleSMSMessageNotFound) && !errors.Is(err, kitesim.ErrSMSChallengeInactive) {
		return ErrICloudOnboardingTemporary
	}
	if !challenge.ExpiresAt.After(now) || errors.Is(err, kitesim.ErrSMSChallengeInactive) {
		return s.retryICloudOnboardingSMSRound(ctx, task, "The Apple verification SMS was not received before the challenge expired.")
	}
	retryAt := now.Add(iCloudOnboardingSMSPoll)
	if retryAt.After(challenge.ExpiresAt) {
		retryAt = challenge.ExpiresAt
	}
	return s.waitICloudOnboardingTask(ctx, task, &retryAt, "pending", "Waiting for the Apple verification SMS.")
}

func (s *Service) verifyICloudOnboardingSMS(ctx context.Context, task *iCloudOnboardingTaskModel, secret iCloudOnboardingSecret) error {
	code := strings.TrimSpace(task.ManualVerificationCode)
	if code == "" {
		return s.advanceICloudOnboardingTask(ctx, task, "sms_wait", nil, nil)
	}
	next := map[string]string{
		appleSMSICloudLogin: "icloud_finish", appleSMSPhoneEnrollment: "icloud_finish",
		appleSMSOldCookieLogin:       "old_cookie_finish",
		appleSMSICloudCookieLogin:    "icloud_cookie_finish",
		appleSMSFamilyLogin:          "family_join_intent",
		appleSMSFamilyReconcileLogin: "family_join_apply",
		appleSMSManageLogin:          "manage_profile",
	}[task.PendingSMSPurpose]
	if next == "" {
		return s.failICloudOnboardingTask(ctx, task, "invalid_sms_state", "Apple SMS verification state is invalid.")
	}
	prepared, err := s.prepareICloudOnboardingSMSVerification(ctx, task)
	if err != nil || !prepared {
		return err
	}
	response, err := s.executeICloudOnboardingApple(ctx, task, secret, AppleOnboardingRequest{
		Operation: appleOnboardingVerifySMS, SMSPurpose: task.PendingSMSPurpose, Code: code,
	})
	if err != nil {
		var appleErr *AppleOnboardingError
		if errors.As(err, &appleErr) && appleErr.CodeRejected {
			return s.retryICloudOnboardingSMSRound(ctx, task, appleErr.SafeMessage)
		}
		return s.handleICloudOnboardingAppleError(ctx, task, err)
	}
	updates := map[string]any{"manual_verification_code": "", "sms_sent_at": nil, "sms_poll_deadline": nil}
	if task.PendingSMSPurpose != appleSMSOldCookieLogin {
		updates["pending_sms_purpose"] = ""
	}
	if len(response.Session) > 0 {
		updates["session_payload"] = iCloudJSON(response.Session)
	}
	if err := s.advanceICloudOnboardingTask(ctx, task, next, nil, updates); err != nil {
		return err
	}
	// Apple has accepted the code and the workflow is already advanced. A
	// transient local completion error must not replay the Apple verification.
	if task.KitesimPhoneID != nil && s.smsPhones != nil {
		if challenge, lookupErr := s.smsPhones.GetSMSChallengeByOwner(context.WithoutCancel(ctx), iCloudOnboardingSMSOwner(task)); lookupErr == nil {
			_ = s.smsPhones.CompleteSMSChallenge(context.WithoutCancel(ctx), challenge.ID)
		}
	}
	return nil
}

func (s *Service) prepareICloudOnboardingSMSVerification(ctx context.Context, task *iCloudOnboardingTaskModel) (bool, error) {
	now := s.now().UTC().Truncate(time.Millisecond)
	result := s.db.WithContext(ctx).Model(&iCloudOnboardingTaskModel{}).
		Where("id = ? AND generation = ? AND claim_token = ? AND dispatch_status = ? AND stage = ?", task.ID, task.Generation, task.ClaimToken, "running", "sms_verify").
		Updates(map[string]any{"stage": "sms_verify_recover", "manual_verification_code": "", "updated_at": now})
	if result.Error != nil {
		return false, ErrICloudOnboardingTemporary
	}
	if result.RowsAffected != 1 {
		return false, nil
	}
	task.Stage = "sms_verify_recover"
	task.ManualVerificationCode = ""
	task.UpdatedAt = now
	return true, nil
}

func (s *Service) recoverICloudOnboardingSMSVerification(ctx context.Context, task *iCloudOnboardingTaskModel) error {
	switch task.PendingSMSPurpose {
	case appleSMSICloudLogin, appleSMSOldCookieLogin, appleSMSICloudCookieLogin, appleSMSPhoneEnrollment, appleSMSFamilyLogin, appleSMSFamilyReconcileLogin, appleSMSManageLogin:
	default:
		return s.failICloudOnboardingTask(ctx, task, "invalid_sms_state", "Apple SMS verification recovery state is invalid.")
	}
	restart := appleOnboardingRestartStage(task.PendingSMSPurpose)
	s.cancelICloudOnboardingSMSChallenge(context.WithoutCancel(ctx), task)
	updates := map[string]any{"session_payload": nil, "manual_verification_code": "", "sms_sent_at": nil, "sms_poll_deadline": nil}
	if task.PendingSMSPurpose != appleSMSOldCookieLogin {
		updates["pending_sms_purpose"] = ""
	}
	return s.advanceICloudOnboardingTask(ctx, task, restart, nil, updates)
}

func (s *Service) waitForICloudOnboardingSMSChallenge(ctx context.Context, task *iCloudOnboardingTaskModel, reservation kitesim.SMSReservation, session json.RawMessage) error {
	challenge, err := s.smsPhones.GetSMSChallengeByOwner(ctx, iCloudOnboardingSMSOwner(task))
	if err != nil || challenge.ID != reservation.ID || challenge.Status != kitesim.SMSChallengeSent || challenge.SentAt == nil {
		return ErrICloudOnboardingTemporary
	}
	updates := map[string]any{
		"stage": "sms_wait", "sms_sent_at": *challenge.SentAt, "sms_poll_deadline": challenge.ExpiresAt,
		"manual_verification_code": "", "stage_attempts": task.StageAttempts,
	}
	if len(session) > 0 {
		updates["session_payload"] = iCloudJSON(session)
	}
	retryAt := s.now().UTC().Add(iCloudOnboardingSMSPoll)
	if retryAt.After(challenge.ExpiresAt) {
		retryAt = challenge.ExpiresAt
	}
	return s.waitICloudOnboardingTaskWithUpdates(ctx, task, &retryAt, "pending", "Waiting for the Apple verification SMS.", updates)
}

func (s *Service) retryICloudOnboardingSMSRound(ctx context.Context, task *iCloudOnboardingTaskModel, message string) error {
	return s.retryICloudOnboardingSMSRoundAt(ctx, task, message, nil)
}

func (s *Service) retryICloudOnboardingSMSRoundAt(ctx context.Context, task *iCloudOnboardingTaskModel, message string, retryAt *time.Time) error {
	s.cancelICloudOnboardingSMSChallenge(context.WithoutCancel(ctx), task)
	attempts := task.StageAttempts + 1
	if attempts >= task.MaxAttempts {
		return s.failICloudOnboardingTask(ctx, task, "sms_verification_exhausted", "Apple SMS verification did not complete within the retry limit.")
	}
	restart := appleOnboardingRestartStage(task.PendingSMSPurpose)
	updates := map[string]any{
		"stage_attempts": attempts, "session_payload": nil, "manual_verification_code": "", "sms_sent_at": nil, "sms_poll_deadline": nil,
		"last_error_category": "sms_round_failed", "last_safe_error": safeICloudImportMessage(message),
	}
	if task.PendingSMSPurpose != appleSMSOldCookieLogin {
		updates["pending_sms_purpose"] = ""
	}
	omitICloudOldCookieSafeError(task, updates)
	if retryAt != nil {
		updates["stage"] = restart
		return s.waitICloudOnboardingTaskWithUpdates(ctx, task, retryAt, "pending", message, updates)
	}
	return s.advanceICloudOnboardingTask(ctx, task, restart, nil, updates)
}

func (s *Service) cancelICloudOnboardingSMSChallenge(ctx context.Context, task *iCloudOnboardingTaskModel) {
	if s.smsPhones == nil || task == nil || task.KitesimPhoneID == nil || strings.TrimSpace(task.PendingSMSPurpose) == "" {
		return
	}
	challenge, err := s.smsPhones.GetSMSChallengeByOwner(ctx, iCloudOnboardingSMSOwner(task))
	if err == nil {
		_ = s.smsPhones.CancelSMSChallenge(ctx, challenge.ID)
	}
}

func iCloudOnboardingSMSOwner(task *iCloudOnboardingTaskModel) string {
	return fmt.Sprintf("icloud-onboarding:%d:%s:%d", task.ID, strings.TrimSpace(task.PendingSMSPurpose), task.StageAttempts)
}

func (s *Service) finishICloudOnboardingICloud(ctx context.Context, task *iCloudOnboardingTaskModel, secret iCloudOnboardingSecret, afterFamily bool) error {
	operation := appleOnboardingFinishICloud
	if afterFamily {
		operation = appleOnboardingFinishICloudCookie
	}
	response, err := s.executeICloudOnboardingApple(ctx, task, secret, AppleOnboardingRequest{
		Operation: operation,
	})
	if err != nil {
		return s.handleICloudOnboardingAppleError(ctx, task, err)
	}
	updates := map[string]any{}
	if len(response.Session) > 0 {
		updates["session_payload"] = iCloudJSON(response.Session)
	}
	if code := strings.ToUpper(strings.TrimSpace(response.CountryCode)); code != "" {
		updates["country_code"] = code
	}
	if response.ICloudOpened != nil {
		updates["icloud_opened"] = *response.ICloudOpened
		if !*response.ICloudOpened {
			updates["icloud_activation_confirmed_at"] = gorm.Expr("NULL")
			if isICloudOldCookieBackfill(task) {
				return s.waitICloudOnboardingTaskWithUpdates(ctx, task, nil, "waiting", "Waiting for manual iCloud activation.", updatesWith(updates, "stage", "waiting_icloud_activation"))
			}
		}
	}
	if isICloudOldCookieBackfill(task) {
		if response.OldChannel == nil || strings.TrimSpace(response.OldChannel.Cookie) == "" {
			return s.retryICloudOnboardingTask(ctx, task, "old_cookie_prepare", nil, "old_cookie_missing", "iCloud did not return a usable V2 session cookie; authentication will restart.", map[string]any{
				"session_payload": nil, "manual_verification_code": "", "sms_sent_at": nil, "sms_poll_deadline": nil,
			})
		}
		return s.completeICloudOldCookieBackfill(ctx, task, *response.OldChannel)
	}
	next := "family_select"
	switch {
	case afterFamily || task.TaskKind == "refresh":
		next = "manage_prepare"
	case hasICloudDirectFamilyInvite(task):
		next = "family_prepare"
	case task.AccountRole == "primary" || task.BoundPhoneSource == "manual":
		// Preserve the historical resource-first path for imports that do not
		// carry a direct invitation URL.
		next = "manage_prepare"
	}
	return s.advanceICloudOnboardingTask(ctx, task, next, nil, updates)
}

func (s *Service) selectICloudOnboardingFamily(ctx context.Context, task *iCloudOnboardingTaskModel) error {
	if hasICloudDirectFamilyInvite(task) {
		return s.advanceICloudOnboardingTask(ctx, task, "family_prepare", nil, nil)
	}
	if strings.TrimSpace(task.CountryCode) == "" {
		return s.failICloudOnboardingTask(ctx, task, "country_unresolved", "The Apple ID country could not be determined for family selection.")
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	selected := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		primaryID, err := s.selectICloudFamilyPrimaryID(ctx, tx, task, now)
		if err != nil {
			return err
		}
		if primaryID == 0 {
			return nil
		}
		updated := tx.Model(&iCloudOnboardingTaskModel{}).
			Where("id = ? AND generation = ? AND claim_token = ? AND dispatch_status = ?", task.ID, task.Generation, task.ClaimToken, "running").
			Updates(map[string]any{
				"family_primary_resource_id": primaryID, "family_reservation_confirmed": false,
				"stage": "family_prepare", "onboarding_status": iCloudOnboardingProcessing,
				"dispatch_status": "pending", "claim_token": "", "next_attempt_at": nil,
				"last_error_category": "", "last_safe_error": "", "stage_attempts": 0, "updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrICloudImportClaim
		}
		selected = true
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrICloudImportClaim) {
			return nil
		}
		return ErrICloudOnboardingTemporary
	}
	if !selected {
		retryAt := now.Add(iCloudOnboardingFamilyRetry)
		return s.waitICloudOnboardingTask(ctx, task, &retryAt, "pending", "Waiting for an available primary family account in the same region.")
	}
	_ = s.refreshICloudOnboardingImport(context.WithoutCancel(ctx), iCloudOnboardingImportID(task))
	_ = s.ScheduleICloudOnboardingDispatcher(context.WithoutCancel(ctx), 0)
	return nil
}

func (s *Service) iCloudOnboardingFamilyInvite(ctx context.Context, task *iCloudOnboardingTaskModel) (string, string, error) {
	if invite := strings.TrimSpace(task.FamilyInviteURL); invite != "" {
		return invite, "", nil
	}
	if task.FamilyPrimaryResourceID == nil {
		return "", "", s.failICloudOnboardingTask(ctx, task, "family_not_selected", "No primary family account was selected.")
	}
	var row struct {
		Invite        string `gorm:"column:family_invite_url"`
		PrimaryEmail  string `gorm:"column:primary_email"`
		Status        string `gorm:"column:status"`
		ErrorCategory string `gorm:"column:family_sync_error_category"`
	}
	if err := s.db.WithContext(ctx).Table("icloud_resources").Select("family_invite_url, primary_email, status, family_sync_error_category").Where("id = ?", *task.FamilyPrimaryResourceID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", s.retryICloudOnboardingFamilySelection(ctx, task, "family_primary_unavailable", "The selected primary family account no longer exists.")
		}
		return "", "", ErrICloudOnboardingTemporary
	}
	if row.Status == iCloudResourceDisabled || row.Status == iCloudResourceDeleted {
		return "", "", s.retryICloudOnboardingFamilySelection(ctx, task, "family_primary_unavailable", "The selected primary family account is unavailable.")
	}
	if strings.TrimSpace(row.Invite) == "" {
		return "", "", s.retryICloudOnboardingFamilySelection(ctx, task, "family_invite_unavailable", "The selected primary family invitation is unavailable.")
	}
	if isICloudFamilyInviteFailure(row.ErrorCategory) {
		return "", "", s.retryICloudOnboardingFamilySelection(ctx, task, row.ErrorCategory, "The selected primary family invitation is unavailable.")
	}
	return row.Invite, strings.ToLower(strings.TrimSpace(row.PrimaryEmail)), nil
}

func (s *Service) iCloudOnboardingFamilyRecoveryInvite(ctx context.Context, task *iCloudOnboardingTaskModel) (string, string, error) {
	if invite := strings.TrimSpace(task.FamilyInviteURL); invite != "" {
		return invite, "", nil
	}
	if task.FamilyPrimaryResourceID == nil {
		return "", "", s.failICloudOnboardingTask(ctx, task, "family_not_selected", "No primary family account was selected.")
	}
	var row struct {
		Invite       string `gorm:"column:family_invite_url"`
		PrimaryEmail string `gorm:"column:primary_email"`
	}
	if err := s.db.WithContext(ctx).Table("icloud_resources").Select("family_invite_url, primary_email").Where("id = ?", *task.FamilyPrimaryResourceID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", s.failICloudOnboardingTask(ctx, task, "family_primary_unavailable", "The selected primary family account no longer exists.")
		}
		return "", "", ErrICloudOnboardingTemporary
	}
	return strings.TrimSpace(row.Invite), strings.ToLower(strings.TrimSpace(row.PrimaryEmail)), nil
}

func (s *Service) retryICloudOnboardingFamilySelection(ctx context.Context, task *iCloudOnboardingTaskModel, category, message string) error {
	s.cancelICloudOnboardingSMSChallenge(context.WithoutCancel(ctx), task)
	now := s.now().UTC().Truncate(time.Millisecond)
	terminal := false
	importID := uint(0)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked iCloudOnboardingTaskModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND generation = ? AND claim_token = ? AND dispatch_status = ?", task.ID, task.Generation, task.ClaimToken, "running").
			First(&locked).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrICloudImportClaim
			}
			return err
		}
		importID = iCloudOnboardingImportID(&locked)
		if locked.FamilyPrimaryResourceID != nil && isICloudFamilyInviteFailure(category) {
			if err := tx.Model(&iCloudResourceModel{}).Where("id = ? AND account_role = ?", *locked.FamilyPrimaryResourceID, "primary").Updates(map[string]any{
				"family_sync_error_category": safeICloudImportMessage(category), "updated_at": now,
			}).Error; err != nil {
				return err
			}
		}
		attempts := min(locked.Attempts+1, locked.MaxAttempts)
		terminal = attempts >= locked.MaxAttempts
		updates := map[string]any{
			"family_primary_resource_id": nil, "family_reservation_confirmed": false,
			"attempts": attempts, "stage_attempts": 0, "claim_token": "", "session_payload": nil,
			"pending_sms_purpose": "", "manual_verification_code": "", "sms_sent_at": nil, "sms_poll_deadline": nil,
			"last_error_category": safeICloudImportMessage(category), "last_safe_error": safeICloudImportMessage(message), "updated_at": now,
		}
		if terminal {
			updates["onboarding_status"] = iCloudOnboardingFailed
			updates["stage"] = "family_select"
			updates["dispatch_status"] = "failed"
			updates["forward_preparation_id"] = nil
			updates["next_attempt_at"] = nil
			updates["last_error_category"] = "family_selection_exhausted"
			updates["last_safe_error"] = "No usable primary family invitation remained after repeated selection attempts."
			updates["secret_payload"] = nil
			updates["finished_at"] = now
		} else {
			updates["onboarding_status"] = iCloudOnboardingProcessing
			updates["stage"] = "family_select"
			updates["dispatch_status"] = "pending"
			updates["next_attempt_at"] = now
		}
		updated := tx.Model(&iCloudOnboardingTaskModel{}).
			Where("id = ? AND generation = ? AND claim_token = ? AND dispatch_status = ?", locked.ID, locked.Generation, locked.ClaimToken, "running").Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrICloudImportClaim
		}
		if terminal {
			if err := markICloudOnboardingResourceFailedTx(tx, &locked, "No usable primary family invitation remained after repeated selection attempts.", now); err != nil {
				return err
			}
		}
		if terminal && locked.ImportID != nil {
			return releaseICloudAppleIDReservationTx(tx, iCloudAppleIDReservationOnboarding, *locked.ImportID, locked.PrimaryEmail)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrICloudImportClaim) {
			return nil
		}
		return ErrICloudOnboardingTemporary
	}
	_ = s.refreshICloudOnboardingImport(context.WithoutCancel(ctx), importID)
	if !terminal {
		_ = s.ScheduleICloudOnboardingDispatcher(context.WithoutCancel(ctx), 0)
	}
	return nil
}

func (s *Service) joinICloudOnboardingFamily(ctx context.Context, task *iCloudOnboardingTaskModel, secret iCloudOnboardingSecret) error {
	response, err := s.executeICloudOnboardingApple(ctx, task, secret, AppleOnboardingRequest{
		Operation: appleOnboardingJoinFamily,
	})
	if err != nil {
		return s.handleICloudOnboardingAppleError(ctx, task, err)
	}
	updates := map[string]any{}
	if len(response.Session) > 0 {
		updates["session_payload"] = iCloudJSON(response.Session)
	}
	var reconcileErr error
	if hasICloudDirectFamilyInvite(task) {
		reconcileErr = s.reconcileICloudOnboardingDirectFamily(ctx, task, response.FamilyChannel)
	} else {
		reconcileErr = s.reconcileICloudOnboardingFamily(ctx, task, response.FamilyChannel)
	}
	if reconcileErr != nil {
		category := iCloudFamilyErrorCategory(reconcileErr)
		if category == "" {
			return ErrICloudOnboardingTemporary
		}
		if iCloudFamilyErrorRetryable(reconcileErr) {
			retryAt := s.now().UTC().Add(iCloudOnboardingFamilyRetry)
			var providerErr *iCloudFamilyError
			if errors.As(reconcileErr, &providerErr) && providerErr.RetryAfter > 0 {
				retryAt = s.now().UTC().Add(providerErr.RetryAfter)
			}
			return s.retryICloudOnboardingTask(ctx, task, task.Stage, &retryAt, category, safeICloudImportMessage(reconcileErr.Error()), updates)
		}
		return s.failICloudOnboardingTask(ctx, task, category, safeICloudImportMessage(reconcileErr.Error()))
	}
	if task.TaskKind == "onboarding" && task.AccountRole == "child" &&
		(task.FamilyPrimaryResourceID != nil || hasICloudDirectFamilyInvite(task)) && !task.FamilyReservationConfirmed {
		return s.waitICloudOnboardingTaskWithUpdates(ctx, task, nil, "waiting", "Waiting for manual family sharing setup.", map[string]any{
			"stage": iCloudOnboardingStageFamilySharing, "session_payload": nil,
			"pending_sms_purpose": "", "manual_verification_code": "", "sms_sent_at": nil, "sms_poll_deadline": nil,
		})
	}
	return s.advanceICloudOnboardingTask(ctx, task, "manage_prepare", nil, updates)
}

// reconcileICloudOnboardingDirectFamily verifies membership using the family
// session returned by the supplied invitation. It deliberately does not look
// up or mutate a primary resource; direct-invite imports are ordinary child
// accounts and the legacy primary fields remain untouched for old rows.
func (s *Service) reconcileICloudOnboardingDirectFamily(ctx context.Context, task *iCloudOnboardingTaskModel, channel *AppleOnboardingChannel) error {
	if s == nil || s.db == nil || task == nil || task.ID == 0 || channel == nil {
		return ErrICloudFamilyCapacityUnavailable
	}
	ctx = withAppleRouteEmail(ctx, task.PrimaryEmail)
	client := s.family
	if client == nil {
		client = newRoutedICloudFamilyClient(s.appleRoutes)
	}
	snapshot, err := client.fetch(ctx, iCloudResourceChannelModel{
		Kind: channel.Kind, Host: channel.Host, Cookie: channel.Cookie, SetupCookie: channel.SetupCookie,
		UserAgent: channel.UserAgent,
	})
	if err != nil {
		return err
	}
	if !snapshot.Linked || !snapshot.Member {
		return &iCloudFamilyError{Category: "family_membership_pending", SafeMessage: "Apple family membership is not visible yet.", Retryable: true}
	}
	if !strings.EqualFold(snapshot.CurrentUserAppleID, task.PrimaryEmail) {
		return &iCloudFamilyError{Category: "family_identity_mismatch", SafeMessage: "Apple family session belongs to a different account."}
	}
	if snapshot.CurrentDSID == snapshot.OrganizerDSID {
		return &iCloudFamilyError{Category: "family_conflict", SafeMessage: "The child Apple account is the organizer of another family."}
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	updated := s.db.WithContext(ctx).Model(&iCloudOnboardingTaskModel{}).
		Where("id = ? AND generation = ? AND claim_token = ? AND dispatch_status = ?", task.ID, task.Generation, task.ClaimToken, "running").
		Updates(map[string]any{"family_reservation_confirmed": true, "updated_at": now})
	if updated.Error != nil {
		return ErrICloudOnboardingTemporary
	}
	if updated.RowsAffected != 1 {
		return ErrICloudImportClaim
	}
	return nil
}

func (s *Service) fetchICloudOnboardingManage(ctx context.Context, task *iCloudOnboardingTaskModel, secret iCloudOnboardingSecret) error {
	response, err := s.executeICloudOnboardingApple(ctx, task, secret, AppleOnboardingRequest{Operation: appleOnboardingFetchManage})
	if err != nil {
		return s.handleICloudOnboardingAppleError(ctx, task, err)
	}
	updates := map[string]any{}
	if len(response.Session) > 0 {
		updates["session_payload"] = iCloudJSON(response.Session)
	}
	if code := strings.ToUpper(strings.TrimSpace(response.CountryCode)); code != "" {
		updates["country_code"] = code
	}
	if task.KitesimPhoneID == nil {
		binding, err := s.bindICloudOnboardingTrustedPhone(ctx, task, response.TrustedPhoneLastTwo)
		if err != nil || binding == nil {
			return err
		}
		phoneID := binding.PhoneID
		updates["bound_phone_number"] = binding.PhoneNumber
		updates["bound_phone_country_code"] = firstNonEmpty(strings.ToUpper(strings.TrimSpace(binding.CountryCode)), task.CountryCode)
		updates["bound_phone_source"] = firstNonEmpty(task.BoundPhoneSource, "kitesim")
		updates["kitesim_phone_id"] = &phoneID
	}
	next := "forwarding_prepare"
	if task.TaskKind == "refresh" {
		next = "resource_refresh"
	}
	return s.advanceICloudOnboardingTask(ctx, task, next, nil, updates)
}

func (s *Service) prepareICloudOnboardingForwarding(ctx context.Context, task *iCloudOnboardingTaskModel) error {
	if task.ForwardPreparationID != nil {
		if preparation, err := s.usableICloudOnboardingForwarding(ctx, task); err == nil {
			return s.advanceICloudOnboardingTask(ctx, task, "forwarding_add_intent", nil, map[string]any{
				"selected_forward_to": strings.ToLower(strings.TrimSpace(preparation.ForwardToEmail)),
			})
		} else if !errors.Is(err, ErrICloudImportPreparationConflict) && !errors.Is(err, ErrICloudImportPreparationNotFound) {
			return err
		}
		// A stale preparation cannot ever receive a usable code. Drop only the
		// local pointer; the next dispatch creates a fresh mailbox.
		return s.advanceICloudOnboardingTask(ctx, task, "forwarding_prepare", nil, map[string]any{
			"forward_preparation_id": nil,
		})
	}
	view, err := s.CreateAdminICloudImportPreparation(ctx, task.OperatorUserID)
	if err != nil {
		if errors.Is(err, ErrICloudForwardingUnavailable) {
			return s.failICloudOnboardingTask(ctx, task, "forwarding_unavailable", "No authorized forwarding mailbox is available.")
		}
		return ErrICloudOnboardingTemporary
	}
	return s.advanceICloudOnboardingTask(ctx, task, "forwarding_add_intent", nil, map[string]any{
		"forward_preparation_id": view.ID,
		"selected_forward_to":    strings.ToLower(strings.TrimSpace(view.ForwardToEmail)),
	})
}

func (s *Service) iCloudOnboardingForwarding(ctx context.Context, task *iCloudOnboardingTaskModel) (*iCloudImportPreparationModel, error) {
	if task == nil || task.ForwardPreparationID == nil || task.OperatorUserID == 0 {
		return nil, ErrICloudImportPreparationConflict
	}
	var model iCloudImportPreparationModel
	if err := s.db.WithContext(ctx).Where("id = ? AND operator_user_id = ?", *task.ForwardPreparationID, task.OperatorUserID).First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrICloudImportPreparationConflict
		}
		return nil, ErrICloudOnboardingTemporary
	}
	return &model, nil
}

func (s *Service) usableICloudOnboardingForwarding(ctx context.Context, task *iCloudOnboardingTaskModel) (*iCloudImportPreparationModel, error) {
	model, err := s.iCloudOnboardingForwarding(ctx, task)
	if err != nil {
		return nil, err
	}
	if model.ConsumedAt != nil || !model.ExpiresAt.After(s.now().UTC()) {
		return nil, ErrICloudImportPreparationConflict
	}
	return model, nil
}

func (s *Service) retryICloudOnboardingForwardingPreparation(ctx context.Context, task *iCloudOnboardingTaskModel) error {
	return s.retryICloudOnboardingTask(ctx, task, "forwarding_prepare", nil, "forwarding_preparation_expired", "The forwarding mailbox preparation is no longer usable; a replacement will be created.", map[string]any{
		"forward_preparation_id": nil,
	})
}

func (s *Service) sendICloudOnboardingForwarding(ctx context.Context, task *iCloudOnboardingTaskModel, secret iCloudOnboardingSecret) error {
	preparation, err := s.usableICloudOnboardingForwarding(ctx, task)
	if err != nil {
		if errors.Is(err, ErrICloudImportPreparationConflict) || errors.Is(err, ErrICloudImportPreparationNotFound) {
			return s.retryICloudOnboardingForwardingPreparation(ctx, task)
		}
		return err
	}
	response, err := s.executeICloudOnboardingApple(ctx, task, secret, AppleOnboardingRequest{Operation: appleOnboardingAddForward, ForwardToEmail: preparation.ForwardToEmail})
	if err != nil {
		return s.handleICloudOnboardingAppleError(ctx, task, err)
	}
	updates := map[string]any{}
	if len(response.Session) > 0 {
		updates["session_payload"] = iCloudJSON(response.Session)
	}
	if response.Next == "verified" {
		return s.advanceICloudOnboardingTask(ctx, task, "resource_import", nil, updates)
	}
	retryAt := s.now().UTC().Add(iCloudOnboardingForwardRetry)
	return s.waitICloudOnboardingTaskWithUpdates(ctx, task, &retryAt, "pending", "Waiting for the forwarding verification email.", updatesWith(updates, "stage", "forwarding_wait"))
}

func (s *Service) waitICloudOnboardingForwarding(ctx context.Context, task *iCloudOnboardingTaskModel) error {
	if task.ForwardPreparationID == nil {
		return s.advanceICloudOnboardingTask(ctx, task, "forwarding_prepare", nil, nil)
	}
	view, err := s.GetAdminICloudImportPreparation(ctx, task.OperatorUserID, *task.ForwardPreparationID)
	if err != nil {
		if errors.Is(err, ErrICloudImportPreparationNotFound) || errors.Is(err, ErrICloudImportPreparationConflict) {
			return s.retryICloudOnboardingForwardingPreparation(ctx, task)
		}
		return ErrICloudOnboardingTemporary
	}
	switch view.Status {
	case "code_received":
		return s.advanceICloudOnboardingTask(ctx, task, "forwarding_verify_intent", nil, nil)
	case "expired", "consumed":
		return s.retryICloudOnboardingTask(ctx, task, "forwarding_prepare", nil, "forwarding_preparation_expired", "The forwarding mailbox preparation expired before verification.", map[string]any{"forward_preparation_id": nil})
	case "waiting":
		retryAt := s.now().UTC().Add(iCloudOnboardingForwardRetry)
		return s.waitICloudOnboardingTask(ctx, task, &retryAt, "pending", "Waiting for the forwarding verification email.")
	default:
		return s.retryICloudOnboardingForwardingPreparation(ctx, task)
	}
}

func (s *Service) verifyICloudOnboardingForwarding(ctx context.Context, task *iCloudOnboardingTaskModel, secret iCloudOnboardingSecret) error {
	preparation, err := s.usableICloudOnboardingForwarding(ctx, task)
	if err != nil {
		if errors.Is(err, ErrICloudImportPreparationConflict) || errors.Is(err, ErrICloudImportPreparationNotFound) {
			return s.retryICloudOnboardingForwardingPreparation(ctx, task)
		}
		return err
	}
	if strings.TrimSpace(preparation.VerificationCode) == "" {
		return s.advanceICloudOnboardingTask(ctx, task, "forwarding_wait", nil, nil)
	}
	response, err := s.executeICloudOnboardingApple(ctx, task, secret, AppleOnboardingRequest{
		Operation: appleOnboardingVerifyForward, ForwardToEmail: preparation.ForwardToEmail, ForwardCode: preparation.VerificationCode,
	})
	if err != nil {
		return s.handleICloudOnboardingAppleError(ctx, task, err)
	}
	updates := map[string]any{}
	if len(response.Session) > 0 {
		updates["session_payload"] = iCloudJSON(response.Session)
	}
	return s.advanceICloudOnboardingTask(ctx, task, "resource_import", nil, updates)
}

func (s *Service) importICloudOnboardingResource(ctx context.Context, task *iCloudOnboardingTaskModel, secret iCloudOnboardingSecret) error {
	if task.KitesimPhoneID == nil || strings.TrimSpace(task.BoundPhoneNumber) == "" {
		return s.failICloudOnboardingTask(ctx, task, "phone_binding_missing", "The Apple ID has no permanent eSIM phone binding.")
	}
	response, err := s.executeICloudOnboardingApple(ctx, task, secret, AppleOnboardingRequest{
		Operation: appleOnboardingExport, ForwardToEmail: task.SelectedForwardTo,
	})
	if err != nil {
		return s.handleICloudOnboardingAppleError(ctx, task, err)
	}
	if response.NewChannel == nil || strings.TrimSpace(response.NewChannel.Cookie) == "" {
		return s.failICloudOnboardingTask(ctx, task, "new_cookie_missing", "Apple Account did not return a usable new session cookie.")
	}
	if task.ICloudOpened && (response.OldChannel == nil || strings.TrimSpace(response.OldChannel.Cookie) == "") {
		return s.retryICloudOnboardingTask(ctx, task, "icloud_cookie_prepare", nil, "old_cookie_missing", "The saved iCloud V2 session was lost; iCloud sign-in will restart.", map[string]any{
			"session_payload": nil, "pending_sms_purpose": "", "manual_verification_code": "",
			"sms_sent_at": nil, "sms_poll_deadline": nil,
		})
	}
	preparation, err := s.iCloudOnboardingForwarding(ctx, task)
	if err != nil {
		if errors.Is(err, ErrICloudImportPreparationConflict) || errors.Is(err, ErrICloudImportPreparationNotFound) {
			return s.retryICloudOnboardingForwardingPreparation(ctx, task)
		}
		return err
	}
	birthday, err := time.Parse("2006-01-02", secret.Birthday)
	if err != nil {
		return s.failICloudOnboardingTask(ctx, task, "invalid_credentials", "Stored Apple birthday is invalid.")
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	var resourceID uint
	validationReady := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked iCloudOnboardingTaskModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND generation = ? AND claim_token = ? AND dispatch_status = ?", task.ID, task.Generation, task.ClaimToken, "running").First(&locked).Error; err != nil {
			return ErrICloudImportClaim
		}
		if locked.ImportID == nil || *locked.ImportID == 0 {
			return ErrICloudOnboardingInvalid
		}
		if err := requireICloudAppleIDReservationTx(tx, iCloudAppleIDReservationOnboarding, *locked.ImportID, locked.PrimaryEmail); err != nil {
			return err
		}
		if locked.ResourceID == nil || *locked.ResourceID != locked.ID {
			return ErrICloudResourceIdentity
		}
		var root iCloudRootModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND type = ?", *locked.ResourceID, "icloud").First(&root).Error; err != nil {
			return fmt.Errorf("onboarding resource root changed: %w", ErrICloudResourceIdentity)
		}
		var existing iCloudResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&existing, root.ID).Error; err != nil {
			return fmt.Errorf("onboarding resource missing: %w", ErrICloudResourceIdentity)
		}
		if iCloudImportEmailKey(existing.PrimaryEmail) != iCloudImportEmailKey(locked.PrimaryEmail) || existing.Status == iCloudResourceDeleted {
			return fmt.Errorf("onboarding resource identity changed: %w", ErrICloudResourceIdentity)
		}
		resourceID = existing.ID
		if existing.AccountRole == "unknown" {
			credentialRevision := existing.CredentialRevision + 1
			if credentialRevision < 2 {
				credentialRevision = 2
			}
			validationGeneration := existing.ValidationGeneration + 1
			if validationGeneration < 2 {
				validationGeneration = 2
			}
			updates := map[string]any{
				"account_role": locked.AccountRole, "family_primary_resource_id": locked.FamilyPrimaryResourceID,
				"region": locked.Region, "country_code": locked.CountryCode, "icloud_opened": locked.ICloudOpened,
				"bound_phone_number": locked.BoundPhoneNumber, "bound_phone_country_code": locked.BoundPhoneCountryCode,
				"bound_phone_source": locked.BoundPhoneSource, "kitesim_phone_id": locked.KitesimPhoneID,
				"family_invite_url":   locked.FamilyInviteURL,
				"selected_forward_to": preparation.ForwardToEmail, "required_forward_to": preparation.ForwardToEmail,
				"for_sale":            false,
				"credential_revision": credentialRevision, "credential_updated_at": now,
				"validation_generation": validationGeneration, "validation_failures": 0,
				"alias_count": 0, "last_alias_sync_at": nil, "alias_provision_candidate": "", "alias_provision_reconcile": false,
				"next_validation_at": nil, "next_provision_at": nil, "last_safe_error": "", "updated_at": now,
			}
			if existing.Status != iCloudResourceDisabled {
				updates["status"] = iCloudResourcePending
				updates["next_validation_at"] = now
				validationReady = true
			}
			updated := tx.Model(&iCloudResourceModel{}).Where("id = ? AND account_role = ?", existing.ID, "unknown").Updates(updates)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrICloudResourceIdentity
			}
			if err := tx.Model(&iCloudAliasModel{}).Where("resource_id = ? AND status <> ?", existing.ID, iCloudResourceDeleted).
				Updates(map[string]any{"status": "missing", "updated_at": now}).Error; err != nil {
				return err
			}
		} else {
			if existing.AccountRole != locked.AccountRole {
				return fmt.Errorf("onboarding resource role changed: %w", ErrICloudResourceIdentity)
			}
			updates := map[string]any{
				"family_primary_resource_id": locked.FamilyPrimaryResourceID,
				"region":                     locked.Region, "country_code": locked.CountryCode, "icloud_opened": locked.ICloudOpened,
				"bound_phone_number": locked.BoundPhoneNumber, "bound_phone_country_code": locked.BoundPhoneCountryCode,
				"bound_phone_source": locked.BoundPhoneSource, "kitesim_phone_id": locked.KitesimPhoneID,
				"family_invite_url":   locked.FamilyInviteURL,
				"selected_forward_to": preparation.ForwardToEmail, "required_forward_to": preparation.ForwardToEmail,
				"for_sale":            false,
				"validation_failures": 0, "next_validation_at": nil, "next_provision_at": nil,
				"last_safe_error": "", "updated_at": now,
			}
			if existing.Status != iCloudResourceDisabled {
				updates["status"] = iCloudResourcePending
				updates["next_validation_at"] = now
				validationReady = true
			}
			updated := tx.Model(&iCloudResourceModel{}).
				Where("id = ? AND account_role = ?", existing.ID, locked.AccountRole).Updates(updates)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrICloudResourceIdentity
			}
		}
		rootUpdated := tx.Model(&iCloudRootModel{}).Where("id = ? AND version = ?", root.ID, root.Version).
			Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now})
		if rootUpdated.Error != nil {
			return rootUpdated.Error
		}
		if rootUpdated.RowsAffected != 1 {
			return ErrICloudImportClaim
		}
		answers, err := json.Marshal(secret.SecurityAnswers)
		if err != nil {
			return err
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "resource_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"apple_password", "security_answers", "birthday", "updated_at"}),
		}).Create(&iCloudResourceCredentialModel{
			ResourceID: resourceID, ApplePassword: secret.Password, SecurityAnswers: iCloudJSON(answers),
			Birthday: birthday, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			return err
		}
		channels := []iCloudImportChannel{appleOnboardingImportChannel(*response.NewChannel)}
		if locked.ICloudOpened && response.OldChannel != nil {
			channels = append(channels, appleOnboardingImportChannel(*response.OldChannel))
		}
		if err := upsertICloudImportChannelsTx(tx, resourceID, channels, true, now); err != nil {
			return err
		}
		consumed := tx.Model(&iCloudImportPreparationModel{}).Where("id = ? AND consumed_at IS NULL", preparation.ID).Updates(map[string]any{"consumed_at": now, "updated_at": now})
		if consumed.Error != nil || consumed.RowsAffected != 1 {
			return ErrICloudImportPreparationConflict
		}
		updates := map[string]any{
			"resource_id": resourceID, "secret_payload": nil, "session_payload": nil,
			"manual_verification_code": "", "pending_sms_purpose": "", "sms_sent_at": nil, "sms_poll_deadline": nil,
			"forward_preparation_id": nil, "claim_token": "", "next_attempt_at": nil,
			"last_error_category": "", "last_safe_error": "", "updated_at": now,
		}
		updates["onboarding_status"] = iCloudOnboardingCompleted
		updates["stage"] = "completed"
		updates["dispatch_status"] = "succeeded"
		updates["finished_at"] = now
		updated := tx.Model(&iCloudOnboardingTaskModel{}).Where("id = ? AND generation = ? AND claim_token = ?", locked.ID, locked.Generation, locked.ClaimToken).Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrICloudImportClaim
		}
		return releaseICloudAppleIDReservationTx(tx, iCloudAppleIDReservationOnboarding, *locked.ImportID, locked.PrimaryEmail)
	})
	if err != nil {
		if errors.Is(err, ErrICloudImportClaim) {
			return nil
		}
		if isICloudOnboardingDuplicateResourceError(err) {
			return s.failICloudOnboardingTask(ctx, task, "duplicate_resource", "The Apple ID already exists as an iCloud resource.")
		}
		return ErrICloudOnboardingTemporary
	}
	_ = s.refreshICloudOnboardingImport(context.WithoutCancel(ctx), iCloudOnboardingImportID(task))
	if validationReady {
		_ = s.ScheduleICloudValidationDispatcher(context.WithoutCancel(ctx), 0)
	}
	return nil
}

func isICloudOnboardingDuplicateResourceError(err error) bool {
	return errors.Is(err, ErrICloudResourceIdentity) || isICloudDuplicateError(err)
}

func appleOnboardingImportChannel(channel AppleOnboardingChannel) iCloudImportChannel {
	return iCloudImportChannel{
		Kind: channel.Kind, Host: channel.Host, Cookie: channel.Cookie, Origin: channel.Origin,
		Referer: channel.Referer, UserAgent: channel.UserAgent, FDClientInfo: channel.FDClientInfo,
		APIKey: channel.APIKey, DSID: channel.DSID, ClientID: channel.ClientID,
		ClientBuildNumber: channel.ClientBuildNumber, ClientMasteringNumber: channel.ClientMasteringNumber, Scnt: channel.Scnt,
	}
}

func (s *Service) executeICloudOnboardingApple(ctx context.Context, task *iCloudOnboardingTaskModel, secret iCloudOnboardingSecret, request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	if s.onboardingApple == nil {
		return AppleOnboardingResponse{}, ErrICloudOnboardingProvider
	}
	request.Email = task.PrimaryEmail
	request.Secret = secret
	request.Session = append(request.Session[:0], task.SessionPayload...)
	request.PhoneNumber = firstNonEmpty(request.PhoneNumber, task.BoundPhoneNumber)
	request.PhoneCountryCode = firstNonEmpty(request.PhoneCountryCode, task.BoundPhoneCountryCode)
	request.SkipPhoneEnrollment = request.SkipPhoneEnrollment || task.TaskKind == "refresh" || task.AccountRole == "primary" || task.BoundPhoneSource == "manual"
	return s.onboardingApple.Execute(ctx, request)
}

func (s *Service) handleICloudOnboardingAppleError(ctx context.Context, task *iCloudOnboardingTaskModel, err error) error {
	var appleErr *AppleOnboardingError
	if !errors.As(err, &appleErr) {
		return ErrICloudOnboardingTemporary
	}
	if restart := strings.TrimSpace(appleErr.RestartStage); restart != "" {
		if isICloudOldCookieBackfill(task) && restart == "icloud_prepare" {
			restart = "old_cookie_prepare"
		}
		if isICloudPostFamilyRecoveryTask(task) {
			switch restart {
			case "icloud_prepare":
				restart = "icloud_cookie_prepare"
			case "family_prepare":
				restart = "family_reconcile_prepare"
			}
		}
		switch restart {
		case "icloud_prepare", "old_cookie_prepare", "icloud_cookie_prepare", "family_prepare", "family_reconcile_prepare", "manage_prepare",
			"forwarding_prepare", "forwarding_add_intent", "forwarding_add_apply", "forwarding_wait",
			"forwarding_verify_intent", "forwarding_verify_apply", "resource_import":
		default:
			return s.failICloudOnboardingTask(ctx, task, "invalid_restart_stage", "Apple onboarding could not recover its authentication session.")
		}
		updates := map[string]any{"session_payload": nil, "manual_verification_code": "", "sms_sent_at": nil, "sms_poll_deadline": nil}
		if !isICloudOldCookieBackfill(task) {
			updates["pending_sms_purpose"] = ""
		}
		s.cancelICloudOnboardingSMSChallenge(context.WithoutCancel(ctx), task)
		return s.retryICloudOnboardingTask(ctx, task, restart, nil, firstNonEmpty(appleErr.Category, "session_expired"), appleErr.SafeMessage, updates)
	}
	if appleErr.Retryable {
		retryAt := appleErr.RetryAt
		if retryAt == nil {
			value := appleOnboardingRetryAt("", s.now().UTC())
			retryAt = &value
		}
		value := retryAt.UTC().Truncate(time.Millisecond)
		return s.retryICloudOnboardingTask(ctx, task, task.Stage, &value, firstNonEmpty(appleErr.Category, "apple_retryable"), appleErr.SafeMessage, nil)
	}
	category := strings.TrimSpace(appleErr.Category)
	if category == "" {
		category = "apple_rejected"
	}
	if category == "family_invite_expired" || category == "family_invite_unavailable" || category == "family_invite_invalid" {
		if isICloudPostFamilyRecoveryTask(task) {
			s.cancelICloudOnboardingSMSChallenge(context.WithoutCancel(ctx), task)
			return s.waitICloudPostFamilyRecovery(ctx, task, category, appleErr.SafeMessage, task.Attempts+1, map[string]any{
				"stage": "family_reconcile_prepare", "session_payload": nil,
				"pending_sms_purpose": "", "manual_verification_code": "", "sms_sent_at": nil, "sms_poll_deadline": nil,
			})
		}
		return s.retryICloudOnboardingFamilySelection(ctx, task, category, appleErr.SafeMessage)
	}
	return s.failICloudOnboardingTask(ctx, task, category, appleErr.SafeMessage)
}

func (s *Service) retryICloudOnboardingTask(ctx context.Context, task *iCloudOnboardingTaskModel, stage string, retryAt *time.Time, category, message string, extra map[string]any) error {
	attempts := task.Attempts + 1
	if attempts >= task.MaxAttempts {
		if isICloudPostFamilyRecoveryTask(task) {
			return s.waitICloudPostFamilyRecovery(ctx, task, category, message, attempts, updatesWith(extra, "stage", stage))
		}
		return s.failICloudOnboardingTask(ctx, task, category, message)
	}
	updates := map[string]any{
		"attempts": attempts, "stage_attempts": 0,
		"last_error_category": safeICloudImportMessage(category), "last_safe_error": safeICloudImportMessage(message),
	}
	for key, value := range extra {
		updates[key] = value
	}
	omitICloudOldCookieSafeError(task, updates)
	if retryAt != nil {
		updates["stage"] = stage
		return s.waitICloudOnboardingTaskWithUpdates(ctx, task, retryAt, "pending", message, updates)
	}
	return s.advanceICloudOnboardingTask(ctx, task, stage, nil, updates)
}

func (s *Service) advanceICloudOnboardingTask(ctx context.Context, task *iCloudOnboardingTaskModel, stage string, nextAttempt *time.Time, extra map[string]any) error {
	now := s.now().UTC().Truncate(time.Millisecond)
	if s.queue != nil && iCloudOnboardingMajorStageBoundary(task, stage) {
		delayedUntil := now.Add(iCloudOnboardingStageDelay())
		if nextAttempt == nil || delayedUntil.After(*nextAttempt) {
			nextAttempt = &delayedUntil
		}
	}
	updates := map[string]any{
		"onboarding_status": iCloudOnboardingProcessing, "stage": stage, "dispatch_status": "pending", "claim_token": "",
		"next_attempt_at": nextAttempt, "stage_attempts": 0, "last_error_category": "", "last_safe_error": "", "updated_at": now,
	}
	for key, value := range extra {
		updates[key] = value
	}
	omitICloudOldCookieSafeError(task, updates)
	result := s.db.WithContext(ctx).Model(&iCloudOnboardingTaskModel{}).
		Where("id = ? AND generation = ? AND claim_token = ? AND dispatch_status = ?", task.ID, task.Generation, task.ClaimToken, "running").Updates(updates)
	if result.Error != nil {
		return ErrICloudOnboardingTemporary
	}
	if result.RowsAffected != 1 {
		return nil
	}
	_ = s.refreshICloudOnboardingImport(context.WithoutCancel(ctx), iCloudOnboardingImportID(task))
	delay := time.Duration(0)
	if nextAttempt != nil && nextAttempt.After(now) {
		delay = nextAttempt.Sub(now)
	}
	_ = s.ScheduleICloudOnboardingDispatcher(context.WithoutCancel(ctx), delay)
	return nil
}

func iCloudOnboardingStageDelay() time.Duration {
	return iCloudOnboardingStageDelayMinimum + time.Duration(rand.Intn(int(iCloudOnboardingStageDelayMaximum-iCloudOnboardingStageDelayMinimum)+1))
}

func iCloudOnboardingSMSRetryAt(retryAt, now time.Time) time.Time {
	retryAt = retryAt.UTC().Truncate(time.Millisecond)
	now = now.UTC().Truncate(time.Millisecond)
	if !retryAt.After(now) {
		return now.Add(time.Second)
	}
	return retryAt
}

func iCloudOnboardingMajorStageBoundary(task *iCloudOnboardingTaskModel, nextStage string) bool {
	if task == nil || firstNonEmpty(task.TaskKind, "onboarding") != "onboarding" {
		return false
	}
	currentPhase := iCloudOnboardingTaskPhase(task)
	return currentPhase > 0 && iCloudOnboardingPhase(nextStage) > currentPhase
}

func iCloudOnboardingTaskPhase(task *iCloudOnboardingTaskModel) int {
	if task == nil {
		return 0
	}
	if strings.HasPrefix(strings.TrimSpace(task.Stage), "sms_") {
		switch strings.TrimSpace(task.PendingSMSPurpose) {
		case appleSMSFamilyLogin, appleSMSFamilyReconcileLogin:
			return 2
		case appleSMSManageLogin:
			return 3
		default:
			return 1
		}
	}
	return iCloudOnboardingPhase(task.Stage)
}

func iCloudOnboardingPhase(stage string) int {
	switch strings.TrimSpace(stage) {
	case "accepted", "icloud_prepare", "old_cookie_prepare", "icloud_cookie_prepare",
		"sms_send", "sms_wait", "sms_verify", "sms_verify_recover",
		"icloud_finish", "old_cookie_finish", "icloud_cookie_finish":
		return 1
	case "family_select", "family_prepare", "family_reconcile_prepare", "family_join_intent", "family_join_apply":
		return 2
	case "manage_prepare", "manage_profile", "forwarding_prepare", "forwarding_add_intent", "forwarding_add_apply",
		"forwarding_wait", "forwarding_verify_intent", "forwarding_verify_apply", "resource_import":
		return 3
	default:
		return 0
	}
}

func (s *Service) waitICloudOnboardingTask(ctx context.Context, task *iCloudOnboardingTaskModel, retryAt *time.Time, dispatch, message string) error {
	return s.waitICloudOnboardingTaskWithUpdates(ctx, task, retryAt, dispatch, message, nil)
}

func (s *Service) waitICloudOnboardingTaskWithUpdates(ctx context.Context, task *iCloudOnboardingTaskModel, retryAt *time.Time, dispatch, message string, extra map[string]any) error {
	now := s.now().UTC().Truncate(time.Millisecond)
	updates := map[string]any{
		"onboarding_status": iCloudOnboardingWaiting, "dispatch_status": dispatch, "claim_token": "",
		"next_attempt_at": retryAt, "last_safe_error": safeICloudImportMessage(message), "updated_at": now,
	}
	for key, value := range extra {
		updates[key] = value
	}
	omitICloudOldCookieSafeError(task, updates)
	result := s.db.WithContext(ctx).Model(&iCloudOnboardingTaskModel{}).
		Where("id = ? AND generation = ? AND claim_token = ? AND dispatch_status = ?", task.ID, task.Generation, task.ClaimToken, "running").Updates(updates)
	if result.Error != nil {
		return ErrICloudOnboardingTemporary
	}
	if result.RowsAffected != 1 {
		return nil
	}
	_ = s.refreshICloudOnboardingImport(context.WithoutCancel(ctx), iCloudOnboardingImportID(task))
	if dispatch == "pending" {
		delay := time.Duration(0)
		if retryAt != nil {
			delay = time.Until(*retryAt)
		}
		_ = s.ScheduleICloudOnboardingDispatcher(context.WithoutCancel(ctx), delay)
	}
	return nil
}

func isICloudPostFamilyRecoveryTask(task *iCloudOnboardingTaskModel) bool {
	if task == nil || firstNonEmpty(task.TaskKind, "onboarding") != "onboarding" || task.AccountRole != "child" ||
		(task.FamilyPrimaryResourceID == nil && !hasICloudDirectFamilyInvite(task)) {
		return false
	}
	if task.FamilyReservationConfirmed || task.Stage == "family_join_apply" || task.Stage == "family_reconcile_prepare" {
		return true
	}
	switch task.Stage {
	case "sms_send", "sms_wait", "sms_verify", "sms_verify_recover":
		return task.PendingSMSPurpose == appleSMSFamilyReconcileLogin
	default:
		return false
	}
}

func isICloudPostFamilyRecoveryWaiting(task iCloudOnboardingTaskModel) bool {
	return isICloudPostFamilyRecoveryTask(&task) && task.Status == iCloudOnboardingWaiting && task.DispatchStatus == "waiting" &&
		strings.TrimSpace(task.LastErrorCategory) != "" && iCloudPostFamilyRecoveryStage(task) != ""
}

func iCloudPostFamilyRecoveryStage(task iCloudOnboardingTaskModel) string {
	if (task.Stage == "forwarding_add_apply" && task.LastErrorCategory == "forward_add_failed") ||
		(task.Stage == "forwarding_verify_apply" && task.LastErrorCategory == "forward_code_rejected") ||
		(task.Stage == "forwarding_wait" && task.LastErrorCategory == "forwarding_preparation_expired") {
		return "forwarding_prepare"
	}
	switch task.Stage {
	case "family_prepare", "family_join_intent":
		return "family_prepare"
	case "family_reconcile_prepare":
		return "family_reconcile_prepare"
	case "family_join_apply":
		return "family_join_apply"
	case "sms_send", "sms_wait", "sms_verify", "sms_verify_recover":
		switch task.PendingSMSPurpose {
		case appleSMSICloudCookieLogin:
			return "icloud_cookie_prepare"
		case appleSMSFamilyReconcileLogin:
			return "family_reconcile_prepare"
		case appleSMSManageLogin:
			return "manage_prepare"
		default:
			return ""
		}
	case "icloud_cookie_prepare", "icloud_cookie_finish", "manage_prepare", "manage_profile",
		"forwarding_prepare", "forwarding_add_intent", "forwarding_add_apply", "forwarding_wait",
		"forwarding_verify_intent", "forwarding_verify_apply", "resource_import":
		return task.Stage
	default:
		return ""
	}
}

func (s *Service) waitICloudPostFamilyRecovery(ctx context.Context, task *iCloudOnboardingTaskModel, category, message string, attempts int, extra map[string]any) error {
	now := s.now().UTC().Truncate(time.Millisecond)
	updates := map[string]any{
		"onboarding_status": iCloudOnboardingWaiting, "dispatch_status": "waiting", "claim_token": "", "next_attempt_at": nil,
		"attempts": min(attempts, task.MaxAttempts), "last_error_category": safeICloudImportMessage(category),
		"last_safe_error": safeICloudImportMessage(message), "finished_at": nil, "updated_at": now,
	}
	for key, value := range extra {
		updates[key] = value
	}
	omitICloudOldCookieSafeError(task, updates)
	updated := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if task.FamilyPrimaryResourceID != nil && isICloudFamilyInviteFailure(category) {
			if err := tx.Model(&iCloudResourceModel{}).Where("id = ? AND account_role = ?", *task.FamilyPrimaryResourceID, "primary").Updates(map[string]any{
				"family_sync_error_category": safeICloudImportMessage(category), "updated_at": now,
			}).Error; err != nil {
				return err
			}
		}
		result := tx.Model(&iCloudOnboardingTaskModel{}).
			Where("id = ? AND generation = ? AND claim_token = ? AND dispatch_status = ?", task.ID, task.Generation, task.ClaimToken, "running").
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrICloudImportClaim
		}
		updated = true
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrICloudImportClaim) {
			return nil
		}
		return ErrICloudOnboardingTemporary
	}
	if !updated {
		return nil
	}
	_ = s.refreshICloudOnboardingImport(context.WithoutCancel(ctx), iCloudOnboardingImportID(task))
	return nil
}

func (s *Service) failICloudOnboardingTask(ctx context.Context, task *iCloudOnboardingTaskModel, category, message string) error {
	s.cancelICloudOnboardingSMSChallenge(context.WithoutCancel(ctx), task)
	if task.TaskKind == "refresh" && task.ResourceID != nil {
		return s.failICloudRefreshTask(ctx, task, category, message)
	}
	if isICloudPostFamilyRecoveryTask(task) {
		return s.waitICloudPostFamilyRecovery(ctx, task, category, message, task.Attempts+1, nil)
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	updated := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&iCloudOnboardingTaskModel{}).
			Where("id = ? AND generation = ? AND claim_token = ? AND dispatch_status = ?", task.ID, task.Generation, task.ClaimToken, "running").
			Updates(map[string]any{
				"onboarding_status": iCloudOnboardingFailed, "dispatch_status": "failed", "claim_token": "", "next_attempt_at": nil,
				"attempts":            min(task.Attempts+1, task.MaxAttempts),
				"last_error_category": safeICloudImportMessage(category), "last_safe_error": safeICloudImportMessage(message),
				"secret_payload": nil, "session_payload": nil, "manual_verification_code": "", "pending_sms_purpose": "",
				"sms_sent_at": nil, "sms_poll_deadline": nil, "forward_preparation_id": nil,
				"finished_at": now, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrICloudImportClaim
		}
		updated = true
		if err := markICloudOnboardingResourceFailedTx(tx, task, message, now); err != nil {
			return err
		}
		if task.ImportID != nil {
			return releaseICloudAppleIDReservationTx(tx, iCloudAppleIDReservationOnboarding, *task.ImportID, task.PrimaryEmail)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrICloudImportClaim) {
			return nil
		}
		return ErrICloudOnboardingTemporary
	}
	if !updated {
		return nil
	}
	_ = s.refreshICloudOnboardingImport(context.WithoutCancel(ctx), iCloudOnboardingImportID(task))
	return nil
}

func markICloudOnboardingResourceFailedTx(tx *gorm.DB, task *iCloudOnboardingTaskModel, message string, now time.Time) error {
	if tx == nil || task == nil || firstNonEmpty(task.TaskKind, "onboarding") != "onboarding" || task.ResourceID == nil {
		return nil
	}
	result := tx.Model(&iCloudResourceModel{}).
		Where("id = ? AND (account_role = ? OR account_role = ?) AND status NOT IN ?", *task.ResourceID, task.AccountRole, "unknown", []string{iCloudResourceDisabled, iCloudResourceDeleted}).
		Updates(map[string]any{
			"status": iCloudResourceAbnormal, "for_sale": false,
			"next_validation_at": nil, "next_provision_at": nil,
			"last_safe_error": safeICloudImportMessage(message), "updated_at": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return nil
	}
	return tx.Model(&iCloudRootModel{}).Where("id = ?", *task.ResourceID).
		Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}).Error
}

func (s *Service) ReleaseICloudOnboardingTask(ctx context.Context, payload iCloudOnboardingTask, safeError string) error {
	if s == nil || s.db == nil || payload.TaskID == 0 || payload.Generation == 0 {
		return ErrICloudOnboardingTemporary
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	return s.releaseICloudOnboardingTask(ctx, payload, safeError, now, false)
}

func (s *Service) releaseICloudOnboardingTask(ctx context.Context, payload iCloudOnboardingTask, safeError string, now time.Time, staleOnly bool) error {
	var importID uint
	var postFamilyRecovery *iCloudOnboardingTaskModel
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var task iCloudOnboardingTaskModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND generation = ? AND onboarding_status IN ? AND dispatch_status IN ?", payload.TaskID, payload.Generation, []string{iCloudOnboardingProcessing, iCloudOnboardingWaiting}, []string{"queued", "running"}).First(&task).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return ErrICloudOnboardingTemporary
		}
		if staleOnly && !iCloudOnboardingLeaseExpired(task, now) {
			return nil
		}
		importID = iCloudOnboardingImportID(&task)
		attempts := min(task.Attempts+1, task.MaxAttempts)
		if attempts < task.MaxAttempts {
			updates := map[string]any{
				"onboarding_status": iCloudOnboardingProcessing, "dispatch_status": "pending", "generation": task.Generation + 1,
				"claim_token": "", "attempts": attempts, "next_attempt_at": now.Add(time.Second),
				"last_safe_error": safeICloudImportMessage(safeError), "updated_at": now,
			}
			omitICloudOldCookieSafeError(&task, updates)
			return tx.Model(&iCloudOnboardingTaskModel{}).Where("id = ? AND generation = ?", task.ID, task.Generation).Updates(updates).Error
		}
		message := "Onboarding could not continue after repeated infrastructure failures."
		if isICloudPostFamilyRecoveryTask(&task) {
			stored := task
			postFamilyRecovery = &stored
			updates := map[string]any{
				"onboarding_status": iCloudOnboardingWaiting, "dispatch_status": "waiting", "claim_token": "", "attempts": attempts,
				"next_attempt_at": nil, "last_error_category": "infrastructure_retries_exhausted", "last_safe_error": message,
				"finished_at": nil, "updated_at": now,
			}
			omitICloudOldCookieSafeError(&task, updates)
			return tx.Model(&iCloudOnboardingTaskModel{}).Where("id = ? AND generation = ?", task.ID, task.Generation).Updates(updates).Error
		}
		updates := map[string]any{
			"onboarding_status": iCloudOnboardingFailed, "dispatch_status": "failed", "claim_token": "", "attempts": attempts,
			"next_attempt_at": nil, "last_error_category": "infrastructure_retries_exhausted", "last_safe_error": message,
			"secret_payload": nil, "session_payload": nil, "manual_verification_code": "", "pending_sms_purpose": "",
			"sms_sent_at": nil, "sms_poll_deadline": nil, "forward_preparation_id": nil,
			"finished_at": now, "updated_at": now,
		}
		omitICloudOldCookieSafeError(&task, updates)
		result := tx.Model(&iCloudOnboardingTaskModel{}).Where("id = ? AND generation = ?", task.ID, task.Generation).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrICloudOnboardingTemporary
		}
		if err := markICloudOnboardingResourceFailedTx(tx, &task, message, now); err != nil {
			return err
		}
		if task.TaskKind == "onboarding" && task.ImportID != nil {
			if err := releaseICloudAppleIDReservationTx(tx, iCloudAppleIDReservationOnboarding, *task.ImportID, task.PrimaryEmail); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrICloudOnboardingTemporary) {
			return err
		}
		return ErrICloudOnboardingTemporary
	}
	if postFamilyRecovery != nil {
		s.cancelICloudOnboardingSMSChallenge(context.WithoutCancel(ctx), postFamilyRecovery)
	}
	return s.refreshICloudOnboardingImport(context.WithoutCancel(ctx), importID)
}

func iCloudOnboardingLeaseExpired(task iCloudOnboardingTaskModel, now time.Time) bool {
	switch task.DispatchStatus {
	case "queued":
		return task.NextAttemptAt != nil && !task.NextAttemptAt.After(now.Add(-iCloudOnboardingQueueLease))
	case "running":
		return task.StartedAt != nil && !task.StartedAt.After(now.Add(-iCloudOnboardingRunningLease))
	default:
		return false
	}
}

func (s *Service) refreshICloudOnboardingImport(context.Context, uint) error {
	// Batch summaries are derived on read from icloud_resources. There is no
	// mutable import row to refresh.
	return nil
}

func (s *Service) SubmitICloudOnboardingSMSCode(ctx context.Context, taskID, operatorUserID uint, code, requestID, pathValue string) error {
	code = strings.TrimSpace(code)
	if taskID == 0 || operatorUserID == 0 || s.operationLogs == nil || len(code) < 4 || len(code) > 10 {
		return ErrICloudOnboardingInvalid
	}
	if _, err := strconv.Atoi(code); err != nil {
		return ErrICloudOnboardingInvalid
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var task iCloudOnboardingTaskModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND onboarding_status = ? AND dispatch_status = ? AND stage = ? AND kitesim_phone_id IS NULL", taskID, iCloudOnboardingWaiting, "waiting", "sms_wait").
			First(&task).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrICloudOnboardingInvalid
			}
			return err
		}
		if task.TaskKind == "onboarding" {
			if err := s.ensureICloudOnboardingAppleIDReservationTx(tx, &task); err != nil {
				return err
			}
		}
		updates := map[string]any{
			"manual_verification_code": code, "onboarding_status": iCloudOnboardingProcessing, "dispatch_status": "pending",
			"generation": gorm.Expr("generation + 1"), "next_attempt_at": now, "last_safe_error": "", "updated_at": now,
		}
		omitICloudOldCookieSafeError(&task, updates)
		result := tx.Model(&iCloudOnboardingTaskModel{}).
			Where("id = ? AND onboarding_status = ? AND dispatch_status = ? AND stage = ? AND kitesim_phone_id IS NULL", taskID, iCloudOnboardingWaiting, "waiting", "sms_wait").
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrICloudOnboardingInvalid
		}
		return s.operationLogs.CreateInTx(ctx, tx, &governancedomain.OperationLog{
			OperatorUserID: operatorUserID, OperationType: "icloud.admin_account_onboarding.sms_code_submit",
			ResourceType: "icloud_account_onboarding_task", ResourceID: fmt.Sprintf("icloud-onboarding-task:%d", taskID),
			Path: pathValue, Result: "success", SafeSummary: "Submitted a manual Apple verification code.", RequestID: requestID,
		})
	})
	if err != nil {
		if errors.Is(err, ErrICloudOnboardingInvalid) || errors.Is(err, ErrICloudResourceIdentity) {
			return err
		}
		return ErrICloudOnboardingTemporary
	}
	_ = s.ScheduleICloudOnboardingDispatcher(context.WithoutCancel(ctx), 0)
	return nil
}

func (s *Service) ConfirmICloudOnboardingFamilyReset(ctx context.Context, taskID, operatorUserID uint, requestID, pathValue string) error {
	if taskID == 0 || operatorUserID == 0 {
		return ErrICloudOnboardingInvalid
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	var importID uint
	resumeOnboarding := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var task iCloudOnboardingTaskModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, taskID).Error; err != nil {
			return ErrICloudOnboardingInvalid
		}
		familyLinked := task.FamilyPrimaryResourceID != nil || hasICloudDirectFamilyInvite(&task)
		if task.Status == iCloudOnboardingCompleted && task.Stage == "completed" && task.AccountRole == "child" &&
			task.ResourceID != nil && familyLinked && task.FamilyReservationConfirmed {
			importID = iCloudOnboardingImportID(&task)
			if task.ImportID != nil {
				return releaseICloudAppleIDReservationTx(tx, iCloudAppleIDReservationOnboarding, *task.ImportID, task.PrimaryEmail)
			}
			return nil
		}
		if task.Status == iCloudOnboardingProcessing && task.Stage == "manage_prepare" && task.DispatchStatus == "pending" &&
			task.TaskKind == "onboarding" && task.AccountRole == "child" && task.ResourceID != nil && familyLinked && task.FamilyReservationConfirmed {
			importID = iCloudOnboardingImportID(&task)
			resumeOnboarding = true
			return nil
		}
		if !isICloudOnboardingFamilySharingWaitingTask(&task) {
			return ErrICloudOnboardingInvalid
		}
		if task.ResourceID == nil {
			return ErrICloudOnboardingInvalid
		}
		if err := s.ensureICloudOnboardingAppleIDReservationTx(tx, &task); err != nil {
			return err
		}
		importID = iCloudOnboardingImportID(&task)
		if task.Stage == iCloudOnboardingStageFamilySharing {
			if task.TaskKind != "onboarding" || task.AccountRole != "child" ||
				(!familyLinked) || !task.FamilyReservationConfirmed {
				return ErrICloudOnboardingInvalid
			}
			resumeOnboarding = true
			if err := tx.Model(&iCloudOnboardingTaskModel{}).Where("id = ?", task.ID).Updates(map[string]any{
				"onboarding_status": iCloudOnboardingProcessing, "stage": "manage_prepare", "dispatch_status": "pending",
				"generation": task.Generation + 1, "claim_token": "", "session_payload": nil,
				"manual_verification_code": "", "pending_sms_purpose": "", "sms_sent_at": nil, "sms_poll_deadline": nil,
				"forward_preparation_id": nil, "stage_attempts": 0, "next_attempt_at": now,
				"last_error_category": "", "last_safe_error": "", "finished_at": nil, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			if s.operationLogs == nil {
				return ErrICloudImportDependency
			}
			return s.operationLogs.CreateInTx(ctx, tx, &governancedomain.OperationLog{
				OperatorUserID: operatorUserID, OperationType: "icloud.admin_account_onboarding.family_reset_confirm",
				ResourceType: "icloud_account_onboarding_task", ResourceID: fmt.Sprintf("icloud-onboarding-task:%d", task.ID),
				Path: pathValue, Result: "success", SafeSummary: "Confirmed manual family sharing setup.", RequestID: requestID,
			})
		}
		if err := tx.Model(&iCloudResourceModel{}).Where("id = ?", *task.ResourceID).Updates(map[string]any{"next_validation_at": now, "last_safe_error": "", "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&iCloudOnboardingTaskModel{}).Where("id = ?", task.ID).Updates(map[string]any{
			"onboarding_status": iCloudOnboardingCompleted, "stage": "completed", "dispatch_status": "succeeded",
			"forward_preparation_id": nil, "last_safe_error": "", "finished_at": now, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		if s.operationLogs == nil {
			return ErrICloudImportDependency
		}
		if err := s.operationLogs.CreateInTx(ctx, tx, &governancedomain.OperationLog{
			OperatorUserID: operatorUserID, OperationType: "icloud.admin_account_onboarding.family_reset_confirm",
			ResourceType: "icloud_account_onboarding_task", ResourceID: fmt.Sprintf("icloud-onboarding-task:%d", task.ID),
			Path: pathValue, Result: "success", SafeSummary: "Confirmed the family sharing reset.", RequestID: requestID,
		}); err != nil {
			return err
		}
		if task.ImportID != nil {
			return releaseICloudAppleIDReservationTx(tx, iCloudAppleIDReservationOnboarding, *task.ImportID, task.PrimaryEmail)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrICloudOnboardingInvalid) || errors.Is(err, ErrICloudResourceIdentity) {
			return err
		}
		return ErrICloudOnboardingTemporary
	}
	_ = s.refreshICloudOnboardingImport(context.WithoutCancel(ctx), importID)
	if resumeOnboarding {
		_ = s.ScheduleICloudOnboardingDispatcher(context.WithoutCancel(ctx), 0)
		return nil
	}
	_ = s.ScheduleICloudValidationDispatcher(context.WithoutCancel(ctx), 0)
	return nil
}

func (s *Service) RetryICloudOnboardingPostFamily(
	ctx context.Context,
	taskID, operatorUserID uint,
	idempotencyKey, requestID, pathValue string,
) error {
	if s == nil || s.db == nil || s.operationLogs == nil || taskID == 0 || operatorUserID == 0 {
		return ErrICloudOnboardingInvalid
	}
	idempotencyKey, err := normalizeAdminICloudIdempotencyKey(idempotencyKey)
	if err != nil {
		return ErrICloudOnboardingInvalid
	}
	fingerprint, err := adminICloudCommandFingerprint(struct {
		TaskID uint `json:"taskId"`
	}{TaskID: taskID})
	if err != nil {
		return ErrICloudOnboardingTemporary
	}
	type retryReceipt struct {
		TaskID     uint   `json:"taskId"`
		Generation uint64 `json:"generation"`
	}
	receiptResult := retryReceipt{}
	receipt := coreapp.AdminResourceCommandReceipt{
		OperatorUserID: operatorUserID, IdempotencyKey: idempotencyKey,
		Operation: "icloud.admin_account_onboarding.post_family_retry",
		Subject:   fmt.Sprintf("icloud-onboarding-task:%d", taskID), RequestFingerprint: fingerprint,
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	replayed := false
	importID := uint(0)
	var challengeTask *iCloudOnboardingTaskModel
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		wasReplayed, reserveErr := s.reserveAdminICloudCommand(ctx, tx, receipt, &receiptResult)
		if reserveErr != nil || wasReplayed {
			replayed = wasReplayed
			return reserveErr
		}
		var task iCloudOnboardingTaskModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, taskID).Error; err != nil {
			return ErrICloudOnboardingInvalid
		}
		stage := iCloudPostFamilyRecoveryStage(task)
		if !isICloudPostFamilyRecoveryWaiting(task) || stage == "" {
			return ErrICloudOnboardingInvalid
		}
		if err := s.ensureICloudOnboardingAppleIDReservationTx(tx, &task); err != nil {
			return err
		}
		importID = iCloudOnboardingImportID(&task)
		stored := task
		challengeTask = &stored
		generation := task.Generation + 1
		updates := map[string]any{
			"onboarding_status": iCloudOnboardingProcessing, "stage": stage, "dispatch_status": "pending", "generation": generation,
			"claim_token": "", "attempts": 0, "stage_attempts": 0, "next_attempt_at": now,
			"manual_verification_code": "", "pending_sms_purpose": "", "sms_sent_at": nil, "sms_poll_deadline": nil,
			"last_error_category": "", "last_safe_error": "", "finished_at": nil, "updated_at": now,
		}
		if stage == "forwarding_prepare" && task.Stage != "forwarding_prepare" {
			updates["forward_preparation_id"] = nil
		}
		result := tx.Model(&iCloudOnboardingTaskModel{}).Where("id = ? AND generation = ?", task.ID, task.Generation).Updates(updates)
		if result.Error != nil || result.RowsAffected != 1 {
			return ErrICloudOnboardingTemporary
		}
		receiptResult = retryReceipt{TaskID: task.ID, Generation: generation}
		if err := s.operationLogs.CreateInTx(ctx, tx, &governancedomain.OperationLog{
			OperatorUserID: operatorUserID, OperationType: "icloud.admin_account_onboarding.post_family_retry",
			ResourceType: "icloud_account_onboarding_task", ResourceID: fmt.Sprintf("icloud-onboarding-task:%d", task.ID),
			Path: pathValue, Result: "success", SafeSummary: "Retried Apple onboarding after family membership was already confirmed.", RequestID: requestID,
		}); err != nil {
			return err
		}
		return s.completeAdminICloudCommand(ctx, tx, operatorUserID, idempotencyKey, receiptResult)
	})
	if err != nil {
		if errors.Is(err, ErrICloudOnboardingInvalid) || errors.Is(err, ErrICloudResourceIdentity) {
			return err
		}
		if errors.Is(normalizeAdminICloudCommandError(err), ErrICloudImportConflict) {
			return ErrICloudOnboardingConflict
		}
		return ErrICloudOnboardingTemporary
	}
	if replayed {
		return nil
	}
	if challengeTask != nil {
		s.cancelICloudOnboardingSMSChallenge(context.WithoutCancel(ctx), challengeTask)
	}
	_ = s.refreshICloudOnboardingImport(context.WithoutCancel(ctx), importID)
	_ = s.ScheduleICloudOnboardingDispatcher(context.WithoutCancel(ctx), 0)
	return nil
}

func (s *Service) ConfirmICloudOnboardingActivation(ctx context.Context, taskID, operatorUserID uint, requestID, pathValue string) error {
	if taskID == 0 || operatorUserID == 0 {
		return ErrICloudOnboardingInvalid
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	var importID uint
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var task iCloudOnboardingTaskModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, taskID).Error; err != nil {
			return ErrICloudOnboardingInvalid
		}
		if task.ICloudActivationConfirmedAt != nil {
			importID = iCloudOnboardingImportID(&task)
			return nil
		}
		if task.Status != iCloudOnboardingWaiting || task.Stage != "waiting_icloud_activation" ||
			(task.AccountRole != "primary" && task.AccountRole != "child") {
			return ErrICloudOnboardingInvalid
		}
		if task.TaskKind == "onboarding" {
			if err := s.ensureICloudOnboardingAppleIDReservationTx(tx, &task); err != nil {
				return err
			}
		}
		importID = iCloudOnboardingImportID(&task)
		nextStage := "icloud_prepare"
		if isICloudOldCookieBackfill(&task) {
			nextStage = "old_cookie_prepare"
		} else if task.FamilyReservationConfirmed {
			nextStage = "icloud_cookie_prepare"
		} else if firstNonEmpty(task.TaskKind, "onboarding") == "onboarding" && hasICloudDirectFamilyInvite(&task) {
			nextStage = "family_prepare"
		} else if firstNonEmpty(task.TaskKind, "onboarding") == "onboarding" && task.AccountRole == "child" &&
			task.BoundPhoneSource != "manual" {
			nextStage = "family_select"
		}
		updates := map[string]any{
			"onboarding_status": iCloudOnboardingProcessing, "stage": nextStage, "dispatch_status": "pending",
			"generation": task.Generation + 1, "claim_token": "", "session_payload": nil,
			"icloud_activation_confirmed_at": now,
			"next_attempt_at":                now, "last_error_category": "", "last_safe_error": "", "updated_at": now,
		}
		omitICloudOldCookieSafeError(&task, updates)
		if err := tx.Model(&iCloudOnboardingTaskModel{}).Where("id = ?", task.ID).Updates(updates).Error; err != nil {
			return err
		}
		if s.operationLogs == nil {
			return ErrICloudImportDependency
		}
		return s.operationLogs.CreateInTx(ctx, tx, &governancedomain.OperationLog{
			OperatorUserID: operatorUserID, OperationType: "icloud.admin_account_onboarding.activation_confirm",
			ResourceType: "icloud_account_onboarding_task", ResourceID: fmt.Sprintf("icloud-onboarding-task:%d", task.ID),
			Path: pathValue, Result: "success", SafeSummary: "Confirmed manual iCloud activation.", RequestID: requestID,
		})
	})
	if err != nil {
		if errors.Is(err, ErrICloudOnboardingInvalid) || errors.Is(err, ErrICloudResourceIdentity) {
			return err
		}
		return ErrICloudOnboardingTemporary
	}
	_ = s.refreshICloudOnboardingImport(context.WithoutCancel(ctx), importID)
	_ = s.ScheduleICloudOnboardingDispatcher(context.WithoutCancel(ctx), 0)
	return nil
}

func claimedAppleSMSCode(message kitesim.MessageItem) string {
	match := appleSMSCodePattern.FindStringSubmatch(message.Content)
	if len(match) == 2 {
		return match[1]
	}
	return ""
}

func updatesWith(values map[string]any, key string, value any) map[string]any {
	if values == nil {
		values = make(map[string]any)
	}
	values[key] = value
	return values
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func iCloudOnboardingImportID(task *iCloudOnboardingTaskModel) uint {
	if task == nil || task.ImportID == nil {
		return 0
	}
	return *task.ImportID
}
