package infra

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	coredomain "github.com/donnel666/remail/internal/core/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MicrosoftAliasScheduleModel struct {
	ResourceID               uint       `gorm:"primaryKey;column:resource_id"`
	Generation               uint64     `gorm:"not null;default:1"`
	Status                   string     `gorm:"type:varchar(32);not null;default:'pending'"`
	NextRunAt                time.Time  `gorm:"not null;column:next_run_at"`
	Attempts                 int        `gorm:"not null;default:0"`
	FailureStreak            int        `gorm:"not null;default:0;column:failure_streak"`
	BlockedResourceSignature string     `gorm:"type:char(64);not null;default:'';column:blocked_resource_signature"`
	BlockedResourceUpdatedAt *time.Time `gorm:"column:blocked_resource_updated_at"`
	BlockedLastAllocatedAt   *time.Time `gorm:"column:blocked_last_allocated_at"`
	ClaimToken               string     `gorm:"type:char(32);not null;default:'';column:claim_token"`
	LastSafeError            string     `gorm:"type:varchar(500);not null;default:'';column:last_safe_error"`
	LastRunAt                *time.Time `gorm:"column:last_run_at"`
	CreatedAt                time.Time  `gorm:"not null;autoCreateTime"`
	UpdatedAt                time.Time  `gorm:"not null;autoUpdateTime"`
}

func (MicrosoftAliasScheduleModel) TableName() string {
	return "microsoft_alias_schedules"
}

type MicrosoftExplicitAliasModel struct {
	ID          uint      `gorm:"primaryKey;autoIncrement"`
	ResourceID  uint      `gorm:"column:resource_id"`
	OwnerUserID uint      `gorm:"not null;column:owner_user_id"`
	Email       string    `gorm:"type:varchar(255);not null"`
	Status      string    `gorm:"type:varchar(32);not null;default:'normal'"`
	CreatedAt   time.Time `gorm:"not null;autoCreateTime"`
	UpdatedAt   time.Time `gorm:"not null;autoUpdateTime"`
}

func (MicrosoftExplicitAliasModel) TableName() string {
	return "explicit_aliases"
}

type MicrosoftAliasAttemptModel struct {
	ID                         uint       `gorm:"primaryKey;autoIncrement"`
	ResourceID                 uint       `gorm:"not null;column:resource_id"`
	Candidate                  string     `gorm:"type:varchar(255);not null"`
	Status                     string     `gorm:"type:varchar(32);not null"`
	QuotaAt                    time.Time  `gorm:"not null;column:quota_at"`
	Category                   string     `gorm:"type:varchar(64);not null;default:''"`
	LastSafeError              string     `gorm:"type:varchar(500);not null;default:'';column:last_safe_error"`
	WasAttempted               bool       `gorm:"not null;default:false;column:was_attempted"`
	UncertainSince             *time.Time `gorm:"column:uncertain_since"`
	NegativeConfirmations      int        `gorm:"not null;default:0;column:negative_confirmations"`
	LastNegativeConfirmationAt *time.Time `gorm:"column:last_negative_confirmation_at"`
	CompletedAt                *time.Time `gorm:"column:completed_at"`
	CreatedAt                  time.Time  `gorm:"not null;autoCreateTime"`
	UpdatedAt                  time.Time  `gorm:"not null;autoUpdateTime"`
}

func (MicrosoftAliasAttemptModel) TableName() string {
	return "microsoft_alias_attempts"
}

type MicrosoftAliasStore struct {
	db                           *gorm.DB
	operationLogs                operationLogTxWriter
	recoveredBindingScanPageSize int
}

const (
	microsoftAliasScheduleEnsureBatch             = 10000
	microsoftAliasRecoveredBindingScanPageSize    = 256
	microsoftAliasDispatchEligibilityScanMax      = 512
	microsoftAliasNegativeConfirmationMinInterval = time.Hour
	legacyMicrosoftAliasPublicOnlyMessage         = "Microsoft resource is not publicly available for alias creation."

	// microsoftExplicitAliasOwnerUserID must remain 1. The first activated
	// account is the platform super administrator, and every explicit alias is
	// platform inventory owned by that account, not by a resource owner or by a
	// caller-selected user. Migration 00039 enforces the same invariant in MySQL.
	microsoftExplicitAliasOwnerUserID uint = 1
)

var errMicrosoftAliasGenerationStillRunning = errors.New("microsoft alias generation is still running")

func NewMicrosoftAliasStore(db *gorm.DB) *MicrosoftAliasStore {
	return &MicrosoftAliasStore{
		db:                           db,
		operationLogs:                governanceinfra.NewOperationLogRepo(db),
		recoveredBindingScanPageSize: microsoftAliasRecoveredBindingScanPageSize,
	}
}

