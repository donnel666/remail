package infra

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"gorm.io/gorm"
)

type AdminLogRepo struct {
	db *gorm.DB
}

func NewAdminLogRepo(db *gorm.DB) *AdminLogRepo {
	return &AdminLogRepo{db: db}
}

func (r *AdminLogRepo) ListSystemLogs(ctx context.Context, filter governanceapp.AdminLogListFilter) ([]governanceapp.AdminSystemLogView, int64, error) {
	query := r.systemQuery(ctx, filter)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count system logs: %w", err)
	}
	models := make([]SystemLogModel, 0)
	if err := query.Order("created_at DESC, id DESC").Offset(filter.Offset).Limit(filter.Limit).Find(&models).Error; err != nil {
		return nil, 0, fmt.Errorf("list system logs: %w", err)
	}
	items := make([]governanceapp.AdminSystemLogView, len(models))
	for i := range models {
		items[i] = governanceapp.AdminSystemLogView{
			ID: models[i].ID, Level: models[i].Level, Module: models[i].Module,
			EventType: models[i].EventType, RequestID: models[i].RequestID,
			BizType: models[i].BizType, BizID: models[i].BizID,
			Message: sanitizeSystemLogText(models[i].Message, 500),
			Detail:  sanitizeSystemLogText(models[i].Detail, 1000), CreatedAt: models[i].CreatedAt,
		}
	}
	return items, total, nil
}

func (r *AdminLogRepo) ListOperationLogs(ctx context.Context, filter governanceapp.AdminLogListFilter) ([]governanceapp.AdminOperationLogView, int64, error) {
	query := r.operationQuery(ctx, filter)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count operation logs: %w", err)
	}
	rows := make([]adminOperationLogRow, 0)
	if err := query.
		Select(`operation_logs.id, operation_logs.operator_user_id,
operation_logs.operation_type, operation_logs.resource_type, operation_logs.resource_id,
operation_logs.path, operation_logs.result, operation_logs.safe_summary,
operation_logs.request_id, operation_logs.created_at,
COALESCE(NULLIF(users.email, ''), CONCAT('User #', operation_logs.operator_user_id)) AS operator`).
		Order("operation_logs.created_at DESC, operation_logs.id DESC").
		Offset(filter.Offset).
		Limit(filter.Limit).
		Scan(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("list operation logs: %w", err)
	}
	items := make([]governanceapp.AdminOperationLogView, len(rows))
	for i := range rows {
		items[i] = governanceapp.AdminOperationLogView{
			ID: rows[i].ID, OperatorUserID: rows[i].OperatorUserID, Operator: rows[i].Operator,
			OperationType: rows[i].OperationType, ResourceType: rows[i].ResourceType,
			ResourceID: rows[i].ResourceID, Path: rows[i].Path, Result: rows[i].Result,
			SafeSummary: rows[i].SafeSummary, RequestID: rows[i].RequestID, CreatedAt: rows[i].CreatedAt,
		}
	}
	return items, total, nil
}

func (r *AdminLogRepo) CountSystemLogs(ctx context.Context) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&SystemLogModel{}).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count system log facets: %w", err)
	}
	return count, nil
}

func (r *AdminLogRepo) CountOperationLogs(ctx context.Context) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&OperationLogModel{}).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count operation log facets: %w", err)
	}
	return count, nil
}

func (r *AdminLogRepo) CleanupLogs(ctx context.Context, category string, before time.Time, audit *governancedomain.OperationLog) (int64, error) {
	if r == nil || r.db == nil || audit == nil {
		return 0, errors.New("cleanup logs: invalid repository state")
	}
	var deleteSQL string
	switch category {
	case governanceapp.AdminLogCategorySystem:
		deleteSQL = "DELETE FROM system_logs WHERE created_at < ?"
	case governanceapp.AdminLogCategoryOperation:
		deleteSQL = "DELETE FROM operation_logs WHERE created_at < ?"
	default:
		return 0, errors.New("cleanup logs: invalid category")
	}

	var removed int64
	// ponytail: synchronous atomic cleanup; move to a durable job only if indexed production deletes exceed the HTTP timeout.
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Exec(deleteSQL, before.UTC())
		if result.Error != nil {
			return fmt.Errorf("delete %s logs: %w", category, result.Error)
		}
		removed = result.RowsAffected
		if err := tx.Create(operationLogModel(audit)).Error; err != nil {
			return fmt.Errorf("create cleanup audit: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

func (r *AdminLogRepo) systemQuery(ctx context.Context, filter governanceapp.AdminLogListFilter) *gorm.DB {
	query := r.db.WithContext(ctx).Model(&SystemLogModel{})
	if filter.Level != "" {
		query = query.Where("level = ?", filter.Level)
	}
	if filter.From != nil {
		query = query.Where("created_at >= ?", filter.From.UTC())
	}
	if filter.To != nil {
		query = query.Where("created_at <= ?", filter.To.UTC())
	}
	if filter.Search != "" {
		pattern := "%" + strings.TrimSpace(filter.Search) + "%"
		query = query.Where(`module LIKE ? OR event_type LIKE ? OR biz_type LIKE ? OR biz_id LIKE ?
OR message LIKE ? OR detail LIKE ? OR request_id LIKE ?`, pattern, pattern, pattern, pattern, pattern, pattern, pattern)
	}
	return query
}

func (r *AdminLogRepo) operationQuery(ctx context.Context, filter governanceapp.AdminLogListFilter) *gorm.DB {
	query := r.db.WithContext(ctx).
		Table("operation_logs").
		Joins("LEFT JOIN users ON users.id = operation_logs.operator_user_id")
	if filter.Result != "" {
		query = query.Where("operation_logs.result = ?", filter.Result)
	}
	if filter.From != nil {
		query = query.Where("operation_logs.created_at >= ?", filter.From.UTC())
	}
	if filter.To != nil {
		query = query.Where("operation_logs.created_at <= ?", filter.To.UTC())
	}
	if filter.Search != "" {
		pattern := "%" + strings.TrimSpace(filter.Search) + "%"
		query = query.Where(`users.email LIKE ? OR CAST(operation_logs.operator_user_id AS CHAR) LIKE ?
OR operation_logs.operation_type LIKE ? OR operation_logs.resource_type LIKE ?
OR operation_logs.resource_id LIKE ? OR operation_logs.path LIKE ?
OR operation_logs.safe_summary LIKE ? OR operation_logs.request_id LIKE ?`, pattern, pattern, pattern, pattern, pattern, pattern, pattern, pattern)
	}
	return query
}

type adminOperationLogRow struct {
	ID             uint64
	OperatorUserID uint
	Operator       string
	OperationType  string
	ResourceType   string
	ResourceID     string
	Path           string
	Result         string
	SafeSummary    string
	RequestID      string
	CreatedAt      time.Time
}
