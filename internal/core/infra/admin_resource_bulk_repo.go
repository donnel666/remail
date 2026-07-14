package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AdminResourceBulkCommandModel struct {
	ID                   uint64     `gorm:"primaryKey;autoIncrement"`
	OperatorUserID       uint       `gorm:"not null;column:operator_user_id"`
	Action               string     `gorm:"type:varchar(32);not null"`
	SelectionMode        string     `gorm:"type:varchar(16);not null;column:selection_mode"`
	SelectionJSON        []byte     `gorm:"type:json;not null;column:selection_json"`
	SelectionFingerprint string     `gorm:"type:char(64);not null;column:selection_fingerprint"`
	IdempotencyKey       string     `gorm:"type:varchar(128);not null;default:'';column:idempotency_key"`
	MaxResourceID        uint       `gorm:"not null;default:0;column:max_resource_id"`
	CheckpointResourceID uint       `gorm:"not null;default:0;column:checkpoint_resource_id"`
	Status               string     `gorm:"type:varchar(32);not null;default:'queued'"`
	MatchedCount         int        `gorm:"not null;default:0;column:matched_count"`
	ProcessedCount       int        `gorm:"not null;default:0;column:processed_count"`
	AffectedCount        int        `gorm:"not null;default:0;column:affected_count"`
	SkippedCount         int        `gorm:"not null;default:0;column:skipped_count"`
	ReasonBuckets        []byte     `gorm:"type:json;column:reason_buckets"`
	Attempts             int        `gorm:"not null;default:0"`
	MaxAttempts          int        `gorm:"not null;default:3;column:max_attempts"`
	ClaimToken           string     `gorm:"type:char(36);not null;default:'';column:claim_token"`
	DispatchToken        string     `gorm:"type:char(36);not null;default:'';column:dispatch_token"`
	LastSafeError        string     `gorm:"type:varchar(500);not null;default:'';column:last_safe_error"`
	RequestID            string     `gorm:"type:varchar(64);not null;default:'';column:request_id"`
	Path                 string     `gorm:"type:varchar(255);not null;default:''"`
	DispatchedAt         *time.Time `gorm:"column:dispatched_at"`
	StartedAt            *time.Time `gorm:"column:started_at"`
	FinishedAt           *time.Time `gorm:"column:finished_at"`
	CreatedAt            time.Time  `gorm:"not null;autoCreateTime"`
	UpdatedAt            time.Time  `gorm:"not null;autoUpdateTime"`
}

func (AdminResourceBulkCommandModel) TableName() string { return "admin_resource_bulk_commands" }

type AdminResourceBulkRepo struct {
	db            *gorm.DB
	read          *AdminResourceRepo
	operationLogs *governanceinfra.OperationLogRepo
}

func NewAdminResourceBulkRepo(db *gorm.DB) *AdminResourceBulkRepo {
	return &AdminResourceBulkRepo{db: db, read: NewAdminResourceRepo(db), operationLogs: governanceinfra.NewOperationLogRepo(db)}
}

func (r *AdminResourceBulkRepo) dbFor(ctx context.Context) *gorm.DB {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return tx.WithContext(ctx)
	}
	return r.db.WithContext(ctx)
}

