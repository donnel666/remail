package infra

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	daumapp "github.com/donnel666/remail/internal/daum/app"
	"github.com/donnel666/remail/internal/daum/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const importBatchSize = 1000

type importRecord struct {
	ID                 uint64 `gorm:"primaryKey"`
	OwnerUserID        uint
	OperatorUserID     uint `gorm:"uniqueIndex:uq_daum_import_key"`
	Status             string
	ErrorStrategy      string
	LongLived          bool
	Protocol           string
	SourceObjectKey    string
	FailureObjectKey   string
	FileName           string
	RequestID          string
	IdempotencyKey     string `gorm:"uniqueIndex:uq_daum_import_key"`
	RequestFingerprint string
	Generation         uint64
	ClaimToken         string
	AcceptedCount      int
	ImportedCount      int
	SkippedCount       int
	FailedCount        int
	DispatchStatus     string
	DispatchAttempts   int
	Attempts           int
	MaxAttempts        int
	LastSafeError      string
	StartedAt          *time.Time
	FinishedAt         *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (importRecord) TableName() string { return "daum_resource_imports" }

type importItem struct {
	ID            uint64 `gorm:"primaryKey"`
	ImportID      uint64 `gorm:"uniqueIndex:uq_daum_import_line"`
	LineNumber    int    `gorm:"uniqueIndex:uq_daum_import_line"`
	ResourceID    *uint
	Outcome       string
	Category      string
	LastSafeError string
	CreatedAt     time.Time
}

func (importItem) TableName() string { return "daum_resource_import_items" }

type ImportStatus importRecord

func (ImportStatus) TableName() string { return "daum_resource_imports" }

type ImportItemStatus struct {
	LineNumber    int
	ResourceID    *uint
	Outcome       string
	Category      string
	LastSafeError string
}
type ImportItemsPage struct {
	Items []ImportItemStatus
	Total int64
}

func (s *Service) CreateImport(ctx context.Context, operator, owner uint, strategy, key string, content []byte) (uint64, bool, error) {
	return s.CreateImportWithArtifact(ctx, operator, owner, strategy, key, "inline://"+Fingerprint(owner, strategy, content), "inline.txt", "", content)
}
func (s *Service) CreateImportWithArtifact(ctx context.Context, operator, owner uint, strategy, key, objectKey, fileName, requestID string, content []byte) (uint64, bool, error) {
	return s.CreateImportWithOptions(ctx, operator, owner, strategy, key, objectKey, fileName, requestID, content, false)
}
func (s *Service) CreateImportWithOptions(ctx context.Context, operator, owner uint, strategy, key, objectKey, fileName, requestID string, content []byte, longLived bool, protocols ...string) (uint64, bool, error) {
	protocolName := "imap"
	if len(protocols) > 0 {
		protocolName = protocols[0]
	}
	if !validProtocol(protocolName) {
		return 0, false, domain.ErrInvalidResource
	}
	if operator == 0 || owner == 0 || strings.TrimSpace(key) == "" || len(key) > 128 || objectKey == "" || len(content) == 0 {
		return 0, false, domain.ErrInvalidResource
	}
	if strategy == "" {
		strategy = domain.ErrorStrategySkip
	}
	if strategy != domain.ErrorStrategySkip && strategy != domain.ErrorStrategyAbort {
		return 0, false, domain.ErrInvalidImportFormat
	}
	fp := Fingerprint(owner, fmt.Sprintf("%s:%t:%s", strategy, longLived, protocolName), content)
	var stored importRecord
	created := false
	err := s.transaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
		candidate := importRecord{OwnerUserID: owner, OperatorUserID: operator, SourceObjectKey: objectKey, FileName: fileName, RequestID: requestID, IdempotencyKey: key, RequestFingerprint: fp, Status: domain.ImportProcessing, ErrorStrategy: strategy, LongLived: longLived, Protocol: protocolName, Generation: 1, DispatchStatus: "pending", MaxAttempts: 3}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
		if result.Error != nil {
			return result.Error
		}
		created = result.RowsAffected == 1
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("operator_user_id = ? AND idempotency_key = ?", operator, key).Take(&stored).Error; err != nil {
			return err
		}
		if stored.RequestFingerprint != fp {
			return domain.ErrImportConflict
		}
		if created && s.OperationLogs != nil {
			return s.OperationLogs.Create(ctx, &governancedomain.OperationLog{OperatorUserID: operator, OperationType: "daum.resource.import", ResourceType: "daum_resource_import", ResourceID: fmt.Sprint(stored.ID), Result: "success", SafeSummary: "Daum import accepted.", RequestID: requestID})
		}
		return nil
	})
	return stored.ID, !created, err
}
func (s *Service) FindImportReplay(ctx context.Context, operator, owner uint, strategy, key string, content []byte, longLived bool, protocolName string) (*ImportStatus, error) {
	if s == nil || s.DB == nil {
		return nil, domain.ErrDependency
	}
	if strategy == "" {
		strategy = domain.ErrorStrategySkip
	}
	if !validProtocol(protocolName) || operator == 0 || owner == 0 || strings.TrimSpace(key) == "" || len(key) > 128 || (strategy != domain.ErrorStrategySkip && strategy != domain.ErrorStrategyAbort) {
		return nil, domain.ErrInvalidResource
	}
	var row ImportStatus
	err := s.dbFor(ctx).Where("operator_user_id = ? AND idempotency_key = ?", operator, key).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.RequestFingerprint != Fingerprint(owner, fmt.Sprintf("%s:%t:%s", strategy, longLived, protocolName), content) {
		return nil, domain.ErrImportConflict
	}
	return &row, nil
}