func (s *MicrosoftAliasStore) EnsureSchedules(ctx context.Context, now time.Time) (int64, error) {
	paused := s.db.WithContext(ctx).Exec(`
UPDATE microsoft_alias_schedules AS schedule
JOIN (
    SELECT resource_id
    FROM (
        SELECT candidate.resource_id
        FROM microsoft_alias_schedules AS candidate
        JOIN microsoft_resources AS mr ON mr.id = candidate.resource_id
        WHERE candidate.status = 'pending'
          AND mr.status <> 'normal'
        ORDER BY candidate.resource_id
        LIMIT ?
    ) AS limited
) AS due ON due.resource_id = schedule.resource_id
JOIN microsoft_resources AS current_resource ON current_resource.id = schedule.resource_id
SET schedule.status = 'paused',
	    schedule.generation = schedule.generation + 1,
    schedule.claim_token = '',
    schedule.last_safe_error = ?,
    schedule.blocked_resource_signature = SHA2(CONCAT_WS(
        CHAR(0),
        current_resource.status,
        current_resource.email_address,
        current_resource.password,
        current_resource.client_id,
        current_resource.refresh_token,
        COALESCE((
            SELECT binding.account_email
            FROM microsoft_binding_mailboxes AS binding
            WHERE binding.resource_id = current_resource.id
              AND binding.status <> 'expired'
            LIMIT 1
        ), ''),
        COALESCE((
            SELECT binding.binding_address
            FROM microsoft_binding_mailboxes AS binding
            WHERE binding.resource_id = current_resource.id
              AND binding.status <> 'expired'
            LIMIT 1
        ), ''),
	        COALESCE((
	            SELECT binding.status
	            FROM microsoft_binding_mailboxes AS binding
	            WHERE binding.resource_id = current_resource.id
	              AND binding.status <> 'expired'
	            LIMIT 1
	        ), '')
    ), 256),
    schedule.blocked_resource_updated_at = current_resource.updated_at,
    schedule.blocked_last_allocated_at = current_resource.last_allocated_at,
    schedule.updated_at = ?
WHERE schedule.status = 'pending'
  AND current_resource.status <> 'normal'`, microsoftAliasScheduleEnsureBatch, mailapp.MicrosoftAliasResourceNotNormalMessage, now)
	if paused.Error != nil {
		return 0, fmt.Errorf("pause ineligible microsoft alias schedules: %w", paused.Error)
	}

	var pausedResourceIDs []uint
	// Migration 21 rewrites existing blocked signatures to this current shape.
	if err := s.db.WithContext(ctx).
		Table("microsoft_alias_schedules AS schedule").
		Select("schedule.resource_id").
		Joins("JOIN microsoft_resources AS mr ON mr.id = schedule.resource_id").
		Joins("LEFT JOIN microsoft_binding_mailboxes AS binding ON binding.resource_id = mr.id AND binding.status <> ?", "expired").
		Where(`schedule.status = ?
  AND mr.status = ?
	  AND (
	    schedule.last_safe_error IN (?, ?)
	    OR schedule.blocked_resource_signature <>
	      SHA2(CONCAT_WS(
        CHAR(0),
        mr.status,
        mr.email_address,
        mr.password,
        mr.client_id,
        mr.refresh_token,
        COALESCE(binding.account_email, ''),
        COALESCE(binding.binding_address, ''),
	        COALESCE(binding.status, '')
	      ), 256)
	  )`, "paused", "normal", legacyMicrosoftAliasPublicOnlyMessage, mailapp.MicrosoftAliasResourceNotNormalMessage).
		Order("schedule.resource_id ASC").
		Limit(microsoftAliasScheduleEnsureBatch).
		Pluck("schedule.resource_id", &pausedResourceIDs).Error; err != nil {
		return 0, fmt.Errorf("find paused microsoft alias schedules: %w", err)
	}
	// Binding-domain readiness is not part of the resource signature. Select
	// paused binding rows separately and apply the exact Go mailbox parser before
	// waking them; a SQL approximation would repeatedly wake malformed addresses.
	remainingWakeCapacity := microsoftAliasScheduleEnsureBatch - len(pausedResourceIDs)
	if remainingWakeCapacity > 0 {
		type recoveredBindingCandidate struct {
			ResourceID          uint   `gorm:"column:resource_id"`
			LastSafeError       string `gorm:"column:last_safe_error"`
			BindingAddress      string `gorm:"column:binding_address"`
			BindingAccountEmail string `gorm:"column:binding_account_email"`
			ResourceEmail       string `gorm:"column:resource_email"`
			BindingDomainReady  bool   `gorm:"column:binding_domain_ready"`
		}
		pageSize := s.recoveredBindingScanPageSize
		if pageSize <= 0 {
			pageSize = microsoftAliasRecoveredBindingScanPageSize
		}
		seenResourceIDs := make(map[uint]struct{}, len(pausedResourceIDs))
		for _, resourceID := range pausedResourceIDs {
			seenResourceIDs[resourceID] = struct{}{}
		}
		var afterResourceID uint
		for remainingWakeCapacity > 0 {
			var recoveredCandidates []recoveredBindingCandidate
			if err := s.db.WithContext(ctx).
				Table("microsoft_alias_schedules AS schedule").
				Select(`schedule.resource_id AS resource_id,
	schedule.last_safe_error AS last_safe_error,
	binding.binding_address AS binding_address,
	binding.account_email AS binding_account_email,
mr.email_address AS resource_email,
EXISTS (
    SELECT 1
    FROM domain_resources AS binding_domain
    WHERE binding_domain.domain = LOWER(SUBSTRING_INDEX(binding.binding_address, '@', -1))
      AND binding_domain.purpose = 'binding'
      AND binding_domain.status = 'normal'
) AS binding_domain_ready`).
				Joins("JOIN microsoft_resources AS mr ON mr.id = schedule.resource_id").
				Joins("JOIN microsoft_binding_mailboxes AS binding ON binding.resource_id = mr.id AND binding.status <> ?", "expired").
				Where(`schedule.status = ?
	  AND mr.status = ?
	  AND schedule.last_safe_error IN (?, ?)
		  AND schedule.resource_id > ?`,
					"paused",
					"normal",
					mailapp.MicrosoftAliasExternalRecoveryMessage,
					mailapp.MicrosoftAliasBindingUnresolvedMessage,
					afterResourceID,
				).
				Order("schedule.resource_id ASC").
				Limit(pageSize).
				Scan(&recoveredCandidates).Error; err != nil {
				return 0, fmt.Errorf("find recovered microsoft alias bindings: %w", err)
			}
			if len(recoveredCandidates) == 0 {
				break
			}
			afterResourceID = recoveredCandidates[len(recoveredCandidates)-1].ResourceID
			for _, candidate := range recoveredCandidates {
				bindingReady := microsoftAliasBindingReady(
					candidate.BindingAddress,
					candidate.BindingAccountEmail,
					candidate.ResourceEmail,
					candidate.BindingDomainReady,
				)
				bindingRecoverable := microsoftAliasBindingRecoverable(
					candidate.BindingAddress,
					candidate.BindingAccountEmail,
					candidate.ResourceEmail,
					candidate.BindingDomainReady,
				)
				if candidate.LastSafeError == mailapp.MicrosoftAliasExternalRecoveryMessage && !bindingRecoverable ||
					candidate.LastSafeError == mailapp.MicrosoftAliasBindingUnresolvedMessage && !bindingReady {
					continue
				}
				if _, exists := seenResourceIDs[candidate.ResourceID]; exists {
					continue
				}
				seenResourceIDs[candidate.ResourceID] = struct{}{}
				pausedResourceIDs = append(pausedResourceIDs, candidate.ResourceID)
				remainingWakeCapacity--
				if remainingWakeCapacity == 0 {
					break
				}
			}
		}
	}
	var wokenRows int64
	if len(pausedResourceIDs) > 0 {
		woken := s.db.WithContext(ctx).
			Model(&MicrosoftAliasScheduleModel{}).
			Where("resource_id IN ? AND status = ?", pausedResourceIDs, "paused").
			Updates(map[string]any{
				"status":                      "pending",
				"generation":                  gorm.Expr("generation + 1"),
				"next_run_at":                 now,
				"failure_streak":              0,
				"blocked_resource_signature":  "",
				"blocked_resource_updated_at": nil,
				"blocked_last_allocated_at":   nil,
				"last_safe_error":             "",
				"updated_at":                  now,
			})
		if woken.Error != nil {
			return 0, fmt.Errorf("wake microsoft alias schedules: %w", woken.Error)
		}
		wokenRows = woken.RowsAffected
	}

	inserted := s.db.WithContext(ctx).Exec(`
INSERT IGNORE INTO microsoft_alias_schedules (
    resource_id,
    status,
    next_run_at,
    attempts,
    last_safe_error,
    created_at,
    updated_at
)
SELECT
    mr.id,
    'pending',
    ?,
    0,
    '',
    ?,
    ?
FROM microsoft_resources AS mr
LEFT JOIN microsoft_alias_schedules AS schedule ON schedule.resource_id = mr.id
WHERE mr.status = 'normal'
  AND schedule.resource_id IS NULL
ORDER BY mr.id
LIMIT ?`, now, now, now, microsoftAliasScheduleEnsureBatch)
	if inserted.Error != nil {
		return 0, fmt.Errorf("ensure microsoft alias schedules: %w", inserted.Error)
	}
	return paused.RowsAffected + wokenRows + inserted.RowsAffected, nil
}

// EnsureScheduleForResource is the narrow counterpart to the daily broad
// scanner. It creates a missing schedule for a normal resource, or wakes a
// paused schedule only when the same eligibility facts used by administrator
// expedite show that its prior block is no longer current.
func (s *MicrosoftAliasStore) EnsureScheduleForResource(ctx context.Context, resourceID uint, now time.Time) (bool, error) {
	if resourceID == 0 {
		return false, nil
	}
	ensured := false
	persist := func(tx *gorm.DB) error {
		inserted := tx.Exec(`
INSERT IGNORE INTO microsoft_alias_schedules (
    resource_id, status, next_run_at, attempts, last_safe_error, created_at, updated_at
)
SELECT id, 'pending', ?, 0, '', ?, ?
FROM microsoft_resources
WHERE id = ? AND status = 'normal'`, now, now, now, resourceID)
		if inserted.Error != nil {
			return fmt.Errorf("ensure validated microsoft alias schedule: %w", inserted.Error)
		}
		if inserted.RowsAffected > 0 {
			ensured = true
			return nil
		}

		var state struct {
			ResourceID               uint   `gorm:"column:resource_id"`
			ResourceStatus           string `gorm:"column:resource_status"`
			ScheduleStatus           string `gorm:"column:schedule_status"`
			BlockedResourceSignature string `gorm:"column:blocked_resource_signature"`
			LastSafeError            string `gorm:"column:last_safe_error"`
			ResourceSignature        string `gorm:"column:resource_signature"`
			BindingAddress           string `gorm:"column:binding_address"`
			BindingStatus            string `gorm:"column:binding_status"`
			BindingAccountEmail      string `gorm:"column:binding_account_email"`
			ResourceEmail            string `gorm:"column:resource_email"`
			BindingDomainReady       bool   `gorm:"column:binding_domain_ready"`
		}
		if err := tx.Raw(`
SELECT
    schedule.resource_id AS resource_id,
    resource.status AS resource_status,
    schedule.status AS schedule_status,
    schedule.blocked_resource_signature AS blocked_resource_signature,
    schedule.last_safe_error AS last_safe_error,
	    COALESCE(binding.binding_address, '') AS binding_address,
	    COALESCE(binding.status, '') AS binding_status,
	    COALESCE(binding.account_email, '') AS binding_account_email,
    resource.email_address AS resource_email,
    EXISTS (
        SELECT 1
        FROM domain_resources AS binding_domain
        WHERE binding_domain.domain = LOWER(SUBSTRING_INDEX(binding.binding_address, '@', -1))
          AND binding_domain.purpose = 'binding'
          AND binding_domain.status = 'normal'
    ) AS binding_domain_ready,
    SHA2(CONCAT_WS(
        CHAR(0),
        resource.status,
        resource.email_address,
        resource.password,
        resource.client_id,
        resource.refresh_token,
	        COALESCE(binding.account_email, ''),
	        COALESCE(binding.binding_address, ''),
	        COALESCE(binding.status, '')
    ), 256) AS resource_signature
FROM microsoft_alias_schedules AS schedule
JOIN microsoft_resources AS resource ON resource.id = schedule.resource_id
LEFT JOIN microsoft_binding_mailboxes AS binding
  ON binding.resource_id = resource.id
 AND binding.status <> 'expired'
WHERE schedule.resource_id = ?
LIMIT 1
FOR UPDATE`, resourceID).Scan(&state).Error; err != nil {
			return fmt.Errorf("load validated microsoft alias schedule: %w", err)
		}
		if state.ResourceID == 0 || state.ResourceStatus != "normal" || state.ScheduleStatus != "paused" ||
			!canWakeMicrosoftAliasSchedule(
				state.BlockedResourceSignature,
				state.ResourceSignature,
				state.LastSafeError,
				microsoftAliasBindingReady(
					state.BindingAddress,
					state.BindingAccountEmail,
					state.ResourceEmail,
					state.BindingDomainReady,
				),
				microsoftAliasBindingRecoverable(
					state.BindingAddress,
					state.BindingAccountEmail,
					state.ResourceEmail,
					state.BindingDomainReady,
				),
			) {
			return nil
		}
		woken := tx.Model(&MicrosoftAliasScheduleModel{}).
			Where("resource_id = ? AND status = ?", resourceID, "paused").
			Updates(microsoftAliasScheduleWakeUpdates(now))
		if woken.Error != nil {
			return fmt.Errorf("wake validated microsoft alias schedule: %w", woken.Error)
		}
		ensured = woken.RowsAffected > 0
		return nil
	}
	var err error
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		err = persist(tx.WithContext(ctx))
	} else {
		err = s.db.WithContext(ctx).Transaction(persist)
	}
	return ensured, err
}

