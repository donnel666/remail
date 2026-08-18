package kitesim

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	SMSChallengeReserved             = "reserved"
	SMSChallengeSent                 = "sent"
	SMSChallengeCompleted            = "completed"
	SMSChallengeCanceled             = "canceled"
	SMSChallengeExpired              = "expired"
	SMSChallengeSendFailed           = "send_failed"
	SMSChallengeInfrastructureFailed = "infrastructure_failed"

	defaultSMSChallengeTTL = 2 * time.Minute
	appleSMSClockSkew      = 15 * time.Second
)

var appleSMSCodePatterns = []*regexp.Regexp{
	// Apple sometimes wraps the body or appends a provider footer. Match the
	// body text only; the caller is intentionally not part of verification.
	regexp.MustCompile(`你的Apple账户验证码是[[:space:]]*([0-9]{6})[[:space:]]*，切勿向任何人泄露，以防账户或信息被盗。`),
	regexp.MustCompile(`(?i)Your Apple Account Code is:[[:space:]]*([0-9]{6})[[:space:]]*\.[[:space:]]*Don(?:'|’|‘)t share it with anyone\.`),
}

type smsChallengeModel struct {
	ID                 uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	PhoneID            uint       `gorm:"column:phone_id;index:idx_kitesim_sms_challenge_phone"`
	UsageEventID       uint64     `gorm:"column:usage_event_id;uniqueIndex:uk_kitesim_sms_challenge_usage"`
	OwnerKey           *string    `gorm:"column:owner_key;uniqueIndex:uk_kitesim_sms_challenge_owner"`
	Purpose            string     `gorm:"column:purpose"`
	Status             string     `gorm:"column:status"`
	ActivePhoneID      *uint      `gorm:"column:active_phone_id;uniqueIndex:uk_kitesim_sms_challenge_active_phone"`
	SentAt             *time.Time `gorm:"column:sent_at"`
	ExpiresAt          time.Time  `gorm:"column:expires_at;index:idx_kitesim_sms_challenge_expiry"`
	FinishedAt         *time.Time `gorm:"column:finished_at"`
	MessageFingerprint *string    `gorm:"column:message_fingerprint;uniqueIndex:uk_kitesim_sms_challenge_message"`
	MessageCaller      *string    `gorm:"column:message_caller"`
	MessageContent     *string    `gorm:"column:message_content"`
	MessageTime        *string    `gorm:"column:message_time"`
	MessageReceivedAt  *time.Time `gorm:"column:message_received_at"`
	CreatedAt          time.Time  `gorm:"column:created_at"`
	UpdatedAt          time.Time  `gorm:"column:updated_at"`
}

func (smsChallengeModel) TableName() string { return "kitesim_sms_challenges" }

type SMSReservation struct {
	ID            uint64
	PhoneID       uint
	Purpose       string
	Status        string
	CooldownUntil time.Time
	ExpiresAt     time.Time
}

