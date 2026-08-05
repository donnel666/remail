package gmail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	gmailResourceImportAcceptanceTTL   = 10 * time.Minute
	gmailResourceImportTaskTimeout     = 30 * time.Minute
	gmailResourceImportActivationDelay = time.Second
	gmailResourceImportQueueLease      = 2 * time.Minute
	gmailResourceImportRunningLease    = gmailResourceImportTaskTimeout + 5*time.Minute
	gmailResourceImportBatchSize       = 500
	gmailResourceImportMaxAttempts     = 3
	gmailResourceImportDefaultMaxBytes = 100 * 1024 * 1024
	gmailResourceImportMaxBytes        = 512 * 1024 * 1024
	gmailResourceImportMultipartBytes  = 1 * 1024 * 1024

	gmailResourceImportRedisPrefix = "remail:{gmail-resource-import}:"
	gmailResourceImportSequenceKey = gmailResourceImportRedisPrefix + "sequence"
	gmailResourceImportDispatchKey = gmailResourceImportRedisPrefix + "dispatch"
)

var (
	ErrGmailImportConflict       = errors.New("gmail: resource import idempotency conflict")
	ErrGmailImportDependency     = errors.New("gmail: resource import dependency unavailable")
	ErrGmailImportInvalidOwner   = errors.New("gmail: invalid resource import owner")
	ErrGmailImportInvalidClaim   = errors.New("gmail: resource import claim is no longer valid")
	ErrGmailImportNotFound       = errors.New("gmail: resource import not found")
	ErrGmailImportStorage        = errors.New("gmail: resource import storage unavailable")
	ErrGmailImportInvalidCommand = errors.New("gmail: invalid resource import command")
	ErrGmailImportTemporary      = errors.New("gmail: resource import temporarily unavailable")
)

type gmailResourceImportTask struct {
	ImportID   uint64 `json:"importId"`
	Generation uint64 `json:"generation"`
}

type ResourceImportStatusView struct {
	ImportID      uint64
	Status        string
	Accepted      int
	Imported      int
	Skipped       int
	TaskStatus    string
	Attempts      int
	MaxAttempts   int
	StartedAt     *time.Time
	FinishedAt    *time.Time
	LastSafeError string
	RequestID     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type gmailResourceImportRecord struct {
	ResourceImportStatusView
	OwnerUserID         uint
	OperatorUserID      uint
	SourceObjectKey     string
	FailureObjectKey    string
	ErrorStrategy       string
	RequestFingerprint  string
	Generation          uint64
	ClaimToken          string
	PreparingGeneration uint64
	Prepared            bool
	PreparedAccepted    int
	PreparedImported    int
	PreparedSkipped     int
}

type gmailResourceImportFailure struct {
	Line        int    `json:"line"`
	Email       string `json:"-"`
	Category    string `json:"category"`
	SafeMessage string `json:"lastSafeError"`
}

type gmailResourceImportItem struct {
	LineNumber    int    `json:"lineNumber"`
	ResourceID    uint   `json:"resourceId,omitempty"`
	Outcome       string `json:"outcome"`
	Category      string `json:"category,omitempty"`
	LastSafeError string `json:"lastSafeError,omitempty"`
}

type gmailResourceImportWrite struct {
	ResourceID uint
	Restored   bool
}

func maxGmailResourceImportBytesValue() int64 {
	return int64(min(runtimeconfig.Int("resource_import_max_bytes", gmailResourceImportDefaultMaxBytes, 1), gmailResourceImportMaxBytes))
}

func gmailResourceImportMultipartMaxBytes(fileMax int64) int64 {
	return fileMax + gmailResourceImportMultipartBytes
}

func (s *Service) validateGmailResourceImportOwner(ctx context.Context, ownerUserID uint) error {
	if s == nil || s.validateImportOwner == nil || ownerUserID == 0 {
		return ErrGmailImportDependency
	}
	valid, err := s.validateImportOwner(ctx, ownerUserID)
	if err != nil {
		return fmt.Errorf("validate Gmail import owner: %w", ErrGmailImportDependency)
	}
	if !valid {
		return ErrGmailImportInvalidOwner
	}
	return nil
}

func (s *Service) AcceptAdminGmailTXTFile(
	ctx context.Context,
	operatorUserID uint,
	ownerUserID uint,
	fileName string,
	content []byte,
	errorStrategy string,
	idempotencyKey string,
	requestID string,
	pathValue string,
) (*ResourceImportStatusView, bool, error) {
	if s == nil || s.redis == nil || s.queue == nil || s.files == nil {
		return nil, false, ErrGmailImportDependency
	}
	if operatorUserID == 0 || ownerUserID == 0 || len(content) == 0 {
		return nil, false, ErrGmailImportInvalidCommand
	}
	strategy, ok := normalizeGmailImportErrorStrategy(errorStrategy)
	if !ok {
		return nil, false, ErrGmailImportInvalidCommand
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		return nil, false, ErrGmailImportInvalidCommand
	}
	if err := s.validateGmailResourceImportOwner(ctx, ownerUserID); err != nil {
		return nil, false, err
	}

	fingerprint := gmailResourceImportFingerprint(ownerUserID, strategy, content)
	if existing, found, err := s.findGmailResourceImportByIdempotency(ctx, operatorUserID, idempotencyKey, fingerprint); err != nil || found {
		return existing, found, err
	}

	now := s.now().UTC()
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		requestID = platform.NewUUIDV7String()
	}
	sourceObjectKey := gmailResourceImportObjectKey("source", ownerUserID, now, requestID, ".txt")
	storedSource, err := s.files.SavePrivate(ctx, governancedomain.PrivateFile{
		ObjectKey: sourceObjectKey, FileName: cleanGmailResourceImportFileName(fileName),
		ContentType: "text/plain; charset=utf-8", ContentBytes: content,
	})
	if err != nil {
		return nil, false, ErrGmailImportStorage
	}

	importID, created, storedFingerprint, err := s.createGmailResourceImportState(ctx, gmailResourceImportCreateInput{
		OperatorUserID: operatorUserID, OwnerUserID: ownerUserID,
		SourceObjectKey: storedSource.ObjectKey, ErrorStrategy: strategy,
		RequestFingerprint: fingerprint, IdempotencyKey: idempotencyKey,
		RequestID: requestID, Path: strings.TrimSpace(pathValue), FileName: cleanGmailResourceImportFileName(fileName),
		CreatedAt: now,
	})
	if err != nil {
		_ = s.files.DeletePrivate(ctx, storedSource.ObjectKey)
		return nil, false, err
	}
	if !created {
		_ = s.files.DeletePrivate(ctx, storedSource.ObjectKey)
		if storedFingerprint != fingerprint {
			return nil, false, ErrGmailImportConflict
		}
		view, statusErr := s.GetAdminGmailResourceImport(ctx, importID)
		return view, true, statusErr
	}

	if s.logs == nil {
		if s.rollbackGmailResourceImportAcceptance(ctx, importID, operatorUserID, idempotencyKey) {
			_ = s.files.DeletePrivate(ctx, storedSource.ObjectKey)
		}
		return nil, false, ErrGmailImportDependency
	}
	if logErr := s.logs.Create(ctx, &governancedomain.OperationLog{
		OperatorUserID: operatorUserID, OperationType: "gmail.admin_resource.import",
		ResourceType: "gmail_resource_import", ResourceID: fmt.Sprintf("gmail-import:%d", importID),
		Path: strings.TrimSpace(pathValue), Result: "success",
		SafeSummary: "Gmail resource import accepted.", RequestID: requestID,
	}); logErr != nil {
		if s.rollbackGmailResourceImportAcceptance(ctx, importID, operatorUserID, idempotencyKey) {
			_ = s.files.DeletePrivate(ctx, storedSource.ObjectKey)
		}
		return nil, false, fmt.Errorf("write Gmail import acceptance audit: %w", ErrGmailImportDependency)
	}
	if err := s.activateGmailResourceImportState(ctx, importID, operatorUserID, idempotencyKey, now); err != nil {
		return nil, false, err
	}
	if err := s.DispatchGmailResourceImports(ctx, 100); err != nil {
		slog.Warn("Gmail resource import dispatcher failed", "import_id", importID, "error", err)
	}
	view, err := s.GetAdminGmailResourceImport(ctx, importID)
	return view, false, err
}

