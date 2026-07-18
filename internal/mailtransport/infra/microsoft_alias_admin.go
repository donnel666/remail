package infra

import (
	"context"
	"errors"
	"fmt"
	"time"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ mailapp.MicrosoftAliasAdminScheduleStore = (*MicrosoftAliasStore)(nil)

func (s *MicrosoftAliasStore) GetAdminSchedule(
	ctx context.Context,
	resourceID uint,
	yearStart, yearEnd, weekStart, weekEnd time.Time,
) (*mailapp.MicrosoftAliasAdminSchedule, error) {
	if resourceID == 0 {
		return nil, mailapp.ErrMicrosoftAliasResourceNotFound
	}
	db := s.adminAliasDBFor(ctx)
	var exists int64
	if err := db.Table("microsoft_resources").Where("id = ?", resourceID).Count(&exists).Error; err != nil {
		return nil, aliasAdminUnavailable("check resource", err)
	}
	if exists == 0 {
		return nil, mailapp.ErrMicrosoftAliasResourceNotFound
	}
	usage, err := loadMicrosoftAliasUsage(db, resourceID, yearStart, yearEnd, weekStart, weekEnd)
	if err != nil {
		return nil, aliasAdminUnavailable("load quota usage", err)
	}
	result := &mailapp.MicrosoftAliasAdminSchedule{
		WeekCreated: usage.WeekCount,
		YearCreated: usage.YearCount,
	}
	var schedule MicrosoftAliasScheduleModel
	err = db.Where("resource_id = ?", resourceID).Take(&schedule).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return result, nil
	}
	if err != nil {
		return nil, aliasAdminUnavailable("load schedule", err)
	}
	nextRunAt := schedule.NextRunAt.UTC()
	result.NextRunAt = &nextRunAt
	result.ScheduleStatus = schedule.Status
	result.UpdatedAt = schedule.UpdatedAt.UTC()
	return result, nil
}