type SMSChallenge struct {
	ID         uint64
	PhoneID    uint
	Purpose    string
	OwnerKey   string
	Status     string
	SentAt     *time.Time
	ExpiresAt  time.Time
	FinishedAt *time.Time
	Message    *MessageItem
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ReserveSMSAttempt is the compatibility entry point. New durable callers
// should use ReserveSMSChallenge with a stable owner key and persist the ID.
func (s *Service) ReserveSMSAttempt(ctx context.Context, phoneID uint, purpose string) (SMSReservation, error) {
	now := time.Now().UTC()
	if s != nil && s.now != nil {
		now = s.now().UTC()
	}
	return s.ReserveSMSChallenge(ctx, phoneID, purpose, "", now.Add(defaultSMSChallengeTTL))
}

// ReserveSMSChallenge atomically consumes quota and owns the phone until the
// challenge is completed, canceled, fails to send, or expires. Reusing the
// same non-empty owner key returns the original challenge for crash recovery,
// except that a completed or canceled challenge yields the owner to a later session.
func (s *Service) ReserveSMSChallenge(ctx context.Context, phoneID uint, purpose, ownerKey string, expiresAt time.Time) (SMSReservation, error) {
	purpose = strings.TrimSpace(purpose)
	ownerKey = strings.TrimSpace(ownerKey)
	if s == nil || s.db == nil || phoneID == 0 || purpose == "" || len(purpose) > 64 || len(ownerKey) > 160 {
		return SMSReservation{}, ErrInvalidInput
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	if expiresAt.IsZero() {
		expiresAt = now.Add(defaultSMSChallengeTTL)
	} else {
		expiresAt = expiresAt.UTC().Truncate(time.Millisecond)
	}
	if !expiresAt.After(now) {
		return SMSReservation{}, ErrInvalidInput
	}

	var reservation SMSReservation
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var phone phoneModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&phone, phoneID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPhoneMissing
		}
		if err != nil {
			return err
		}
		if phone.DeletedAt != nil || phone.DisabledAt != nil || PhoneStatus(phone.Status) != PhoneActive {
			return smsInactivePhoneError(tx, phoneID)
		}

		if ownerKey != "" {
			var existing smsChallengeModel
			err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("owner_key = ?", ownerKey).Take(&existing).Error
			if err == nil {
				if existing.PhoneID != phoneID || existing.Purpose != purpose {
					return ErrSMSChallengeOwnerConflict
				}
				if err := expireSMSChallengeIfNeededTx(tx, &existing, now); err != nil {
					return err
				}
				if existing.Status != SMSChallengeCompleted && existing.Status != SMSChallengeCanceled {
					reservation = smsReservation(existing, phone.SMSCooldownUntil)
					return nil
				}
				if err := tx.Model(&smsChallengeModel{}).
					Where("id = ? AND owner_key = ? AND status IN ?", existing.ID, ownerKey, []string{SMSChallengeCompleted, SMSChallengeCanceled}).
					Update("owner_key", nil).Error; err != nil {
					return err
				}
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}

		var active smsChallengeModel
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("active_phone_id = ?", phoneID).Take(&active).Error
		if err == nil {
			if err := expireSMSChallengeIfNeededTx(tx, &active, now); err != nil {
				return err
			}
			if smsChallengeActive(active.Status) {
				return &SMSPhoneUnavailableError{RetryAt: active.ExpiresAt, Reason: "phone number already has an active SMS challenge"}
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		cooldownUntil, stage, err := reserveSMSPhonePolicyTx(tx, &phone, now)
		if err != nil {
			return err
		}
		event := phoneUsageEventModel{PhoneID: phoneID, Purpose: purpose, Result: SMSChallengeReserved, CreatedAt: now}
		if err := tx.Create(&event).Error; err != nil {
			return err
		}
		var storedOwner *string
		if ownerKey != "" {
			storedOwner = &ownerKey
		}
		activePhoneID := phoneID
		challenge := smsChallengeModel{
			PhoneID: phoneID, UsageEventID: event.ID, OwnerKey: storedOwner, Purpose: purpose,
			Status: SMSChallengeReserved, ActivePhoneID: &activePhoneID, ExpiresAt: expiresAt,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&challenge).Error; err != nil {
			return err
		}
		if err := tx.Model(&phoneModel{}).Where("id = ?", phoneID).Updates(map[string]any{
			"sms_cooldown_until": cooldownUntil,
			"sms_cooldown_stage": stage,
			"sms_last_used_at":   now,
		}).Error; err != nil {
			return err
		}
		reservation = smsReservation(challenge, &cooldownUntil)
		return nil
	})
	if err == nil || ownerKey == "" || !duplicateOperationError(err) {
		return reservation, err
	}
	existing, loadErr := s.GetSMSChallengeByOwner(ctx, ownerKey)
	if loadErr != nil {
		return SMSReservation{}, err
	}
	if existing.PhoneID != phoneID || existing.Purpose != purpose {
		return SMSReservation{}, ErrSMSChallengeOwnerConflict
	}
	return SMSReservation{ID: existing.ID, PhoneID: existing.PhoneID, Purpose: existing.Purpose, Status: existing.Status, ExpiresAt: existing.ExpiresAt}, nil
}

func reserveSMSPhonePolicyTx(tx *gorm.DB, phone *phoneModel, now time.Time) (time.Time, uint8, error) {
	if phone.SMSBlacklistedUntil != nil {
		if phone.SMSBlacklistedUntil.After(now) {
			return time.Time{}, 0, &SMSPhoneUnavailableError{RetryAt: *phone.SMSBlacklistedUntil, Reason: "phone number is blacklisted"}
		}
		if err := tx.Model(&phoneModel{}).Where("id = ?", phone.ID).Updates(map[string]any{
			"sms_consecutive_failures": 0,
			"sms_blacklisted_until":    nil,
		}).Error; err != nil {
			return time.Time{}, 0, err
		}
		phone.SMSConsecutiveFailures = 0
		phone.SMSBlacklistedUntil = nil
	}
	if phone.SMSCooldownUntil != nil && phone.SMSCooldownUntil.After(now) {
		return time.Time{}, 0, &SMSPhoneUnavailableError{RetryAt: *phone.SMSCooldownUntil, Reason: "phone number is cooling down"}
	}
	limit := runtimeconfig.Int(runtimeconfig.ICloudPhoneHourlySMSLimitKey, 10, 1)
	windowStart := now.Add(-time.Hour)
	var count int64
	if err := tx.Model(&phoneUsageEventModel{}).Where("phone_id = ? AND created_at > ?", phone.ID, windowStart).Count(&count).Error; err != nil {
		return time.Time{}, 0, err
	}
	if count >= int64(limit) {
		var oldest phoneUsageEventModel
		if err := tx.Where("phone_id = ? AND created_at > ?", phone.ID, windowStart).Order("created_at ASC, id ASC").Take(&oldest).Error; err != nil {
			return time.Time{}, 0, err
		}
		return time.Time{}, 0, &SMSPhoneUnavailableError{RetryAt: oldest.CreatedAt.Add(time.Hour), Reason: "hourly SMS limit reached"}
	}

	base := runtimeconfig.Duration(runtimeconfig.ICloudPhoneCooldownBaseSecondsKey, 30*time.Second, time.Second, 1)
	maximum := runtimeconfig.Duration(runtimeconfig.ICloudPhoneCooldownMaxSecondsKey, 120*time.Second, time.Second, 1)
	if maximum < base {
		maximum = base
	}
	stage := phone.SMSCooldownStage
	if phone.SMSLastUsedAt == nil || !phone.SMSLastUsedAt.After(windowStart) {
		stage = 0
	}
	if stage < ^uint8(0) {
		stage++
	}
	delay := base
	for index := uint8(1); index < stage && delay < maximum; index++ {
		if delay > maximum/2 {
			delay = maximum
		} else {
			delay *= 2
		}
	}
	if delay > maximum {
		delay = maximum
	}
	return now.Add(delay), stage, nil
}

func (s *Service) MarkSMSAttemptSent(ctx context.Context, reservationID uint64) error {
	return s.finishSMSAttempt(ctx, reservationID, SMSChallengeSent)
}

// ConfirmSMSAttemptSent clears the consecutive send-failure cycle only after
// the provider accepted the SMS request. MarkSMSAttemptSent atomically claims
// the sole send attempt before that call so crash recovery never sends twice.
func (s *Service) ConfirmSMSAttemptSent(ctx context.Context, reservationID uint64) error {
	if s == nil || s.db == nil || reservationID == 0 {
		return ErrSMSReservationNotFound
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		phone, challenge, err := lockSMSPhoneAndChallengeTx(tx, reservationID)
		if err != nil {
			return err
		}
		if err := expireSMSChallengeIfNeededTx(tx, &challenge, now); err != nil {
			return err
		}
		if challenge.Status != SMSChallengeSent {
			return ErrSMSChallengeInactive
		}
		return tx.Model(&phoneModel{}).Where("id = ?", phone.ID).Updates(map[string]any{
			"sms_consecutive_failures": 0,
			"sms_blacklisted_until":    nil,
		}).Error
	})
}

func (s *Service) MarkSMSAttemptSendFailed(ctx context.Context, reservationID uint64) error {
	return s.finishSMSAttempt(ctx, reservationID, SMSChallengeSendFailed)
}

func (s *Service) MarkSMSAttemptInfrastructureFailed(ctx context.Context, reservationID uint64) error {
	return s.finishSMSAttempt(ctx, reservationID, SMSChallengeInfrastructureFailed)
}

func (s *Service) finishSMSAttempt(ctx context.Context, challengeID uint64, result string) error {
	if s == nil || s.db == nil || challengeID == 0 {
		return ErrSMSReservationNotFound
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		phone, challenge, err := lockSMSPhoneAndChallengeTx(tx, challengeID)
		if err != nil {
			return err
		}
		if err := expireSMSChallengeIfNeededTx(tx, &challenge, now); err != nil {
			return err
		}
		if challenge.Status == result {
			if result == SMSChallengeSent {
				return ErrSMSChallengeInactive
			}
			return nil
		}
		recoveringSentFailure := challenge.Status == SMSChallengeSent &&
			(result == SMSChallengeSendFailed || result == SMSChallengeInfrastructureFailed)
		if challenge.Status != SMSChallengeReserved && !recoveringSentFailure {
			return ErrSMSChallengeInactive
		}
		if err := tx.Model(&phoneUsageEventModel{}).Where("id = ? AND result IN ?", challenge.UsageEventID, []string{SMSChallengeReserved, SMSChallengeSent}).
			Updates(map[string]any{"result": result, "resolved_at": now}).Error; err != nil {
			return err
		}
		updates := map[string]any{"status": result, "updated_at": now}
		switch result {
		case SMSChallengeSent:
			updates["sent_at"] = now
		case SMSChallengeSendFailed, SMSChallengeInfrastructureFailed:
			updates["active_phone_id"] = nil
			updates["finished_at"] = now
			if result == SMSChallengeSendFailed {
				failures := phone.SMSConsecutiveFailures + 1
				phoneUpdates := map[string]any{"sms_consecutive_failures": failures}
				threshold := uint(runtimeconfig.Int(runtimeconfig.ICloudPhoneSendFailureThresholdKey, 3, 1))
				if failures >= threshold {
					phoneUpdates["sms_blacklisted_until"] = now.Add(runtimeconfig.Duration(runtimeconfig.ICloudPhoneBlacklistHoursKey, 24*time.Hour, time.Hour, 1))
				}
				if err := tx.Model(&phoneModel{}).Where("id = ?", phone.ID).Updates(phoneUpdates).Error; err != nil {
					return err
				}
			}
		default:
			return ErrInvalidInput
		}
		return tx.Model(&smsChallengeModel{}).Where("id = ? AND status IN ?", challenge.ID, []string{SMSChallengeReserved, SMSChallengeSent}).Updates(updates).Error
	})
}