func (r *AdminResourceBulkRepo) CreateWithLog(ctx context.Context, command *coreapp.AdminResourceBulkCommand, log *governancedomain.OperationLog) (bool, error) {
	if command == nil {
		return false, domain.ErrInvalidResourceCommand
	}
	selectionJSON, err := json.Marshal(command.Selection)
	if err != nil {
		return false, fmt.Errorf("marshal admin resource bulk selection: %w", err)
	}
	created := false
	err = withRetryableMySQLTransaction(ctx, r.db, func(tx *gorm.DB) error {
		created = false
		if command.IdempotencyKey != "" {
			existing, err := findAdminBulkByIdempotencyTx(tx, command.OperatorUserID, command.IdempotencyKey)
			if err != nil {
				return err
			}
			if existing != nil {
				if existing.Action != string(command.Action) || existing.SelectionFingerprint != command.SelectionFingerprint {
					return domain.ErrResourceIdempotencyConflict
				}
				*command, err = adminBulkModelToDomain(existing)
				return err
			}
		}
		maxResourceID := uint(0)
		matched := 0
		if command.Selection.Mode == coreapp.AdminResourceBulkIDs {
			matched = len(command.Selection.ResourceIDs)
			if matched > 0 {
				maxResourceID = command.Selection.ResourceIDs[matched-1]
			}
		} else {
			if err := tx.Table("email_resources").
				Select("COALESCE(MAX(id), 0)").
				Where("type = ?", string(domain.ResourceTypeMicrosoft)).
				Row().Scan(&maxResourceID); err != nil {
				return fmt.Errorf("capture admin bulk high-water mark: %w", err)
			}
			// Filter batches expand durably in the dispatcher, but the operator
			// needs an immediate matched-count in the acceptance response instead of
			// a bare 0. Count how many resources match the filter up to the captured
			// high-water mark now; the dispatcher's per-page CompletePage still
			// accumulates the authoritative matched total as it processes.
			var matched64 int64
			if err := r.read.adminMicrosoftFilterQuery(ctx, command.Selection.Filter.ListFilter(), time.Now().UTC(), "").
				Where("er.id <= ?", maxResourceID).
				Count(&matched64).Error; err != nil {
				return fmt.Errorf("count admin bulk matches: %w", err)
			}
			matched = int(matched64)
		}
		model := &AdminResourceBulkCommandModel{
			OperatorUserID: command.OperatorUserID, Action: string(command.Action), SelectionMode: string(command.Selection.Mode),
			SelectionJSON: selectionJSON, SelectionFingerprint: command.SelectionFingerprint, IdempotencyKey: command.IdempotencyKey,
			MaxResourceID: maxResourceID, Status: "queued", MatchedCount: matched,
			ReasonBuckets: []byte(`{}`), MaxAttempts: command.MaxAttempts,
			RequestID: command.RequestID, Path: command.Path,
		}
		if model.MaxAttempts <= 0 {
			model.MaxAttempts = 3
		}
		if err := tx.Create(model).Error; err != nil {
			if isDuplicateKeyError(err) && command.IdempotencyKey != "" {
				existing, findErr := findAdminBulkByIdempotencyTx(tx, command.OperatorUserID, command.IdempotencyKey)
				if findErr != nil {
					return findErr
				}
				if existing != nil && existing.Action == string(command.Action) && existing.SelectionFingerprint == command.SelectionFingerprint {
					*command, findErr = adminBulkModelToDomain(existing)
					return findErr
				}
				return domain.ErrResourceIdempotencyConflict
			}
			return fmt.Errorf("create admin resource bulk command: %w", err)
		}
		mapped, err := adminBulkModelToDomain(model)
		if err != nil {
			return err
		}
		*command = mapped
		created = true
		if log != nil {
			log.ResourceID = fmt.Sprintf("bulk:%d", model.ID)
			if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
				return err
			}
		}
		return nil
	})
	return created, err
}

func (r *AdminResourceBulkRepo) FindByID(ctx context.Context, id uint64) (*coreapp.AdminResourceBulkCommand, error) {
	var model AdminResourceBulkCommandModel
	err := r.db.WithContext(ctx).First(&model, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find admin resource bulk command: %w", err)
	}
	result, err := adminBulkModelToDomain(&model)
	return &result, err
}

