package icloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	coreDomain "github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/go-sql-driver/mysql"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	iCloudImportDefaultMaxBytes      int64 = 100 * 1024 * 1024
	iCloudImportMaxBytes             int64 = 512 * 1024 * 1024
	iCloudImportMultipartBytes       int64 = 1024 * 1024
	iCloudImportBatchSize                  = 500
	iCloudImportMaxAttempts                = 3
	iCloudImportTaskTimeout                = 30 * time.Minute
	iCloudImportActivationDelay            = time.Second
	iCloudImportTaskUniqueTTL              = iCloudImportTaskTimeout + iCloudImportActivationDelay
	iCloudImportQueueLease                 = 2 * time.Minute
	iCloudImportRunningLease               = iCloudImportTaskTimeout + 5*time.Minute
	iCloudImportEmailMaxLength             = 320
	iCloudImportHostMaxLength              = 255
	iCloudImportDSIDMaxLength              = 191
	iCloudImportClientMaxLength            = 191
	iCloudAppleAccountValueMaxLength       = 1000
	iCloudImportBuildMaxLength             = 64
	iCloudImportCookieMaxBytes             = 65_535
)

var iCloudCurlContinuationPattern = regexp.MustCompile(`[ \t]*\\[ \t]*\r?\n[ \t]*`)

type iCloudImportLine struct {
	LineNumber         int
	ExistingResourceID uint
	PrimaryEmail       string
	Channels           []iCloudImportChannel
}

type iCloudImportFailure struct {
	Line        int
	Email       string
	Category    string
	SafeMessage string
}

type iCloudImportCreateInput struct {
	OperatorUserID     uint
	OwnerUserID        uint
	SourceObjectKey    string
	ErrorStrategy      coreDomain.ImportErrorStrategy
	ResourceExpireAt   time.Time
	IdempotencyKey     string
	RequestFingerprint string
	RequestID          string
	Path               string
}

func maxICloudImportBytesValue() int64 {
	return int64(min(runtimeconfig.Int("resource_import_max_bytes", int(iCloudImportDefaultMaxBytes), 1), int(iCloudImportMaxBytes)))
}

func iCloudImportMultipartMaxBytes(fileMax int64) int64 {
	return fileMax + iCloudImportMultipartBytes
}

func (s *Service) validateICloudImportOwner(ctx context.Context, ownerUserID uint) error {
	if s == nil || s.validateImportOwner == nil || ownerUserID == 0 {
		return ErrICloudImportDependency
	}
	valid, err := s.validateImportOwner(ctx, ownerUserID)
	if err != nil {
		return ErrICloudImportDependency
	}
	if !valid {
		return ErrICloudImportInvalidOwner
	}
	return nil
}

// AcceptAdminICloudTXTFile mirrors the Microsoft acceptance boundary: source
// material is private, idempotency is durable, and queue failure never turns
// an accepted import into a terminal failure.
func (s *Service) AcceptAdminICloudTXTFile(
	ctx context.Context,
	operatorUserID uint,
	ownerUserID uint,
	fileName string,
	content []byte,
	errorStrategy coreDomain.ImportErrorStrategy,
	resourceExpireAt time.Time,
	idempotencyKey string,
	requestID string,
	pathValue string,
) (*ImportStatusView, bool, error) {
	if s == nil || s.db == nil || s.queue == nil || s.files == nil {
		return nil, false, ErrICloudImportDependency
	}
	if operatorUserID == 0 || ownerUserID == 0 || len(content) == 0 {
		return nil, false, ErrICloudImportInvalid
	}
	if !utf8.Valid(content) {
		return nil, false, ErrICloudImportInvalid
	}
	if int64(len(content)) > maxICloudImportBytesValue() {
		return nil, false, ErrICloudImportInvalid
	}
	strategy, ok := coreDomain.NormalizeImportErrorStrategy(string(errorStrategy))
	if !ok {
		return nil, false, ErrICloudImportInvalid
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		return nil, false, ErrICloudImportInvalid
	}
	if err := s.validateICloudImportOwner(ctx, ownerUserID); err != nil {
		return nil, false, err
	}
	resourceExpireAt = normalizeICloudResourceExpireAt(resourceExpireAt)
	fingerprint := iCloudImportFingerprint(ownerUserID, strategy, resourceExpireAt, content)
	if existing, err := s.findICloudImportByIdempotency(ctx, operatorUserID, idempotencyKey); err != nil {
		return nil, false, err
	} else if existing != nil {
		if existing.RequestFingerprint != fingerprint {
			return nil, false, ErrICloudImportConflict
		}
		return existing.statusView(), true, nil
	}
	now := s.now().UTC()
	if !validICloudResourceExpireAt(resourceExpireAt, now) {
		return nil, false, ErrICloudImportInvalid
	}

	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		requestID = platform.NewUUIDV7String()
	}
	sourceObjectKey := iCloudImportObjectKey("source", ownerUserID, now, requestID, ".txt")
	stored, err := s.files.SavePrivate(ctx, governancedomain.PrivateFile{
		ObjectKey: sourceObjectKey, FileName: cleanICloudImportFileName(fileName),
		ContentType: "text/plain; charset=utf-8", ContentBytes: content,
	})
	if err != nil {
		return nil, false, ErrICloudImportStorage
	}

	model, created, err := s.createICloudImport(ctx, iCloudImportCreateInput{
		OperatorUserID: operatorUserID, OwnerUserID: ownerUserID, SourceObjectKey: stored.ObjectKey,
		ErrorStrategy: strategy, ResourceExpireAt: resourceExpireAt,
		IdempotencyKey: idempotencyKey, RequestFingerprint: fingerprint,
		RequestID: requestID, Path: strings.TrimSpace(pathValue),
	})
	if err != nil {
		_ = s.files.DeletePrivate(ctx, stored.ObjectKey)
		return nil, false, err
	}
	if !created {
		_ = s.files.DeletePrivate(ctx, stored.ObjectKey)
		if model.RequestFingerprint != fingerprint {
			return nil, false, ErrICloudImportConflict
		}
		return model.statusView(), true, nil
	}

	_ = s.DispatchICloudImports(context.WithoutCancel(ctx), 100)
	return model.statusView(), false, nil
}