func (s *Service) CompleteSMSChallenge(ctx context.Context, challengeID uint64) error {
	return s.finishActiveSMSChallenge(ctx, challengeID, SMSChallengeCompleted)
}

func (s *Service) CancelSMSChallenge(ctx context.Context, challengeID uint64) error {
	return s.finishActiveSMSChallenge(ctx, challengeID, SMSChallengeCanceled)
}

func (s *Service) finishActiveSMSChallenge(ctx context.Context, challengeID uint64, terminal string) error {
	if s == nil || s.db == nil || challengeID == 0 {
		return ErrSMSReservationNotFound
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		phone, challenge, err := lockSMSPhoneAndChallengeTx(tx, challengeID)
		if err != nil {
			return err
		}
		if err := expireSMSChallengeIfNeededTx(tx, &challenge, now); err != nil {
			return err
		}
		if challenge.Status == terminal {
			return nil
		}
		if !smsChallengeActive(challenge.Status) {
			return ErrSMSChallengeInactive
		}
		if terminal == SMSChallengeCompleted && challenge.Status != SMSChallengeSent {
			return ErrSMSChallengeInactive
		}
		if challenge.Status == SMSChallengeReserved {
			if err := resolveReservedSMSUsageTx(tx, challenge.UsageEventID, now); err != nil {
				return err
			}
		}
		if err := tx.Model(&smsChallengeModel{}).Where("id = ? AND status IN ?", challenge.ID, []string{SMSChallengeReserved, SMSChallengeSent}).Updates(map[string]any{
			"status": terminal, "active_phone_id": nil, "finished_at": now, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		if terminal != SMSChallengeCompleted {
			return nil
		}
		return tx.Model(&phoneModel{}).Where("id = ?", phone.ID).Updates(map[string]any{
			"sms_consecutive_failures": 0,
			"sms_blacklisted_until":    nil,
		}).Error
	})
}

// Keep every transaction that mutates both rows on the phone -> challenge lock order.
func lockSMSPhoneAndChallengeTx(tx *gorm.DB, challengeID uint64) (phoneModel, smsChallengeModel, error) {
	var reference struct {
		PhoneID uint `gorm:"column:phone_id"`
	}
	if err := tx.Model(&smsChallengeModel{}).Select("phone_id").Where("id = ?", challengeID).Take(&reference).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return phoneModel{}, smsChallengeModel{}, ErrSMSReservationNotFound
		}
		return phoneModel{}, smsChallengeModel{}, err
	}
	var phone phoneModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&phone, reference.PhoneID).Error; err != nil {
		return phoneModel{}, smsChallengeModel{}, err
	}
	var challenge smsChallengeModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND phone_id = ?", challengeID, reference.PhoneID).Take(&challenge).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return phoneModel{}, smsChallengeModel{}, ErrSMSReservationNotFound
		}
		return phoneModel{}, smsChallengeModel{}, err
	}
	return phone, challenge, nil
}