func (s *Service) claimImport(ctx context.Context, id, generation uint64) (*importRecord, error) {
	var rec importRecord
	err := s.transaction(ctx, func(_ context.Context, tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&rec).Error; err != nil {
			return err
		}
		if rec.Generation != generation || rec.Status != domain.ImportProcessing {
			return domain.ErrInvalidClaim
		}
		if rec.DispatchStatus != "pending" && rec.DispatchStatus != "queued" {
			return domain.ErrInvalidClaim
		}
		now := s.Now().UTC()
		rec.ClaimToken = platform.NewUUIDV7String()
		rec.StartedAt = &now
		rec.DispatchStatus = "running"
		return tx.Model(&importRecord{}).Where("id = ? AND generation = ?", id, generation).Updates(map[string]any{"attempts": rec.Attempts, "dispatch_status": "running", "claim_token": rec.ClaimToken, "started_at": now, "updated_at": now}).Error
	})
	return &rec, err
}

func lockImport(tx *gorm.DB, rec *importRecord) error {
	var current importRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND generation = ? AND claim_token = ? AND status = ? AND dispatch_status = 'running'", rec.ID, rec.Generation, rec.ClaimToken, domain.ImportProcessing).Take(&current).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrInvalidClaim
		}
		return err
	}
	return nil
}
func (s *Service) releaseImport(ctx context.Context, rec *importRecord) error {
	now := s.Now().UTC()
	updates := map[string]any{"dispatch_status": "pending", "claim_token": "", "generation": gorm.Expr("generation + 1"), "last_safe_error": "Import infrastructure is temporarily unavailable.", "updated_at": now}
	return s.dbFor(ctx).Model(&importRecord{}).Where("id = ? AND generation = ? AND claim_token = ? AND status = ? AND dispatch_status = 'running'", rec.ID, rec.Generation, rec.ClaimToken, domain.ImportProcessing).Updates(updates).Error
}

func (s *Service) ProcessImport(ctx context.Context, id, generation uint64, owner uint, strategy string, content []byte) error {
	rec, err := s.claimImport(ctx, id, generation)
	if errors.Is(err, domain.ErrInvalidClaim) {
		return nil
	}
	if err != nil {
		return err
	}
	if rec.OwnerUserID != owner || rec.ErrorStrategy != strategy {
		return domain.ErrInvalidClaim
	}
	return s.finishImportAttempt(ctx, rec, s.processClaimedImport(ctx, rec, content))
}

func (s *Service) finishImportAttempt(ctx context.Context, rec *importRecord, err error) error {
	if err != nil && !errors.Is(err, domain.ErrInvalidClaim) {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if failure, deterministic := deterministicImportFailure(err); deterministic {
			finishErr := s.finishImport(cleanup, rec, []domain.ImportLineError{failure}, true)
			if finishErr == nil {
				return nil
			}
			// A failure to save diagnostics is still an infrastructure error.
			err = errors.Join(err, finishErr)
		}
		err = errors.Join(err, s.releaseImport(cleanup, rec))
	}
	return err
}