func canWakeMicrosoftAliasSchedule(blockedSignature, resourceSignature, lastSafeError string, bindingReady, bindingRecoverable bool) bool {
	return strings.TrimSpace(blockedSignature) == "" ||
		blockedSignature != resourceSignature ||
		lastSafeError == legacyMicrosoftAliasPublicOnlyMessage ||
		lastSafeError == mailapp.MicrosoftAliasResourceNotNormalMessage ||
		(bindingRecoverable && lastSafeError == mailapp.MicrosoftAliasExternalRecoveryMessage) ||
		(bindingReady && lastSafeError == mailapp.MicrosoftAliasBindingUnresolvedMessage)
}

func microsoftAliasBindingReady(address, bindingAccountEmail, resourceEmail string, bindingDomainReady bool) bool {
	return isConcreteMicrosoftBindingAddress(address) &&
		normalizeBindingEmail(bindingAccountEmail) != "" &&
		normalizeBindingEmail(bindingAccountEmail) == normalizeBindingEmail(resourceEmail) &&
		bindingDomainReady
}

func microsoftAliasBindingRecoverable(address, bindingAccountEmail, resourceEmail string, bindingDomainReady bool) bool {
	return (isConcreteMicrosoftBindingAddress(address) || isMaskedMicrosoftBindingAddress(address)) &&
		normalizeBindingEmail(bindingAccountEmail) != "" &&
		normalizeBindingEmail(bindingAccountEmail) == normalizeBindingEmail(resourceEmail) &&
		bindingDomainReady
}

func microsoftAliasBindingExternal(address string, bindingDomainReady bool) bool {
	if isConcreteMicrosoftBindingAddress(address) || isMaskedMicrosoftBindingAddress(address) {
		return !bindingDomainReady
	}
	return false
}

func microsoftAliasScheduleWakeUpdates(now time.Time) map[string]any {
	return map[string]any{
		"status":                      "pending",
		"generation":                  gorm.Expr("generation + 1"),
		"claim_token":                 "",
		"next_run_at":                 now,
		"failure_streak":              0,
		"blocked_resource_signature":  "",
		"blocked_resource_updated_at": nil,
		"blocked_last_allocated_at":   nil,
		"last_safe_error":             "",
		"updated_at":                  now,
	}
}

func (s *MicrosoftAliasStore) FindDispatchable(ctx context.Context, limit int, now, _ time.Time, _ time.Time) ([]mailapp.MicrosoftAliasTask, error) {
	if limit <= 0 {
		limit = 10
	}
	tasks := make([]mailapp.MicrosoftAliasTask, 0, limit)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		pending, err := lockDueMicrosoftAliasSchedules(tx, now, microsoftAliasDispatchScanLimit(limit))
		if err != nil {
			return err
		}
		states, err := loadMicrosoftAliasDispatchEligibility(tx, pending)
		if err != nil {
			return err
		}
		for _, schedule := range pending {
			state, ok := states[schedule.ResourceID]
			if !ok {
				return fmt.Errorf("load microsoft alias dispatch eligibility: resource %d is missing", schedule.ResourceID)
			}
			if safeMessage := microsoftAliasDispatchIneligibleMessage(state); safeMessage != "" {
				if err := pausePendingMicrosoftAliasSchedule(tx, schedule, state, now, safeMessage); err != nil {
					return err
				}
				continue
			}
			if len(tasks) == limit {
				break
			}
			tasks = append(tasks, mailapp.MicrosoftAliasTask{ResourceID: schedule.ResourceID, Generation: schedule.Generation})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("find microsoft alias schedules: %w", err)
	}
	return tasks, nil
}

func (s *MicrosoftAliasStore) MarkQueued(ctx context.Context, task mailapp.MicrosoftAliasTask, now time.Time) (bool, error) {
	result := s.db.WithContext(ctx).Model(&MicrosoftAliasScheduleModel{}).
		Where("resource_id = ? AND generation = ? AND status = ? AND next_run_at <= ?",
			task.ResourceID, task.Generation, "pending", now).
		Updates(map[string]any{
			"status":          "queued",
			"claim_token":     "",
			"last_safe_error": "",
			"updated_at":      now,
		})
	if result.Error != nil {
		return false, fmt.Errorf("mark microsoft alias queued: %w", result.Error)
	}
	return result.RowsAffected == 1, nil
}

type microsoftAliasDispatchEligibility struct {
	ResourceID          uint       `gorm:"column:resource_id"`
	ResourceStatus      string     `gorm:"column:resource_status"`
	ResourceEmail       string     `gorm:"column:resource_email"`
	ResourceSignature   string     `gorm:"column:resource_signature"`
	ResourceUpdatedAt   time.Time  `gorm:"column:resource_updated_at"`
	LastAllocatedAt     *time.Time `gorm:"column:last_allocated_at"`
	BindingAddress      string     `gorm:"column:binding_address"`
	BindingStatus       string     `gorm:"column:binding_status"`
	BindingAccountEmail string     `gorm:"column:binding_account_email"`
	BindingDomainReady  bool       `gorm:"column:binding_domain_ready"`
}

func microsoftAliasDispatchScanLimit(remaining int) int {
	limit := remaining * 4
	if limit < 64 {
		limit = 64
	}
	return min(limit, microsoftAliasDispatchEligibilityScanMax)
}

func lockDueMicrosoftAliasSchedules(tx *gorm.DB, now time.Time, limit int) ([]MicrosoftAliasScheduleModel, error) {
	if limit <= 0 {
		return nil, nil
	}
	var schedules []MicrosoftAliasScheduleModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
		Where("status = ? AND next_run_at <= ?", "pending", now).
		Order("next_run_at ASC, resource_id ASC").
		Limit(limit).
		Find(&schedules).Error; err != nil {
		return nil, fmt.Errorf("lock pending microsoft alias schedules: %w", err)
	}
	return schedules, nil
}