func (s *Service) GetAdminGmailResourceImport(ctx context.Context, importID uint64) (*ResourceImportStatusView, error) {
	record, err := s.gmailResourceImportRecord(ctx, importID)
	if err != nil {
		return nil, err
	}
	if record.Status == "accepting" {
		return nil, ErrGmailImportDependency
	}
	view := record.ResourceImportStatusView
	return &view, nil
}

func (s *Service) DispatchGmailResourceImports(ctx context.Context, limit int) error {
	if s == nil || s.redis == nil || s.queue == nil {
		return ErrGmailImportDependency
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	now := s.now().UTC()
	ids, err := s.redis.ZRangeByScore(ctx, gmailResourceImportDispatchKey, &redis.ZRangeBy{
		Min: "-inf", Max: strconv.FormatInt(now.UnixMilli(), 10), Count: int64(limit),
	}).Result()
	if err != nil {
		return fmt.Errorf("list pending Gmail resource imports: %w", err)
	}
	var result error
	for _, rawID := range ids {
		importID, parseErr := strconv.ParseUint(rawID, 10, 64)
		if parseErr != nil || importID == 0 {
			_ = s.redis.ZRem(ctx, gmailResourceImportDispatchKey, rawID).Err()
			continue
		}
		record, recordErr := s.gmailResourceImportRecord(ctx, importID)
		if errors.Is(recordErr, ErrGmailImportNotFound) {
			_ = s.redis.ZRem(ctx, gmailResourceImportDispatchKey, rawID).Err()
			continue
		}
		if recordErr != nil {
			result = errors.Join(result, recordErr)
			continue
		}
		if record.Status != "processing" {
			_ = s.redis.ZRem(ctx, gmailResourceImportDispatchKey, rawID).Err()
			continue
		}
		if record.TaskStatus == "running" {
			recovered, recoverErr := s.recoverStaleGmailResourceImport(ctx, record.ImportID, record.Generation, now)
			if recoverErr != nil {
				result = errors.Join(result, recoverErr)
				continue
			}
			if !recovered {
				continue
			}
			record, recordErr = s.gmailResourceImportRecord(ctx, importID)
			if recordErr != nil {
				result = errors.Join(result, recordErr)
				continue
			}
		}
		if err := s.enqueueGmailResourceImport(ctx, gmailResourceImportTask{ImportID: record.ImportID, Generation: record.Generation}); err != nil {
			result = errors.Join(result, err)
			continue
		}
		if err := s.markGmailResourceImportQueued(ctx, record.ImportID, record.Generation, now); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (s *Service) ProcessGmailResourceImport(ctx context.Context, task gmailResourceImportTask) error {
	if s == nil || s.db == nil || s.redis == nil || s.files == nil {
		return ErrGmailImportDependency
	}
	if task.ImportID == 0 || task.Generation == 0 {
		return ErrGmailImportInvalidCommand
	}
	record, err := s.gmailResourceImportRecord(ctx, task.ImportID)
	if errors.Is(err, ErrGmailImportNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if record.Status != "processing" {
		return nil
	}
	claimToken, claimed, err := s.markGmailResourceImportRunning(ctx, task, s.now().UTC())
	if err != nil {
		return err
	}
	if !claimed {
		current, findErr := s.gmailResourceImportRecord(ctx, task.ImportID)
		if findErr != nil {
			return findErr
		}
		if current.Status == "processing" && current.Generation == task.Generation &&
			current.TaskStatus != "failed" && current.TaskStatus != "succeeded" {
			if current.Prepared {
				finalized, finalizeErr := s.finalizePreparedGmailResourceImport(ctx, current, task.Generation, current.ClaimToken)
				if finalizeErr != nil {
					return finalizeErr
				}
				if finalized {
					return nil
				}
			}
			return ErrGmailImportTemporary
		}
		return nil
	}
	record.ClaimToken = claimToken
	if record.Prepared {
		finalized, finalizeErr := s.finalizePreparedGmailResourceImport(ctx, record, task.Generation, claimToken)
		if finalizeErr != nil {
			return finalizeErr
		}
		if finalized {
			return nil
		}
	}
	if record.Prepared || record.PreparingGeneration != 0 {
		if err := s.clearPreparedGmailResourceImport(ctx, record.ImportID, task.Generation, claimToken); err != nil {
			return err
		}
	}

	source, err := s.files.ReadPrivate(ctx, record.SourceObjectKey)
	if err != nil {
		return ErrGmailImportStorage
	}
	lines, failures, fatalFailure := parseGmailResourceImport(string(source.ContentBytes), record.ErrorStrategy)
	if fatalFailure != nil {
		return s.failGmailResourceImport(ctx, record, task.Generation, claimToken, []gmailResourceImportFailure{*fatalFailure})
	}

	lines, duplicateFailures, duplicateFatal := deduplicateGmailResourceImportLines(lines, record.ErrorStrategy)
	if duplicateFatal != nil {
		return s.failGmailResourceImport(ctx, record, task.Generation, claimToken, []gmailResourceImportFailure{*duplicateFatal})
	}
	failures = append(failures, duplicateFailures...)

	lines, existingFailures, existingFatal, err := s.removeExistingGmailImportLines(ctx, lines, record.ErrorStrategy)
	if err != nil {
		return err
	}
	if existingFatal != nil {
		return s.failGmailResourceImport(ctx, record, task.Generation, claimToken, []gmailResourceImportFailure{*existingFatal})
	}
	failures = append(failures, existingFailures...)
	if len(lines) == 0 && len(failures) == 0 {
		return s.failGmailResourceImport(ctx, record, task.Generation, claimToken, []gmailResourceImportFailure{{
			Category: "invalid_format", SafeMessage: "Invalid import format.",
		}})
	}

	failureObjectKey, summary, err := s.saveGmailResourceImportFailures(ctx, record, failures)
	if err != nil {
		return err
	}
	// Persist the safe row result before MySQL commits. A Redis failure rolls
	// the transaction back; a commit followed by a Redis finish failure is
	// finalized from this prepared result without recreating Gmail resources.
	writes, err := s.createGmailResourcesForImport(ctx, record.OwnerUserID, record.RequestID, lines, func(created []gmailResourceImportWrite) error {
		items := gmailResourceImportResultItems(lines, created, failures)
		return s.prepareGmailResourceImportResult(
			ctx, record.ImportID, task.Generation, claimToken, items,
			len(lines), len(created), len(failures), failureObjectKey, summary,
		)
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return s.failGmailResourceImport(ctx, record, task.Generation, claimToken, []gmailResourceImportFailure{{
				Category: "duplicate_email", SafeMessage: "An email address in the import already exists.",
			}})
		}
		return err
	}
	if err := s.finishPreparedGmailResourceImport(ctx, record.ImportID, task.Generation, claimToken, s.now().UTC()); err != nil {
		return err
	}
	if len(writes) > 0 {
		if err := s.scheduleLocalResourceValidationDispatcher(ctx, 0); err != nil {
			slog.Warn("wake Gmail validation dispatcher after import failed", "import_id", record.ImportID, "error", err)
		}
	}
	return nil
}

func (s *Service) ReleaseGmailResourceImport(ctx context.Context, task gmailResourceImportTask, safeError string) error {
	if s == nil || s.redis == nil || task.ImportID == 0 || task.Generation == 0 {
		return ErrGmailImportInvalidClaim
	}
	now := s.now().UTC()
	result, err := gmailResourceImportReleaseScript.Run(ctx, s.redis,
		[]string{gmailResourceImportStatusKey(task.ImportID), gmailResourceImportDispatchKey},
		strconv.FormatUint(task.ImportID, 10), task.Generation, strings.TrimSpace(safeError), now.UnixMilli(),
	).Int64()
	if err != nil {
		return fmt.Errorf("release Gmail resource import: %w", err)
	}
	if result == 0 {
		return ErrGmailImportInvalidClaim
	}
	return nil
}

func parseGmailResourceImport(content, strategy string) ([]localResourceImportLine, []gmailResourceImportFailure, *gmailResourceImportFailure) {
	strategy, ok := normalizeGmailImportErrorStrategy(strategy)
	if !ok || strings.TrimSpace(content) == "" {
		failure := gmailResourceImportFailure{Category: "invalid_format", SafeMessage: "Invalid import format."}
		return nil, nil, &failure
	}
	var lines []localResourceImportLine
	var failures []gmailResourceImportFailure
	for index, raw := range strings.Split(content, "\n") {
		raw = strings.TrimSuffix(raw, "\r")
		if strings.TrimSpace(raw) == "" {
			continue
		}
		line, valid := parseLocalResourceImportLine(raw)
		if !valid {
			failure := gmailResourceImportFailure{
				Line: index + 1, Email: gmailResourceImportFailureEmail(raw),
				Category: "invalid_format", SafeMessage: "Invalid import format.",
			}
			if strategy == "abort" {
				return nil, nil, &failure
			}
			failures = append(failures, failure)
			continue
		}
		line.lineNumber = index + 1
		lines = append(lines, line)
	}
	return lines, failures, nil
}

func deduplicateGmailResourceImportLines(lines []localResourceImportLine, strategy string) ([]localResourceImportLine, []gmailResourceImportFailure, *gmailResourceImportFailure) {
	seen := make(map[string]int, len(lines))
	result := make([]localResourceImportLine, 0, len(lines))
	var failures []gmailResourceImportFailure
	for _, line := range lines {
		if firstLine, exists := seen[line.identity]; exists {
			failure := gmailResourceImportFailure{
				Line: line.lineNumber, Email: line.email, Category: "duplicate_email",
				SafeMessage: fmt.Sprintf("Duplicate email address in import file; first occurrence is line %d.", firstLine),
			}
			if strategy == "abort" {
				return nil, nil, &failure
			}
			failures = append(failures, failure)
			continue
		}
		seen[line.identity] = line.lineNumber
		result = append(result, line)
	}
	return result, failures, nil
}

func (s *Service) removeExistingGmailImportLines(ctx context.Context, lines []localResourceImportLine, strategy string) ([]localResourceImportLine, []gmailResourceImportFailure, *gmailResourceImportFailure, error) {
	if len(lines) == 0 {
		return lines, nil, nil, nil
	}
	identities := make([]string, len(lines))
	for i := range lines {
		identities[i] = lines[i].identity
	}
	var existing []struct {
		Identity string `gorm:"column:identity"`
		Status   string `gorm:"column:status"`
	}
	if err := s.dbFor(ctx).Model(&localResourceModel{}).Select("identity, status").Where("identity IN ?", identities).Find(&existing).Error; err != nil {
		return nil, nil, nil, fmt.Errorf("find existing Gmail import identities: %w", err)
	}
	existingStatus := make(map[string]string, len(existing))
	for _, item := range existing {
		existingStatus[item.Identity] = item.Status
	}
	result := make([]localResourceImportLine, 0, len(lines))
	var failures []gmailResourceImportFailure
	for _, line := range lines {
		if status, exists := existingStatus[line.identity]; exists && status != LocalResourceDeleted {
			failure := gmailResourceImportFailure{
				Line: line.lineNumber, Email: line.email,
				Category: "duplicate_email", SafeMessage: "Email address already exists.",
			}
			if strategy == "abort" {
				return nil, nil, &failure, nil
			}
			failures = append(failures, failure)
			continue
		}
		result = append(result, line)
	}
	return result, failures, nil, nil
}

func gmailResourceImportResultItems(
	lines []localResourceImportLine,
	writes []gmailResourceImportWrite,
	failures []gmailResourceImportFailure,
) []gmailResourceImportItem {
	items := make([]gmailResourceImportItem, 0, len(writes)+len(failures))
	for i, write := range writes {
		outcome := "imported"
		if write.Restored {
			outcome = "restored"
		}
		items = append(items, gmailResourceImportItem{
			LineNumber: lines[i].lineNumber, ResourceID: write.ResourceID, Outcome: outcome,
		})
	}
	for _, failure := range failures {
		if failure.Line <= 0 {
			continue
		}
		items = append(items, gmailResourceImportItem{
			LineNumber: failure.Line, Outcome: "skipped", Category: failure.Category, LastSafeError: failure.SafeMessage,
		})
	}
	return items
}

func (s *Service) createGmailResourcesForImport(
	ctx context.Context,
	ownerUserID uint,
	requestID string,
	lines []localResourceImportLine,
	beforeCommit func([]gmailResourceImportWrite) error,
) ([]gmailResourceImportWrite, error) {
	if len(lines) == 0 {
		if beforeCommit != nil {
			return nil, beforeCommit(nil)
		}
		return nil, nil
	}
	writes := make([]gmailResourceImportWrite, len(lines))
	now := s.now().UTC()
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		identities := make([]string, len(lines))
		for i := range lines {
			identities[i] = lines[i].identity
		}
		var candidates []localResourceModel
		if err := tx.Where("identity IN ?", identities).Order("id ASC").Find(&candidates).Error; err != nil {
			return fmt.Errorf("find existing Gmail import resources: %w", err)
		}
		existingIDs := make([]uint, 0, len(candidates))
		for _, item := range candidates {
			existingIDs = append(existingIDs, item.ID)
		}
		sort.Slice(existingIDs, func(i, j int) bool { return existingIDs[i] < existingIDs[j] })
		existingByIdentity := make(map[string]localResourceModel, len(candidates))
		if len(existingIDs) > 0 {
			var roots []resourceRootModel
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id IN ? AND type = ?", existingIDs, "gmail").Order("id ASC").Find(&roots).Error; err != nil {
				return fmt.Errorf("lock deleted Gmail import roots: %w", err)
			}
			if len(roots) != len(existingIDs) {
				return ErrLocalResourceMissing
			}
			var existing []localResourceModel
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id IN ?", existingIDs).Order("id ASC").Find(&existing).Error; err != nil {
				return fmt.Errorf("lock deleted Gmail import resources: %w", err)
			}
			if len(existing) != len(existingIDs) {
				return ErrLocalResourceMissing
			}
			for _, item := range existing {
				if item.Status != LocalResourceDeleted {
					return gorm.ErrDuplicatedKey
				}
				existingByIdentity[item.Identity] = item
			}
		}

		newIndexes := make([]int, 0, len(lines)-len(existingByIdentity))
		for i, line := range lines {
			item, restored := existingByIdentity[line.identity]
			if !restored {
				newIndexes = append(newIndexes, i)
				continue
			}
			rootResult := tx.Model(&resourceRootModel{}).
				Where("id = ? AND type = ?", item.ID, "gmail").
				Updates(map[string]any{
					"owner_user_id": ownerUserID, "version": gorm.Expr("version + 1"), "updated_at": now,
				})
			if rootResult.Error != nil {
				return fmt.Errorf("restore deleted Gmail resource root: %w", rootResult.Error)
			}
			if rootResult.RowsAffected != 1 {
				return ErrLocalResourceMissing
			}
			resourceResult := tx.Model(&localResourceModel{}).
				Where("id = ? AND status = ?", item.ID, LocalResourceDeleted).
				Updates(map[string]any{
					"resource_type": "gmail", "owner_user_id": ownerUserID,
					"email": line.email, "identity": line.identity, "password": line.password,
					"two_factor_secret": line.twoFactorSecret, "app_password": line.appPassword,
					"credential_revision":   gorm.Expr("CASE WHEN credential_revision < 1 THEN 1 ELSE credential_revision + 1 END"),
					"credential_updated_at": now, "for_sale": false,
					"status":                LocalResourcePending,
					"validation_generation": gorm.Expr("CASE WHEN validation_generation < 1 THEN 1 ELSE validation_generation + 1 END"),
					"validation_failures":   0, "validation_request_id": strings.TrimSpace(requestID), "validation_command_hash": "",
					"alloc_bucket": uint16(item.ID % 2048), "last_allocated_at": nil,
					"last_safe_error": "", "last_checked_at": nil,
					"updated_at": now,
				})
			if resourceResult.Error != nil {
				return fmt.Errorf("restore deleted Gmail resource: %w", resourceResult.Error)
			}
			if resourceResult.RowsAffected != 1 {
				return gorm.ErrDuplicatedKey
			}
			writes[i] = gmailResourceImportWrite{ResourceID: item.ID, Restored: true}
		}

		if len(newIndexes) > 0 {
			roots := make([]resourceRootModel, len(newIndexes))
			for i := range roots {
				roots[i] = resourceRootModel{Type: "gmail", OwnerUserID: ownerUserID, Version: 1, CreatedAt: now, UpdatedAt: now}
			}
			if err := tx.CreateInBatches(&roots, gmailResourceImportBatchSize).Error; err != nil {
				return fmt.Errorf("create Gmail import roots: %w", err)
			}
			resources := make([]localResourceModel, len(newIndexes))
			for i, lineIndex := range newIndexes {
				line := lines[lineIndex]
				resources[i] = localResourceModel{
					ID: roots[i].ID, ResourceType: "gmail", OwnerUserID: ownerUserID,
					Email: line.email, Identity: line.identity, Password: line.password,
					TwoFactorSecret: line.twoFactorSecret, AppPassword: line.appPassword,
					CredentialRevision: 1, CredentialUpdatedAt: now, ForSale: false,
					Status: LocalResourcePending, ValidationGeneration: 1,
					ValidationRequestID: strings.TrimSpace(requestID), AllocBucket: uint16(roots[i].ID % 2048),
					CreatedAt: now, UpdatedAt: now,
				}
				writes[lineIndex] = gmailResourceImportWrite{ResourceID: roots[i].ID}
			}
			if err := tx.CreateInBatches(&resources, gmailResourceImportBatchSize).Error; err != nil {
				return fmt.Errorf("create Gmail import resources: %w", err)
			}
		}
		if beforeCommit != nil {
			return beforeCommit(writes)
		}
		return nil
	})
	return writes, err
}