func (r *AdminResourceBulkRepo) ClaimDispatchable(ctx context.Context, limit int, runningStaleBefore, queuedDispatchStaleBefore time.Time) ([]coreapp.AdminResourceBulkCommand, error) {
	if limit <= 0 {
		limit = 32
	}
	models := make([]AdminResourceBulkCommandModel, 0, limit)
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Where("attempts < max_attempts").Where(
			"(status = ? AND (dispatched_at IS NULL OR dispatched_at < ?)) OR (status = ? AND updated_at < ?)",
			"queued", queuedDispatchStaleBefore, "running", runningStaleBefore,
		).
			Order("updated_at ASC, id ASC").
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Limit(limit)
		if err := query.Find(&models).Error; err != nil {
			return fmt.Errorf("claim admin resource bulk commands: %w", err)
		}
		for i := range models {
			token := platform.NewUUIDV4String()
			if err := tx.Model(&AdminResourceBulkCommandModel{}).
				Where("id = ?", models[i].ID).
				UpdateColumns(map[string]any{
					"dispatch_token": token,
					"dispatched_at":  now,
					"updated_at":     gorm.Expr("updated_at"),
				}).Error; err != nil {
				return fmt.Errorf("fence admin resource bulk dispatch: %w", err)
			}
			models[i].DispatchToken = token
			models[i].DispatchedAt = &now
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := make([]coreapp.AdminResourceBulkCommand, len(models))
	for i := range models {
		mapped, err := adminBulkModelToDomain(&models[i])
		if err != nil {
			return nil, err
		}
		result[i] = mapped
	}
	return result, nil
}

func (r *AdminResourceBulkRepo) MarkRunning(ctx context.Context, id uint64, dispatchToken string) (*coreapp.AdminResourceBulkCommand, bool, error) {
	var result *coreapp.AdminResourceBulkCommand
	claimed := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var model AdminResourceBulkCommandModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND dispatch_token = ? AND status IN ?", id, dispatchToken, []string{"queued", "running"}).
			First(&model).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock admin resource bulk execution: %w", err)
		}
		now := time.Now().UTC()
		if model.Attempts >= model.MaxAttempts {
			return tx.Model(&AdminResourceBulkCommandModel{}).Where("id = ?", id).Updates(map[string]any{
				"status": "failed", "dispatch_token": "", "dispatched_at": nil,
				"finished_at": now, "last_safe_error": "Batch retry attempts exhausted.", "updated_at": now,
			}).Error
		}
		claimToken := platform.NewUUIDV4String()
		updates := map[string]any{
			"status": "running", "attempts": model.Attempts + 1, "claim_token": claimToken,
			"dispatch_token": "", "dispatched_at": nil, "last_safe_error": "", "updated_at": now,
		}
		if model.StartedAt == nil {
			updates["started_at"] = now
			model.StartedAt = &now
		}
		if err := tx.Model(&AdminResourceBulkCommandModel{}).Where("id = ? AND dispatch_token = ?", id, dispatchToken).Updates(updates).Error; err != nil {
			return fmt.Errorf("mark admin resource bulk running: %w", err)
		}
		model.Status = "running"
		model.Attempts++
		model.ClaimToken = claimToken
		model.DispatchToken = ""
		model.DispatchedAt = nil
		model.UpdatedAt = now
		mapped, err := adminBulkModelToDomain(&model)
		if err != nil {
			return err
		}
		result = &mapped
		claimed = true
		return nil
	})
	return result, claimed, err
}

func (r *AdminResourceBulkRepo) ListCandidateIDs(ctx context.Context, command *coreapp.AdminResourceBulkCommand, limit int, now time.Time) ([]uint, error) {
	if command == nil || limit <= 0 {
		return nil, nil
	}
	if command.Selection.Mode == coreapp.AdminResourceBulkIDs {
		result := make([]uint, 0, limit)
		for _, id := range command.Selection.ResourceIDs {
			if id <= command.CheckpointResourceID {
				continue
			}
			result = append(result, id)
			if len(result) == limit {
				break
			}
		}
		return result, nil
	}
	filter := command.Selection.Filter.ListFilter()
	query := r.read.adminMicrosoftFilterQuery(ctx, filter, now, "").
		Where("er.id > ? AND er.id <= ?", command.CheckpointResourceID, command.MaxResourceID).
		Select("er.id").Order("er.id ASC").Limit(limit)
	var ids []uint
	if err := query.Scan(&ids).Error; err != nil {
		return nil, fmt.Errorf("list admin resource bulk candidates: %w", err)
	}
	return ids, nil
}

func (r *AdminResourceBulkRepo) CompletePage(ctx context.Context, id uint64, claimToken string, checkpoint uint, matched, processed, affected, skipped int, reasons map[string]int64, done bool) error {
	reasonJSON, err := json.Marshal(reasons)
	if err != nil {
		return fmt.Errorf("marshal admin resource bulk reasons: %w", err)
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"checkpoint_resource_id": checkpoint,
		"matched_count":          gorm.Expr("matched_count + ?", matched),
		"processed_count":        gorm.Expr("processed_count + ?", processed),
		"affected_count":         gorm.Expr("affected_count + ?", affected),
		"skipped_count":          gorm.Expr("skipped_count + ?", skipped),
		"reason_buckets":         reasonJSON,
		"status":                 "queued",
		// Attempts is the consecutive failure budget, not a page counter.
		// Completing a page proves forward progress and starts a fresh budget.
		"attempts":        0,
		"claim_token":     "",
		"dispatch_token":  "",
		"dispatched_at":   nil,
		"last_safe_error": "",
		"updated_at":      now,
	}
	if done {
		updates["status"] = "succeeded"
		updates["finished_at"] = now
	}
	result := r.dbFor(ctx).Model(&AdminResourceBulkCommandModel{}).
		Where("id = ? AND status = ? AND claim_token = ?", id, "running", claimToken).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("complete admin resource bulk page: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return domain.ErrInvalidResourceStatus
	}
	return nil
}

