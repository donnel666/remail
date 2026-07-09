package infra

import (
	"context"
	"fmt"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	"gorm.io/gorm"
)

type RetentionRepo struct {
	db *gorm.DB
}

func NewRetentionRepo(db *gorm.DB) *RetentionRepo {
	return &RetentionRepo{db: db}
}

func (r *RetentionRepo) DeleteAPILogsBefore(ctx context.Context, before time.Time, limit int) (int64, error) {
	return r.deleteBySQL(ctx, "DELETE FROM api_logs WHERE created_at < ? LIMIT ?", before, limit)
}

func (r *RetentionRepo) DeleteIdempotencyKeysBefore(ctx context.Context, before time.Time, limit int) (int64, error) {
	return r.deleteBySQL(ctx, "DELETE FROM idempotency_keys WHERE created_at < ? LIMIT ?", before, limit)
}

func (r *RetentionRepo) DeleteMailmatchMessagesBefore(ctx context.Context, before time.Time, status string, limit int) (int64, error) {
	if status != "" {
		return r.deleteBySQL(ctx, "DELETE FROM mailmatch_messages WHERE status = ? AND received_at < ? LIMIT ?", status, before, limit)
	}
	return r.deleteBySQL(ctx, "DELETE FROM mailmatch_messages WHERE received_at < ? LIMIT ?", before, limit)
}

func (r *RetentionRepo) DeleteFetchJobsTerminalBefore(ctx context.Context, before time.Time, limit int) (int64, error) {
	return r.deleteBySQL(ctx, "DELETE FROM mailmatch_fetch_jobs WHERE status IN ('succeeded', 'failed', 'skipped') AND updated_at < ? LIMIT ?", before, limit)
}

func (r *RetentionRepo) ListInboundMailObjectsBefore(ctx context.Context, before time.Time, limit int) ([]governanceapp.RetentionInboundMailObject, error) {
	if limit <= 0 {
		limit = 5000
	}
	rows := make([]governanceapp.RetentionInboundMailObject, 0)
	if err := r.db.WithContext(ctx).
		Table("inbound_mails").
		Select("id, source_object_key AS object_key").
		Where("created_at < ?", before).
		Order("id ASC").
		Limit(limit).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list inbound mail retention objects: %w", err)
	}
	return rows, nil
}

func (r *RetentionRepo) DeleteInboundMailsByID(ctx context.Context, ids []uint64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	result := r.db.WithContext(ctx).Exec("DELETE FROM inbound_mails WHERE id IN ?", ids)
	if result.Error != nil {
		return 0, fmt.Errorf("delete inbound mails: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (r *RetentionRepo) deleteBySQL(ctx context.Context, sql string, args ...any) (int64, error) {
	result := r.db.WithContext(ctx).Exec(sql, args...)
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}