func loadMicrosoftAliasDispatchEligibility(tx *gorm.DB, schedules []MicrosoftAliasScheduleModel) (map[uint]microsoftAliasDispatchEligibility, error) {
	result := make(map[uint]microsoftAliasDispatchEligibility, len(schedules))
	if len(schedules) == 0 {
		return result, nil
	}
	resourceIDs := make([]uint, 0, len(schedules))
	for _, schedule := range schedules {
		resourceIDs = append(resourceIDs, schedule.ResourceID)
	}
	var rows []microsoftAliasDispatchEligibility
	if err := tx.Table("microsoft_resources AS resource").
		Select(`resource.id AS resource_id,
resource.status AS resource_status,
resource.email_address AS resource_email,
resource.updated_at AS resource_updated_at,
resource.last_allocated_at AS last_allocated_at,
	COALESCE(binding.binding_address, '') AS binding_address,
	COALESCE(binding.status, '') AS binding_status,
	COALESCE(binding.account_email, '') AS binding_account_email,
EXISTS (
    SELECT 1
    FROM domain_resources AS binding_domain
    WHERE binding_domain.domain = LOWER(SUBSTRING_INDEX(binding.binding_address, '@', -1))
      AND binding_domain.purpose = 'binding'
      AND binding_domain.status = 'normal'
) AS binding_domain_ready,
SHA2(CONCAT_WS(
    CHAR(0),
    resource.status,
    resource.email_address,
    resource.password,
    resource.client_id,
    resource.refresh_token,
	    COALESCE(binding.account_email, ''),
	    COALESCE(binding.binding_address, ''),
	    COALESCE(binding.status, '')
), 256) AS resource_signature`).
		Joins("LEFT JOIN microsoft_binding_mailboxes AS binding ON binding.resource_id = resource.id AND binding.status <> ?", "expired").
		Where("resource.id IN ?", resourceIDs).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load microsoft alias dispatch eligibility: %w", err)
	}
	for _, row := range rows {
		result[row.ResourceID] = row
	}
	return result, nil
}

func microsoftAliasDispatchIneligibleMessage(state microsoftAliasDispatchEligibility) string {
	if state.ResourceStatus != "normal" {
		return mailapp.MicrosoftAliasResourceNotNormalMessage
	}
	if microsoftAliasBindingExternal(state.BindingAddress, state.BindingDomainReady) {
		return mailapp.MicrosoftAliasExternalRecoveryMessage
	}
	return ""
}

func pausePendingMicrosoftAliasSchedule(
	tx *gorm.DB,
	schedule MicrosoftAliasScheduleModel,
	state microsoftAliasDispatchEligibility,
	now time.Time,
	safeMessage string,
) error {
	updated := tx.Model(&MicrosoftAliasScheduleModel{}).
		Where("resource_id = ? AND status = ? AND claim_token = ?", schedule.ResourceID, "pending", schedule.ClaimToken).
		Updates(map[string]any{
			"status":                      "paused",
			"generation":                  gorm.Expr("generation + 1"),
			"claim_token":                 "",
			"last_safe_error":             safeAliasStoreMessage(safeMessage),
			"blocked_resource_signature":  state.ResourceSignature,
			"blocked_resource_updated_at": state.ResourceUpdatedAt,
			"blocked_last_allocated_at":   state.LastAllocatedAt,
			"updated_at":                  now,
		})
	if updated.Error != nil {
		return fmt.Errorf("pause ineligible microsoft alias dispatch candidate: %w", updated.Error)
	}
	return nil
}

func (s *MicrosoftAliasStore) Claim(ctx context.Context, task mailapp.MicrosoftAliasTask, now time.Time) (*mailapp.MicrosoftAliasAccount, bool, error) {
	var account *mailapp.MicrosoftAliasAccount
	claimed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var schedule MicrosoftAliasScheduleModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("resource_id = ? AND generation = ?", task.ResourceID, task.Generation).
			First(&schedule).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock microsoft alias schedule: %w", err)
		}
		switch schedule.Status {
		case "queued":
		case "running":
			return errMicrosoftAliasGenerationStillRunning
		default:
			return nil
		}
		claimToken, err := newMicrosoftAliasClaimToken()
		if err != nil {
			return err
		}

		var row struct {
			ResourceID         uint       `gorm:"column:resource_id"`
			EmailAddress       string     `gorm:"column:email_address"`
			Password           string     `gorm:"column:password"`
			BindingAddress     string     `gorm:"column:binding_address"`
			BindingStatus      string     `gorm:"column:binding_status"`
			BindingAccount     string     `gorm:"column:binding_account_email"`
			BindingDomainReady bool       `gorm:"column:binding_domain_ready"`
			ResourceStatus     string     `gorm:"column:resource_status"`
			ResourceSignature  string     `gorm:"column:resource_signature"`
			ResourceUpdatedAt  time.Time  `gorm:"column:resource_updated_at"`
			LastAllocatedAt    *time.Time `gorm:"column:last_allocated_at"`
		}
		if err := tx.Raw(`
SELECT
    mr.id AS resource_id,
    mr.email_address AS email_address,
    mr.password AS password,
	    COALESCE(binding.binding_address, '') AS binding_address,
	    COALESCE(binding.status, '') AS binding_status,
	    COALESCE(binding.account_email, '') AS binding_account_email,
    EXISTS (
        SELECT 1
        FROM domain_resources AS binding_domain
        WHERE binding_domain.domain = LOWER(SUBSTRING_INDEX(binding.binding_address, '@', -1))
          AND binding_domain.purpose = 'binding'
          AND binding_domain.status = 'normal'
    ) AS binding_domain_ready,
    mr.status AS resource_status,
    mr.updated_at AS resource_updated_at,
    mr.last_allocated_at AS last_allocated_at,
    SHA2(CONCAT_WS(
        CHAR(0),
        mr.status,
        mr.email_address,
        mr.password,
        mr.client_id,
        mr.refresh_token,
	        COALESCE(binding.account_email, ''),
	        COALESCE(binding.binding_address, ''),
	        COALESCE(binding.status, '')
    ), 256) AS resource_signature
FROM microsoft_resources AS mr
LEFT JOIN microsoft_binding_mailboxes AS binding
  ON binding.resource_id = mr.id
 AND binding.status <> 'expired'
WHERE mr.id = ?
LIMIT 1`, task.ResourceID).Scan(&row).Error; err != nil {
			return fmt.Errorf("load microsoft alias account: %w", err)
		}
		if row.ResourceID == 0 {
			return fmt.Errorf("microsoft alias resource not found")
		}
		if row.ResourceStatus != "normal" {
			result := tx.Model(&MicrosoftAliasScheduleModel{}).
				Where("resource_id = ? AND generation = ? AND status = ?", task.ResourceID, task.Generation, "queued").
				Updates(map[string]any{
					"status":                      "paused",
					"generation":                  gorm.Expr("generation + 1"),
					"claim_token":                 "",
					"last_safe_error":             mailapp.MicrosoftAliasResourceNotNormalMessage,
					"blocked_resource_signature":  row.ResourceSignature,
					"blocked_resource_updated_at": row.ResourceUpdatedAt,
					"blocked_last_allocated_at":   row.LastAllocatedAt,
					"updated_at":                  now,
				})
			if result.Error != nil {
				return fmt.Errorf("pause ineligible microsoft alias schedule: %w", result.Error)
			}
			return nil
		}
		if microsoftAliasBindingExternal(row.BindingAddress, row.BindingDomainReady) {
			result := tx.Model(&MicrosoftAliasScheduleModel{}).
				Where("resource_id = ? AND generation = ? AND status = ?", task.ResourceID, task.Generation, "queued").
				Updates(map[string]any{
					"status":                      "paused",
					"generation":                  gorm.Expr("generation + 1"),
					"claim_token":                 "",
					"last_safe_error":             mailapp.MicrosoftAliasExternalRecoveryMessage,
					"blocked_resource_signature":  row.ResourceSignature,
					"blocked_resource_updated_at": row.ResourceUpdatedAt,
					"blocked_last_allocated_at":   row.LastAllocatedAt,
					"updated_at":                  now,
				})
			if result.Error != nil {
				return fmt.Errorf("pause external-recovery microsoft alias schedule: %w", result.Error)
			}
			return nil
		}
		result := tx.Model(&MicrosoftAliasScheduleModel{}).
			Where("resource_id = ? AND generation = ? AND status = ?", task.ResourceID, task.Generation, "queued").
			Updates(map[string]any{
				"status":                      "running",
				"claim_token":                 claimToken,
				"attempts":                    gorm.Expr("attempts + 1"),
				"last_safe_error":             "",
				"blocked_resource_signature":  row.ResourceSignature,
				"blocked_resource_updated_at": row.ResourceUpdatedAt,
				"blocked_last_allocated_at":   row.LastAllocatedAt,
				"last_run_at":                 now,
				"updated_at":                  now,
			})
		if result.Error != nil {
			return fmt.Errorf("mark microsoft alias schedule running: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return nil
		}
		account = &mailapp.MicrosoftAliasAccount{
			ResourceID:     row.ResourceID,
			EmailAddress:   row.EmailAddress,
			Password:       row.Password,
			BindingAddress: row.BindingAddress,
			ResourceStatus: row.ResourceStatus,
			FailureStreak:  schedule.FailureStreak,
			ClaimToken:     claimToken,
		}
		claimed = true
		return nil
	})
	return account, claimed, err
}