func (s *Service) failGmailResourceImport(
	ctx context.Context,
	record *gmailResourceImportRecord,
	generation uint64,
	claimToken string,
	failures []gmailResourceImportFailure,
) error {
	failureObjectKey, _, err := s.saveGmailResourceImportFailures(ctx, record, failures)
	if err != nil {
		return err
	}
	items := make([]gmailResourceImportItem, 0, len(failures))
	for _, failure := range failures {
		if failure.Line > 0 {
			items = append(items, gmailResourceImportItem{
				LineNumber: failure.Line, Outcome: "skipped", Category: failure.Category, LastSafeError: failure.SafeMessage,
			})
		}
	}
	if err := s.beginGmailResourceImportResult(ctx, record.ImportID, generation, claimToken); err != nil {
		return err
	}
	if err := s.recordGmailResourceImportItems(ctx, record.ImportID, items); err != nil {
		return err
	}
	safeError := "Invalid import format."
	if len(failures) > 0 && strings.TrimSpace(failures[0].SafeMessage) != "" {
		safeError = failures[0].SafeMessage
	}
	terminal, err := s.markGmailResourceImportBusinessFailure(
		ctx, record.ImportID, generation, claimToken, failureObjectKey, safeError, len(items), s.now().UTC(),
	)
	if err != nil {
		return err
	}
	if !terminal {
		if err := s.scheduleDispatcher(ctx); err != nil {
			slog.Warn("wake Gmail import dispatcher after business failure failed", "import_id", record.ImportID, "error", err)
		}
	}
	return nil
}