func (s *Service) GetSMSChallenge(ctx context.Context, challengeID uint64) (SMSChallenge, error) {
	if s == nil || s.db == nil || challengeID == 0 {
		return SMSChallenge{}, ErrSMSReservationNotFound
	}
	return s.loadSMSChallenge(ctx, "id = ?", challengeID)
}

func (s *Service) GetSMSChallengeByOwner(ctx context.Context, ownerKey string) (SMSChallenge, error) {
	ownerKey = strings.TrimSpace(ownerKey)
	if s == nil || s.db == nil || ownerKey == "" {
		return SMSChallenge{}, ErrSMSReservationNotFound
	}
	return s.loadSMSChallenge(ctx, "owner_key = ?", ownerKey)
}

func (s *Service) loadSMSChallenge(ctx context.Context, query string, value any) (SMSChallenge, error) {
	now := s.now().UTC().Truncate(time.Millisecond)
	var result SMSChallenge
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var challenge smsChallengeModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(query, value).Take(&challenge).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSMSReservationNotFound
			}
			return err
		}
		if err := expireSMSChallengeIfNeededTx(tx, &challenge, now); err != nil {
			return err
		}
		result = smsChallenge(challenge)
		return nil
	})
	return result, err
}

func expireSMSChallengeIfNeededTx(tx *gorm.DB, challenge *smsChallengeModel, now time.Time) error {
	if challenge == nil || !smsChallengeActive(challenge.Status) || challenge.ExpiresAt.After(now) {
		return nil
	}
	if challenge.Status == SMSChallengeReserved {
		if err := resolveReservedSMSUsageTx(tx, challenge.UsageEventID, now); err != nil {
			return err
		}
	}
	if err := tx.Model(&smsChallengeModel{}).Where("id = ? AND status IN ?", challenge.ID, []string{SMSChallengeReserved, SMSChallengeSent}).Updates(map[string]any{
		"status": SMSChallengeExpired, "active_phone_id": nil, "finished_at": now, "updated_at": now,
	}).Error; err != nil {
		return err
	}
	challenge.Status = SMSChallengeExpired
	challenge.ActivePhoneID = nil
	challenge.FinishedAt = &now
	challenge.UpdatedAt = now
	return nil
}