func (s *Service) GetAdminICloudResourceImport(ctx context.Context, importID uint) (*ImportStatusView, error) {
	if s == nil || s.db == nil || importID == 0 {
		return nil, ErrICloudImportNotFound
	}
	var model iCloudImportModel
	err := s.db.WithContext(ctx).First(&model, importID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrICloudImportNotFound
	}
	if err != nil {
		return nil, ErrICloudImportTemporary
	}
	return model.statusView(), nil
}

func (s *Service) findICloudImportByIdempotency(ctx context.Context, operatorUserID uint, idempotencyKey string) (*iCloudImportModel, error) {
	var model iCloudImportModel
	err := s.db.WithContext(ctx).
		Where("operator_user_id = ? AND idempotency_key = ?", operatorUserID, idempotencyKey).
		First(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrICloudImportTemporary
	}
	return &model, nil
}

func (s *Service) createICloudImport(ctx context.Context, input iCloudImportCreateInput) (*iCloudImportModel, bool, error) {
	var stored iCloudImportModel
	created := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing iCloudImportModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("operator_user_id = ? AND idempotency_key = ?", input.OperatorUserID, input.IdempotencyKey).
			First(&existing).Error
		if err == nil {
			if existing.RequestFingerprint != input.RequestFingerprint {
				return ErrICloudImportConflict
			}
			stored = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrICloudImportTemporary
		}
		stored = iCloudImportModel{
			OwnerUserID: input.OwnerUserID, OperatorUserID: input.OperatorUserID,
			SourceObjectKey: input.SourceObjectKey, Status: iCloudImportProcessing,
			ErrorStrategy: string(input.ErrorStrategy), ResourceExpireAt: input.ResourceExpireAt,
			RequestID: input.RequestID, Path: input.Path,
			IdempotencyKey: input.IdempotencyKey, RequestFingerprint: input.RequestFingerprint,
			DispatchStatus: "pending", Generation: 1, MaxAttempts: iCloudImportMaxAttempts,
		}
		if err := tx.Create(&stored).Error; err != nil {
			if isICloudDuplicateError(err) {
				return ErrICloudImportConflict
			}
			return ErrICloudImportTemporary
		}
		if s.operationLogs == nil {
			return ErrICloudImportDependency
		}
		if err := s.operationLogs.CreateInTx(ctx, tx, &governancedomain.OperationLog{
			OperatorUserID: input.OperatorUserID, OperationType: "icloud.admin_resource.import",
			ResourceType: "icloud_resource_import", ResourceID: fmt.Sprintf("icloud-import:%d", stored.ID),
			Path: input.Path, Result: "success", SafeSummary: "iCloud resource import accepted.", RequestID: input.RequestID,
		}); err != nil {
			return ErrICloudImportTemporary
		}
		created = true
		return nil
	})
	if err == nil {
		return &stored, created, nil
	}
	if !errors.Is(err, ErrICloudImportConflict) {
		return nil, false, err
	}
	// A concurrent identical acceptance may have won the unique key after the
	// transaction's initial lookup. Read it once outside that snapshot.
	existing, findErr := s.findICloudImportByIdempotency(ctx, input.OperatorUserID, input.IdempotencyKey)
	if findErr != nil {
		return nil, false, findErr
	}
	if existing != nil && existing.RequestFingerprint == input.RequestFingerprint {
		return existing, false, nil
	}
	return nil, false, ErrICloudImportConflict
}