func (s *MicrosoftAliasStore) ReloadEligibleAccount(ctx context.Context, resourceID uint, claimToken string) (*mailapp.MicrosoftAliasAccount, bool, string, error) {
	var row struct {
		ResourceID         uint   `gorm:"column:resource_id"`
		ResourceStatus     string `gorm:"column:resource_status"`
		ResourceEmail      string `gorm:"column:resource_email"`
		Password           string `gorm:"column:password"`
		BindingAddress     string `gorm:"column:binding_address"`
		BindingStatus      string `gorm:"column:binding_status"`
		BindingAccount     string `gorm:"column:binding_account_email"`
		BindingDomainReady bool   `gorm:"column:binding_domain_ready"`
		FailureStreak      int    `gorm:"column:failure_streak"`
	}
	if err := s.db.WithContext(ctx).Raw(`
SELECT
    schedule.resource_id AS resource_id,
    schedule.failure_streak AS failure_streak,
    mr.status AS resource_status,
    mr.email_address AS resource_email,
    mr.password AS password,
	    COALESCE(binding.binding_address, '') AS binding_address,
	    COALESCE(binding.status, '') AS binding_status,
	    COALESCE(binding.account_email, '') AS binding_account_email,
    EXISTS (
        SELECT 1
        FROM domain_resources AS binding_domain
        WHERE binding_domain.domain = LOWER(SUBSTRING_INDEX(binding.binding_address, '@', -1))
          AND binding_domain.purpose = 'binding'
          AND binding_domain.status = 'normal'
    ) AS binding_domain_ready
FROM microsoft_alias_schedules AS schedule
JOIN microsoft_resources AS mr ON mr.id = schedule.resource_id
LEFT JOIN microsoft_binding_mailboxes AS binding
  ON binding.resource_id = mr.id
 AND binding.status <> 'expired'
WHERE schedule.resource_id = ?
  AND schedule.status = 'running'
  AND schedule.claim_token = ?
LIMIT 1`, resourceID, claimToken).Scan(&row).Error; err != nil {
		return nil, false, "", fmt.Errorf("load microsoft alias eligibility: %w", err)
	}
	if row.ResourceID == 0 {
		return nil, false, "", mailapp.ErrMicrosoftAliasStaleClaim
	}
	if row.ResourceStatus != "normal" {
		return nil, false, mailapp.MicrosoftAliasResourceNotNormalMessage, nil
	}
	if microsoftAliasBindingExternal(row.BindingAddress, row.BindingDomainReady) {
		return nil, false, mailapp.MicrosoftAliasExternalRecoveryMessage, nil
	}
	if !microsoftAliasBindingReady(
		row.BindingAddress,
		row.BindingAccount,
		row.ResourceEmail,
		row.BindingDomainReady,
	) {
		return nil, false, mailapp.MicrosoftAliasBindingUnresolvedMessage, nil
	}
	return &mailapp.MicrosoftAliasAccount{
		ResourceID:     row.ResourceID,
		EmailAddress:   row.ResourceEmail,
		Password:       row.Password,
		BindingAddress: row.BindingAddress,
		ResourceStatus: row.ResourceStatus,
		FailureStreak:  row.FailureStreak,
		ClaimToken:     claimToken,
	}, true, "", nil
}

func (s *MicrosoftAliasStore) SaveBindingAddress(ctx context.Context, resourceID uint, claimToken, expectedAddress, bindingAddress string) error {
	expectedAddress = normalizeBindingEmail(expectedAddress)
	bindingAddress = normalizeBindingEmail(bindingAddress)
	concreteAddress := isConcreteMicrosoftBindingAddress(bindingAddress)
	maskedAddress := isMaskedMicrosoftBindingAddress(bindingAddress)
	if resourceID == 0 || strings.TrimSpace(claimToken) == "" || (!concreteAddress && !maskedAddress) {
		return mailapp.ErrMicrosoftAliasStaleClaim
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var schedule MicrosoftAliasScheduleModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("resource_id = ? AND status = ? AND claim_token = ?", resourceID, "running", claimToken).
			Take(&schedule).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return mailapp.ErrMicrosoftAliasStaleClaim
		} else if err != nil {
			return fmt.Errorf("lock microsoft alias binding schedule: %w", err)
		}

		var resource struct {
			EmailAddress string `gorm:"column:email_address"`
			OwnerUserID  uint   `gorm:"column:owner_user_id"`
			Status       string `gorm:"column:status"`
		}
		if err := tx.Table("microsoft_resources AS mr").
			Select("mr.email_address, er.owner_user_id, mr.status").
			Joins("JOIN email_resources AS er ON er.id = mr.id AND er.type = 'microsoft'").
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("mr.id = ?", resourceID).
			Take(&resource).Error; err != nil {
			return fmt.Errorf("lock microsoft alias binding resource: %w", err)
		}
		if resource.Status != "normal" {
			return mailapp.ErrMicrosoftAliasStaleClaim
		}

		var current MicrosoftBindingMailboxModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("resource_id = ?", resourceID).Take(&current).Error
		currentExists := err == nil
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("lock microsoft alias binding: %w", err)
		}
		currentAddress := ""
		if currentExists {
			if current.Status != string(maildomain.MicrosoftBindingExpired) {
				currentAddress = normalizeBindingEmail(current.BindingAddress)
			}
		}
		if currentAddress != expectedAddress {
			return mailapp.ErrMicrosoftAliasStaleClaim
		}
		verifiedAddress := false
		if concreteAddress {
			err := lockActiveBindingDomainForRecoveryTx(tx, bindingAddress)
			if err == nil {
				verifiedAddress = true
			} else if !errors.Is(err, ErrMicrosoftBindingRecoveryIneligible) {
				return err
			}
		}
		if currentExists && microsoftAliasBindingAddressAlreadySaved(
			current,
			resource.OwnerUserID,
			resource.EmailAddress,
			bindingAddress,
			verifiedAddress,
		) {
			return nil
		}
		if verifiedAddress {
			var occupied struct {
				ResourceID uint `gorm:"column:resource_id"`
			}
			err = tx.Table("microsoft_binding_mailboxes").Select("resource_id").
				Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("active_binding_address = ?", bindingAddress).Take(&occupied).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("lock microsoft alias binding address: %w", err)
			}
			if err == nil && occupied.ResourceID != resourceID {
				return ErrMicrosoftBindingAddressOccupied
			}
		}

		now := time.Now().UTC()
		bindingStatus := maildomain.MicrosoftBindingFailed
		category := bindingStatusCategory(bindingStatus)
		var verifiedAt *time.Time
		if verifiedAddress {
			bindingStatus = maildomain.MicrosoftBindingVerified
			category = ""
			verifiedAt = &now
		}
		if currentExists {
			updated := tx.Model(&MicrosoftBindingMailboxModel{}).Where("id = ?", current.ID).Updates(map[string]any{
				"resource_type":   "microsoft",
				"owner_user_id":   resource.OwnerUserID,
				"account_email":   normalizeBindingEmail(resource.EmailAddress),
				"binding_address": bindingAddress,
				"purpose":         "validation",
				"status":          string(bindingStatus),
				"code_msg_id":     "",
				"category":        category,
				"last_safe_error": "",
				"selected_at":     now,
				"code_sent_at":    nil,
				"verified_at":     verifiedAt,
				"expires_at":      nil,
				"updated_at":      now,
			})
			if updated.Error != nil {
				return fmt.Errorf("update microsoft alias binding address: %w", updated.Error)
			}
		} else {
			if err := tx.Create(&MicrosoftBindingMailboxModel{
				ResourceID: resourceID, ResourceType: "microsoft", OwnerUserID: resource.OwnerUserID,
				AccountEmail: normalizeBindingEmail(resource.EmailAddress), BindingAddress: bindingAddress,
				Purpose: "validation", Status: string(bindingStatus), Category: category, SelectedAt: &now, VerifiedAt: verifiedAt,
			}).Error; err != nil {
				return fmt.Errorf("create microsoft alias binding address: %w", err)
			}
		}
		if err := tx.Table("email_resources").Where("id = ? AND type = ?", resourceID, "microsoft").Updates(map[string]any{
			"version":    gorm.Expr("version + 1"),
			"updated_at": now,
		}).Error; err != nil {
			return fmt.Errorf("advance microsoft alias binding resource version: %w", err)
		}
		return nil
	})
}