func (s *Service) saveGmailResourceImportFailures(
	ctx context.Context,
	record *gmailResourceImportRecord,
	failures []gmailResourceImportFailure,
) (string, string, error) {
	if len(failures) == 0 {
		return "", "", nil
	}
	now := s.now().UTC()
	objectKey := gmailResourceImportObjectKey("failures", record.OwnerUserID, now, record.RequestID, ".csv")
	stored, err := s.files.SavePrivate(ctx, governancedomain.PrivateFile{
		ObjectKey: objectKey, FileName: "gmail-import-failures.csv",
		ContentType: "text/csv; charset=utf-8", ContentBytes: []byte(gmailResourceImportFailuresDetail(failures)),
	})
	if err != nil {
		return "", "", ErrGmailImportStorage
	}
	return stored.ObjectKey, skippedGmailResourceImportSummary(len(failures)), nil
}

func gmailResourceImportFailuresDetail(failures []gmailResourceImportFailure) string {
	var b strings.Builder
	b.WriteString("line,email,category,message\n")
	for _, failure := range failures {
		fmt.Fprintf(&b, "%d,%s,%s,%s\n",
			failure.Line,
			gmailResourceImportCSVSafe(failure.Email),
			gmailResourceImportCSVSafe(failure.Category),
			gmailResourceImportCSVSafe(failure.SafeMessage),
		)
	}
	return b.String()
}

func gmailResourceImportCSVSafe(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	trimmed := strings.TrimLeft(value, " ")
	if trimmed != "" && strings.ContainsRune("\t=+-@", rune(trimmed[0])) {
		value = "'" + value
	}
	value = strings.ReplaceAll(value, `"`, `""`)
	return `"` + value + `"`
}

func skippedGmailResourceImportSummary(count int) string {
	if count == 1 {
		return "Skipped 1 import entry."
	}
	return fmt.Sprintf("Skipped %d import entries.", count)
}

func gmailResourceImportFailureEmail(raw string) string {
	email, _, _ := strings.Cut(raw, "----")
	return strings.TrimSpace(email)
}

func normalizeGmailImportErrorStrategy(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "skip":
		return "skip", true
	case "abort":
		return "abort", true
	default:
		return "", false
	}
}

func gmailResourceImportFingerprint(ownerUserID uint, strategy string, content []byte) string {
	contentSum := sha256.Sum256(content)
	payload := fmt.Sprintf("gmail\x00%d\x00%s\x00%s", ownerUserID, strategy, hex.EncodeToString(contentSum[:]))
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func gmailResourceImportObjectKey(kind string, ownerUserID uint, now time.Time, requestID, suffix string) string {
	return fmt.Sprintf("imports/gmail/%s/%04d/%02d/%02d/%d/%s%s",
		kind, now.Year(), now.Month(), now.Day(), ownerUserID, safeGmailImportObjectSegment(requestID), suffix)
}

func cleanGmailResourceImportFileName(fileName string) string {
	base := path.Base(strings.TrimSpace(fileName))
	if base == "." || base == "/" || base == "" {
		return "gmail-resources.txt"
	}
	return base
}

func safeGmailImportObjectSegment(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return platform.NewUUIDV7String()
	}
	return b.String()
}