// DispatchICloudImports is the durable import dispatcher. It intentionally
// leaves a row pending when Asynq is temporarily unavailable.
func (s *Service) DispatchICloudImports(ctx context.Context, limit int) error {
	if s == nil || s.db == nil || s.queue == nil {
		return ErrICloudImportDependency
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	if err := s.recoverStaleICloudImports(ctx, s.now().UTC()); err != nil {
		return err
	}
	var models []iCloudImportModel
	if err := s.db.WithContext(ctx).
		Where("status = ? AND dispatch_status = ?", iCloudImportProcessing, "pending").
		Order("id ASC").Limit(limit).Find(&models).Error; err != nil {
		return ErrICloudImportTemporary
	}
	var result error
	for _, model := range models {
		accepted, err := s.enqueueICloudImport(ctx, iCloudImportTask{ImportID: model.ID, Generation: model.Generation})
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if !accepted {
			continue
		}
		updated := s.db.WithContext(ctx).Model(&iCloudImportModel{}).
			Where("id = ? AND status = ? AND dispatch_status = ? AND generation = ?", model.ID, iCloudImportProcessing, "pending", model.Generation).
			Updates(map[string]any{"dispatch_status": "queued", "last_safe_error": "", "updated_at": s.now().UTC()})
		if updated.Error != nil {
			result = errors.Join(result, ErrICloudImportTemporary)
		}
	}
	return result
}

// recoverStaleICloudImports turns abandoned queue/worker leases back into a
// new fenced generation. The old task may still arrive, but its generation no
// longer matches and therefore cannot mutate the import.
func (s *Service) recoverStaleICloudImports(ctx context.Context, now time.Time) error {
	if s == nil || s.db == nil {
		return ErrICloudImportDependency
	}
	queuedBefore := now.Add(-iCloudImportQueueLease)
	runningBefore := now.Add(-iCloudImportRunningLease)
	result := s.db.WithContext(ctx).Model(&iCloudImportModel{}).
		Where(`status = ? AND (
			(dispatch_status = ? AND updated_at <= ?) OR
			(dispatch_status = ? AND started_at IS NOT NULL AND started_at <= ?)
		)`, iCloudImportProcessing, "queued", queuedBefore, "running", runningBefore).
		Updates(map[string]any{
			"dispatch_status": "pending",
			"generation":      gorm.Expr("generation + 1"),
			"claim_token":     "",
			"started_at":      nil,
			"last_safe_error": "Import worker lease expired; dispatcher will retry.",
			"updated_at":      now,
		})
	if result.Error != nil {
		return ErrICloudImportTemporary
	}
	return nil
}

func (s *Service) enqueueICloudImport(ctx context.Context, task iCloudImportTask) (bool, error) {
	if s == nil || s.queue == nil || task.ImportID == 0 || task.Generation == 0 {
		return false, ErrICloudImportDependency
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return false, ErrICloudImportTemporary
	}
	_, err = s.queue.EnqueueContext(ctx, asynq.NewTask(typeICloudImport, payload),
		asynq.Queue(platform.QueueDefault), asynq.Unique(iCloudImportTaskUniqueTTL),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()), asynq.Timeout(iCloudImportTaskTimeout),
		asynq.ProcessIn(iCloudImportActivationDelay), asynq.Retention(0),
	)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return false, nil
	}
	if err != nil {
		return false, ErrICloudImportTemporary
	}
	return true, nil
}

func parseICloudImport(content string, strategy coreDomain.ImportErrorStrategy) ([]iCloudImportLine, []iCloudImportFailure, *iCloudImportFailure) {
	normalizedStrategy, ok := coreDomain.NormalizeImportErrorStrategy(string(strategy))
	if !ok || !utf8.ValidString(content) || strings.TrimSpace(content) == "" {
		failure := iCloudImportFailure{Category: "invalid_format", SafeMessage: "Invalid iCloud import format."}
		return nil, nil, &failure
	}
	strategy = normalizedStrategy
	var lines []iCloudImportLine
	var failures []iCloudImportFailure
	content = iCloudCurlContinuationPattern.ReplaceAllString(content, " ")
	for index, raw := range strings.Split(content, "\n") {
		raw = strings.TrimSuffix(raw, "\r")
		if strings.TrimSpace(raw) == "" {
			continue
		}
		line, failure := parseICloudImportLine(index+1, raw)
		if failure != nil {
			if strategy == coreDomain.ImportErrorStrategyAbort {
				return nil, nil, failure
			}
			failures = append(failures, *failure)
			continue
		}
		lines = append(lines, *line)
	}
	return lines, failures, nil
}

func parseICloudImportLine(lineNumber int, raw string) (*iCloudImportLine, *iCloudImportFailure) {
	return parseICloudCurlImportLine(lineNumber, raw)
}

func isICloudImportEmail(value string) bool {
	if value == "" || utf8.RuneCountInString(value) > iCloudImportEmailMaxLength || strings.Count(value, "@") != 1 {
		return false
	}
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Address == value
}

func validICloudImportValue(value string, maxLength int) bool {
	value = strings.TrimSpace(value)
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxLength && !strings.ContainsAny(value, "\r\n")
}

func validICloudImportCookie(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && utf8.ValidString(value) && len(value) <= iCloudImportCookieMaxBytes && !strings.ContainsAny(value, "\r\n") && hasRequiredICloudCookies(value)
}