func microsoftAliasBindingAddressAlreadySaved(current MicrosoftBindingMailboxModel, ownerUserID uint, accountEmail, bindingAddress string, verified bool) bool {
	if current.ResourceType != "microsoft" || current.OwnerUserID != ownerUserID ||
		normalizeBindingEmail(current.AccountEmail) != normalizeBindingEmail(accountEmail) ||
		normalizeBindingEmail(current.BindingAddress) != bindingAddress ||
		current.Purpose != "validation" || current.CodeMessageID != "" ||
		current.LastSafeError != "" || current.SelectedAt == nil ||
		current.CodeSentAt != nil || current.ExpiresAt != nil {
		return false
	}
	if verified {
		return current.Status == string(maildomain.MicrosoftBindingVerified) &&
			current.Category == "" && current.VerifiedAt != nil
	}
	return current.Status == string(maildomain.MicrosoftBindingFailed) &&
		current.Category == bindingStatusCategory(maildomain.MicrosoftBindingFailed) &&
		current.VerifiedAt == nil
}

func (s *MicrosoftAliasStore) CheckEligibility(ctx context.Context, resourceID uint, claimToken string) (bool, string, error) {
	_, eligible, safeMessage, err := s.ReloadEligibleAccount(ctx, resourceID, claimToken)
	return eligible, safeMessage, err
}

func (s *MicrosoftAliasStore) Usage(ctx context.Context, resourceID uint, yearStart, yearEnd, weekStart, weekEnd time.Time) (mailapp.MicrosoftAliasUsage, error) {
	usage, err := loadMicrosoftAliasUsage(s.db.WithContext(ctx), resourceID, yearStart, yearEnd, weekStart, weekEnd)
	if err != nil {
		return mailapp.MicrosoftAliasUsage{}, fmt.Errorf("count microsoft alias quota: %w", err)
	}
	return usage, nil
}

func (s *MicrosoftAliasStore) Reserve(
	ctx context.Context,
	resourceID uint,
	claimToken string,
	candidates []string,
	yearStart, yearEnd, weekStart, weekEnd, now time.Time,
) ([]mailapp.MicrosoftAliasAttempt, mailapp.MicrosoftAliasUsage, error) {
	var attempts []mailapp.MicrosoftAliasAttempt
	var usage mailapp.MicrosoftAliasUsage
	err := withAliasDeadlockRetry(ctx, s.db, func(tx *gorm.DB) error {
		// The transaction can run more than once (deadlock retry); reset the
		// captured accumulators so a retry does not append onto a partial result.
		attempts = attempts[:0]
		usage = mailapp.MicrosoftAliasUsage{}
		var schedule MicrosoftAliasScheduleModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("resource_id = ? AND status = ? AND claim_token = ?", resourceID, "running", claimToken).
			First(&schedule).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return mailapp.ErrMicrosoftAliasStaleClaim
		} else if err != nil {
			return fmt.Errorf("lock microsoft alias reservation: %w", err)
		}

		var outstanding []MicrosoftAliasAttemptModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("resource_id = ? AND status = ?", resourceID, mailapp.MicrosoftAliasAttemptUncertain).
			Order("id ASC").
			Find(&outstanding).Error; err != nil {
			return fmt.Errorf("load uncertain microsoft alias attempts: %w", err)
		}
		if len(outstanding) > 0 {
			ids := make([]uint, 0, len(outstanding))
			for _, attempt := range outstanding {
				ids = append(ids, attempt.ID)
				attempts = append(attempts, mailapp.MicrosoftAliasAttempt{
					ID:                         attempt.ID,
					Alias:                      attempt.Candidate,
					Status:                     mailapp.MicrosoftAliasAttemptRunning,
					WasUncertain:               true,
					WasAttempted:               attempt.WasAttempted,
					UncertainSince:             attempt.UncertainSince,
					NegativeConfirmations:      attempt.NegativeConfirmations,
					LastNegativeConfirmationAt: attempt.LastNegativeConfirmationAt,
				})
			}
			if err := tx.Model(&MicrosoftAliasAttemptModel{}).
				Where("id IN ?", ids).
				Updates(map[string]any{
					"status":          mailapp.MicrosoftAliasAttemptRunning,
					"category":        "",
					"last_safe_error": "",
					"updated_at":      now,
				}).Error; err != nil {
				return fmt.Errorf("resume uncertain microsoft alias attempts: %w", err)
			}
			var err error
			usage, err = loadMicrosoftAliasUsage(tx, resourceID, yearStart, yearEnd, weekStart, weekEnd)
			return err
		}

		var err error
		usage, err = loadMicrosoftAliasUsage(tx, resourceID, yearStart, yearEnd, weekStart, weekEnd)
		if err != nil {
			return err
		}
		remaining := minAliasQuota(
			runtimeconfig.Int("microsoft_alias_weekly_limit", mailapp.MicrosoftAliasWeeklyLimit, 1)-usage.WeekCount,
			runtimeconfig.Int("microsoft_alias_yearly_limit", mailapp.MicrosoftAliasYearlyLimit, 1)-usage.YearCount,
		)
		if remaining <= 0 {
			return nil
		}

		var reusable []MicrosoftAliasAttemptModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("resource_id = ? AND status = ? AND was_attempted = ?", resourceID, mailapp.MicrosoftAliasAttemptFailed, false).
			Order("id ASC").
			Limit(remaining).
			Find(&reusable).Error; err != nil {
			return fmt.Errorf("load reusable microsoft alias attempts: %w", err)
		}
		for _, attempt := range reusable {
			result := tx.Model(&MicrosoftAliasAttemptModel{}).
				Where("id = ? AND status = ? AND was_attempted = ?", attempt.ID, mailapp.MicrosoftAliasAttemptFailed, false).
				Updates(map[string]any{
					"status":                        mailapp.MicrosoftAliasAttemptRunning,
					"quota_at":                      now,
					"category":                      "",
					"last_safe_error":               "",
					"uncertain_since":               nil,
					"negative_confirmations":        0,
					"last_negative_confirmation_at": nil,
					"completed_at":                  nil,
					"updated_at":                    now,
				})
			if result.Error != nil {
				return fmt.Errorf("reuse microsoft alias candidate: %w", result.Error)
			}
			if result.RowsAffected == 0 {
				continue
			}
			attempts = append(attempts, mailapp.MicrosoftAliasAttempt{
				ID:     attempt.ID,
				Alias:  attempt.Candidate,
				Status: mailapp.MicrosoftAliasAttemptRunning,
			})
		}
		remaining -= len(attempts)
		if remaining <= 0 {
			usage.YearCount += len(attempts)
			usage.WeekCount += len(attempts)
			return nil
		}
		candidates = normalizeAliasRows(candidates)
		if len(candidates) > remaining {
			candidates = candidates[:remaining]
		}
		for _, candidate := range candidates {
			model := MicrosoftAliasAttemptModel{
				ResourceID: resourceID,
				Candidate:  candidate,
				Status:     mailapp.MicrosoftAliasAttemptRunning,
				QuotaAt:    now,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			result := tx.Create(&model)
			if result.Error != nil {
				if isDuplicateKeyError(result.Error) {
					continue
				}
				return fmt.Errorf("reserve microsoft alias candidate: %w", result.Error)
			}
			if result.RowsAffected == 0 {
				continue
			}
			attempts = append(attempts, mailapp.MicrosoftAliasAttempt{
				ID:     model.ID,
				Alias:  model.Candidate,
				Status: model.Status,
			})
		}
		usage.YearCount += len(attempts)
		usage.WeekCount += len(attempts)
		return nil
	})
	return attempts, usage, err
}

