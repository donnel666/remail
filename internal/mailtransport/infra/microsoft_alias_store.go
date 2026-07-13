package infra

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MicrosoftAliasScheduleModel struct {
	ResourceID               uint       `gorm:"primaryKey;column:resource_id"`
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
	db            *gorm.DB
	operationLogs operationLogTxWriter
}

const (
	microsoftAliasScheduleEnsureBatch             = 10000
	microsoftAliasNegativeConfirmationMinInterval = time.Hour
	legacyMicrosoftAliasPublicOnlyMessage         = "Microsoft resource is not publicly available for alias creation."
)

func NewMicrosoftAliasStore(db *gorm.DB) *MicrosoftAliasStore {
	return &MicrosoftAliasStore{
		db:            db,
		operationLogs: governanceinfra.NewOperationLogRepo(db),
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
        WHERE candidate.status IN ('pending', 'queued')
          AND mr.status <> 'normal'
        ORDER BY candidate.resource_id
        LIMIT ?
    ) AS limited
) AS due ON due.resource_id = schedule.resource_id
JOIN microsoft_resources AS current_resource ON current_resource.id = schedule.resource_id
SET schedule.status = 'paused',
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
            SELECT binding.binding_address
            FROM microsoft_binding_mailboxes AS binding
            WHERE binding.resource_id = current_resource.id
              AND binding.status <> 'expired'
            LIMIT 1
        ), '')
    ), 256),
    schedule.blocked_resource_updated_at = current_resource.updated_at,
    schedule.blocked_last_allocated_at = current_resource.last_allocated_at,
    schedule.updated_at = ?
WHERE schedule.status IN ('pending', 'queued')
  AND current_resource.status <> 'normal'`, microsoftAliasScheduleEnsureBatch, mailapp.MicrosoftAliasResourceNotNormalMessage, now)
	if paused.Error != nil {
		return 0, fmt.Errorf("pause ineligible microsoft alias schedules: %w", paused.Error)
	}

	var pausedResourceIDs []uint
	// Old releases paused private resources with the public-only error below.
	// Wake those rows once, and compare both legacy sale-bit signatures so a
	// later for_sale toggle does not wake an otherwise permanently paused task.
	if err := s.db.WithContext(ctx).
		Table("microsoft_alias_schedules AS schedule").
		Select("schedule.resource_id").
		Joins("JOIN microsoft_resources AS mr ON mr.id = schedule.resource_id").
		Joins("LEFT JOIN microsoft_binding_mailboxes AS binding ON binding.resource_id = mr.id AND binding.status <> ?", "expired").
		Where(`schedule.status = ?
  AND mr.status = ?
  AND (
    schedule.last_safe_error IN (?, ?)
    OR schedule.blocked_resource_signature NOT IN (
      SHA2(CONCAT_WS(
        CHAR(0),
        mr.status,
        mr.email_address,
        mr.password,
        mr.client_id,
        mr.refresh_token,
        COALESCE(binding.binding_address, '')
      ), 256),
      SHA2(CONCAT_WS(
        CHAR(0),
        mr.status,
        TRUE,
        mr.email_address,
        mr.password,
        mr.client_id,
        mr.refresh_token,
        COALESCE(binding.binding_address, '')
      ), 256),
      SHA2(CONCAT_WS(
        CHAR(0),
        mr.status,
        FALSE,
        mr.email_address,
        mr.password,
        mr.client_id,
        mr.refresh_token,
        COALESCE(binding.binding_address, '')
      ), 256)
    )
  )`, "paused", "normal", legacyMicrosoftAliasPublicOnlyMessage, mailapp.MicrosoftAliasResourceNotNormalMessage).
		Order("schedule.resource_id ASC").
		Limit(microsoftAliasScheduleEnsureBatch).
		Pluck("schedule.resource_id", &pausedResourceIDs).Error; err != nil {
		return 0, fmt.Errorf("find paused microsoft alias schedules: %w", err)
	}
	var wokenRows int64
	if len(pausedResourceIDs) > 0 {
		woken := s.db.WithContext(ctx).
			Model(&MicrosoftAliasScheduleModel{}).
			Where("resource_id IN ? AND status = ?", pausedResourceIDs, "paused").
			Updates(map[string]any{
				"status":                      "pending",
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
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		}
		if err := tx.Raw(`