// ProcessICloudImport executes one fenced durable import generation. The task
// carries only the import ID and generation; all session material is loaded
// from the private artifact after the claim succeeds.
func (s *Service) ProcessICloudImport(ctx context.Context, task iCloudImportTask) error {
	if s == nil || s.db == nil || s.files == nil {
		return ErrICloudImportDependency
	}
	if task.ImportID == 0 || task.Generation == 0 {
		return ErrICloudImportInvalid
	}
	record, err := s.iCloudImportRecord(ctx, task.ImportID)
	if errors.Is(err, ErrICloudImportNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if record.Status != iCloudImportProcessing {
		return nil
	}
	claimToken, claimed, err := s.markICloudImportRunning(ctx, task)
	if err != nil {
		return err
	}
	if !claimed {
		current, findErr := s.iCloudImportRecord(ctx, task.ImportID)
		if errors.Is(findErr, ErrICloudImportNotFound) {
			return nil
		}
		if findErr != nil {
			return findErr
		}
		if current.Status == iCloudImportProcessing && current.Generation == task.Generation && current.DispatchStatus != "succeeded" && current.DispatchStatus != "failed" {
			return ErrICloudImportTemporary
		}
		return nil
	}
	record.ClaimToken = claimToken
	return s.processICloudImportClaimed(ctx, record)
}

func (s *Service) processICloudImportClaimed(ctx context.Context, record *iCloudImportModel) error {
	if record == nil || record.ID == 0 || record.ClaimToken == "" || record.OwnerUserID == 0 || strings.TrimSpace(record.SourceObjectKey) == "" {
		return ErrICloudImportInvalid
	}
	strategy, ok := coreDomain.NormalizeImportErrorStrategy(record.ErrorStrategy)
	if !ok {
		return s.failICloudImport(ctx, record, iCloudImportFailure{
			Category: "invalid_format", SafeMessage: "Invalid iCloud import format.",
		})
	}
	source, err := s.files.ReadPrivate(ctx, record.SourceObjectKey)
	if err != nil || source == nil {
		return ErrICloudImportStorage
	}
	lines, failures, fatal := parseICloudImport(string(source.ContentBytes), strategy)
	if fatal != nil {
		return s.failICloudImport(ctx, record, *fatal)
	}

	lines, duplicateFailures, duplicateFatal := deduplicateICloudImportLines(lines, strategy)
	if duplicateFatal != nil {
		return s.failICloudImport(ctx, record, *duplicateFatal)
	}
	failures = append(failures, duplicateFailures...)
	processedLines, err := s.iCloudImportProcessedLines(ctx, record.ID)
	if err != nil {
		return err
	}
	if len(processedLines) > 0 {
		remaining := lines[:0]
		for _, line := range lines {
			if _, processed := processedLines[line.LineNumber]; !processed {
				remaining = append(remaining, line)
			}
		}
		lines = remaining
	}

	lines, existingFailures, existingFatal, err := s.removeExistingICloudImportLines(ctx, record.OwnerUserID, lines, strategy)
	if err != nil {
		return err
	}
	if existingFatal != nil {
		return s.failICloudImport(ctx, record, *existingFatal)
	}
	failures = append(failures, existingFailures...)

	if len(lines) == 0 && len(failures) == 0 && len(processedLines) == 0 {
		return s.failICloudImport(ctx, record, iCloudImportFailure{
			Category: "invalid_format", SafeMessage: "Invalid iCloud import format.",
		})
	}

	failureObjectKey, summary, err := s.saveICloudImportFailures(ctx, record, failures)
	if err != nil {
		return err
	}
	if err := s.createICloudResourcesAndMarkImportSucceeded(ctx, record, lines, failures, failureObjectKey, summary); err != nil {
		if errors.Is(err, ErrICloudImportClaim) {
			return ErrICloudImportClaim
		}
		if isICloudDuplicateError(err) {
			return s.failICloudImport(ctx, record, iCloudImportFailure{
				Category: "duplicate_resource", SafeMessage: "An iCloud resource in the import already exists.",
			})
		}
		return ErrICloudImportTemporary
	}
	return nil
}

func (s *Service) iCloudImportRecord(ctx context.Context, importID uint) (*iCloudImportModel, error) {
	if s == nil || s.db == nil || importID == 0 {
		return nil, ErrICloudImportNotFound
	}
	var record iCloudImportModel
	err := s.db.WithContext(ctx).First(&record, importID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrICloudImportNotFound
	}
	if err != nil {
		return nil, ErrICloudImportTemporary
	}
	return &record, nil
}

func (s *Service) iCloudImportProcessedLines(ctx context.Context, importID uint) (map[int]struct{}, error) {
	var rows []struct {
		LineNumber int `gorm:"column:line_number"`
	}
	if err := s.db.WithContext(ctx).Model(&iCloudImportItemModel{}).
		Select("line_number").Where("import_id = ?", importID).Find(&rows).Error; err != nil {
		return nil, ErrICloudImportTemporary
	}
	processed := make(map[int]struct{}, len(rows))
	for _, row := range rows {
		processed[row.LineNumber] = struct{}{}
	}
	return processed, nil
}

func (s *Service) markICloudImportRunning(ctx context.Context, task iCloudImportTask) (string, bool, error) {
	if s == nil || s.db == nil || task.ImportID == 0 || task.Generation == 0 {
		return "", false, ErrICloudImportInvalid
	}
	claimToken := platform.NewUUIDV7String()
	now := s.now().UTC()
	result := s.db.WithContext(ctx).Model(&iCloudImportModel{}).
		Where("id = ? AND status = ? AND dispatch_status IN ? AND generation = ?", task.ImportID, iCloudImportProcessing, []string{"pending", "queued"}, task.Generation).
		Updates(map[string]any{
			"dispatch_status": "running", "claim_token": claimToken,
			"started_at": now, "updated_at": now,
		})
	if result.Error != nil {
		return "", false, ErrICloudImportTemporary
	}
	return claimToken, result.RowsAffected == 1, nil
}

// ReleaseICloudImport returns an exhausted infrastructure task to the durable
// dispatcher without consuming a business import attempt.
func (s *Service) ReleaseICloudImport(ctx context.Context, task iCloudImportTask, safeError string) error {
	if s == nil || s.db == nil || task.ImportID == 0 || task.Generation == 0 {
		return ErrICloudImportClaim
	}
	result := s.db.WithContext(ctx).Model(&iCloudImportModel{}).
		Where("id = ? AND status = ? AND dispatch_status IN ? AND generation = ?", task.ImportID, iCloudImportProcessing, []string{"queued", "running"}, task.Generation).
		Updates(map[string]any{
			"dispatch_status": "pending", "generation": gorm.Expr("generation + 1"), "claim_token": "",
			"last_safe_error": safeICloudImportMessage(safeError), "updated_at": s.now().UTC(),
		})
	if result.Error != nil {
		return ErrICloudImportTemporary
	}
	if result.RowsAffected != 1 {
		return ErrICloudImportClaim
	}
	return nil
}

func deduplicateICloudImportLines(lines []iCloudImportLine, strategy coreDomain.ImportErrorStrategy) ([]iCloudImportLine, []iCloudImportFailure, *iCloudImportFailure) {
	seenEmails := make(map[string]int, len(lines))
	result := make([]iCloudImportLine, 0, len(lines))
	var failures []iCloudImportFailure
	for _, line := range lines {
		category, firstLine := "", 0
		if firstLine = seenEmails[iCloudImportEmailKey(line.PrimaryEmail)]; firstLine != 0 {
			category = "duplicate_email"
		}
		if category != "" {
			failure := iCloudImportFailure{
				Line: line.LineNumber, Email: line.PrimaryEmail, Category: category,
				SafeMessage: fmt.Sprintf("Duplicate iCloud resource in import file; first occurrence is line %d.", firstLine),
			}
			if strategy == coreDomain.ImportErrorStrategyAbort {
				return nil, nil, &failure
			}
			failures = append(failures, failure)
			continue
		}
		seenEmails[iCloudImportEmailKey(line.PrimaryEmail)] = line.LineNumber
		result = append(result, line)
	}
	return result, failures, nil
}

func (s *Service) removeExistingICloudImportLines(ctx context.Context, ownerUserID uint, lines []iCloudImportLine, strategy coreDomain.ImportErrorStrategy) ([]iCloudImportLine, []iCloudImportFailure, *iCloudImportFailure, error) {
	if len(lines) == 0 {
		return lines, nil, nil, nil
	}
	emails := make([]string, 0, len(lines))
	for _, line := range lines {
		emails = append(emails, line.PrimaryEmail)
	}
	var existing []struct {
		ID           uint   `gorm:"column:id"`
		OwnerUserID  uint   `gorm:"column:owner_user_id"`
		PrimaryEmail string `gorm:"column:primary_email"`
		Status       string `gorm:"column:status"`
	}
	if err := s.db.WithContext(ctx).Table("icloud_resources AS ir").
		Select("ir.id, er.owner_user_id, ir.primary_email, ir.status").
		Joins("JOIN email_resources AS er ON er.id = ir.id AND er.type = ?", "icloud").
		Where("LOWER(ir.primary_email) IN ?", emails).Find(&existing).Error; err != nil {
		return nil, nil, nil, ErrICloudImportTemporary
	}
	byEmail := make(map[string]struct {
		ID          uint
		OwnerUserID uint
		Status      string
	}, len(existing))
	for _, item := range existing {
		value := struct {
			ID          uint
			OwnerUserID uint
			Status      string
		}{item.ID, item.OwnerUserID, item.Status}
		byEmail[iCloudImportEmailKey(item.PrimaryEmail)] = value
	}
	result := make([]iCloudImportLine, 0, len(lines))
	var failures []iCloudImportFailure
	for _, line := range lines {
		emailMatch, emailExists := byEmail[iCloudImportEmailKey(line.PrimaryEmail)]
		if !emailExists {
			result = append(result, line)
			continue
		}
		match := emailMatch
		// Credentials are deliberately replaceable. Cookie health is a channel
		// concern and must never make the account itself abnormal.
		if match.ID != 0 && match.OwnerUserID == ownerUserID {
			line.ExistingResourceID = match.ID
			result = append(result, line)
			continue
		}
		category := "duplicate_email"
		failure := iCloudImportFailure{
			Line: line.LineNumber, Email: line.PrimaryEmail, Category: category,
			SafeMessage: "iCloud resource already exists.",
		}
		if strategy == coreDomain.ImportErrorStrategyAbort {
			return nil, nil, &failure, nil
		}
		failures = append(failures, failure)
	}
	return result, failures, nil, nil
}

func (s *Service) createICloudResourcesAndMarkImportSucceeded(
	ctx context.Context,
	record *iCloudImportModel,
	lines []iCloudImportLine,
	failures []iCloudImportFailure,
	failureObjectKey string,
	summary string,
) error {
	if s == nil || s.db == nil || record == nil || record.ID == 0 || record.ClaimToken == "" {
		return ErrICloudImportClaim
	}
	for start := 0; start < len(lines); start += iCloudImportBatchSize {
		end := min(start+iCloudImportBatchSize, len(lines))
		chunk := lines[start:end]
		now := s.now().UTC()
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			locked, err := lockICloudImportTx(tx, record.ID, record.ClaimToken)
			if err != nil {
				return err
			}
			expiresAt := locked.ResourceExpireAt
			if expiresAt.IsZero() {
				expiresAt = locked.CreatedAt.AddDate(0, 1, 0)
				if locked.CreatedAt.IsZero() {
					expiresAt = now.AddDate(0, 1, 0)
				}
			}
			newLineIndexes := make([]int, 0, len(chunk))
			for index, line := range chunk {
				if line.ExistingResourceID == 0 {
					newLineIndexes = append(newLineIndexes, index)
				}
			}
			roots := make([]iCloudRootModel, len(newLineIndexes))
			for index := range roots {
				roots[index] = iCloudRootModel{Type: "icloud", OwnerUserID: locked.OwnerUserID, Version: 1, CreatedAt: now, UpdatedAt: now}
			}
			if len(roots) > 0 {
				if err := tx.CreateInBatches(&roots, iCloudImportBatchSize).Error; err != nil {
					return err
				}
			}
			resourceIDs := make([]uint, len(chunk))
			for index, lineIndex := range newLineIndexes {
				resourceIDs[lineIndex] = roots[index].ID
			}
			resources := make([]iCloudResourceModel, 0, len(newLineIndexes))
			items := make([]iCloudImportItemModel, 0, len(chunk))
			for index, line := range chunk {
				if line.ExistingResourceID != 0 {
					var root iCloudRootModel
					if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
						Where("id = ? AND type = ? AND owner_user_id = ?", line.ExistingResourceID, "icloud", locked.OwnerUserID).First(&root).Error; err != nil {
						return ErrICloudImportClaim
					}
					var existing iCloudResourceModel
					if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&existing, root.ID).Error; err != nil {
						return err
					}
					credentialRevision := existing.CredentialRevision + 1
					if credentialRevision == 1 {
						credentialRevision = 2
					}
					validationGeneration := existing.ValidationGeneration + 1
					if validationGeneration == 1 {
						validationGeneration = 2
					}
					updates := map[string]any{
						"primary_email": line.PrimaryEmail,
						"expire_at":     expiresAt, "credential_revision": credentialRevision, "credential_updated_at": now,
						"validation_generation": validationGeneration, "validation_failures": 0,
						"selected_forward_to": "", "alias_count": 0, "last_alias_sync_at": nil,
						"alias_provision_candidate": "", "alias_provision_reconcile": false,
						"next_provision_at": nil, "last_safe_error": "", "updated_at": now,
					}
					if existing.Status != iCloudResourceDisabled {
						updates["status"] = iCloudResourcePending
						updates["next_validation_at"] = now
					} else {
						updates["next_validation_at"] = nil
					}
					resourceUpdated := tx.Model(&iCloudResourceModel{}).Where("id = ?", existing.ID).Updates(updates)
					if resourceUpdated.Error != nil {
						return resourceUpdated.Error
					}
					if resourceUpdated.RowsAffected != 1 {
						return ErrICloudImportClaim
					}
					if err := tx.Model(&iCloudAliasModel{}).
						Where("resource_id = ? AND status <> ?", existing.ID, iCloudResourceDeleted).
						Updates(map[string]any{"status": "missing", "updated_at": now}).Error; err != nil {
						return err
					}
					rootUpdated := tx.Model(&iCloudRootModel{}).Where("id = ? AND version = ?", root.ID, root.Version).
						Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now})
					if rootUpdated.Error != nil {
						return rootUpdated.Error
					}
					if rootUpdated.RowsAffected != 1 {
						return ErrICloudImportClaim
					}
					if err := upsertICloudImportChannelsTx(tx, existing.ID, line.Channels, now); err != nil {
						return err
					}
					resourceIDs[index] = existing.ID
				} else {
					resources = append(resources, iCloudResourceModel{
						ID: resourceIDs[index], ResourceType: "icloud", PrimaryEmail: line.PrimaryEmail,
						ExpireAt: expiresAt, ForSale: false, Status: iCloudResourcePending,
						CredentialRevision: 1, CredentialUpdatedAt: now, ValidationGeneration: 1, NextValidationAt: iCloudTimePointer(now), CreatedAt: now, UpdatedAt: now,
					})
				}
				resourceID := resourceIDs[index]
				items = append(items, iCloudImportItemModel{ImportID: locked.ID, ResourceID: &resourceID, LineNumber: line.LineNumber, Outcome: "imported"})
			}
			if len(resources) > 0 {
				if err := tx.CreateInBatches(&resources, iCloudImportBatchSize).Error; err != nil {
					return err
				}
				for index, line := range chunk {
					if line.ExistingResourceID != 0 {
						continue
					}
					if err := upsertICloudImportChannelsTx(tx, resourceIDs[index], line.Channels, now); err != nil {
						return err
					}
				}
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&items, iCloudImportBatchSize).Error; err != nil {
				return err
			}
			updated := tx.Model(&iCloudImportModel{}).
				Where("id = ? AND status = ? AND dispatch_status = ? AND claim_token = ?", locked.ID, iCloudImportProcessing, "running", record.ClaimToken).
				Updates(map[string]any{"imported_count": gorm.Expr("imported_count + ?", len(chunk)), "updated_at": now})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrICloudImportClaim
			}
			return nil
		})
		if err != nil {
			return err
		}
	}

	now := s.now().UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockICloudImportTx(tx, record.ID, record.ClaimToken)
		if err != nil {
			return err
		}
		items := make([]iCloudImportItemModel, 0, len(failures))
		for _, failure := range failures {
			if failure.Line > 0 {
				items = append(items, iCloudImportItemModel{
					ImportID: locked.ID, LineNumber: failure.Line, Outcome: "skipped",
					Category: failure.Category, LastSafeError: safeICloudImportMessage(failure.SafeMessage),
				})
			}
		}
		if len(items) > 0 {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&items, iCloudImportBatchSize).Error; err != nil {
				return err
			}
		}
		var counts struct {
			Imported int `gorm:"column:imported"`
			Skipped  int `gorm:"column:skipped"`
		}
		if err := tx.Model(&iCloudImportItemModel{}).
			Select("COALESCE(SUM(CASE WHEN outcome = 'imported' THEN 1 ELSE 0 END), 0) AS imported, COALESCE(SUM(CASE WHEN outcome = 'skipped' THEN 1 ELSE 0 END), 0) AS skipped").
			Where("import_id = ?", locked.ID).Scan(&counts).Error; err != nil {
			return err
		}
		updated := tx.Model(&iCloudImportModel{}).
			Where("id = ? AND status = ? AND dispatch_status = ? AND claim_token = ?", locked.ID, iCloudImportProcessing, "running", record.ClaimToken).
			Updates(map[string]any{
				"status": iCloudImportImported, "imported_count": counts.Imported, "accepted_count": counts.Imported, "skipped_count": counts.Skipped,
				"failure_object_key": failureObjectKey, "last_safe_error": safeICloudImportMessage(summary),
				"dispatch_status": "succeeded", "claim_token": "", "finished_at": now, "updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrICloudImportClaim
		}
		return nil
	})
}