func (s *MicrosoftAliasStore) Complete(ctx context.Context, resourceID uint, claimToken string, outcomes []mailapp.MicrosoftAliasAttemptOutcome, completedAt time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var schedule MicrosoftAliasScheduleModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("resource_id = ? AND status = ? AND claim_token = ?", resourceID, "running", claimToken).
			First(&schedule).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return mailapp.ErrMicrosoftAliasStaleClaim
		} else if err != nil {
			return fmt.Errorf("lock microsoft alias completion: %w", err)
		}
		var runningCount int64
		if err := tx.Model(&MicrosoftAliasAttemptModel{}).
			Where("resource_id = ? AND status = ?", resourceID, mailapp.MicrosoftAliasAttemptRunning).
			Count(&runningCount).Error; err != nil {
			return fmt.Errorf("count running microsoft alias attempts: %w", err)
		}
		if runningCount != int64(len(outcomes)) {
			return fmt.Errorf("microsoft alias completion count mismatch: running=%d outcomes=%d", runningCount, len(outcomes))
		}
		ownerUserID := uint(0)
		for _, outcome := range outcomes {
			if normalizeMicrosoftAliasAttemptStatus(outcome.Status) == mailapp.MicrosoftAliasAttemptSucceeded {
				var err error
				ownerUserID, err = lockMicrosoftExplicitAliasOwner(tx)
				if err != nil {
					return err
				}
				break
			}
		}
		for _, outcome := range outcomes {
			var attempt MicrosoftAliasAttemptModel
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND resource_id = ? AND status = ?", outcome.AttemptID, resourceID, mailapp.MicrosoftAliasAttemptRunning).
				First(&attempt).Error; err != nil {
				return fmt.Errorf("lock microsoft alias attempt: %w", err)
			}
			status := normalizeMicrosoftAliasAttemptStatus(outcome.Status)
			updates := map[string]any{
				"status":          status,
				"category":        strings.TrimSpace(outcome.Category),
				"last_safe_error": safeAliasStoreMessage(outcome.SafeMessage),
				"updated_at":      completedAt,
			}
			wasAttempted := attempt.WasAttempted || outcome.Attempted
			updates["was_attempted"] = wasAttempted
			if status == mailapp.MicrosoftAliasAttemptUncertain {
				updates["completed_at"] = nil
				if outcome.Attempted {
					updates["uncertain_since"] = completedAt
					updates["negative_confirmations"] = 0
					updates["last_negative_confirmation_at"] = nil
				} else {
					if attempt.UncertainSince == nil {
						updates["uncertain_since"] = completedAt
					}
					if outcome.ReconciledAbsent && negativeAliasConfirmationIsDue(attempt.LastNegativeConfirmationAt, completedAt) {
						updates["negative_confirmations"] = attempt.NegativeConfirmations + 1
						updates["last_negative_confirmation_at"] = completedAt
					}
				}
			} else {
				updates["completed_at"] = completedAt
				if outcome.Attempted || status == mailapp.MicrosoftAliasAttemptSucceeded || !wasAttempted {
					updates["uncertain_since"] = nil
					updates["negative_confirmations"] = 0
					updates["last_negative_confirmation_at"] = nil
				} else if outcome.ReconciledAbsent && negativeAliasConfirmationIsDue(attempt.LastNegativeConfirmationAt, completedAt) {
					updates["negative_confirmations"] = attempt.NegativeConfirmations + 1
					updates["last_negative_confirmation_at"] = completedAt
				}
			}
			if status == mailapp.MicrosoftAliasAttemptSucceeded {
				if outcome.Attempted || !attempt.WasAttempted {
					updates["quota_at"] = completedAt
				}
			}
			if err := tx.Model(&MicrosoftAliasAttemptModel{}).
				Where("id = ?", attempt.ID).
				Updates(updates).Error; err != nil {
				return fmt.Errorf("update microsoft alias attempt: %w", err)
			}
			if status == mailapp.MicrosoftAliasAttemptSucceeded {
				alias := MicrosoftExplicitAliasModel{
					ResourceID:  resourceID,
					OwnerUserID: ownerUserID,
					Email:       attempt.Candidate,
					Status:      "normal",
					CreatedAt:   completedAt,
					UpdatedAt:   completedAt,
				}
				if err := tx.Clauses(clause.OnConflict{
					Columns: []clause.Column{{Name: "resource_id"}, {Name: "email"}},
					DoUpdates: clause.Assignments(map[string]any{
						"owner_user_id": ownerUserID,
					}),
				}).Create(&alias).Error; err != nil {
					return fmt.Errorf("insert confirmed microsoft explicit alias: %w", err)
				}
			}
		}
		result := tx.Model(&MicrosoftAliasScheduleModel{}).
			Where("resource_id = ? AND status = ? AND claim_token = ?", resourceID, "running", claimToken).
			Update("updated_at", completedAt)
		if result.Error != nil {
			return result.Error
		}
		return nil
	})
}

func lockMicrosoftExplicitAliasOwner(tx *gorm.DB) (uint, error) {
	var owner struct {
		ID uint `gorm:"column:id"`
	}
	if err := tx.Raw(`
SELECT id
FROM users
WHERE id = ?
  AND role = 'super_admin'
LIMIT 1
FOR SHARE`, microsoftExplicitAliasOwnerUserID).Scan(&owner).Error; err != nil {
		return 0, fmt.Errorf("lock microsoft alias super administrator owner: %w", err)
	}
	if owner.ID != microsoftExplicitAliasOwnerUserID {
		return 0, mailapp.ErrMicrosoftAliasOwnerUnavailable
	}
	return owner.ID, nil
}

func (s *MicrosoftAliasStore) Defer(ctx context.Context, resourceID uint, claimToken string, nextRunAt time.Time, safeError string, failed bool) error {
	if err := updateMicrosoftAliasScheduleTx(s.db.WithContext(ctx), resourceID, claimToken, "pending", nextRunAt, safeError, &failed); err != nil {
		return fmt.Errorf("defer microsoft alias schedule: %w", err)
	}
	return nil
}

func (s *MicrosoftAliasStore) Pause(ctx context.Context, resourceID uint, claimToken string, safeError string) error {
	safeError = safeAliasStoreMessage(safeError)
	if safeError == mailapp.MicrosoftAliasBindingUnresolvedMessage || safeError == mailapp.MicrosoftAliasExternalRecoveryMessage {
		if err := pauseMicrosoftAliasScheduleWithCurrentSignature(s.db.WithContext(ctx), resourceID, claimToken, safeError); err != nil {
			return fmt.Errorf("pause microsoft alias schedule: %w", err)
		}
		return nil
	}
	if err := updateMicrosoftAliasScheduleTx(s.db.WithContext(ctx), resourceID, claimToken, "paused", time.Now().UTC(), safeError, nil); err != nil {
		return fmt.Errorf("pause microsoft alias schedule: %w", err)
	}
	return nil
}