type gmailResourceImportCreateInput struct {
	OperatorUserID     uint
	OwnerUserID        uint
	SourceObjectKey    string
	ErrorStrategy      string
	RequestFingerprint string
	IdempotencyKey     string
	RequestID          string
	Path               string
	FileName           string
	CreatedAt          time.Time
}

func (s *Service) createGmailResourceImportState(ctx context.Context, input gmailResourceImportCreateInput) (uint64, bool, string, error) {
	raw, err := gmailResourceImportCreateScript.Run(ctx, s.redis,
		[]string{gmailResourceImportIdempotencyKey(input.OperatorUserID, input.IdempotencyKey), gmailResourceImportSequenceKey},
		gmailResourceImportRedisPrefix+"status:", input.OwnerUserID, input.OperatorUserID,
		input.SourceObjectKey, input.ErrorStrategy, input.RequestFingerprint, input.RequestID,
		input.Path, input.FileName, input.CreatedAt.UnixMilli(), gmailResourceImportMaxAttempts,
		gmailResourceImportAcceptanceTTL.Milliseconds(),
	).Result()
	if err != nil {
		return 0, false, "", fmt.Errorf("create Gmail resource import state: %w", err)
	}
	values, ok := raw.([]interface{})
	if !ok || len(values) != 2 {
		return 0, false, "", ErrGmailImportDependency
	}
	created, err := strconv.ParseBool(redisResultString(values[0]))
	if err != nil {
		created = redisResultString(values[0]) == "1"
	}
	fingerprint, importID, err := parseGmailImportIdempotencyValue(redisResultString(values[1]))
	if err != nil {
		return 0, false, "", err
	}
	return importID, created, fingerprint, nil
}

func (s *Service) activateGmailResourceImportState(
	ctx context.Context,
	importID uint64,
	operatorUserID uint,
	idempotencyKey string,
	now time.Time,
) error {
	result, err := gmailResourceImportActivateScript.Run(ctx, s.redis,
		[]string{
			gmailResourceImportStatusKey(importID),
			gmailResourceImportIdempotencyKey(operatorUserID, idempotencyKey),
			gmailResourceImportDispatchKey,
		},
		strconv.FormatUint(importID, 10), now.UnixMilli(),
	).Int64()
	if err != nil {
		return fmt.Errorf("activate Gmail resource import state: %w", ErrGmailImportDependency)
	}
	if result != 1 {
		return ErrGmailImportDependency
	}
	return nil
}

func (s *Service) rollbackGmailResourceImportAcceptance(ctx context.Context, importID uint64, operatorUserID uint, idempotencyKey string) bool {
	if s == nil || s.redis == nil || importID == 0 {
		return false
	}
	rolledBack, err := gmailResourceImportRollbackScript.Run(ctx, s.redis,
		[]string{
			gmailResourceImportStatusKey(importID),
			gmailResourceImportIdempotencyKey(operatorUserID, idempotencyKey),
			gmailResourceImportDispatchKey,
		},
		strconv.FormatUint(importID, 10),
	).Int64()
	if err != nil {
		slog.Warn("rollback Gmail import acceptance failed", "import_id", importID, "error", err)
		return false
	}
	return rolledBack == 1
}

func (s *Service) findGmailResourceImportByIdempotency(ctx context.Context, operatorUserID uint, idempotencyKey, fingerprint string) (*ResourceImportStatusView, bool, error) {
	value, err := s.redis.Get(ctx, gmailResourceImportIdempotencyKey(operatorUserID, idempotencyKey)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read Gmail resource import idempotency key: %w", err)
	}
	storedFingerprint, importID, err := parseGmailImportIdempotencyValue(value)
	if err != nil {
		return nil, false, err
	}
	if storedFingerprint != fingerprint {
		return nil, false, ErrGmailImportConflict
	}
	view, err := s.GetAdminGmailResourceImport(ctx, importID)
	return view, true, err
}

func (s *Service) gmailResourceImportRecord(ctx context.Context, importID uint64) (*gmailResourceImportRecord, error) {
	if s == nil || s.redis == nil || importID == 0 {
		return nil, ErrGmailImportNotFound
	}
	values, err := s.redis.HGetAll(ctx, gmailResourceImportStatusKey(importID)).Result()
	if err != nil {
		return nil, fmt.Errorf("read Gmail resource import status: %w", err)
	}
	if len(values) == 0 {
		return nil, ErrGmailImportNotFound
	}
	record := &gmailResourceImportRecord{}
	if record.ImportID, err = parseRedisUint64(values, "import_id"); err != nil || record.ImportID != importID {
		return nil, ErrGmailImportDependency
	}
	ownerID, ownerErr := parseRedisUint64(values, "owner_user_id")
	operatorID, operatorErr := parseRedisUint64(values, "operator_user_id")
	if ownerErr != nil || operatorErr != nil {
		return nil, ErrGmailImportDependency
	}
	record.OwnerUserID, record.OperatorUserID = uint(ownerID), uint(operatorID)
	record.SourceObjectKey = values["source_object_key"]
	record.FailureObjectKey = values["failure_object_key"]
	record.ErrorStrategy = values["error_strategy"]
	record.RequestFingerprint = values["request_fingerprint"]
	record.ClaimToken = values["claim_token"]
	record.Status = values["status"]
	record.TaskStatus = values["task_status"]
	record.RequestID = values["request_id"]
	record.LastSafeError = values["last_safe_error"]
	if record.Accepted, err = parseRedisInt(values, "accepted"); err != nil {
		return nil, ErrGmailImportDependency
	}
	if record.Imported, err = parseRedisInt(values, "imported"); err != nil {
		return nil, ErrGmailImportDependency
	}
	if record.Skipped, err = parseRedisInt(values, "skipped"); err != nil {
		return nil, ErrGmailImportDependency
	}
	if record.Attempts, err = parseRedisInt(values, "attempts"); err != nil {
		return nil, ErrGmailImportDependency
	}
	if record.MaxAttempts, err = parseRedisInt(values, "max_attempts"); err != nil {
		return nil, ErrGmailImportDependency
	}
	if record.Generation, err = parseRedisUint64(values, "generation"); err != nil {
		return nil, ErrGmailImportDependency
	}
	record.PreparingGeneration = parseOptionalRedisUint64(values["preparing_generation"])
	record.Prepared = values["prepared"] == "1"
	if record.Prepared {
		if record.PreparedAccepted, err = parseRedisInt(values, "prepared_accepted"); err != nil {
			return nil, ErrGmailImportDependency
		}
		if record.PreparedImported, err = parseRedisInt(values, "prepared_imported"); err != nil {
			return nil, ErrGmailImportDependency
		}
		if record.PreparedSkipped, err = parseRedisInt(values, "prepared_skipped"); err != nil {
			return nil, ErrGmailImportDependency
		}
	}
	if record.CreatedAt, err = parseRedisTime(values, "created_at"); err != nil {
		return nil, ErrGmailImportDependency
	}
	if record.UpdatedAt, err = parseRedisTime(values, "updated_at"); err != nil {
		return nil, ErrGmailImportDependency
	}
	record.StartedAt = parseOptionalRedisTime(values["started_at"])
	record.FinishedAt = parseOptionalRedisTime(values["finished_at"])
	return record, nil
}