func resolveReservedSMSUsageTx(tx *gorm.DB, usageEventID uint64, now time.Time) error {
	return tx.Model(&phoneUsageEventModel{}).Where("id = ? AND result = ?", usageEventID, SMSChallengeReserved).Updates(map[string]any{
		"result": SMSChallengeInfrastructureFailed, "resolved_at": now,
	}).Error
}

func smsInactivePhoneError(tx *gorm.DB, phoneID uint) error {
	var bindings int64
	if err := tx.Model(&phoneBindingModel{}).Where("phone_id = ?", phoneID).Count(&bindings).Error; err != nil {
		return err
	}
	if bindings > 0 {
		return ErrSMSPhoneBoundUnavailable
	}
	return ErrPhoneMissing
}

func smsChallengeActive(status string) bool {
	return status == SMSChallengeReserved || status == SMSChallengeSent
}

func smsReservation(challenge smsChallengeModel, cooldown *time.Time) SMSReservation {
	result := SMSReservation{
		ID: challenge.ID, PhoneID: challenge.PhoneID, Purpose: challenge.Purpose,
		Status: challenge.Status, ExpiresAt: challenge.ExpiresAt,
	}
	if cooldown != nil {
		result.CooldownUntil = *cooldown
	}
	return result
}

func smsChallenge(model smsChallengeModel) SMSChallenge {
	result := SMSChallenge{
		ID: model.ID, PhoneID: model.PhoneID, Purpose: model.Purpose, Status: model.Status,
		SentAt: model.SentAt, ExpiresAt: model.ExpiresAt, FinishedAt: model.FinishedAt,
		CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt,
	}
	if model.OwnerKey != nil {
		result.OwnerKey = *model.OwnerKey
	}
	if model.MessageFingerprint != nil && model.MessageCaller != nil && model.MessageContent != nil && model.MessageTime != nil {
		result.Message = &MessageItem{Caller: *model.MessageCaller, Content: *model.MessageContent, Time: *model.MessageTime}
	}
	return result
}