func (s *Service) failICloudImport(ctx context.Context, record *iCloudImportModel, failure iCloudImportFailure) error {
	if record == nil || record.ID == 0 || record.ClaimToken == "" {
		return ErrICloudImportClaim
	}
	failureObjectKey, _, err := s.saveICloudImportFailures(ctx, record, []iCloudImportFailure{failure})
	if err != nil {
		return err
	}
	terminal, err := s.markICloudImportBusinessFailure(ctx, record, failure, failureObjectKey)
	if err != nil {
		return err
	}
	if !terminal {
		_ = s.ScheduleICloudImportDispatcher(context.WithoutCancel(ctx), time.Second)
	}
	return nil
}

func (s *Service) markICloudImportBusinessFailure(ctx context.Context, record *iCloudImportModel, failure iCloudImportFailure, failureObjectKey string) (bool, error) {
	terminal := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockICloudImportTx(tx, record.ID, record.ClaimToken)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		nextAttempts := locked.Attempts + 1
		updates := map[string]any{
			"attempts": nextAttempts, "failure_object_key": failureObjectKey,
			"last_safe_error": safeICloudImportMessage(failure.SafeMessage), "claim_token": "", "updated_at": now,
		}
		if nextAttempts >= locked.MaxAttempts {
			terminal = true
			updates["status"] = iCloudImportFailed
			updates["dispatch_status"] = "failed"
			updates["finished_at"] = now
		} else {
			updates["dispatch_status"] = "pending"
			updates["generation"] = locked.Generation + 1
		}
		updated := tx.Model(&iCloudImportModel{}).
			Where("id = ? AND status = ? AND dispatch_status = ? AND claim_token = ?", locked.ID, iCloudImportProcessing, "running", record.ClaimToken).
			Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrICloudImportClaim
		}
		return nil
	})
	if errors.Is(err, ErrICloudImportClaim) {
		return false, ErrICloudImportClaim
	}
	if err != nil {
		return false, ErrICloudImportTemporary
	}
	return terminal, nil
}