func (s *Service) enqueueGmailResourceImport(ctx context.Context, task gmailResourceImportTask) error {
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("encode Gmail resource import task: %w", err)
	}
	_, err = s.queue.EnqueueContext(ctx, asynq.NewTask(typeGmailResourceImport, payload),
		asynq.Queue(platform.QueueDefault),
		asynq.Unique(gmailResourceImportTaskTimeout+gmailResourceImportActivationDelay),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()),
		asynq.Timeout(gmailResourceImportTaskTimeout),
		asynq.ProcessIn(gmailResourceImportActivationDelay),
		asynq.Retention(0),
	)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("enqueue Gmail resource import task: %w", err)
	}
	return nil
}

func (s *Service) markGmailResourceImportQueued(ctx context.Context, importID, generation uint64, now time.Time) error {
	_, err := gmailResourceImportQueuedScript.Run(ctx, s.redis,
		[]string{gmailResourceImportStatusKey(importID), gmailResourceImportDispatchKey},
		strconv.FormatUint(importID, 10), generation, now.UnixMilli(), now.Add(gmailResourceImportQueueLease).UnixMilli(),
	).Int64()
	if err != nil {
		return fmt.Errorf("mark Gmail resource import queued: %w", err)
	}
	return nil
}

func (s *Service) markGmailResourceImportRunning(ctx context.Context, task gmailResourceImportTask, now time.Time) (string, bool, error) {
	claimToken := platform.NewUUIDV7String()
	result, err := gmailResourceImportRunningScript.Run(ctx, s.redis,
		[]string{gmailResourceImportStatusKey(task.ImportID), gmailResourceImportDispatchKey},
		strconv.FormatUint(task.ImportID, 10), task.Generation, claimToken, now.UnixMilli(), now.Add(gmailResourceImportRunningLease).UnixMilli(),
	).Int64()
	if err != nil {
		return "", false, fmt.Errorf("claim Gmail resource import: %w", err)
	}
	return claimToken, result == 1, nil
}

func (s *Service) recoverStaleGmailResourceImport(ctx context.Context, importID, generation uint64, now time.Time) (bool, error) {
	result, err := gmailResourceImportRecoverScript.Run(ctx, s.redis,
		[]string{gmailResourceImportStatusKey(importID), gmailResourceImportDispatchKey},
		strconv.FormatUint(importID, 10), generation, now.UnixMilli(),
	).Int64()
	if err != nil {
		return false, fmt.Errorf("recover stale Gmail resource import: %w", err)
	}
	return result == 1, nil
}

func (s *Service) recordGmailResourceImportItems(ctx context.Context, importID uint64, items []gmailResourceImportItem) error {
	if len(items) == 0 {
		return nil
	}
	key := gmailResourceImportItemsKey(importID)
	for start := 0; start < len(items); start += gmailResourceImportBatchSize {
		end := min(start+gmailResourceImportBatchSize, len(items))
		values := make(map[string]interface{}, end-start)
		for _, item := range items[start:end] {
			encoded, err := json.Marshal(item)
			if err != nil {
				return fmt.Errorf("encode Gmail resource import item: %w", err)
			}
			values[strconv.Itoa(item.LineNumber)] = encoded
		}
		if err := s.redis.HSet(ctx, key, values).Err(); err != nil {
			return fmt.Errorf("record Gmail resource import items: %w", err)
		}
	}
	return nil
}

func (s *Service) beginGmailResourceImportResult(ctx context.Context, importID, generation uint64, claimToken string) error {
	result, err := gmailResourceImportBeginResultScript.Run(ctx, s.redis,
		[]string{gmailResourceImportStatusKey(importID), gmailResourceImportItemsKey(importID)},
		generation, claimToken,
	).Int64()
	if err != nil {
		return fmt.Errorf("begin Gmail resource import result: %w", err)
	}
	if result == 0 {
		return ErrGmailImportInvalidClaim
	}
	return nil
}

func (s *Service) prepareGmailResourceImportResult(
	ctx context.Context,
	importID, generation uint64,
	claimToken string,
	items []gmailResourceImportItem,
	accepted, imported, skipped int,
	failureObjectKey, safeSummary string,
) error {
	if err := s.beginGmailResourceImportResult(ctx, importID, generation, claimToken); err != nil {
		return err
	}
	if err := s.recordGmailResourceImportItems(ctx, importID, items); err != nil {
		return err
	}
	result, err := gmailResourceImportPrepareResultScript.Run(ctx, s.redis,
		[]string{gmailResourceImportStatusKey(importID)},
		generation, claimToken, accepted, imported, skipped,
		failureObjectKey, strings.TrimSpace(safeSummary), s.now().UTC().UnixMilli(),
	).Int64()
	if err != nil {
		return fmt.Errorf("prepare Gmail resource import result: %w", err)
	}
	if result == 0 {
		return ErrGmailImportInvalidClaim
	}
	return nil
}

func (s *Service) clearPreparedGmailResourceImport(ctx context.Context, importID, generation uint64, claimToken string) error {
	result, err := gmailResourceImportClearResultScript.Run(ctx, s.redis,
		[]string{gmailResourceImportStatusKey(importID), gmailResourceImportItemsKey(importID)},
		generation, claimToken,
	).Int64()
	if err != nil {
		return fmt.Errorf("clear prepared Gmail resource import result: %w", err)
	}
	if result == 0 {
		return ErrGmailImportInvalidClaim
	}
	return nil
}

func (s *Service) finishPreparedGmailResourceImport(
	ctx context.Context,
	importID, generation uint64,
	claimToken string,
	now time.Time,
) error {
	result, err := gmailResourceImportFinishScript.Run(ctx, s.redis,
		[]string{gmailResourceImportStatusKey(importID), gmailResourceImportDispatchKey},
		strconv.FormatUint(importID, 10), generation, claimToken, now.UnixMilli(),
	).Int64()
	if err != nil {
		return fmt.Errorf("finish Gmail resource import: %w", err)
	}
	if result == 0 {
		return ErrGmailImportInvalidClaim
	}
	return nil
}

func (s *Service) finalizePreparedGmailResourceImport(
	ctx context.Context,
	record *gmailResourceImportRecord,
	generation uint64,
	claimToken string,
) (bool, error) {
	committed, err := s.preparedGmailResourceImportCommitted(ctx, record)
	if err != nil || !committed {
		return false, err
	}
	if err := s.finishPreparedGmailResourceImport(ctx, record.ImportID, generation, claimToken, s.now().UTC()); err != nil {
		return false, err
	}
	if record.PreparedImported > 0 {
		if err := s.scheduleDispatcher(ctx); err != nil {
			slog.Warn("wake Gmail validation dispatcher after resumed import failed", "import_id", record.ImportID, "error", err)
		}
	}
	return true, nil
}

