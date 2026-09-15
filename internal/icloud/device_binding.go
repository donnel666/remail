package icloud

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/kitesim"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const typeICloudDeviceSync = "icloud:device_binding_sync"
const deviceBindingPoll = 10 * time.Second
const deviceBindingDelay = 30 * time.Second
const deviceBindingTimeout = 10 * time.Minute

type deviceBindingModel struct {
	Email         string `gorm:"primaryKey;size:320"`
	ResourceID    *uint
	PhoneID       uint
	Phone         string
	Password      string
	IdentityHash  string
	Generation    uint64
	Status        string
	RemoteID      string
	CodeAPI       string
	CallbackToken *string `gorm:"size:64;uniqueIndex"`
	ChallengeID   uint64
	SubmitAt      time.Time
	SubmittedAt   *time.Time
	DeadlineAt    time.Time
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (deviceBindingModel) TableName() string { return "icloud_device_bindings" }

// DeviceBinding is the durable device enrollment result shared by web and CMD workflows.
type DeviceBinding struct {
	Status    string `json:"status"`
	CodeAPI   string `json:"codeApi,omitempty"`
	RemoteID  string `json:"remoteId,omitempty"`
	LastError string `json:"lastError,omitempty"`
}

func deviceBindingTag(b deviceBindingModel) string {
	digest := sha256.Sum256([]byte(b.Email))
	return "remail:" + hex.EncodeToString(digest[:8]) + ":" + strconv.FormatUint(b.Generation, 10)
}

// EnsureDeviceBinding schedules the first import no earlier than 30 seconds
// after phone confirmation. Repeated calls do not rotate credentials or resubmit.
func (s *Service) EnsureDeviceBinding(ctx context.Context, email, password string, phoneID uint, phone string, resourceID *uint, confirmedAt time.Time) (*DeviceBinding, error) {
	return s.ensureDeviceBinding(ctx, email, password, phoneID, phone, resourceID, confirmedAt, false, nil)
}

func (s *Service) ensureDeviceBinding(ctx context.Context, email, password string, phoneID uint, phone string, resourceID *uint, confirmedAt time.Time, retry bool, audit *governancedomain.OperationLog) (*DeviceBinding, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || password == "" || phoneID == 0 || strings.TrimSpace(phone) == "" {
		return nil, ErrICloudOnboardingInvalid
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", email, password, phoneID)))
	identity := hex.EncodeToString(digest[:])
	now := s.now().UTC()
	if confirmedAt.IsZero() || confirmedAt.After(now) {
		confirmedAt = now
	}
	var binding deviceBindingModel
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if resourceID != nil {
			var resource iCloudResourceModel
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "primary_email", "status", "kitesim_phone_id").First(&resource, *resourceID).Error; err != nil {
				return err
			}
			if resource.Status == iCloudResourceDeleted || resource.Status == iCloudResourceDisabled || !strings.EqualFold(resource.PrimaryEmail, email) || resource.KitesimPhoneID == nil || *resource.KitesimPhoneID != phoneID {
				return ErrICloudResourceStatus
			}
		}
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("email = ?", email).Take(&binding).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil && binding.IdentityHash == identity && (!retry || binding.Status != "failed") {
			if resourceID != nil && binding.ResourceID == nil {
				binding.ResourceID = resourceID
				if err := tx.Model(&binding).Update("resource_id", resourceID).Error; err != nil {
					return err
				}
			}
			if resourceID != nil {
				return tx.Model(&iCloudResourceModel{}).Where("id = ?", *resourceID).Updates(map[string]any{"device_code_api": binding.CodeAPI, "device_bind_status": binding.Status, "device_account_id": binding.RemoteID}).Error
			}
			return nil
		}
		if !DeviceCodeConfigured() {
			return errDeviceUnauthorized
		}
		var secret [32]byte
		if _, err := rand.Read(secret[:]); err != nil {
			return err
		}
		token := base64.RawURLEncoding.EncodeToString(secret[:])
		binding = deviceBindingModel{Email: email, ResourceID: resourceID, PhoneID: phoneID, Phone: phone, Password: password, IdentityHash: identity,
			Generation: binding.Generation + 1, Status: "pending", CallbackToken: &token, SubmitAt: confirmedAt.Add(deviceBindingDelay), DeadlineAt: now.Add(deviceBindingTimeout), CreatedAt: now, UpdatedAt: now}
		if err := tx.Save(&binding).Error; err != nil {
			return err
		}
		if resourceID != nil {
			if err := tx.Model(&iCloudResourceModel{}).Where("id = ? AND LOWER(primary_email) = ? AND status NOT IN ?", *resourceID, email, []string{iCloudResourceDeleted, iCloudResourceDisabled}).
				Updates(map[string]any{"device_code_api": "", "device_bind_status": "pending", "device_account_id": ""}).Error; err != nil {
				return err
			}
		}
		if audit != nil {
			return s.operationLogs.CreateInTx(ctx, tx, audit)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if binding.Status == "pending" || binding.Status == "binding" {
		_ = s.ScheduleDeviceBindingSync(context.WithoutCancel(ctx))
	}
	return &DeviceBinding{Status: binding.Status, CodeAPI: binding.CodeAPI, RemoteID: binding.RemoteID, LastError: binding.LastError}, nil
}

// ScheduleDeviceBindingSync queues one shared task, independent of account count.
func (s *Service) ScheduleDeviceBindingSync(ctx context.Context) error {
	if s.queue == nil {
		return nil
	}
	_, err := s.queue.EnqueueContext(ctx, asynq.NewTask(typeICloudDeviceSync, nil),
		asynq.Queue(platform.QueueDefault), asynq.Unique(iCloudDispatcherTaskTimeout),
		asynq.Timeout(iCloudDispatcherTaskTimeout), asynq.MaxRetry(0), asynq.Retention(0))
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	return err
}

func (s *Service) syncDeviceBindings(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrICloudOnboardingTemporary
	}
	if s.deviceRedis != nil {
		allowed, err := s.deviceRedis.SetNX(ctx, "icloud:device:sync:interval", "1", deviceBindingPoll).Result()
		if err != nil {
			return err
		}
		if !allowed {
			return nil
		}
	}
	var bindings []deviceBindingModel
	if err := s.db.WithContext(ctx).Where("status IN ?", []string{"pending", "binding"}).Order("submit_at, email").Find(&bindings).Error; err != nil {
		return err
	}
	if len(bindings) == 0 {
		return nil
	}
	// Expired callbacks and temporary passwords are cleared even when the
	// external listing is slow or unavailable.
	active := bindings[:0]
	for _, binding := range bindings {
		if !s.now().Before(binding.DeadlineAt) {
			if err := s.finishDeviceBinding(ctx, binding, "failed", "", "", "Device binding timed out; inspect the remote account before retrying."); err != nil {
				return err
			}
			continue
		}
		active = append(active, binding)
	}
	bindings = active
	var remote []deviceRemoteAccount
	var listErr error
	for _, b := range bindings {
		if b.Status == "binding" {
			remote, listErr = s.device.accounts(ctx)
			break
		}
	}
	for _, binding := range bindings {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if binding.ResourceID != nil {
			var active int64
			if err := s.db.WithContext(ctx).Model(&iCloudResourceModel{}).Where("id = ? AND LOWER(primary_email) = ? AND kitesim_phone_id = ? AND status NOT IN ? AND device_bind_status IN ?", *binding.ResourceID, binding.Email, binding.PhoneID, []string{iCloudResourceDisabled, iCloudResourceDeleted}, []string{"pending", "binding"}).Count(&active).Error; err != nil {
				return err
			}
			if active == 0 {
				if err := s.finishDeviceBinding(ctx, binding, "failed", "", "", "Device binding canceled because the account changed."); err != nil {
					return err
				}
				continue
			}
		}
		if !s.now().Before(binding.DeadlineAt) {
			if err := s.finishDeviceBinding(ctx, binding, "failed", "", "", "Device binding timed out; inspect the remote account before retrying."); err != nil {
				return err
			}
			continue
		}
		if binding.Status == "pending" {
			if s.now().Before(binding.SubmitAt) {
				continue
			}
			if err := s.submitDeviceBinding(ctx, binding); err != nil {
				return err
			}
			continue
		}
		if listErr != nil {
			if err := s.deviceBindingError(ctx, binding, listErr.Error()); err != nil {
				return err
			}
			continue
		}
		var matches []deviceRemoteAccount
		for _, item := range remote {
			if strings.EqualFold(strings.TrimSpace(item.Account), binding.Email) && item.Tag == deviceBindingTag(binding) {
				matches = append(matches, item)
			}
		}
		if len(matches) != 1 {
			message := "Waiting for the imported device account to appear."
			if len(matches) > 1 {
				message = "Multiple device records match this binding; verify the platform response (TODO)."
			}
			if err := s.deviceBindingError(ctx, binding, message); err != nil {
				return err
			}
			continue
		}
		item := matches[0]
		switch item.Status {
		case "success":
			api := item.codeURL()
			if !validDeviceCodeURL(api) {
				if err := s.deviceBindingError(ctx, binding, errDeviceResponse.Error()); err != nil {
					return err
				}
				continue
			}
			if err := s.finishDeviceBinding(ctx, binding, "success", string(item.ID), api, ""); err != nil {
				return err
			}
		case "failed", "unavailable", "lock", "direct":
			if err := s.finishDeviceBinding(ctx, binding, "failed", string(item.ID), "", "Device platform reported a failed binding; inspect its account diagnostics."); err != nil {
				return err
			}
		case "pending", "processing":
			if err := s.deviceBindingError(ctx, binding, ""); err != nil {
				return err
			}
		default:
			if err := s.deviceBindingError(ctx, binding, errDeviceResponse.Error()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) deviceBindingError(ctx context.Context, b deviceBindingModel, message string) error {
	return s.db.WithContext(ctx).Model(&deviceBindingModel{}).Where("email = ? AND generation = ? AND status = ?", b.Email, b.Generation, b.Status).Update("last_error", message).Error
}

func (s *Service) submitDeviceBinding(ctx context.Context, b deviceBindingModel) error {
	if !DeviceCodeConfigured() || s.smsPhones == nil {
		return s.deviceBindingError(ctx, b, "Configure the device API key before binding.")
	}
	if b.CallbackToken == nil {
		return s.finishDeviceBinding(ctx, b, "failed", "", "", "Device SMS callback is missing.")
	}
	reservation, err := s.smsPhones.ReserveSMSChallenge(ctx, b.PhoneID, "device_binding", deviceBindingTag(b), b.DeadlineAt)
	if err != nil {
		return s.deviceBindingError(ctx, b, "Waiting for the phone to become available for device enrollment.")
	}
	b.ChallengeID = reservation.ID
	if err := s.db.WithContext(ctx).Model(&deviceBindingModel{}).Where("email = ? AND generation = ? AND status = ?", b.Email, b.Generation, "pending").Update("challenge_id", reservation.ID).Error; err != nil {
		return err
	}
	// A sent reservation can survive a later local transaction failure. While
	// the binding remains pending, no external import has been committed to run.
	if reservation.Status != kitesim.SMSChallengeSent {
		if err := s.smsPhones.MarkSMSAttemptSent(ctx, reservation.ID); err != nil {
			return s.deviceBindingError(ctx, b, "Device enrollment SMS reservation needs recovery.")
		}
	}
	now := s.now().UTC()
	claimed := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if b.ResourceID != nil {
			var resource iCloudResourceModel
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").
				Where("id = ? AND LOWER(primary_email) = ? AND kitesim_phone_id = ? AND status NOT IN ? AND device_bind_status IN ?", *b.ResourceID, b.Email, b.PhoneID, []string{iCloudResourceDeleted, iCloudResourceDisabled}, []string{"pending", "binding"}).Take(&resource).Error; err != nil {
				return err
			}
		}
		result := tx.Model(&deviceBindingModel{}).Where("email = ? AND generation = ? AND status = ?", b.Email, b.Generation, "pending").
			Updates(map[string]any{"status": "binding", "submitted_at": now, "challenge_id": reservation.ID, "last_error": "", "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		if b.ResourceID != nil {
			if err := tx.Model(&iCloudResourceModel{}).Where("id = ?", *b.ResourceID).Update("device_bind_status", "binding").Error; err != nil {
				return err
			}
		}
		claimed = true
		return nil
	})
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	b.Status = "binding"
	b.SubmittedAt = &now
	callback := deviceSMSBaseURL() + "/sms/icloud-device/" + *b.CallbackToken
	// The submitted marker is committed before external I/O. An ambiguous reply
	// is reconciled through the shared list poller instead of replaying importData.
	if err := s.device.importAccount(ctx, b, callback); err != nil {
		if errors.Is(err, errDeviceImportRejected) || errors.Is(err, errDeviceUnauthorized) {
			return s.finishDeviceBinding(context.WithoutCancel(ctx), b, "failed", "", "", err.Error())
		}
		return s.deviceBindingError(context.WithoutCancel(ctx), b, err.Error())
	}
	_ = s.smsPhones.ConfirmSMSAttemptSent(context.WithoutCancel(ctx), reservation.ID)
	return nil
}

func (s *Service) finishDeviceBinding(ctx context.Context, b deviceBindingModel, status, remoteID, api, message string) error {
	changed := false
	var resumeAt *time.Time
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if b.ResourceID != nil && status == "success" {
			var resource iCloudResourceModel
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "primary_email", "kitesim_phone_id", "status", "device_bind_status").First(&resource, *b.ResourceID).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if errors.Is(err, gorm.ErrRecordNotFound) || resource.Status == iCloudResourceDeleted || resource.Status == iCloudResourceDisabled ||
				!strings.EqualFold(resource.PrimaryEmail, b.Email) || resource.KitesimPhoneID == nil || *resource.KitesimPhoneID != b.PhoneID ||
				(resource.DeviceBindStatus != "pending" && resource.DeviceBindStatus != "binding") {
				status, remoteID, api = "failed", "", ""
				message = "Device binding canceled because the account changed."
			}
		}
		result := tx.Model(&deviceBindingModel{}).Where("email = ? AND generation = ? AND status = ?", b.Email, b.Generation, b.Status).
			Updates(map[string]any{"status": status, "remote_id": remoteID, "code_api": api, "password": "", "callback_token": nil, "last_error": message, "updated_at": s.now().UTC()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		changed = true
		if b.ResourceID == nil {
			return nil
		}
		updates := map[string]any{"device_bind_status": status, "device_code_api": api, "device_account_id": remoteID}
		if err := tx.Model(&iCloudResourceModel{}).Where("id = ? AND LOWER(primary_email) = ? AND kitesim_phone_id = ? AND status <> ? AND device_bind_status IN ?", *b.ResourceID, b.Email, b.PhoneID, iCloudResourceDeleted, []string{"pending", "binding"}).Updates(updates).Error; err != nil {
			return err
		}
		if status == "success" {
			next := s.now().Add(iCloudOnboardingStageDelay())
			result := tx.Model(&iCloudResourceModel{}).Where("id = ? AND device_code_api = ? AND dispatch_status = ? AND onboarding_status = ?", *b.ResourceID, api, "waiting", iCloudOnboardingWaiting).
				Updates(map[string]any{"dispatch_status": "pending", "next_attempt_at": next, "last_safe_error": ""})
			if result.RowsAffected > 0 {
				resumeAt = &next
			}
			return result.Error
		}
		return nil
	})
	if err == nil && changed && s.smsPhones != nil && b.ChallengeID != 0 {
		_ = s.smsPhones.CancelSMSChallenge(context.WithoutCancel(ctx), b.ChallengeID)
	}
	if err == nil && changed && resumeAt != nil {
		_ = s.ScheduleICloudOnboardingDispatcher(context.WithoutCancel(ctx), resumeAt.Sub(s.now()))
	}
	return err
}