SELECT
    schedule.resource_id AS resource_id,
    resource.status AS resource_status,
    schedule.status AS schedule_status,
    schedule.blocked_resource_signature AS blocked_resource_signature,
    schedule.last_safe_error AS last_safe_error,
    SHA2(CONCAT_WS(
        CHAR(0),
        resource.status,
        resource.email_address,
        resource.password,
        resource.client_id,
        resource.refresh_token,
        COALESCE(binding.binding_address, '')
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
			!canWakeMicrosoftAliasSchedule(state.BlockedResourceSignature, state.ResourceSignature, state.LastSafeError) {
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
	})
	return ensured, err
}

func canWakeMicrosoftAliasSchedule(blockedSignature, resourceSignature, lastSafeError string) bool {
	return strings.TrimSpace(blockedSignature) == "" ||
		blockedSignature != resourceSignature ||
		lastSafeError == legacyMicrosoftAliasPublicOnlyMessage ||
		lastSafeError == mailapp.MicrosoftAliasResourceNotNormalMessage
}

func microsoftAliasScheduleWakeUpdates(now time.Time) map[string]any {
	return map[string]any{
		"status":                      "pending",
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

func (s *MicrosoftAliasStore) FindDispatchable(ctx context.Context, limit int, now, queuedStaleBefore, runningStaleBefore time.Time) ([]mailapp.MicrosoftAliasTask, error) {
	if limit <= 0 {
		limit = 10
	}
	tasks := make([]mailapp.MicrosoftAliasTask, 0, limit)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		schedules := make([]MicrosoftAliasScheduleModel, 0, limit)
		load := func(status string, before time.Time, order string) error {
			remaining := limit - len(schedules)
			if remaining <= 0 {
				return nil
			}
			var batch []MicrosoftAliasScheduleModel
			query := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
				Where("status = ?", status)
			if status == "pending" {
				query = query.Where("next_run_at <= ?", before)
			} else {
				query = query.Where("updated_at < ?", before)
			}
			if err := query.Order(order).Limit(remaining).Find(&batch).Error; err != nil {
				return err
			}
			schedules = append(schedules, batch...)
			return nil
		}
		if err := load("running", runningStaleBefore, "updated_at ASC, resource_id ASC"); err != nil {
			return err
		}
		if err := load("queued", queuedStaleBefore, "updated_at ASC, resource_id ASC"); err != nil {
			return err
		}
		if err := load("pending", now, "next_run_at ASC, resource_id ASC"); err != nil {
			return err
		}
		for _, schedule := range schedules {
			token, err := newMicrosoftAliasClaimToken()
			if err != nil {
				return err
			}
			if schedule.Status == "running" {
				if err := tx.Model(&MicrosoftAliasAttemptModel{}).
					Where("resource_id = ? AND status = ?", schedule.ResourceID, mailapp.MicrosoftAliasAttemptRunning).
					Updates(map[string]any{
						"status":          mailapp.MicrosoftAliasAttemptUncertain,
						"category":        "request",
						"last_safe_error": "Microsoft alias result requires reconciliation.",
						"was_attempted":   true,
						"uncertain_since": gorm.Expr("COALESCE(uncertain_since, ?)", now),
						"updated_at":      now,
					}).Error; err != nil {
					return fmt.Errorf("mark stale microsoft alias attempts uncertain: %w", err)
				}
			}
			result := tx.Model(&MicrosoftAliasScheduleModel{}).
				Where("resource_id = ? AND status = ? AND claim_token = ?", schedule.ResourceID, schedule.Status, schedule.ClaimToken).
				Updates(map[string]any{
					"status":          "queued",
					"claim_token":     token,
					"last_safe_error": "",
					"updated_at":      now,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				continue
			}
			tasks = append(tasks, mailapp.MicrosoftAliasTask{
				ResourceID:    schedule.ResourceID,
				DispatchToken: token,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("find microsoft alias schedules: %w", err)
	}
	return tasks, nil
}

func (s *MicrosoftAliasStore) Claim(ctx context.Context, task mailapp.MicrosoftAliasTask, now time.Time) (*mailapp.MicrosoftAliasAccount, bool, error) {
	var account *mailapp.MicrosoftAliasAccount
	claimed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var schedule MicrosoftAliasScheduleModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("resource_id = ? AND status = ? AND claim_token = ?", task.ResourceID, "queued", task.DispatchToken).
			First(&schedule).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock microsoft alias schedule: %w", err)
		}

		var row struct {
			ResourceID        uint       `gorm:"column:resource_id"`
			EmailAddress      string     `gorm:"column:email_address"`
			Password          string     `gorm:"column:password"`
			BindingAddress    string     `gorm:"column:binding_address"`
			ResourceStatus    string     `gorm:"column:resource_status"`
			ResourceSignature string     `gorm:"column:resource_signature"`
			ResourceUpdatedAt time.Time  `gorm:"column:resource_updated_at"`
			LastAllocatedAt   *time.Time `gorm:"column:last_allocated_at"`
		}
		if err := tx.Raw(`
SELECT
    mr.id AS resource_id,
    mr.email_address AS email_address,
    mr.password AS password,
    COALESCE(binding.binding_address, '') AS binding_address,
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
        COALESCE(binding.binding_address, '')
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
				Where("resource_id = ? AND status = ? AND claim_token = ?", task.ResourceID, "queued", task.DispatchToken).
				Updates(map[string]any{
					"status":                      "paused",
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

		result := tx.Model(&MicrosoftAliasScheduleModel{}).
			Where("resource_id = ? AND status = ? AND claim_token = ?", task.ResourceID, "queued", task.DispatchToken).
			Updates(map[string]any{
				"status":                      "running",
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
			ClaimToken:     task.DispatchToken,
		}
		claimed = true
		return nil
	})
	return account, claimed, err
}

func (s *MicrosoftAliasStore) CheckEligibility(ctx context.Context, resourceID uint, claimToken string) (bool, error) {
	var row struct {
		ResourceID uint   `gorm:"column:resource_id"`
		Status     string `gorm:"column:resource_status"`
	}
	if err := s.db.WithContext(ctx).Raw(`
SELECT
    schedule.resource_id AS resource_id,
    mr.status AS resource_status
FROM microsoft_alias_schedules AS schedule
JOIN microsoft_resources AS mr ON mr.id = schedule.resource_id
WHERE schedule.resource_id = ?
  AND schedule.status = 'running'
  AND schedule.claim_token = ?
LIMIT 1`, resourceID, claimToken).Scan(&row).Error; err != nil {
		return false, fmt.Errorf("load microsoft alias eligibility: %w", err)
	}
	if row.ResourceID == 0 {
		return false, mailapp.ErrMicrosoftAliasStaleClaim
	}
	return row.Status == "normal", nil
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
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
			mailapp.MicrosoftAliasWeeklyLimit-usage.WeekCount,
			mailapp.MicrosoftAliasYearlyLimit-usage.YearCount,
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
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model)
			if result.Error != nil {
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
				ownerUserID, err = lockDeterministicMicrosoftAliasOwner(tx)
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

func lockDeterministicMicrosoftAliasOwner(tx *gorm.DB) (uint, error) {
	var owner struct {
		ID uint `gorm:"column:id"`
	}
	if err := tx.Raw(`
SELECT id
FROM users
WHERE role = 'super_admin'
ORDER BY id ASC
LIMIT 1
FOR SHARE`).Scan(&owner).Error; err != nil {
		return 0, fmt.Errorf("lock microsoft alias super administrator owner: %w", err)
	}
	if owner.ID == 0 {
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
	if err := updateMicrosoftAliasScheduleTx(s.db.WithContext(ctx), resourceID, claimToken, "paused", time.Now().UTC(), safeError, nil); err != nil {
		return fmt.Errorf("pause microsoft alias schedule: %w", err)
	}
	return nil
}

func (s *MicrosoftAliasStore) MarkDispatchFailed(ctx context.Context, task mailapp.MicrosoftAliasTask, nextRunAt time.Time, safeError string) error {
	result := s.db.WithContext(ctx).
		Model(&MicrosoftAliasScheduleModel{}).
		Where("resource_id = ? AND status = ? AND claim_token = ?", task.ResourceID, "queued", task.DispatchToken).
		Updates(map[string]any{
			"status":          "pending",
			"claim_token":     "",
			"next_run_at":     nextRunAt,
			"last_safe_error": safeAliasStoreMessage(safeError),
			"updated_at":      time.Now().UTC(),
		})
	if result.Error != nil {
		return fmt.Errorf("mark microsoft alias dispatch failed: %w", result.Error)
	}
	return nil
}

func updateMicrosoftAliasScheduleTx(tx *gorm.DB, resourceID uint, claimToken, status string, nextRunAt time.Time, safeError string, failed *bool) error {
	updates := map[string]any{
		"status":          status,
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
		if !strings.HasSuffix(value, "@outlook.com") || strings.Count(value, "@") != 1 {
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
