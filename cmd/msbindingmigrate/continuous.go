package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func manualPauseRequested(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "PAUSED"))
	// An unreadable control directory must not allow new remote mutations.
	return !errors.Is(err, os.ErrNotExist)
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func waitForResume(ctx context.Context, dir string) error {
	if !manualPauseRequested(dir) {
		return nil
	}
	slog.Info("migration manually paused; waiting for resume")
	for manualPauseRequested(dir) {
		if err := waitForRetry(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	slog.Info("manual pause cleared")
	return nil
}

func readyWork(ctx context.Context, db *gorm.DB, now time.Time) ([]item, error) {
	var rows []item
	err := db.WithContext(ctx).Where("state IN ('running','partial','remote_complete','mail_pending','retry','failed','claimed') AND (retry_at IS NULL OR retry_at<=?)", now).
		Order("CASE WHEN state='claimed' THEN 1 ELSE 0 END, resource_id").Limit(1000).Find(&rows).Error
	return rows, err
}

func pendingTrialWork(ctx context.Context, db *gorm.DB, ids []uint, now time.Time) ([]item, error) {
	if len(ids) == 0 {
		return nil, errors.New("trial continuation requires explicit resource IDs")
	}
	var rows []item
	err := db.WithContext(ctx).Where("resource_id IN ? AND state IN ('running','partial','remote_complete','mail_pending','retry','claimed') AND (retry_at IS NULL OR retry_at<=?)", ids, now).
		Order("resource_id").Limit(1000).Find(&rows).Error
	return rows, err
}

func queueMailRechecks(ctx context.Context, db *gorm.DB) (int64, error) {
	result := db.WithContext(ctx).Exec(`UPDATE ` + table + ` l
 JOIN microsoft_resources m ON m.id=l.resource_id
 JOIN email_resources e ON e.id=m.id AND e.type='microsoft' AND e.owner_user_id=l.owner_id
 JOIN microsoft_binding_mailboxes b ON b.id=l.binding_id AND b.resource_id=m.id
 SET l.state='mail_pending',l.retry_at=NULL,l.retry_count=0,
 l.safe_error='Credential revision changed after the previous mail verification.',l.updated_at=UTC_TIMESTAMP(3)
 WHERE l.state='success' AND l.phase='remote_complete' AND m.status IN ('normal','abnormal')
 AND m.email_address=l.email AND b.binding_address=l.target_address AND b.status='verified'
 AND NOT EXISTS(SELECT 1 FROM operation_logs o WHERE o.resource_type='microsoft_resource'
 AND o.resource_id=CAST(m.id AS CHAR) COLLATE utf8mb4_unicode_ci
 AND o.operation_type='mailtransport.microsoft.binding_domain_mail_verify' AND o.result='success'
 AND o.safe_summary LIKE CONCAT('%credential revision ',m.credential_revision,'.%') COLLATE utf8mb4_unicode_ci)`)
	return result.RowsAffected, result.Error
}

func retryDelay(count uint, err error) time.Duration {
	delay := min(15*time.Minute*time.Duration(1<<min(count, 5)), 6*time.Hour)
	if errors.Is(err, errFence) {
		delay = 24 * time.Hour
	}
	var auth *msacl.AuthError
	if errors.As(err, &auth) {
		switch auth.Status {
		case msacl.AuthStatusPasswordError, msacl.AuthStatusMFARequired, msacl.AuthStatusPhoneVerification, msacl.AuthStatusPasskeyRequired, msacl.AuthStatusUnknownMailbox:
			delay = 24 * time.Hour
		}
		if auth.RetryAfter > delay {
			delay = auth.RetryAfter
		}
	}
	return delay
}

func scheduleRetry(ctx context.Context, db *gorm.DB, expected item, cause error, now time.Time) (time.Time, error) {
	var retryAt time.Time
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current item
		if err := tx.Where("resource_id=?", expected.ResourceID).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&current).Error; err != nil {
			return err
		}
		if current.Generation != expected.Generation || current.State == "success" || current.State == "skipped" {
			return errFence
		}
		retryAt = now.Add(retryDelay(current.RetryCount, cause))
		state := current.State
		if state == "claimed" || state == "retry" || state == "running" {
			state = failureState(state == "running" || !beforeChange(current.Phase), false, cause)
		}
		return tx.Model(&current).Updates(map[string]any{"state": state, "retry_at": retryAt, "retry_count": current.RetryCount + 1, "safe_error": safeError(cause), "updated_at": now}).Error
	})
	return retryAt, err
}