func (s *MicrosoftAliasStore) expediteAdminScheduleInTx(
	tx *gorm.DB,
	resourceID uint,
	now time.Time,
) (*mailapp.MicrosoftAliasExpediteResult, error) {
	var resource struct {
		ID                 uint   `gorm:"column:id"`
		Status             string `gorm:"column:status"`
		Signature          string `gorm:"column:resource_signature"`
		BindingAddress     string `gorm:"column:binding_address"`
		BindingStatus      string `gorm:"column:binding_status"`
		BindingAccount     string `gorm:"column:binding_account_email"`
		ResourceEmail      string `gorm:"column:resource_email"`
		BindingDomainReady bool   `gorm:"column:binding_domain_ready"`
	}
	if err := tx.Raw(`
SELECT
    mr.id AS id,
    mr.status AS status,
    mr.email_address AS resource_email,
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
LIMIT 1
FOR SHARE`, resourceID).Scan(&resource).Error; err != nil {
		return nil, aliasAdminUnavailable("load resource", err)
	}
	if resource.ID == 0 {
		return nil, mailapp.ErrMicrosoftAliasResourceNotFound
	}
	if resource.Status != "normal" {
		return nil, mailapp.ErrMicrosoftAliasResourceConflict
	}

	var schedule MicrosoftAliasScheduleModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("resource_id = ?", resourceID).
		Take(&schedule).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, mailapp.ErrMicrosoftAliasScheduleNotFound
	} else if err != nil {
		return nil, aliasAdminUnavailable("lock schedule", err)
	}
	queuedAt := aliasAdminQueuedAt(schedule)
	startedAt := aliasAdminStartedAt(schedule)
	switch schedule.Status {
	case "queued", "running":
		nextRunAt := schedule.NextRunAt.UTC()
		return &mailapp.MicrosoftAliasExpediteResult{
			Status:    schedule.Status,
			Reused:    true,
			NextRunAt: &nextRunAt,
			QueuedAt:  queuedAt,
			StartedAt: startedAt,
			UpdatedAt: schedule.UpdatedAt.UTC(),
		}, nil
	case "pending":
		alreadyDue := !schedule.NextRunAt.After(now)
		if !alreadyDue {
			update := tx.Model(&MicrosoftAliasScheduleModel{}).
				Where("resource_id = ? AND status = ? AND claim_token = ?", resourceID, "pending", schedule.ClaimToken).
				Updates(map[string]any{
					"generation":  gorm.Expr("generation + 1"),
					"next_run_at": now,
					"updated_at":  now,
				})
			if update.Error != nil {
				return nil, aliasAdminUnavailable("advance schedule", update.Error)
			}
			if update.RowsAffected == 0 {
				return nil, mailapp.ErrMicrosoftAliasResourceConflict
			}
			queuedAt = now
		}
		nextRunAt := now
		updatedAt := schedule.UpdatedAt.UTC()
		if !alreadyDue {
			updatedAt = now
		}
		return &mailapp.MicrosoftAliasExpediteResult{
			Status:         "queued",
			Reused:         alreadyDue,
			NextRunAt:      &nextRunAt,
			QueuedAt:       queuedAt,
			UpdatedAt:      updatedAt,
			WakeDispatcher: true,
		}, nil
	case "paused":
		canWake := canWakeMicrosoftAliasSchedule(
			schedule.BlockedResourceSignature,
			resource.Signature,
			schedule.LastSafeError,
			microsoftAliasBindingReady(
				resource.BindingAddress,
				resource.BindingAccount,
				resource.ResourceEmail,
				resource.BindingDomainReady,
			),
			microsoftAliasBindingRecoverable(
				resource.BindingAddress,
				resource.BindingAccount,
				resource.ResourceEmail,
				resource.BindingDomainReady,
			),
		)
		if !canWake {
			return nil, mailapp.ErrMicrosoftAliasSchedulePaused
		}
		update := tx.Model(&MicrosoftAliasScheduleModel{}).
			Where("resource_id = ? AND status = ?", resourceID, "paused").
			Updates(microsoftAliasScheduleWakeUpdates(now))
		if update.Error != nil {
			return nil, aliasAdminUnavailable("wake changed schedule", update.Error)
		}
		if update.RowsAffected == 0 {
			return nil, mailapp.ErrMicrosoftAliasResourceConflict
		}
		nextRunAt := now
		return &mailapp.MicrosoftAliasExpediteResult{
			Status:         "queued",
			Reused:         false,
			NextRunAt:      &nextRunAt,
			QueuedAt:       now,
			UpdatedAt:      now,
			WakeDispatcher: true,
		}, nil
	default:
		return nil, mailapp.ErrMicrosoftAliasResourceConflict
	}
}

func aliasAdminQueuedAt(schedule MicrosoftAliasScheduleModel) time.Time {
	if schedule.LastRunAt == nil && !schedule.CreatedAt.IsZero() {
		return schedule.CreatedAt.UTC()
	}
	if !schedule.UpdatedAt.IsZero() {
		return schedule.UpdatedAt.UTC()
	}
	return schedule.NextRunAt.UTC()
}

func aliasAdminStartedAt(schedule MicrosoftAliasScheduleModel) *time.Time {
	if schedule.Status != "running" || schedule.LastRunAt == nil {
		return nil
	}
	startedAt := schedule.LastRunAt.UTC()
	return &startedAt
}

func (s *MicrosoftAliasStore) adminAliasDBFor(ctx context.Context) *gorm.DB {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return tx.WithContext(ctx)
	}
	return s.db.WithContext(ctx)
}

func (s *MicrosoftAliasStore) withAdminAliasTx(ctx context.Context, fn func(*gorm.DB) error) error {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return fn(tx.WithContext(ctx))
	}
	return s.db.WithContext(ctx).Transaction(fn)
}

func aliasAdminUnavailable(operation string, err error) error {
	if err == nil {
		return mailapp.ErrMicrosoftAliasAdminUnavailable
	}
	return fmt.Errorf("%w: %s: %w", mailapp.ErrMicrosoftAliasAdminUnavailable, operation, err)
}