func lockICloudImportTx(tx *gorm.DB, importID uint, claimToken string) (*iCloudImportModel, error) {
	var record iCloudImportModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&record, importID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrICloudImportClaim
		}
		return nil, err
	}
	if record.Status != iCloudImportProcessing || record.DispatchStatus != "running" || record.ClaimToken != claimToken {
		return nil, ErrICloudImportClaim
	}
	return &record, nil
}

func (s *Service) saveICloudImportFailures(ctx context.Context, record *iCloudImportModel, failures []iCloudImportFailure) (string, string, error) {
	if len(failures) == 0 {
		return "", "", nil
	}
	now := s.now().UTC()
	stored, err := s.files.SavePrivate(ctx, governancedomain.PrivateFile{
		ObjectKey: iCloudImportObjectKey("failures", record.OwnerUserID, now, record.RequestID, ".csv"),
		FileName:  "icloud-import-failures.csv", ContentType: "text/csv; charset=utf-8",
		ContentBytes: []byte(iCloudImportFailuresCSV(failures)),
	})
	if err != nil || stored == nil {
		return "", "", ErrICloudImportStorage
	}
	return stored.ObjectKey, skippedICloudImportSummary(len(failures)), nil
}

func iCloudImportFailuresCSV(failures []iCloudImportFailure) string {
	var builder strings.Builder
	builder.WriteString("line,email,category,message\n")
	for _, failure := range failures {
		fmt.Fprintf(&builder, "%d,%s,%s,%s\n", failure.Line, csvICloudImportSafe(failure.Email), csvICloudImportSafe(failure.Category), csvICloudImportSafe(failure.SafeMessage))
	}
	return builder.String()
}