func pauseMicrosoftAliasScheduleWithCurrentSignature(db *gorm.DB, resourceID uint, claimToken, safeError string) error {
	now := time.Now().UTC()
	return db.Transaction(func(tx *gorm.DB) error {
		var resource struct {
			ID              uint       `gorm:"column:id"`
			Signature       string     `gorm:"column:resource_signature"`
			UpdatedAt       time.Time  `gorm:"column:updated_at"`
			LastAllocatedAt *time.Time `gorm:"column:last_allocated_at"`
		}
		if err := tx.Raw(`
SELECT
    resource.id AS id,
    resource.updated_at AS updated_at,
    resource.last_allocated_at AS last_allocated_at,
    SHA2(CONCAT_WS(
        CHAR(0),
        resource.status,
        resource.email_address,
        resource.password,
        resource.client_id,
        resource.refresh_token,
	        COALESCE(binding.account_email, ''),
	        COALESCE(binding.binding_address, ''),
	        COALESCE(binding.status, '')
    ), 256) AS resource_signature
FROM microsoft_resources AS resource
LEFT JOIN microsoft_binding_mailboxes AS binding
  ON binding.resource_id = resource.id
 AND binding.status <> 'expired'
WHERE resource.id = ?
LIMIT 1
FOR SHARE`, resourceID).Scan(&resource).Error; err != nil {
			return fmt.Errorf("lock microsoft alias resource signature: %w", err)
		}
		if resource.ID == 0 {
			return mailapp.ErrMicrosoftAliasStaleClaim
		}
		result := tx.Model(&MicrosoftAliasScheduleModel{}).
			Where("resource_id = ? AND status = ? AND claim_token = ?", resourceID, "running", claimToken).
			Updates(map[string]any{
				"status":                      "paused",
				"generation":                  gorm.Expr("generation + 1"),
				"claim_token":                 "",
				"next_run_at":                 now,
				"last_safe_error":             safeError,
				"blocked_resource_signature":  resource.Signature,
				"blocked_resource_updated_at": resource.UpdatedAt,
				"blocked_last_allocated_at":   resource.LastAllocatedAt,
				"updated_at":                  now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return mailapp.ErrMicrosoftAliasStaleClaim
		}
		return nil
	})
}

func (s *MicrosoftAliasStore) MarkDispatchFailed(ctx context.Context, task mailapp.MicrosoftAliasTask, nextRunAt time.Time, safeError string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var schedule MicrosoftAliasScheduleModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("resource_id = ? AND generation = ? AND status IN ?", task.ResourceID, task.Generation, []string{"queued", "running"}).
			Take(&schedule).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock failed microsoft alias dispatch: %w", err)
		}
		now := time.Now().UTC()
		if schedule.Status == "running" {
			if err := tx.Model(&MicrosoftAliasAttemptModel{}).
				Where("resource_id = ? AND status = ?", task.ResourceID, mailapp.MicrosoftAliasAttemptRunning).
				Updates(map[string]any{
					"status":          mailapp.MicrosoftAliasAttemptUncertain,
					"category":        "request",
					"last_safe_error": "Microsoft alias result requires reconciliation.",
					"was_attempted":   true,
					"uncertain_since": gorm.Expr("COALESCE(uncertain_since, ?)", now),
					"updated_at":      now,
				}).Error; err != nil {
				return fmt.Errorf("fence interrupted microsoft alias attempts: %w", err)
			}
		}
		result := tx.Model(&MicrosoftAliasScheduleModel{}).
			Where("resource_id = ? AND generation = ? AND status = ?", task.ResourceID, task.Generation, schedule.Status).
			Updates(map[string]any{
				"status":          "pending",
				"generation":      gorm.Expr("generation + 1"),
				"claim_token":     "",
				"next_run_at":     nextRunAt,
				"last_safe_error": safeAliasStoreMessage(safeError),
				"updated_at":      now,
			})
		if result.Error != nil {
			return fmt.Errorf("mark microsoft alias dispatch failed: %w", result.Error)
		}
		return nil
	})
}

func updateMicrosoftAliasScheduleTx(tx *gorm.DB, resourceID uint, claimToken, status string, nextRunAt time.Time, safeError string, failed *bool) error {
	updates := map[string]any{
		"status":          status,
		"generation":      gorm.Expr("generation + 1"),
		"claim_token":     "",
		"next_run_at":     nextRunAt,
		"last_safe_error": safeAliasStoreMessage(safeError),
		"updated_at":      time.Now().UTC(),
	}
	if status != "paused" {
		updates["blocked_resource_signature"] = ""
		updates["blocked_resource_updated_at"] = nil
		updates["blocked_last_allocated_at"] = nil
	}
	if failed != nil {
		if *failed {
			updates["failure_streak"] = gorm.Expr("failure_streak + 1")
		} else {
			updates["failure_streak"] = 0
		}
	}
	result := tx.Model(&MicrosoftAliasScheduleModel{}).
		Where("resource_id = ? AND status = ? AND claim_token = ?", resourceID, "running", claimToken).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return mailapp.ErrMicrosoftAliasStaleClaim
	}
	return nil
}

func normalizeAliasRows(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if strings.Count(value, "@") != 1 || !coredomain.IsMicrosoftEmailDomain(value) {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func normalizeExistingAliasRows(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		local, domain, ok := strings.Cut(value, "@")
		if !ok || local == "" || domain == "" || strings.Contains(domain, "@") {
			continue
		}
		if !coredomain.IsMicrosoftEmailDomain(value) {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func loadMicrosoftAliasUsage(db *gorm.DB, resourceID uint, yearStart, yearEnd, weekStart, weekEnd time.Time) (mailapp.MicrosoftAliasUsage, error) {
	var usage mailapp.MicrosoftAliasUsage
	err := db.Raw(`
SELECT
    COALESCE(SUM(CASE WHEN quota_at >= ? AND quota_at < ? THEN 1 ELSE 0 END), 0) AS year_count,
    COALESCE(SUM(CASE WHEN quota_at >= ? AND quota_at < ? THEN 1 ELSE 0 END), 0) AS week_count
FROM (
    SELECT attempt.quota_at AS quota_at
    FROM microsoft_alias_attempts AS attempt
    WHERE attempt.resource_id = ?
      AND attempt.status IN ('running', 'succeeded', 'uncertain')
    UNION ALL
    SELECT alias.created_at AS quota_at
    FROM explicit_aliases AS alias
    WHERE alias.resource_id = ?
      AND NOT EXISTS (
          SELECT 1
          FROM microsoft_alias_attempts AS attempt
          WHERE attempt.resource_id = alias.resource_id
            AND attempt.candidate = alias.email
            AND attempt.status IN ('running', 'succeeded', 'uncertain')
      )
) AS quota_rows`,
		yearStart,
		yearEnd,
		weekStart,
		weekEnd,
		resourceID,
		resourceID,
	).Scan(&usage).Error
	return usage, err
}

func newMicrosoftAliasClaimToken() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate microsoft alias claim token: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func normalizeMicrosoftAliasAttemptStatus(value string) string {
	switch strings.TrimSpace(value) {
	case mailapp.MicrosoftAliasAttemptSucceeded:
		return mailapp.MicrosoftAliasAttemptSucceeded
	case mailapp.MicrosoftAliasAttemptUncertain:
		return mailapp.MicrosoftAliasAttemptUncertain
	default:
		return mailapp.MicrosoftAliasAttemptFailed
	}
}

func minAliasQuota(left, right int) int {
	if left < right {
		return left
	}
	return right
}

// BackfillExistingAliases upserts aliases found on the Microsoft side into the
// local explicit_aliases table. Ownership is deliberately not caller input:
// every row must belong to users.id=1, and conflicts are converged to that
// fixed owner. The historical timestamp keeps these rows outside quota windows.
func (s *MicrosoftAliasStore) BackfillExistingAliases(ctx context.Context, resourceID uint, aliases []string) error {
	aliases = normalizeExistingAliasRows(aliases)
	if len(aliases) == 0 {
		return nil
	}
	db := s.db.WithContext(ctx)
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return backfillExistingAliasesTx(tx.WithContext(ctx), resourceID, aliases)
	}
	return db.Transaction(func(tx *gorm.DB) error {
		return backfillExistingAliasesTx(tx, resourceID, aliases)
	})
}

func backfillExistingAliasesTx(db *gorm.DB, resourceID uint, aliases []string) error {
	ownerUserID, err := lockMicrosoftExplicitAliasOwner(db)
	if err != nil {
		return err
	}
	historicalTime := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, alias := range aliases {
		record := MicrosoftExplicitAliasModel{
			ResourceID:  resourceID,
			OwnerUserID: ownerUserID,
			Email:       alias,
			Status:      "normal",
			CreatedAt:   historicalTime,
			UpdatedAt:   historicalTime,
		}
		if err := db.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "resource_id"}, {Name: "email"}},
			DoUpdates: clause.Assignments(map[string]any{
				"owner_user_id": ownerUserID,
			}),
		}).Create(&record).Error; err != nil {
			return fmt.Errorf("backfill explicit alias %s: %w", alias, err)
		}
	}
	return nil
}

func safeAliasStoreMessage(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
	if len(value) > 500 {
		return value[:500]
	}
	return value
}

func negativeAliasConfirmationIsDue(last *time.Time, now time.Time) bool {
	return last == nil || now.Sub(*last) >= microsoftAliasNegativeConfirmationMinInterval
}