func (r *AdminResourceBulkRepo) MarkRetryableFailure(ctx context.Context, id uint64, claimToken, safeError string) (bool, error) {
	exhausted := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var model AdminResourceBulkCommandModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND status = ? AND claim_token = ?", id, "running", claimToken).
			First(&model).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrInvalidResourceStatus
			}
			return err
		}
		now := time.Now().UTC()
		status := "queued"
		updates := map[string]any{
			"status": status, "claim_token": "", "dispatch_token": "", "dispatched_at": nil,
			"last_safe_error": safeValidationMessage(safeError), "updated_at": now,
		}
		if model.Attempts >= model.MaxAttempts {
			exhausted = true
			updates["status"] = "failed"
			updates["finished_at"] = now
		}
		return tx.Model(&AdminResourceBulkCommandModel{}).Where("id = ? AND claim_token = ?", id, claimToken).Updates(updates).Error
	})
	return exhausted, err
}

func (r *AdminResourceBulkRepo) MarkDispatchFailed(ctx context.Context, id uint64, dispatchToken, safeError string) error {
	return r.db.WithContext(ctx).Model(&AdminResourceBulkCommandModel{}).
		Where("id = ? AND dispatch_token = ? AND status IN ?", id, dispatchToken, []string{"queued", "running"}).
		UpdateColumns(map[string]any{
			"dispatch_token": "", "dispatched_at": nil,
			"last_safe_error": safeValidationMessage(safeError), "updated_at": gorm.Expr("updated_at"),
		}).Error
}

func findAdminBulkByIdempotencyTx(tx *gorm.DB, operatorUserID uint, key string) (*AdminResourceBulkCommandModel, error) {
	var model AdminResourceBulkCommandModel
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("operator_user_id = ? AND idempotency_key = ?", operatorUserID, key).
		First(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find admin bulk idempotency key: %w", err)
	}
	return &model, nil
}

func adminBulkModelToDomain(model *AdminResourceBulkCommandModel) (coreapp.AdminResourceBulkCommand, error) {
	var selection coreapp.AdminResourceBulkSelection
	if err := json.Unmarshal(model.SelectionJSON, &selection); err != nil {
		return coreapp.AdminResourceBulkCommand{}, fmt.Errorf("decode admin resource bulk selection: %w", err)
	}
	selection.ResourceIDs = append([]uint(nil), selection.ResourceIDs...)
	sort.Slice(selection.ResourceIDs, func(i, j int) bool { return selection.ResourceIDs[i] < selection.ResourceIDs[j] })
	reasons := map[string]int64{}
	if len(model.ReasonBuckets) > 0 && string(model.ReasonBuckets) != "null" {
		if err := json.Unmarshal(model.ReasonBuckets, &reasons); err != nil {
			return coreapp.AdminResourceBulkCommand{}, fmt.Errorf("decode admin resource bulk reasons: %w", err)
		}
	}
	return coreapp.AdminResourceBulkCommand{
		ID: model.ID, OperatorUserID: model.OperatorUserID, Action: coreapp.AdminResourceBulkAction(model.Action),
		Selection: selection, SelectionFingerprint: model.SelectionFingerprint, IdempotencyKey: model.IdempotencyKey,
		MaxResourceID: model.MaxResourceID, CheckpointResourceID: model.CheckpointResourceID,
		Status: model.Status, MatchedCount: model.MatchedCount, ProcessedCount: model.ProcessedCount,
		AffectedCount: model.AffectedCount, SkippedCount: model.SkippedCount, ReasonCounts: reasons,
		Attempts: model.Attempts, MaxAttempts: model.MaxAttempts, ClaimToken: model.ClaimToken, DispatchToken: model.DispatchToken,
		LastSafeError: model.LastSafeError, RequestID: model.RequestID, Path: model.Path,
		DispatchedAt: model.DispatchedAt, StartedAt: model.StartedAt, FinishedAt: model.FinishedAt,
		CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt,
	}, nil
}

var _ coreapp.AdminResourceBulkRepository = (*AdminResourceBulkRepo)(nil)