// ClaimAppleSMSMessage returns the same persisted message on retries. A
// message can belong to only one challenge, match a supported Apple message
// body, and arrive during the challenge's send window. Caller is not trusted.
func (s *Service) ClaimAppleSMSMessage(ctx context.Context, challengeID uint64) (*MessageItem, error) {
	challenge, err := s.GetSMSChallenge(ctx, challengeID)
	if err != nil {
		return nil, err
	}
	if challenge.Message != nil {
		return challenge.Message, nil
	}
	if challenge.Status != SMSChallengeSent {
		return nil, ErrSMSChallengeInactive
	}
	messages, err := s.FetchSMSMessages(ctx, challenge.PhoneID)
	if err != nil {
		return nil, err
	}
	return s.claimAppleSMSMessage(ctx, challengeID, messages)
}

func (s *Service) claimAppleSMSMessage(ctx context.Context, challengeID uint64, messages []MessageItem) (*MessageItem, error) {
	now := s.now().UTC().Truncate(time.Millisecond)
	var claimed *MessageItem
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var challenge smsChallengeModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&challenge, challengeID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSMSReservationNotFound
			}
			return err
		}
		if err := expireSMSChallengeIfNeededTx(tx, &challenge, now); err != nil {
			return err
		}
		if challenge.MessageFingerprint != nil && challenge.MessageCaller != nil && challenge.MessageContent != nil && challenge.MessageTime != nil {
			claimed = &MessageItem{Caller: *challenge.MessageCaller, Content: *challenge.MessageContent, Time: *challenge.MessageTime}
			return nil
		}
		if challenge.Status != SMSChallengeSent || challenge.SentAt == nil {
			return ErrSMSChallengeInactive
		}
		candidates := appleSMSMessageCandidates(messages, *challenge.SentAt, challenge.ExpiresAt)
		for _, candidate := range candidates {
			fingerprint := smsMessageFingerprint(challenge.PhoneID, candidate.item)
			var used int64
			if err := tx.Model(&smsChallengeModel{}).Where("message_fingerprint = ?", fingerprint).Count(&used).Error; err != nil {
				return err
			}
			if used != 0 {
				continue
			}
			caller, content, messageTime := candidate.item.Caller, candidate.item.Content, candidate.item.Time
			updated := tx.Model(&smsChallengeModel{}).Where("id = ? AND status = ? AND message_fingerprint IS NULL", challenge.ID, SMSChallengeSent).Updates(map[string]any{
				"message_fingerprint": fingerprint, "message_caller": caller, "message_content": content,
				"message_time": messageTime, "message_received_at": candidate.receivedAt, "updated_at": now,
			})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected == 1 {
				claimed = &MessageItem{Caller: caller, Content: content, Time: messageTime}
				return nil
			}
		}
		return ErrAppleSMSMessageNotFound
	})
	return claimed, err
}

type appleSMSMessageCandidate struct {
	item       MessageItem
	receivedAt time.Time
}

func appleSMSMessageCandidates(messages []MessageItem, sentAt, expiresAt time.Time) []appleSMSMessageCandidate {
	result := make([]appleSMSMessageCandidate, 0, len(messages))
	for _, message := range messages {
		if appleSMSCode(message.Content) == "" {
			continue
		}
		receivedAt := parseProviderTime(message.Time)
		if receivedAt == nil || receivedAt.Before(sentAt.Add(-appleSMSClockSkew)) || receivedAt.After(expiresAt.Add(appleSMSClockSkew)) {
			continue
		}
		result = append(result, appleSMSMessageCandidate{item: message, receivedAt: *receivedAt})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].receivedAt.Before(result[j].receivedAt) })
	return result
}

func appleSMSCode(content string) string {
	content = strings.TrimSpace(content)
	for _, pattern := range appleSMSCodePatterns {
		if match := pattern.FindStringSubmatch(content); len(match) == 2 {
			return match[1]
		}
	}
	return ""
}

func smsMessageFingerprint(phoneID uint, message MessageItem) string {
	payload := strings.Join([]string{
		strings.TrimSpace(message.Caller), strings.TrimSpace(message.Content), strings.TrimSpace(message.Time),
	}, "\x00")
	sum := sha256.Sum256([]byte(strconv.FormatUint(uint64(phoneID), 10) + "\x00" + payload))
	return hex.EncodeToString(sum[:])
}