func deterministicImportFailure(err error) (domain.ImportLineError, bool) {
	var lineError *domain.ImportLineError
	if errors.As(err, &lineError) {
		return *lineError, true
	}
	failure := domain.ImportLineError{Category: "invalid_format", SafeMessage: "Daum import entry violates an input requirement."}
	if errors.Is(err, domain.ErrInvalidImportFormat) || errors.Is(err, domain.ErrInvalidResource) {
		return failure, true
	}
	var databaseError *mysql.MySQLError
	if errors.As(err, &databaseError) && databaseError.Number == 1406 {
		failure.Category, failure.SafeMessage = "invalid_length", "Daum import entry exceeds a supported field length."
		return failure, true
	}
	return domain.ImportLineError{}, false
}

func (s *Service) processClaimedImport(ctx context.Context, rec *importRecord, content []byte) error {
	rows, failures, parseErr := ParseImport(string(content), rec.ErrorStrategy)
	for i := range rows {
		rows[i].Protocol = rec.Protocol
		if rows[i].Protocol == "" {
			rows[i].Protocol = "imap"
		}
	}
	if parseErr != nil {
		failures = []domain.ImportLineError{{Line: 0, Category: "invalid_format", SafeMessage: "Invalid Daum import format."}}
		var lineError *domain.ImportLineError
		if errors.As(parseErr, &lineError) {
			failures = []domain.ImportLineError{*lineError}
		}
		return s.finishImport(ctx, rec, failures, true)
	}
	var existingItems []importItem
	if err := s.dbFor(ctx).Where("import_id = ?", rec.ID).Find(&existingItems).Error; err != nil {
		return err
	}
	processed := map[int]bool{}
	accepted := 0
	for _, item := range existingItems {
		processed[item.LineNumber] = true
		if item.Outcome == "imported" || item.Outcome == "restored" {
			accepted++
		}
	}
	remaining := make([]domain.ImportLine, 0, len(rows))
	for start := 0; start < len(rows); start += importBatchSize {
		end := min(start+importBatchSize, len(rows))
		emails := make([]string, 0, end-start)
		for _, row := range rows[start:end] {
			if !processed[row.LineNumber] {
				emails = append(emails, row.Email)
			}
		}
		var duplicates []Resource
		if len(emails) > 0 {
			if err := s.dbFor(ctx).Select("email_address, owner_user_id, status").Where("email_address IN ?", emails).Find(&duplicates).Error; err != nil {
				return err
			}
		}
		seen := map[string]Resource{}
		for _, row := range duplicates {
			seen[strings.ToLower(row.EmailAddress)] = row
		}
		for _, row := range rows[start:end] {
			if processed[row.LineNumber] {
				continue
			}
			if existing, exists := seen[row.Email]; exists && existing.OwnerUserID != rec.OwnerUserID && existing.Status != domain.StatusDeleted {
				failures = append(failures, domain.ImportLineError{Line: row.LineNumber, Email: row.Email, Category: "owner_conflict", SafeMessage: "Daum account belongs to another owner."})
			} else if exists && existing.Status != domain.StatusDeleted {
				failures = append(failures, domain.ImportLineError{Line: row.LineNumber, Email: row.Email, Category: "duplicate_email", SafeMessage: "Email address already exists."})
			} else {
				remaining = append(remaining, row)
			}
		}
	}
	if rec.ErrorStrategy == domain.ErrorStrategyAbort && len(failures) > 0 {
		return s.finishImport(ctx, rec, failures[:1], true)
	}
	accepted += len(remaining)
	if err := s.transaction(ctx, func(_ context.Context, tx *gorm.DB) error {
		if err := lockImport(tx, rec); err != nil {
			return err
		}
		return tx.Model(&importRecord{}).Where("id = ?", rec.ID).Update("accepted_count", accepted).Error
	}); err != nil {
		return err
	}
	for start := 0; start < len(remaining); start += importBatchSize {
		chunk := remaining[start:min(start+importBatchSize, len(remaining))]
		if err := s.transaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
			if err := lockImport(tx, rec); err != nil {
				return err
			}
			for _, row := range chunk {
				var id uint
				outcome := "imported"
				if err := s.importLineTx(ctx, tx, rec.OwnerUserID, row, &id, &outcome); err != nil {
					if failure, deterministic := deterministicImportFailure(err); deterministic {
						failure.Line, failure.Email = row.LineNumber, row.Email
						return errors.Join(err, &failure)
					}
					return err
				}
				if outcome == "skipped" && rec.ErrorStrategy == domain.ErrorStrategyAbort {
					return domain.ErrResourceConflict
				}
				item := importItem{ImportID: rec.ID, LineNumber: row.LineNumber, Outcome: outcome}
				if id > 0 {
					item.ResourceID = &id
					if err := tx.Model(&Resource{}).Where("id = ? AND ? <> ?", id, outcome, "skipped").Update("long_lived", rec.LongLived).Error; err != nil {
						return err
					}
				} else {
					item.Category = "duplicate_email"
					item.LastSafeError = "Email address already exists."
				}
				if err := tx.Create(&item).Error; err != nil {
					return err
				}
			}
			return updateImportCounts(tx, rec.ID, s.Now().UTC())
		}); err != nil {
			return err
		}
	}
	return s.finishImport(ctx, rec, failures, false)
}
func updateImportCounts(tx *gorm.DB, id uint64, now time.Time) error {
	var counts struct{ Imported, Skipped, Failed int }
	if err := tx.Model(&importItem{}).Select("COALESCE(SUM(CASE WHEN outcome IN ('imported','restored') THEN 1 ELSE 0 END),0) AS imported, COALESCE(SUM(CASE WHEN outcome = 'skipped' THEN 1 ELSE 0 END),0) AS skipped, COALESCE(SUM(CASE WHEN outcome = 'failed' THEN 1 ELSE 0 END),0) AS failed").Where("import_id = ?", id).Scan(&counts).Error; err != nil {
		return err
	}
	return tx.Model(&importRecord{}).Where("id = ?", id).Updates(map[string]any{"imported_count": counts.Imported, "skipped_count": counts.Skipped, "failed_count": counts.Failed, "updated_at": now}).Error
}
func (s *Service) finishImport(ctx context.Context, rec *importRecord, failures []domain.ImportLineError, failed bool) error {
	key := ""
	if len(failures) > 0 {
		if s.Files == nil {
			return domain.ErrDependency
		}
		var buf bytes.Buffer
		writer := csv.NewWriter(&buf)
		_ = writer.Write([]string{"line", "email", "category", "message"})
		for _, failure := range failures {
			email := failure.Email
			if strings.HasPrefix(email, "=") || strings.HasPrefix(email, "+") || strings.HasPrefix(email, "-") || strings.HasPrefix(email, "@") {
				email = "'" + email
			}
			_ = writer.Write([]string{strconv.Itoa(failure.Line), email, failure.Category, failure.SafeMessage})
		}
		writer.Flush()
		if writer.Error() != nil {
			return writer.Error()
		}
		key = fmt.Sprintf("private/daum/imports/%d/%d/failures-%d.csv", rec.OwnerUserID, rec.ID, rec.Generation)
		if _, err := s.Files.SavePrivate(ctx, governancedomain.PrivateFile{ObjectKey: key, FileName: "daum-import-failures.csv", ContentType: "text/csv; charset=utf-8", ContentBytes: buf.Bytes()}); err != nil {
			return err
		}
	}
	return s.transaction(ctx, func(_ context.Context, tx *gorm.DB) error {
		if err := lockImport(tx, rec); err != nil {
			return err
		}
		for _, failure := range failures {
			if failure.Line <= 0 {
				continue
			}
			outcome := "skipped"
			if failed {
				outcome = "failed"
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&importItem{ImportID: rec.ID, LineNumber: failure.Line, Outcome: outcome, Category: failure.Category, LastSafeError: failure.SafeMessage}).Error; err != nil {
				return err
			}
		}
		now := s.Now().UTC()
		if err := updateImportCounts(tx, rec.ID, now); err != nil {
			return err
		}
		status, dispatch, safe := domain.ImportImported, "succeeded", ""
		if failed {
			status, dispatch, safe = domain.ImportFailed, "failed", "Daum import rejected; see safe failure details."
		}
		return tx.Model(&importRecord{}).Where("id = ? AND claim_token = ?", rec.ID, rec.ClaimToken).Updates(map[string]any{"status": status, "dispatch_status": dispatch, "claim_token": "", "failure_object_key": key, "last_safe_error": safe, "finished_at": now, "updated_at": now}).Error
	})
}
func (s *Service) ProcessImportTask(ctx context.Context, id, generation uint64) ([]uint, error) {
	if s == nil || s.DB == nil {
		return nil, domain.ErrDependency
	}
	var rec importRecord
	if err := s.dbFor(ctx).Where("id = ? AND generation = ?", id, generation).Take(&rec).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if rec.Status == domain.ImportImported || rec.Status == domain.ImportFailed {
		return s.ListImportResourceIDs(ctx, id)
	}
	claimed, err := s.claimImport(ctx, id, generation)
	if errors.Is(err, domain.ErrInvalidClaim) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if s.Files == nil {
		return nil, s.finishImportAttempt(ctx, claimed, domain.ErrDependency)
	}
	source, err := s.Files.ReadPrivate(ctx, rec.SourceObjectKey)
	if err != nil {
		return nil, s.finishImportAttempt(ctx, claimed, err)
	}
	if source == nil {
		return nil, s.finishImportAttempt(ctx, claimed, domain.ErrDependency)
	}
	if err := s.finishImportAttempt(ctx, claimed, s.processClaimedImport(ctx, claimed, source.ContentBytes)); err != nil {
		return nil, err
	}
	return s.ListImportResourceIDs(ctx, id)
}
func (s *Service) ListImportResourceIDs(ctx context.Context, id uint64) ([]uint, error) {
	var ids []uint
	err := s.dbFor(ctx).Model(&importItem{}).Where("import_id = ? AND resource_id IS NOT NULL AND outcome IN ('imported','restored')", id).Order("line_number ASC").Pluck("resource_id", &ids).Error
	return ids, err
}
func (s *Service) ListImportItems(ctx context.Context, id uint64, offset, limit int) (*ImportItemsPage, error) {
	if id == 0 || offset < 0 || limit < 1 || limit > 200 {
		return nil, domain.ErrInvalidResource
	}
	page := &ImportItemsPage{Items: []ImportItemStatus{}}
	q := s.dbFor(ctx).Model(&importItem{}).Where("import_id = ?", id)
	if err := q.Count(&page.Total).Error; err != nil {
		return nil, err
	}
	if err := q.Select("line_number, resource_id, outcome, category, last_safe_error").Order("line_number ASC").Offset(offset).Limit(limit).Find(&page.Items).Error; err != nil {
		return nil, err
	}
	return page, nil
}
func (s *Service) DispatchPendingImports(ctx context.Context, q daumapp.Queue, limit int) error {
	if s == nil || s.DB == nil || q == nil {
		return domain.ErrDependency
	}
	limit = min(max(limit, 1), 100)
	now := s.Now().UTC()
	if err := s.dbFor(ctx).Model(&importRecord{}).Where("status = ? AND dispatch_status IN ? AND updated_at < ?", domain.ImportProcessing, []string{"queued", "running"}, now.Add(-35*time.Minute)).Updates(map[string]any{"dispatch_status": "pending", "claim_token": "", "generation": gorm.Expr("generation + 1"), "updated_at": now}).Error; err != nil {
		return err
	}
	var rows []importRecord
	if err := s.dbFor(ctx).Where("status = ? AND dispatch_status = 'pending'", domain.ImportProcessing).Order("id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return err
	}
	var result error
	for _, row := range rows {
		if err := daumapp.EnqueueImport(ctx, q, daumapp.ImportTaskPayload{ImportID: row.ID, Generation: row.Generation}); err != nil {
			result = errors.Join(result, err)
			continue
		}
		if err := s.dbFor(ctx).Model(&importRecord{}).Where("id = ? AND generation = ? AND status = ? AND dispatch_status = 'pending'", row.ID, row.Generation, domain.ImportProcessing).Updates(map[string]any{"dispatch_status": "queued", "dispatch_attempts": gorm.Expr("dispatch_attempts + 1"), "updated_at": now}).Error; err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (s *Service) ReleaseImportAssignment(ctx context.Context, id, generation uint64) error {
	return s.dbFor(ctx).Model(&importRecord{}).Where("id = ? AND generation = ? AND status = ? AND dispatch_status IN ?", id, generation, domain.ImportProcessing, []string{"queued", "running"}).Updates(map[string]any{"dispatch_status": "pending", "claim_token": "", "generation": gorm.Expr("generation + 1"), "updated_at": s.Now().UTC()}).Error
}