func csvICloudImportSafe(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	trimmed := strings.TrimLeft(value, " ")
	if trimmed != "" && strings.ContainsRune("\t=+-@", rune(trimmed[0])) {
		value = "'" + value
	}
	value = strings.ReplaceAll(value, `"`, `""`)
	return `"` + value + `"`
}

func skippedICloudImportSummary(count int) string {
	if count == 1 {
		return "Skipped 1 import entry."
	}
	return fmt.Sprintf("Skipped %d import entries.", count)
}

func iCloudImportFingerprint(ownerUserID uint, strategy coreDomain.ImportErrorStrategy, resourceExpireAt time.Time, content []byte) string {
	contentSum := sha256.Sum256(content)
	payload := fmt.Sprintf("icloud\x00%d\x00%s\x00%s\x00%s", ownerUserID, strategy,
		resourceExpireAt.UTC().Format(time.RFC3339Nano), hex.EncodeToString(contentSum[:]))
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func iCloudImportObjectKey(kind string, ownerUserID uint, now time.Time, requestID, suffix string) string {
	return fmt.Sprintf("imports/icloud/%s/%04d/%02d/%02d/%d/%s%s", kind, now.Year(), now.Month(), now.Day(), ownerUserID, safeICloudImportObjectSegment(requestID), suffix)
}

func cleanICloudImportFileName(fileName string) string {
	base := path.Base(strings.TrimSpace(fileName))
	if base == "." || base == "/" || base == "" {
		return "icloud-resources.txt"
	}
	return base
}

func safeICloudImportObjectSegment(value string) string {
	var builder strings.Builder
	for _, runeValue := range value {
		if runeValue >= 'a' && runeValue <= 'z' || runeValue >= 'A' && runeValue <= 'Z' || runeValue >= '0' && runeValue <= '9' || runeValue == '-' || runeValue == '_' {
			builder.WriteRune(runeValue)
		}
	}
	if builder.Len() == 0 {
		return platform.NewUUIDV7String()
	}
	return builder.String()
}

func iCloudImportEmailKey(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func safeICloudImportMessage(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) > 500 {
		return string(runes[:500])
	}
	return value
}

func isICloudDuplicateError(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var mysqlError *mysql.MySQLError
	return errors.As(err, &mysqlError) && mysqlError.Number == 1062
}

func upsertICloudImportChannelsTx(tx *gorm.DB, resourceID uint, channels []iCloudImportChannel, now time.Time) error {
	if tx == nil || resourceID == 0 {
		return ErrICloudImportClaim
	}
	seen := make(map[string]struct{}, len(channels))
	for _, channel := range channels {
		kind := strings.TrimSpace(channel.Kind)
		if kind != iCloudChannelWeb && kind != iCloudChannelAppleAccount {
			return ErrICloudImportInvalid
		}
		seen[kind] = struct{}{}
		row := iCloudResourceChannelModel{
			ResourceID: resourceID, Kind: kind, Host: strings.TrimSpace(channel.Host), Cookie: strings.TrimSpace(channel.Cookie),
			Origin: strings.TrimSpace(channel.Origin), Referer: strings.TrimSpace(channel.Referer), UserAgent: strings.TrimSpace(channel.UserAgent),
			FDClientInfo: strings.TrimSpace(channel.FDClientInfo),
			DSID:         strings.TrimSpace(channel.DSID), ClientID: strings.TrimSpace(channel.ClientID),
			ClientBuildNumber: strings.TrimSpace(channel.ClientBuildNumber), ClientMasteringNumber: strings.TrimSpace(channel.ClientMasteringNumber),
			Scnt: strings.TrimSpace(channel.Scnt), SessionStatus: iCloudSessionUnchecked, CreatedAt: now, UpdatedAt: now,
		}
		var current iCloudResourceChannelModel
		result := tx.Where("resource_id = ? AND kind = ?", resourceID, kind).First(&current)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			continue
		}
		if result.Error != nil {
			return result.Error
		}
		updates := map[string]any{
			"host": row.Host, "cookie": row.Cookie, "origin": row.Origin, "referer": row.Referer, "user_agent": row.UserAgent,
			"fd_client_info": row.FDClientInfo,
			"dsid":           row.DSID, "client_id": row.ClientID, "client_build_number": row.ClientBuildNumber,
			"client_mastering_number": row.ClientMasteringNumber, "scnt": row.Scnt,
			"session_status": iCloudSessionUnchecked, "session_failures": 0, "cooldown_until": nil, "cooldown_stage": 0,
			"next_keepalive_at": nil, "last_checked_at": nil, "last_valid_at": nil, "updated_at": now,
		}
		if err := tx.Model(&iCloudResourceChannelModel{}).Where("id = ?", current.ID).Updates(updates).Error; err != nil {
			return err
		}
	}
	// The import/edit line is the complete credential set. Do not leave an
	// omitted channel silently active after an operator intentionally replaced it.
	if len(seen) == 0 {
		return ErrICloudImportInvalid
	}
	kinds := make([]string, 0, len(seen))
	for kind := range seen {
		kinds = append(kinds, kind)
	}
	if err := tx.Where("resource_id = ? AND kind NOT IN ?", resourceID, kinds).Delete(&iCloudResourceChannelModel{}).Error; err != nil {
		return err
	}
	return nil
}