func (s *Service) preparedGmailResourceImportCommitted(ctx context.Context, record *gmailResourceImportRecord) (bool, error) {
	if record == nil || !record.Prepared {
		return false, nil
	}
	if record.PreparedAccepted != record.PreparedImported {
		return false, ErrGmailImportDependency
	}
	rawItems, err := s.redis.HGetAll(ctx, gmailResourceImportItemsKey(record.ImportID)).Result()
	if err != nil {
		return false, fmt.Errorf("read prepared Gmail resource import items: %w", err)
	}
	if len(rawItems) != record.PreparedImported+record.PreparedSkipped {
		return false, ErrGmailImportDependency
	}
	resourceIDs := make(map[uint]struct{}, record.PreparedImported)
	imported, skipped := 0, 0
	for _, raw := range rawItems {
		var item gmailResourceImportItem
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			return false, ErrGmailImportDependency
		}
		switch item.Outcome {
		case "imported", "restored":
			if item.ResourceID == 0 {
				return false, ErrGmailImportDependency
			}
			resourceIDs[item.ResourceID] = struct{}{}
			imported++
		case "skipped":
			skipped++
		default:
			return false, ErrGmailImportDependency
		}
	}
	if imported != record.PreparedImported || skipped != record.PreparedSkipped || len(resourceIDs) != imported {
		return false, ErrGmailImportDependency
	}
	if len(resourceIDs) == 0 {
		return true, nil
	}
	ids := make([]uint, 0, len(resourceIDs))
	for resourceID := range resourceIDs {
		ids = append(ids, resourceID)
	}
	var rows []struct {
		ID          uint   `gorm:"column:id"`
		OwnerUserID uint   `gorm:"column:owner_user_id"`
		Status      string `gorm:"column:status"`
	}
	if err := s.dbFor(ctx).Model(&localResourceModel{}).
		Select("id, owner_user_id, status").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return false, fmt.Errorf("verify prepared Gmail resource import: %w", err)
	}
	if len(rows) != len(resourceIDs) {
		return false, nil
	}
	for _, row := range rows {
		if row.OwnerUserID != record.OwnerUserID || row.Status == LocalResourceDeleted {
			return false, nil
		}
	}
	return true, nil
}

func (s *Service) markGmailResourceImportBusinessFailure(
	ctx context.Context,
	importID, generation uint64,
	claimToken, failureObjectKey, safeError string,
	skipped int,
	now time.Time,
) (bool, error) {
	result, err := gmailResourceImportBusinessFailureScript.Run(ctx, s.redis,
		[]string{gmailResourceImportStatusKey(importID), gmailResourceImportDispatchKey},
		strconv.FormatUint(importID, 10), generation, claimToken, failureObjectKey,
		strings.TrimSpace(safeError), skipped, now.UnixMilli(),
	).Int64()
	if err != nil {
		return false, fmt.Errorf("fail Gmail resource import: %w", err)
	}
	if result == 0 {
		return false, ErrGmailImportInvalidClaim
	}
	return result == 2, nil
}

func gmailResourceImportStatusKey(importID uint64) string {
	return gmailResourceImportRedisPrefix + "status:" + strconv.FormatUint(importID, 10)
}

func gmailResourceImportItemsKey(importID uint64) string {
	return gmailResourceImportRedisPrefix + "items:" + strconv.FormatUint(importID, 10)
}

func gmailResourceImportIdempotencyKey(operatorUserID uint, idempotencyKey string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", operatorUserID, strings.TrimSpace(idempotencyKey))))
	return gmailResourceImportRedisPrefix + "idempotency:" + hex.EncodeToString(digest[:])
}

func parseGmailImportIdempotencyValue(value string) (string, uint64, error) {
	fingerprint, rawID, ok := strings.Cut(strings.TrimSpace(value), ":")
	if !ok || len(fingerprint) != 64 {
		return "", 0, ErrGmailImportDependency
	}
	importID, err := strconv.ParseUint(rawID, 10, 64)
	if err != nil || importID == 0 {
		return "", 0, ErrGmailImportDependency
	}
	return fingerprint, importID, nil
}

func redisResultString(value interface{}) string {
	switch item := value.(type) {
	case string:
		return item
	case []byte:
		return string(item)
	case int64:
		return strconv.FormatInt(item, 10)
	default:
		return fmt.Sprint(item)
	}
}

func parseRedisUint64(values map[string]string, key string) (uint64, error) {
	value, err := strconv.ParseUint(values[key], 10, 64)
	if err != nil || value == 0 {
		return 0, ErrGmailImportDependency
	}
	return value, nil
}

func parseRedisInt(values map[string]string, key string) (int, error) {
	value, err := strconv.Atoi(values[key])
	if err != nil || value < 0 {
		return 0, ErrGmailImportDependency
	}
	return value, nil
}

func parseRedisTime(values map[string]string, key string) (time.Time, error) {
	milliseconds, err := strconv.ParseInt(values[key], 10, 64)
	if err != nil || milliseconds <= 0 {
		return time.Time{}, ErrGmailImportDependency
	}
	return time.UnixMilli(milliseconds).UTC(), nil
}

func parseOptionalRedisUint64(value string) uint64 {
	parsed, _ := strconv.ParseUint(value, 10, 64)
	return parsed
}

func parseOptionalRedisTime(value string) *time.Time {
	milliseconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || milliseconds <= 0 {
		return nil
	}
	parsed := time.UnixMilli(milliseconds).UTC()
	return &parsed
}

var gmailResourceImportCreateScript = redis.NewScript(`
local existing = redis.call('GET', KEYS[1])
if existing then
    return {0, existing}
end
local import_id = redis.call('INCR', KEYS[2])
local status_key = ARGV[1] .. import_id
local mapping = ARGV[6] .. ':' .. import_id
redis.call('HSET', status_key,
    'import_id', import_id,
    'owner_user_id', ARGV[2],
    'operator_user_id', ARGV[3],
    'source_object_key', ARGV[4],
    'error_strategy', ARGV[5],
    'request_fingerprint', ARGV[6],
    'request_id', ARGV[7],
    'path', ARGV[8],
    'file_name', ARGV[9],
    'status', 'accepting',
    'task_status', 'pending',
    'accepted', 0,
    'imported', 0,
    'skipped', 0,
    'attempts', 0,
    'max_attempts', ARGV[11],
    'generation', 1,
    'claim_token', '',
    'failure_object_key', '',
    'last_safe_error', '',
    'created_at', ARGV[10],
    'updated_at', ARGV[10])
redis.call('PEXPIRE', status_key, ARGV[12])
redis.call('SET', KEYS[1], mapping, 'PX', ARGV[12])
return {1, mapping}
`)

var gmailResourceImportActivateScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'status') ~= 'accepting' then
    return 0
end
if not redis.call('GET', KEYS[2]) then
    return 0
end
redis.call('HSET', KEYS[1],
    'status', 'processing',
    'task_status', 'pending',
    'updated_at', ARGV[2])
redis.call('PERSIST', KEYS[1])
redis.call('PERSIST', KEYS[2])
redis.call('ZADD', KEYS[3], ARGV[2], ARGV[1])
return 1
`)

var gmailResourceImportRollbackScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'status') ~= 'accepting' then
    return 0
end
redis.call('DEL', KEYS[1])
redis.call('DEL', KEYS[2])
redis.call('ZREM', KEYS[3], ARGV[1])
return 1
`)

var gmailResourceImportQueuedScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'status') ~= 'processing' then
    redis.call('ZREM', KEYS[2], ARGV[1])
    return 0
end
if tonumber(redis.call('HGET', KEYS[1], 'generation') or '0') ~= tonumber(ARGV[2]) then
    return 0
end
local task_status = redis.call('HGET', KEYS[1], 'task_status')
if task_status == 'pending' or task_status == 'queued' then
    redis.call('HSET', KEYS[1], 'task_status', 'queued', 'updated_at', ARGV[3])
    redis.call('ZADD', KEYS[2], ARGV[4], ARGV[1])
end
return 1
`)

var gmailResourceImportRunningScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'status') ~= 'processing' then
    redis.call('ZREM', KEYS[2], ARGV[1])
    return 0
end
if tonumber(redis.call('HGET', KEYS[1], 'generation') or '0') ~= tonumber(ARGV[2]) then
    return 0
end
local task_status = redis.call('HGET', KEYS[1], 'task_status')
if task_status ~= 'pending' and task_status ~= 'queued' then
    return 0
end
redis.call('HSET', KEYS[1],
    'task_status', 'running',
    'claim_token', ARGV[3],
    'started_at', ARGV[4],
    'updated_at', ARGV[4])
redis.call('ZADD', KEYS[2], ARGV[5], ARGV[1])
return 1
`)

var gmailResourceImportRecoverScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'status') ~= 'processing' then
    redis.call('ZREM', KEYS[2], ARGV[1])
    return 0
end
if redis.call('HGET', KEYS[1], 'task_status') ~= 'running' then
    return 0
end
if tonumber(redis.call('HGET', KEYS[1], 'generation') or '0') ~= tonumber(ARGV[2]) then
    return 0
end
redis.call('HSET', KEYS[1],
    'task_status', 'pending',
    'generation', tonumber(ARGV[2]) + 1,
    'claim_token', '',
    'last_safe_error', 'Import infrastructure is temporarily unavailable; dispatcher will retry.',
    'updated_at', ARGV[3])
redis.call('ZADD', KEYS[2], ARGV[3], ARGV[1])
return 1
`)

var gmailResourceImportReleaseScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'status') ~= 'processing' then
    redis.call('ZREM', KEYS[2], ARGV[1])
    return 0
end
if tonumber(redis.call('HGET', KEYS[1], 'generation') or '0') ~= tonumber(ARGV[2]) then
    return 0
end
local task_status = redis.call('HGET', KEYS[1], 'task_status')
if task_status ~= 'queued' and task_status ~= 'running' then
    return 0
end
redis.call('HSET', KEYS[1],
    'task_status', 'pending',
    'generation', tonumber(ARGV[2]) + 1,
    'claim_token', '',
    'last_safe_error', ARGV[3],
    'updated_at', ARGV[4])
redis.call('ZADD', KEYS[2], ARGV[4], ARGV[1])
return 1
`)

var gmailResourceImportBeginResultScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'status') ~= 'processing' then
    return 0
end
if redis.call('HGET', KEYS[1], 'task_status') ~= 'running' then
    return 0
end
if tonumber(redis.call('HGET', KEYS[1], 'generation') or '0') ~= tonumber(ARGV[1]) then
    return 0
end
if redis.call('HGET', KEYS[1], 'claim_token') ~= ARGV[2] then
    return 0
end
redis.call('DEL', KEYS[2])
redis.call('HDEL', KEYS[1],
    'prepared',
    'prepared_generation',
    'prepared_accepted',
    'prepared_imported',
    'prepared_skipped',
    'prepared_failure_object_key',
    'prepared_safe_summary')
redis.call('HSET', KEYS[1], 'preparing_generation', ARGV[1])
return 1
`)

var gmailResourceImportPrepareResultScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'status') ~= 'processing' then
    return 0
end
if redis.call('HGET', KEYS[1], 'task_status') ~= 'running' then
    return 0
end
if tonumber(redis.call('HGET', KEYS[1], 'generation') or '0') ~= tonumber(ARGV[1]) then
    return 0
end
if redis.call('HGET', KEYS[1], 'claim_token') ~= ARGV[2] then
    return 0
end
if tonumber(redis.call('HGET', KEYS[1], 'preparing_generation') or '0') ~= tonumber(ARGV[1]) then
    return 0
end
redis.call('HSET', KEYS[1],
    'prepared', 1,
    'prepared_generation', ARGV[1],
    'prepared_accepted', ARGV[3],
    'prepared_imported', ARGV[4],
    'prepared_skipped', ARGV[5],
    'prepared_failure_object_key', ARGV[6],
    'prepared_safe_summary', ARGV[7],
    'updated_at', ARGV[8])
redis.call('HDEL', KEYS[1], 'preparing_generation')
return 1
`)

var gmailResourceImportClearResultScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'status') ~= 'processing' then
    return 0
end
if redis.call('HGET', KEYS[1], 'task_status') ~= 'running' then
    return 0
end
if tonumber(redis.call('HGET', KEYS[1], 'generation') or '0') ~= tonumber(ARGV[1]) then
    return 0
end
if redis.call('HGET', KEYS[1], 'claim_token') ~= ARGV[2] then
    return 0
end
redis.call('DEL', KEYS[2])
redis.call('HDEL', KEYS[1],
    'preparing_generation',
    'prepared',
    'prepared_generation',
    'prepared_accepted',
    'prepared_imported',
    'prepared_skipped',
    'prepared_failure_object_key',
    'prepared_safe_summary')
return 1
`)

var gmailResourceImportBusinessFailureScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'status') ~= 'processing' then
    return 0
end
if redis.call('HGET', KEYS[1], 'task_status') ~= 'running' then
    return 0
end
if tonumber(redis.call('HGET', KEYS[1], 'generation') or '0') ~= tonumber(ARGV[2]) then
    return 0
end
if redis.call('HGET', KEYS[1], 'claim_token') ~= ARGV[3] then
    return 0
end
local attempts = tonumber(redis.call('HGET', KEYS[1], 'attempts') or '0') + 1
local max_attempts = tonumber(redis.call('HGET', KEYS[1], 'max_attempts') or '0')
redis.call('HSET', KEYS[1],
    'attempts', attempts,
    'failure_object_key', ARGV[4],
    'last_safe_error', ARGV[5],
    'skipped', ARGV[6],
    'claim_token', '',
    'updated_at', ARGV[7])
redis.call('HDEL', KEYS[1],
    'preparing_generation',
    'prepared',
    'prepared_generation',
    'prepared_accepted',
    'prepared_imported',
    'prepared_skipped',
    'prepared_failure_object_key',
    'prepared_safe_summary')
if attempts >= max_attempts then
    redis.call('HSET', KEYS[1],
        'status', 'failed',
        'task_status', 'failed',
        'finished_at', ARGV[7])
    redis.call('ZREM', KEYS[2], ARGV[1])
    return 2
end
redis.call('HSET', KEYS[1],
    'task_status', 'pending',
    'generation', tonumber(ARGV[2]) + 1)
redis.call('ZADD', KEYS[2], ARGV[7], ARGV[1])
return 1
`)

var gmailResourceImportFinishScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'status') ~= 'processing' then
    return 0
end
if tonumber(redis.call('HGET', KEYS[1], 'generation') or '0') ~= tonumber(ARGV[2]) then
    return 0
end
if redis.call('HGET', KEYS[1], 'task_status') ~= 'running' then
    return 0
end
if redis.call('HGET', KEYS[1], 'claim_token') ~= ARGV[3] then
    return 0
end
if redis.call('HGET', KEYS[1], 'prepared') ~= '1' then
    return 0
end
redis.call('HSET', KEYS[1],
    'status', 'imported',
    'task_status', 'succeeded',
    'accepted', redis.call('HGET', KEYS[1], 'prepared_accepted'),
    'imported', redis.call('HGET', KEYS[1], 'prepared_imported'),
    'skipped', redis.call('HGET', KEYS[1], 'prepared_skipped'),
    'failure_object_key', redis.call('HGET', KEYS[1], 'prepared_failure_object_key'),
    'last_safe_error', redis.call('HGET', KEYS[1], 'prepared_safe_summary'),
    'claim_token', '',
    'finished_at', ARGV[4],
    'updated_at', ARGV[4])
redis.call('HDEL', KEYS[1],
    'preparing_generation',
    'prepared',
    'prepared_generation',
    'prepared_accepted',
    'prepared_imported',
    'prepared_skipped',
    'prepared_failure_object_key',
    'prepared_safe_summary')
redis.call('ZREM', KEYS[2], ARGV[1])
return 1
`)
