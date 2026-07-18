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

func (r *RetentionRepo) DeleteIdempotencyKeysBefore(ctx context.Context, before time.Time, limit int) (int64, error) {
	return r.deleteBySQL(ctx, "DELETE FROM idempotency_keys WHERE created_at < ? LIMIT ?", before, limit)
}

func (r *RetentionRepo) DeleteMailmatchMessagesBefore(ctx context.Context, before time.Time, resourceType string, limit int) (int64, error) {
	if resourceType != "microsoft" && resourceType != "domain" {
		return 0, fmt.Errorf("delete mailmatch messages: invalid resource type")
	}
	return r.deleteBySQL(ctx, "DELETE FROM mailmatch_messages WHERE resource_type = ? AND received_at < ? ORDER BY received_at, id LIMIT ?", resourceType, before, limit)
}

func (r *RetentionRepo) DeleteAllocationDailyUsagesBefore(ctx context.Context, before time.Time, limit int) (int64, error) {
	return r.deleteBySQL(ctx, "DELETE FROM allocation_daily_usages WHERE usage_date < ? ORDER BY usage_date LIMIT ?", before.UTC().Format("2006-01-02"), limit)
}

func (r *RetentionRepo) DeleteOutboundMailsTerminalBefore(ctx context.Context, before time.Time, limit int) (int64, error) {
	return r.deleteBySQL(ctx, "DELETE FROM outbound_mails WHERE status IN ('sent', 'failed') AND updated_at < ? LIMIT ?", before, limit)
}

func (r *RetentionRepo) DeleteSystemLogsBefore(ctx context.Context, before time.Time, limit int) (int64, error) {
	return r.deleteBySQL(ctx, "DELETE FROM system_logs WHERE created_at < ? ORDER BY created_at, id LIMIT ?", before, limit)
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

func (r *RetentionRepo) ListExistingInboundObjectKeys(ctx context.Context, objectKeys []string) (map[string]struct{}, error) {
	result := make(map[string]struct{}, len(objectKeys))
	if len(objectKeys) == 0 {
		return result, nil
	}
	var rows []string
	if err := r.db.WithContext(ctx).
		Table("inbound_mails").
		Select("source_object_key").
		Where("source_object_key IN ?", objectKeys).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list existing inbound object keys: %w", err)
	}
	for _, objectKey := range rows {
		result[objectKey] = struct{}{}
	}
	return result, nil
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
