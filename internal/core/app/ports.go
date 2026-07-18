package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/core/domain"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform"
)

// EmailResourceRepository defines the persistence contract for email resources.
type EmailResourceRepository interface {
	// CreateMicrosoft creates a new Microsoft resource within a transaction.
	CreateMicrosoft(ctx context.Context, resource *domain.EmailResource, ms *domain.MicrosoftResource) error

	// CreateDomain creates a new Domain resource within a transaction.
	CreateDomain(ctx context.Context, resource *domain.EmailResource, dr *domain.MailDomainResource) error

	// FindByID looks up a resource by ID. Returns nil, nil if not found.
	FindByID(ctx context.Context, id uint) (*domain.EmailResource, error)

	// FindMicrosoftByID looks up a Microsoft resource by resource ID. Returns nil, nil if not found.
	FindMicrosoftByID(ctx context.Context, resourceID uint) (*domain.MicrosoftResource, error)

	// FindDomainByID looks up a domain resource by resource ID. Returns nil, nil if not found.
	FindDomainByID(ctx context.Context, resourceID uint) (*domain.MailDomainResource, error)

	// FindMicrosoftByEmail looks up a Microsoft resource by email address.
	FindMicrosoftByEmail(ctx context.Context, email string) (*domain.MicrosoftResource, error)

	// FindExistingMicrosoftEmails returns the imported emails that already exist.
	FindExistingMicrosoftEmails(ctx context.Context, emails []string) (map[string]struct{}, error)

	// List returns paginated resources owned by a user.
	List(ctx context.Context, ownerUserID uint, filter ResourceListFilter, offset, limit int, afterID uint) ([]domain.EmailResource, error)

	// ListAll returns paginated resources (admin).
	ListAll(ctx context.Context, filter ResourceListFilter, offset, limit int, afterID uint) ([]domain.EmailResource, error)

	// Count returns the total count of resources for a user.
	Count(ctx context.Context, ownerUserID uint, filter ResourceListFilter) (int64, error)

	// CountAll returns the total count of all resources.
	CountAll(ctx context.Context, filter ResourceListFilter) (int64, error)

	// Facets returns aggregate counts for resource list filters.
	Facets(ctx context.Context, ownerUserID uint, filter ResourceListFilter) (*ResourceListFacets, error)

	// UpdateMicrosoft updates non-credential Microsoft resource fields and writes OperationLog.
	UpdateMicrosoftWithLog(ctx context.Context, resource *domain.MicrosoftResource, log *governancedomain.OperationLog) error

	// PublishMicrosoftWithLog atomically publishes one owned Microsoft resource and writes OperationLog only on state change.
	PublishMicrosoftWithLog(ctx context.Context, ownerUserID uint, resourceID uint, log governancedomain.OperationLog) (bool, error)

	// PublishResourcesBatchWithLog validates and publishes selected owned resources.
	PublishResourcesBatchWithLog(ctx context.Context, ownerUserID uint, resourceIDs []uint, microsoftLog governancedomain.OperationLog, domainLog governancedomain.OperationLog) ([]uint, error)

	// PublishResourcesByFilterWithLog publishes owned resources matching a server-side filter in chunks.
	PublishResourcesByFilterWithLog(ctx context.Context, ownerUserID uint, filter ResourceBulkFilter, microsoftLog governancedomain.OperationLog, domainLog governancedomain.OperationLog) (int, error)

	// DeletePrivateMicrosoftWithLog atomically marks one owned private Microsoft resource as deleted and writes OperationLog.
	DeletePrivateMicrosoftWithLog(ctx context.Context, ownerUserID uint, resourceID uint, log governancedomain.OperationLog) error

	// PublishDomainWithLog atomically publishes one owned domain resource and writes OperationLog only on state change.
	PublishDomainWithLog(ctx context.Context, ownerUserID uint, resourceID uint, log governancedomain.OperationLog) (bool, error)

	// DeletePrivateDomainWithLog atomically marks one owned private domain resource as deleted and writes OperationLog.
	DeletePrivateDomainWithLog(ctx context.Context, ownerUserID uint, resourceID uint, log governancedomain.OperationLog) error

	// DeleteResourcesBatchWithLog validates ownership and marks owned private resources as deleted in one transaction.
	DeleteResourcesBatchWithLog(ctx context.Context, ownerUserID uint, resourceIDs []uint, microsoftLog governancedomain.OperationLog, domainLog governancedomain.OperationLog) ([]uint, error)

	// DeleteResourcesByFilterWithLog marks owned private resources matching a server-side filter as deleted with a set-based update.
	DeleteResourcesByFilterWithLog(ctx context.Context, ownerUserID uint, filter ResourceBulkFilter, microsoftLog governancedomain.OperationLog, domainLog governancedomain.OperationLog) (int, error)

	// ListMicrosoftStatus returns API-safe status for a batch of Microsoft resources.
	ListMicrosoftStatus(ctx context.Context, ids []uint) ([]MicrosoftStatusResult, error)

	// ListDomainStatus returns API-safe status for a batch of domain resources.
	ListDomainStatus(ctx context.Context, ids []uint) ([]DomainStatusResult, error)
}

// ResourceImportRepository persists safe import artifact metadata.
type ResourceImportRepository interface {
	Create(ctx context.Context, item *domain.ResourceImport) error
	FindByID(ctx context.Context, id uint) (*domain.ResourceImport, error)
	MarkFailed(ctx context.Context, id uint, failureObjectKey string, safeError string) error
	CreateMicrosoftResourcesAndMarkSucceeded(ctx context.Context, id uint, claimToken string, lines []domain.MicrosoftImportLine, resources []domain.EmailResource, ms []domain.MicrosoftResource, skippedItems []AdminResourceImportSkippedItem, failureObjectKey string, safeSummary string, afterCreate func(context.Context, []domain.MicrosoftResource, []uint) error) ([]uint, error)
}

type AdminResourceImportMetadata struct {
	OperatorUserID     uint
	LongLived          bool
	ErrorStrategy      domain.ImportErrorStrategy
	RequestID          string
	Path               string
	IdempotencyKey     string
	RequestFingerprint string
}

type AdminResourceImportRepository interface {
	FindAdminByIdempotency(ctx context.Context, operatorUserID uint, idempotencyKey string) (*domain.ResourceImport, string, error)
	CreateAdminWithLog(ctx context.Context, item *domain.ResourceImport, metadata AdminResourceImportMetadata, log *governancedomain.OperationLog) (*domain.ResourceImport, bool, error)
}

type AdminResourceImportProgressRepository interface {
	SetAdminImportCounts(ctx context.Context, importID uint, claimToken string, accepted, skipped int) error
	ListAdminImportProcessedLines(ctx context.Context, importID uint) (map[int]struct{}, error)
}

type AdminResourceImportSkippedItem struct {
	LineNumber int
	Category   string
	SafeError  string
}

type AdminResourceImportDispatchItem struct {
	ImportID      uint
	OwnerUserID   uint
	LongLived     bool
	ErrorStrategy domain.ImportErrorStrategy
	RequestID     string
	Generation    uint64
}

type AdminResourceImportDispatchRepository interface {
	ClaimAdminImportDispatchable(ctx context.Context, limit int, runningStaleBefore, queuedDispatchStaleBefore time.Time) ([]AdminResourceImportDispatchItem, error)
	MarkAdminImportDispatched(ctx context.Context, importID uint, generation uint64) (activated bool, err error)
	MarkAdminImportRunning(ctx context.Context, importID uint, generation uint64) (claimToken string, claimed bool, err error)
	MarkAdminImportPending(ctx context.Context, importID uint, generation uint64, safeError string) error
	MarkAdminImportFailed(ctx context.Context, importID uint, claimToken, failureObjectKey, safeError string) error
}

// ResourceImportQueue enqueues asynchronous import work.
type ResourceImportQueue interface {
	EnqueueMicrosoftImport(ctx context.Context, task MicrosoftImportTask) (accepted bool, err error)
}

// MicrosoftBindingInputRecorder records MailTransport auxiliary mailbox inputs
// collected during Microsoft TXT import. Core owns the TXT parsing, while the
// binding state machine remains in BC-MAILTRANSPORT.
type MicrosoftBindingInputRecorder interface {
	RecordMicrosoftBindingInputs(ctx context.Context, inputs []MicrosoftBindingInput) error
}

type MicrosoftBindingInput struct {
	OwnerUserID    uint
	EmailAddress   string
	BindingAddress string
}

// MicrosoftImportTask is the safe queue payload for a Microsoft resource import.
// SourceObjectKey remains available only for legacy in-memory callers. Durable
// workers resolve it from ResourceImport after dequeueing, so private storage
// paths can never be serialized into Redis/Asynq.
type MicrosoftImportTask struct {
	ImportID        uint                       `json:"importId"`
	OwnerUserID     uint                       `json:"ownerUserId"`
	SourceObjectKey string                     `json:"-"`
	LongLived       bool                       `json:"longLived"`
	ErrorStrategy   domain.ImportErrorStrategy `json:"errorStrategy"`
	RequestID       string                     `json:"requestId"`
	Generation      uint64                     `json:"generation,omitempty"`
	ClaimToken      string                     `json:"-"`
}

// MicrosoftImportProcessResult reports resources created or restored by one import task.
type MicrosoftImportProcessResult struct {
	ImportedResourceIDs []uint
	Imported            int
}

// MailServerRepository defines the persistence contract for mail servers.
type MailServerRepository interface {
	Create(ctx context.Context, server *domain.MailServer) error
	FindByID(ctx context.Context, id uint) (*domain.MailServer, error)
	GetOrCreateDefaultInbound(ctx context.Context, ownerUserID uint, name, serverAddress, mxRecord string) (*domain.MailServer, error)
	List(ctx context.Context, ownerUserID uint, offset, limit int) ([]domain.MailServer, error)
	ListAll(ctx context.Context, offset, limit int) ([]domain.MailServer, error)
	Count(ctx context.Context, ownerUserID uint) (int64, error)
	CountAll(ctx context.Context) (int64, error)
}

// GeneratedMailboxRepository defines the persistence contract for generated mailboxes.
type GeneratedMailboxRepository interface {
	List(ctx context.Context, domainResourceID uint, ownerUserID uint, offset, limit int) ([]domain.GeneratedMailbox, error)
	Count(ctx context.Context, domainResourceID uint, ownerUserID uint) (int64, error)
	DisableWithLog(ctx context.Context, mailboxID uint, log *governancedomain.OperationLog) error
}

// TXTParser parses resource import TXT files.
type TXTParser interface {
	ParseMicrosoftImport(content string, strategy domain.ImportErrorStrategy) ([]domain.MicrosoftImportLine, []domain.ImportLineError, error)
}

// ImportUseCase handles supplier resource import operations.
type ImportUseCase struct {
	resources       EmailResourceRepository
	imports         ResourceImportRepository
	parser          TXTParser
	files           governanceapp.FilePort
	queue           ResourceImportQueue
	bindingRecorder MicrosoftBindingInputRecorder
}

var ErrImportTemporaryUnavailable = errors.New("resource import temporarily unavailable")

// NewImportUseCase creates a new ImportUseCase.
func NewImportUseCase(resources EmailResourceRepository, imports ResourceImportRepository, parser TXTParser, files governanceapp.FilePort, queue ResourceImportQueue, recorders ...MicrosoftBindingInputRecorder) *ImportUseCase {
	var recorder MicrosoftBindingInputRecorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	return &ImportUseCase{
		resources:       resources,
		imports:         imports,
		parser:          parser,
		files:           files,
		queue:           queue,
		bindingRecorder: recorder,
	}
}

// AcceptMicrosoftTXTFile stores the TXT artifact and enqueues asynchronous import processing.
func (uc *ImportUseCase) AcceptMicrosoftTXTFile(ctx context.Context, ownerUserID uint, fileName string, content []byte, longLived bool, errorStrategy domain.ImportErrorStrategy, requestID string) (*ImportResult, error) {
	if len(content) == 0 {
		return nil, domain.ErrInvalidImportFormat
	}
	if normalized, ok := domain.NormalizeImportErrorStrategy(string(errorStrategy)); ok {
		errorStrategy = normalized
	} else {
		return nil, domain.ErrInvalidImportFormat
	}

	now := time.Now().UTC()
	importID := strings.TrimSpace(requestID)
	if importID == "" {
		importID = platform.NewUUIDV7String()
	}
	sourceObjectKey := importObjectKey("source", ownerUserID, now, importID, ".txt")
	storedSource, err := uc.files.SavePrivate(ctx, governancedomain.PrivateFile{
		ObjectKey:    sourceObjectKey,
		FileName:     cleanImportFileName(fileName),
		ContentType:  "text/plain; charset=utf-8",
		ContentBytes: content,
	})
	if err != nil {
		return nil, domain.ErrFileStorageUnavailable
	}

	importRecord := &domain.ResourceImport{
		OwnerUserID:     ownerUserID,
		ResourceType:    domain.ResourceTypeMicrosoft,
		LongLived:       longLived,
		ErrorStrategy:   errorStrategy,
		SourceObjectKey: storedSource.ObjectKey,
		Status:          domain.ResourceImportProcessing,
		DispatchStatus:  "pending",
		MaxAttempts:     3,
		RequestID:       importID,
	}
	if err := uc.imports.Create(ctx, importRecord); err != nil {
		return nil, err
	}
	// The import row is the durable queue. A transient Redis failure must not
	// turn an accepted import into a terminal failure; the periodic dispatcher
	// will claim it again from MySQL.
	_, _ = uc.DispatchAdminImports(ctx, 100)

	return &ImportResult{ImportID: importRecord.ID, Imported: 0}, nil
}

func (uc *ImportUseCase) AcceptAdminMicrosoftTXTFile(
	ctx context.Context,
	operatorUserID uint,
	ownerUserID uint,
	fileName string,
	content []byte,
	longLived bool,
	errorStrategy domain.ImportErrorStrategy,
	idempotencyKey string,
	requestID string,
	pathValue string,
) (*ImportResult, error) {
	adminImports, ok := uc.imports.(AdminResourceImportRepository)
	if !ok || operatorUserID == 0 || ownerUserID == 0 {
		return nil, domain.ErrResourceDependency
	}
	if len(content) == 0 {
		return nil, domain.ErrInvalidImportFormat
	}
	normalizedStrategy, ok := domain.NormalizeImportErrorStrategy(string(errorStrategy))
	if !ok {
		return nil, domain.ErrInvalidImportFormat
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		return nil, domain.ErrInvalidResourceCommand
	}
	fingerprint := adminImportFingerprint(ownerUserID, longLived, normalizedStrategy, content)
	existing, existingFingerprint, err := adminImports.FindAdminByIdempotency(ctx, operatorUserID, idempotencyKey)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existingFingerprint != fingerprint {
			return nil, domain.ErrResourceIdempotencyConflict
		}
		return &ImportResult{ImportID: existing.ID, Imported: existing.ImportedCount, Reused: true}, nil
	}

	now := time.Now().UTC()
	sourceObjectKey := importObjectKey("source", ownerUserID, now, requestID, ".txt")
	storedSource, err := uc.files.SavePrivate(ctx, governancedomain.PrivateFile{
		ObjectKey: sourceObjectKey, FileName: cleanImportFileName(fileName),
		ContentType: "text/plain; charset=utf-8", ContentBytes: content,
	})
	if err != nil {
		return nil, domain.ErrFileStorageUnavailable
	}
	importRecord := &domain.ResourceImport{
		OwnerUserID: ownerUserID, ResourceType: domain.ResourceTypeMicrosoft,
		SourceObjectKey: storedSource.ObjectKey, Status: domain.ResourceImportProcessing,
	}
	stored, created, err := adminImports.CreateAdminWithLog(ctx, importRecord, AdminResourceImportMetadata{
		OperatorUserID: operatorUserID, LongLived: longLived, ErrorStrategy: normalizedStrategy,
		RequestID: strings.TrimSpace(requestID), Path: strings.TrimSpace(pathValue),
		IdempotencyKey: idempotencyKey, RequestFingerprint: fingerprint,
	}, &governancedomain.OperationLog{
		OperatorUserID: operatorUserID, OperationType: "core.admin_resource.import",
		ResourceType: "microsoft_resource_import", ResourceID: "pending", Path: strings.TrimSpace(pathValue),
		Result: "success", SafeSummary: "Microsoft resource import accepted.", RequestID: strings.TrimSpace(requestID),
	})
	if err != nil {
		_ = uc.files.DeletePrivate(ctx, storedSource.ObjectKey)
		return nil, err
	}
	if !created {
		_ = uc.files.DeletePrivate(ctx, storedSource.ObjectKey)
		return &ImportResult{ImportID: stored.ID, Imported: stored.ImportedCount, Reused: true}, nil
	}
	// The database row is the durable source of truth. Claiming and enqueueing
	// use a fenced dispatcher token, so a transient Redis failure or a worker
	// racing the HTTP response cannot create an unfenced import execution.
	_, _ = uc.DispatchAdminImports(ctx, 100)
	return &ImportResult{ImportID: stored.ID}, nil
}

func (uc *ImportUseCase) DispatchAdminImports(ctx context.Context, limit int) (int, error) {
	dispatch, ok := uc.imports.(AdminResourceImportDispatchRepository)
	if !ok || uc.queue == nil {
		return 0, domain.ErrResourceDependency
	}
	if limit <= 0 {
		limit = 100
	}
	items, err := dispatch.ClaimAdminImportDispatchable(
		ctx, limit, time.Time{}, time.Time{},
	)
	if err != nil {
		return 0, err
	}
	queued := 0
	var result error
	for _, item := range items {
		accepted, err := uc.queue.EnqueueMicrosoftImport(ctx, MicrosoftImportTask{
			ImportID: item.ImportID, OwnerUserID: item.OwnerUserID,
			LongLived: item.LongLived, ErrorStrategy: item.ErrorStrategy, RequestID: item.RequestID,
			Generation: item.Generation,
		})
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if !accepted {
			continue
		}
		activated, activateErr := dispatch.MarkAdminImportDispatched(ctx, item.ImportID, item.Generation)
		if activateErr != nil {
			result = errors.Join(result, activateErr)
			continue
		}
		if activated {
			queued++
		}
	}
	return queued, result
}

// ProcessMicrosoftImport imports Microsoft resources from a stored TXT artifact.
// Each line uses the P1-I2 Microsoft TXT import format documented in docs/14.
func (uc *ImportUseCase) ProcessMicrosoftImport(ctx context.Context, task MicrosoftImportTask) (*MicrosoftImportProcessResult, error) {
	dispatch, adminTask := uc.imports.(AdminResourceImportDispatchRepository)
	if adminTask {
		generation := task.Generation
		if generation == 0 {
			return nil, domain.ErrResourceImportInvalidClaim
		}
		task.Generation = generation
		claimToken, claimed, err := dispatch.MarkAdminImportRunning(ctx, task.ImportID, generation)
		if err != nil {
			return nil, err
		}
		if !claimed {
			current, findErr := uc.imports.FindByID(ctx, task.ImportID)
			if findErr != nil {
				return nil, findErr
			}
			if current != nil && current.Generation == generation && current.Status == domain.ResourceImportProcessing && current.DispatchStatus != "failed" && current.DispatchStatus != "succeeded" {
				return nil, ErrImportTemporaryUnavailable
			}
			return &MicrosoftImportProcessResult{}, nil
		}
		task.ClaimToken = claimToken
	}

	result, err := uc.processMicrosoftImport(ctx, task)
	if err == nil || strings.TrimSpace(task.ClaimToken) == "" || !adminTask {
		return result, err
	}

	// A deterministic failure may already have atomically moved the import to
	// failed. Only a still-running claim is eligible for durable retry.
	current, findErr := uc.imports.FindByID(ctx, task.ImportID)
	if findErr != nil {
		return result, errors.Join(err, findErr)
	}
	if current == nil || current.Status == domain.ResourceImportImported || current.Status == domain.ResourceImportFailed {
		return result, err
	}
	if isNonRetryableMicrosoftImportError(err) {
		markErr := dispatch.MarkAdminImportFailed(ctx, task.ImportID, task.ClaimToken, "", "Invalid import task.")
		return result, errors.Join(err, markErr)
	}
	return result, err
}

func (uc *ImportUseCase) processMicrosoftImport(ctx context.Context, task MicrosoftImportTask) (*MicrosoftImportProcessResult, error) {
	if task.ImportID == 0 || task.OwnerUserID == 0 {
		return nil, domain.ErrInvalidImportFormat
	}

	importRecord, err := uc.imports.FindByID(ctx, task.ImportID)
	if err != nil {
		return nil, err
	}
	if importRecord == nil {
		return nil, domain.ErrResourceNotFound
	}
	if importRecord.Status == domain.ResourceImportImported || importRecord.Status == domain.ResourceImportFailed {
		return &MicrosoftImportProcessResult{}, nil
	}
	sourceObjectKey := importRecord.SourceObjectKey
	if importRecord.OwnerUserID != task.OwnerUserID || strings.TrimSpace(sourceObjectKey) == "" {
		return nil, domain.ErrInvalidImportFormat
	}
	// Direct in-memory invocations from older callers may still provide the
	// private object key. Treat it only as a consistency check; serialized
	// queue tasks intentionally omit it and always use the durable import fact.
	if strings.TrimSpace(task.SourceObjectKey) != "" && task.SourceObjectKey != sourceObjectKey {
		return nil, domain.ErrInvalidImportFormat
	}
	task.SourceObjectKey = sourceObjectKey

	now := time.Now().UTC()
	importID := strings.TrimSpace(task.RequestID)
	if importID == "" {
		importID = platform.NewUUIDV7String()
	}
	errorStrategy, ok := domain.NormalizeImportErrorStrategy(string(task.ErrorStrategy))
	if !ok {
		return nil, domain.ErrInvalidImportFormat
	}

	source, err := uc.files.ReadPrivate(ctx, task.SourceObjectKey)
	if err != nil {
		return nil, domain.ErrFileStorageUnavailable
	}

	lines, parseFailures, err := uc.parser.ParseMicrosoftImport(string(source.ContentBytes), errorStrategy)
	if err != nil {
		return &MicrosoftImportProcessResult{}, uc.failImport(ctx, task.ImportID, task.ClaimToken, task.OwnerUserID, now, importID, importFailureFromError(err))
	}
	processedLineCount := 0
	if progress, ok := uc.imports.(AdminResourceImportProgressRepository); ok && strings.TrimSpace(task.ClaimToken) != "" {
		processedLines, processedErr := progress.ListAdminImportProcessedLines(ctx, task.ImportID)
		if processedErr != nil {
			return nil, processedErr
		}
		if len(processedLines) > 0 {
			processedLineCount = len(processedLines)
			remaining := lines[:0]
			for _, line := range lines {
				if _, done := processedLines[line.LineNumber]; !done {
					remaining = append(remaining, line)
				}
			}
			lines = remaining
		}
	}
	failures := importFailuresFromLineErrors(parseFailures)
	if len(lines) == 0 && len(failures) == 0 && processedLineCount == 0 {
		return &MicrosoftImportProcessResult{}, uc.failImport(ctx, task.ImportID, task.ClaimToken, task.OwnerUserID, now, importID, importFailure{
			Line:        0,
			Category:    "invalid_format",
			SafeMessage: "Invalid import format.",
		})
	}

	if errorStrategy == domain.ImportErrorStrategyAbort {
		if failure, ok := uc.duplicateInFile(lines); ok {
			return &MicrosoftImportProcessResult{}, uc.failImport(ctx, task.ImportID, task.ClaimToken, task.OwnerUserID, now, importID, failure)
		}
	} else {
		var duplicateFailures []importFailure
		lines, duplicateFailures = uc.skipDuplicateLines(lines)
		failures = append(failures, duplicateFailures...)
	}

	emails := make([]string, 0, len(lines))
	for _, line := range lines {
		emails = append(emails, line.Email)
	}
	existingEmails, err := uc.resources.FindExistingMicrosoftEmails(ctx, emails)
	if err != nil {
		return nil, err
	}
	if len(existingEmails) > 0 {
		nextLines := lines[:0]
		for _, line := range lines {
			if _, exists := existingEmails[microsoftEmailKey(line.Email)]; exists {
				failure := importFailure{
					Line:        line.LineNumber,
					Email:       line.Email,
					Category:    "duplicate_email",
					SafeMessage: "Email address already exists.",
					Err:         domain.ErrDuplicateEmail,
				}
				if errorStrategy == domain.ImportErrorStrategyAbort {
					return &MicrosoftImportProcessResult{}, uc.failImport(ctx, task.ImportID, task.ClaimToken, task.OwnerUserID, now, importID, failure)
				}
				failures = append(failures, failure)
				continue
			}
			nextLines = append(nextLines, line)
		}
		lines = nextLines
	}

	resources := make([]domain.EmailResource, 0, len(lines))
	msResources := make([]domain.MicrosoftResource, 0, len(lines))

	for _, line := range lines {
		resources = append(resources, domain.EmailResource{
			Type:        domain.ResourceTypeMicrosoft,
			OwnerUserID: task.OwnerUserID,
			CreatedAt:   now,
			UpdatedAt:   now,
		})
		msResources = append(msResources, domain.MicrosoftResource{
			EmailAddress: line.Email,
			Password:     line.Password,
			ClientID:     line.ClientID,
			RefreshToken: line.RefreshToken,
			LongLived:    task.LongLived,
			ForSale:      false,
			Status:       domain.MicrosoftStatusPending,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
	}

	failureObjectKey, safeSummary, err := uc.saveImportFailures(ctx, task.OwnerUserID, now, importID, failures)
	if err != nil {
		return nil, err
	}
	if progress, ok := uc.imports.(AdminResourceImportProgressRepository); ok {
		if err := progress.SetAdminImportCounts(ctx, task.ImportID, task.ClaimToken, len(lines), len(failures)); err != nil {
			return nil, err
		}
	}
	skippedItems := make([]AdminResourceImportSkippedItem, 0, len(failures))
	for _, failure := range failures {
		if failure.Line <= 0 {
			continue
		}
		skippedItems = append(skippedItems, AdminResourceImportSkippedItem{
			LineNumber: failure.Line,
			Category:   failure.Category,
			SafeError:  failure.SafeMessage,
		})
	}

	linesByEmail := make(map[string]domain.MicrosoftImportLine, len(lines))
	for _, line := range lines {
		linesByEmail[microsoftEmailKey(line.Email)] = line
	}
	afterCreate := func(txCtx context.Context, created []domain.MicrosoftResource, _ []uint) error {
		chunkLines := make([]domain.MicrosoftImportLine, 0, len(created))
		for _, item := range created {
			if line, ok := linesByEmail[microsoftEmailKey(item.EmailAddress)]; ok {
				chunkLines = append(chunkLines, line)
			}
		}
		// Imported Microsoft resources are created as pending. The import worker
		// wakes the validation dispatcher after commit, so no validation job or
		// second durable batch belongs inside this transaction.
		return uc.recordMicrosoftBindingInputs(txCtx, task.OwnerUserID, chunkLines)
	}
	importedResourceIDs, err := uc.imports.CreateMicrosoftResourcesAndMarkSucceeded(
		ctx,
		task.ImportID,
		task.ClaimToken,
		lines,
		resources,
		msResources,
		skippedItems,
		failureObjectKey,
		safeSummary,
		afterCreate,
	)
	if err != nil {
		if errors.Is(err, domain.ErrDuplicateEmail) {
			return &MicrosoftImportProcessResult{}, uc.failImport(ctx, task.ImportID, task.ClaimToken, task.OwnerUserID, now, importID, importFailure{
				Line:        0,
				Category:    "duplicate_email",
				SafeMessage: "An email address in the import already exists.",
				Err:         domain.ErrDuplicateEmail,
			})
		}
		return nil, err
	}

	return &MicrosoftImportProcessResult{
		ImportedResourceIDs: importedResourceIDs,
		Imported:            len(importedResourceIDs),
	}, nil
}

func isNonRetryableMicrosoftImportError(err error) bool {
	return errors.Is(err, domain.ErrInvalidImportFormat) ||
		errors.Is(err, domain.ErrDuplicateEmail) ||
		errors.Is(err, domain.ErrResourceNotFound) ||
		errors.Is(err, domain.ErrForbiddenResource) ||
		errors.Is(err, domain.ErrInvalidResourceStatus) ||
		errors.Is(err, domain.ErrResourceImportInvalidClaim)
}

func (uc *ImportUseCase) recordMicrosoftBindingInputs(ctx context.Context, ownerUserID uint, lines []domain.MicrosoftImportLine) error {
	if uc.bindingRecorder == nil {
		return nil
	}
	inputs := make([]MicrosoftBindingInput, 0)
	for _, line := range lines {
		if strings.TrimSpace(line.BindingAddress) == "" {
			continue
		}
		inputs = append(inputs, MicrosoftBindingInput{
			OwnerUserID:    ownerUserID,
			EmailAddress:   line.Email,
			BindingAddress: line.BindingAddress,
		})
	}
	if len(inputs) == 0 {
		return nil
	}
	return uc.bindingRecorder.RecordMicrosoftBindingInputs(ctx, inputs)
}

// GetImportStatus returns a safe status view for one import owned by the current user.
func (uc *ImportUseCase) GetImportStatus(ctx context.Context, ownerUserID uint, importID uint) (*ResourceImportStatusView, error) {
	item, err := uc.imports.FindByID(ctx, importID)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, domain.ErrResourceNotFound
	}
	if item.OwnerUserID != ownerUserID {
		return nil, domain.ErrForbiddenResource
	}
	return &ResourceImportStatusView{
		ImportID:      item.ID,
		Status:        string(item.Status),
		Imported:      item.ImportedCount,
		Accepted:      item.AcceptedCount,
		Skipped:       item.SkippedCount,
		TaskStatus:    item.DispatchStatus,
		Attempts:      item.Attempts,
		MaxAttempts:   item.MaxAttempts,
		StartedAt:     item.StartedAt,
		FinishedAt:    item.FinishedAt,
		LastSafeError: item.LastSafeError,
		RequestID:     item.RequestID,
		CreatedAt:     item.CreatedAt,
		UpdatedAt:     item.UpdatedAt,
	}, nil
}

// GetAdminImportStatus returns only the safe durable import status summary. It does
// not expose the uploaded object key, failure object key, or imported secrets.
func (uc *ImportUseCase) GetAdminImportStatus(ctx context.Context, importID uint) (*ResourceImportStatusView, error) {
	item, err := uc.imports.FindByID(ctx, importID)
	if err != nil {
		return nil, err
	}
	if item == nil || item.ResourceType != domain.ResourceTypeMicrosoft || item.OperatorUserID == 0 {
		return nil, domain.ErrResourceNotFound
	}
	return &ResourceImportStatusView{
		ImportID:      item.ID,
		Status:        string(item.Status),
		Imported:      item.ImportedCount,
		Accepted:      item.AcceptedCount,
		Skipped:       item.SkippedCount,
		TaskStatus:    item.DispatchStatus,
		Attempts:      item.Attempts,
		MaxAttempts:   item.MaxAttempts,
		StartedAt:     item.StartedAt,
		FinishedAt:    item.FinishedAt,
		LastSafeError: item.LastSafeError,
		RequestID:     item.RequestID,
		CreatedAt:     item.CreatedAt,
		UpdatedAt:     item.UpdatedAt,
	}, nil
}

// ImportResult holds the result of an import operation.
type ImportResult struct {
	ImportID uint `json:"importId"`
	Imported int  `json:"imported"`
	Reused   bool `json:"reused"`
}

// ResourceImportStatusView is the API-safe import status view.
type ResourceImportStatusView struct {
	ImportID      uint       `json:"importId"`
	Status        string     `json:"status"`
	Imported      int        `json:"imported"`
	Accepted      int        `json:"accepted"`
	Skipped       int        `json:"skipped"`
	TaskStatus    string     `json:"taskStatus"`
	Attempts      int        `json:"attempts"`
	MaxAttempts   int        `json:"maxAttempts"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
	LastSafeError string     `json:"lastSafeError,omitempty"`
	RequestID     string     `json:"requestId"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

type importFailure struct {
	Line        int
	Email       string
	Category    string
	SafeMessage string
	Err         error
}

func (uc *ImportUseCase) duplicateInFile(lines []domain.MicrosoftImportLine) (importFailure, bool) {
	seen := make(map[string]domain.MicrosoftImportLine, len(lines))
	for _, line := range lines {
		key := microsoftEmailKey(line.Email)
		if first, ok := seen[key]; ok {
			return importFailure{
				Line:        line.LineNumber,
				Email:       line.Email,
				Category:    "duplicate_email",
				SafeMessage: fmt.Sprintf("Duplicate email address in import file; first occurrence is line %d.", first.LineNumber),
				Err:         domain.ErrDuplicateEmail,
			}, true
		}
		seen[key] = line
	}
	return importFailure{}, false
}

func (uc *ImportUseCase) skipDuplicateLines(lines []domain.MicrosoftImportLine) ([]domain.MicrosoftImportLine, []importFailure) {
	seen := make(map[string]domain.MicrosoftImportLine, len(lines))
	result := make([]domain.MicrosoftImportLine, 0, len(lines))
	var failures []importFailure
	for _, line := range lines {
		key := microsoftEmailKey(line.Email)
		if first, ok := seen[key]; ok {
			failures = append(failures, importFailure{
				Line:        line.LineNumber,
				Email:       line.Email,
				Category:    "duplicate_email",
				SafeMessage: fmt.Sprintf("Duplicate email address in import file; first occurrence is line %d.", first.LineNumber),
				Err:         domain.ErrDuplicateEmail,
			})
			continue
		}
		seen[key] = line
		result = append(result, line)
	}
	return result, failures
}

func importFailuresFromLineErrors(lineErrors []domain.ImportLineError) []importFailure {
	failures := make([]importFailure, 0, len(lineErrors))
	for _, item := range lineErrors {
		failures = append(failures, importFailure{
			Line:        item.Line,
			Email:       item.Email,
			Category:    item.Category,
			SafeMessage: item.SafeMessage,
			Err:         domain.ErrInvalidImportFormat,
		})
	}
	return failures
}

func microsoftEmailKey(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func importFailureFromError(err error) importFailure {
	if lineErr, ok := err.(*domain.ImportLineError); ok {
		return importFailure{
			Line:        lineErr.Line,
			Email:       lineErr.Email,
			Category:    lineErr.Category,
			SafeMessage: lineErr.SafeMessage,
			Err:         domain.ErrInvalidImportFormat,
		}
	}
	return importFailure{
		Line:        0,
		Category:    "invalid_format",
		SafeMessage: "Invalid import format.",
		Err:         domain.ErrInvalidImportFormat,
	}
}

func (uc *ImportUseCase) failImport(ctx context.Context, importRecordID uint, claimToken string, ownerUserID uint, now time.Time, importID string, failure importFailure) error {
	if failure.Err == nil {
		failure.Err = domain.ErrInvalidImportFormat
	}
	detail := importFailuresDetail([]importFailure{failure})
	failureObjectKey := importObjectKey("failures", ownerUserID, now, importID, ".csv")
	storedFailure, err := uc.files.SavePrivate(ctx, governancedomain.PrivateFile{
		ObjectKey:    failureObjectKey,
		FileName:     "microsoft-import-failures.csv",
		ContentType:  "text/csv; charset=utf-8",
		ContentBytes: []byte(detail),
	})
	if err != nil {
		return domain.ErrFileStorageUnavailable
	}
	if strings.TrimSpace(claimToken) != "" {
		dispatch, ok := uc.imports.(AdminResourceImportDispatchRepository)
		if !ok {
			return domain.ErrResourceDependency
		}
		if err := dispatch.MarkAdminImportFailed(ctx, importRecordID, claimToken, storedFailure.ObjectKey, failure.SafeMessage); err != nil {
			return err
		}
		return nil
	}
	if err := uc.imports.MarkFailed(ctx, importRecordID, storedFailure.ObjectKey, failure.SafeMessage); err != nil {
		return err
	}
	return nil
}

func (uc *ImportUseCase) saveImportFailures(ctx context.Context, ownerUserID uint, now time.Time, importID string, failures []importFailure) (string, string, error) {
	if len(failures) == 0 {
		return "", "", nil
	}
	failureObjectKey := importObjectKey("failures", ownerUserID, now, importID, ".csv")
	storedFailure, err := uc.files.SavePrivate(ctx, governancedomain.PrivateFile{
		ObjectKey:    failureObjectKey,
		FileName:     "microsoft-import-failures.csv",
		ContentType:  "text/csv; charset=utf-8",
		ContentBytes: []byte(importFailuresDetail(failures)),
	})
	if err != nil {
		return "", "", domain.ErrFileStorageUnavailable
	}
	return storedFailure.ObjectKey, skippedImportSummary(len(failures)), nil
}

// MarkImportFailed marks a processing import as failed with a safe system error.
func (uc *ImportUseCase) MarkImportFailed(ctx context.Context, importRecordID uint, safeError string) error {
	return uc.imports.MarkFailed(ctx, importRecordID, "", safeError)
}

// MarkImportPending returns an infrastructure-failed generation to the
// pending dispatcher without consuming a business import attempt.
func (uc *ImportUseCase) MarkImportPending(ctx context.Context, importRecordID uint, generation uint64, safeError string) error {
	dispatch, ok := uc.imports.(AdminResourceImportDispatchRepository)
	if !ok {
		return domain.ErrResourceDependency
	}
	return dispatch.MarkAdminImportPending(ctx, importRecordID, generation, safeError)
}

func importFailuresDetail(failures []importFailure) string {
	var b strings.Builder
	b.WriteString("line,email,category,message\n")
	for _, failure := range failures {
		fmt.Fprintf(&b, "%d,%s,%s,%s\n",
			failure.Line,
			csvSafe(failure.Email),
			csvSafe(failure.Category),
			csvSafe(failure.SafeMessage),
		)
	}
	return b.String()
}

func skippedImportSummary(count int) string {
	if count == 1 {
		return "Skipped 1 import entry."
	}
	return fmt.Sprintf("Skipped %d import entries.", count)
}

func importObjectKey(kind string, ownerUserID uint, now time.Time, importID string, suffix string) string {
	return fmt.Sprintf("imports/microsoft/%s/%04d/%02d/%02d/%d/%s%s",
		kind,
		now.Year(),
		now.Month(),
		now.Day(),
		ownerUserID,
		safeObjectSegment(importID),
		suffix,
	)
}

func adminImportFingerprint(ownerUserID uint, longLived bool, strategy domain.ImportErrorStrategy, content []byte) string {
	contentSum := sha256.Sum256(content)
	payload := fmt.Sprintf("%d\x00%t\x00%s\x00%s", ownerUserID, longLived, strategy, hex.EncodeToString(contentSum[:]))
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func cleanImportFileName(fileName string) string {
	base := path.Base(strings.TrimSpace(fileName))
	if base == "." || base == "/" || base == "" {
		return "microsoft-import.txt"
	}
	return base
}

func safeObjectSegment(value string) string {
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return platform.NewUUIDV7String()
	}
	return b.String()
}

func csvSafe(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, `"`, `""`)
	return `"` + value + `"`
}

// ResourceUseCase handles resource queries.
type ResourceUseCase struct {
	resources EmailResourceRepository
}

// NewResourceUseCase creates a new ResourceUseCase.
func NewResourceUseCase(resources EmailResourceRepository) *ResourceUseCase {
	return &ResourceUseCase{resources: resources}
}

// ResourceItem is the API-safe view of a resource.
type ResourceItem struct {
	ID              uint                `json:"id"`
	Type            domain.ResourceType `json:"type"`
	OwnerID         uint                `json:"ownerId"`
	Status          string              `json:"status"`
	ForSale         *bool               `json:"forSale,omitempty"`
	LongLived       *bool               `json:"longLived,omitempty"`
	GraphAvailable  *bool               `json:"graphAvailable,omitempty"`
	LastSafeError   string              `json:"lastSafeError,omitempty"`
	Email           string              `json:"email,omitempty"`
	Domain          string              `json:"domain,omitempty"`
	DomainTLD       string              `json:"domainTld,omitempty"`
	MailServerID    uint                `json:"mailServerId,omitempty"`
	Purpose         string              `json:"purpose,omitempty"`
	MailboxCount    int                 `json:"mailboxCount,omitempty"`
	LastAllocatedAt *time.Time          `json:"lastAllocatedAt,omitempty"`
	CreatedAt       time.Time           `json:"createdAt"`
	UpdatedAt       time.Time           `json:"updatedAt"`
}

// MicrosoftResourceDetail is the API-safe view of a Microsoft resource (no credentials).
type MicrosoftResourceDetail struct {
	ID              uint       `json:"id"`
	EmailAddress    string     `json:"emailAddress"`
	ForSale         bool       `json:"forSale"`
	LongLived       bool       `json:"longLived"`
	GraphAvailable  bool       `json:"graphAvailable"`
	Status          string     `json:"status"`
	QualityScore    int        `json:"qualityScore"`
	LastSafeError   string     `json:"lastSafeError"`
	LastAllocatedAt *time.Time `json:"lastAllocatedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
}

// DomainResourceDetail is the API-safe view of a domain resource.
type DomainResourceDetail struct {
	ID              uint       `json:"id"`
	Domain          string     `json:"domain"`
	MailServerID    uint       `json:"mailServerId"`
	Purpose         string     `json:"purpose"`
	Status          string     `json:"status"`
	LastSafeError   string     `json:"lastSafeError"`
	LastAllocatedAt *time.Time `json:"lastAllocatedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
}

// ResourceListResult holds paginated resource results.
type ResourceListResult struct {
	Items       []ResourceItem      `json:"items"`
	Total       int64               `json:"total"`
	Offset      int                 `json:"offset"`
	Limit       int                 `json:"limit"`
	NextAfterID *uint               `json:"nextAfterId,omitempty"`
	Facets      *ResourceListFacets `json:"facets,omitempty"`
}

type ResourceFacetCounts struct {
	All        int64
	Normal     int64
	Pending    int64
	Validating int64
	Abnormal   int64
	Disabled   int64
}

type ResourceBooleanFacets struct {
	All int64
	Yes int64
	No  int64
}

type ResourceKeyFacet struct {
	Key   string
	Count int64
}

type ResourceListFacets struct {
	Status         ResourceFacetCounts
	Private        ResourceBooleanFacets
	LongLived      ResourceBooleanFacets
	GraphAvailable ResourceBooleanFacets
	Suffixes       []ResourceKeyFacet
	TLDs           []ResourceKeyFacet
}

// ResourceBatchPublishResult holds the result of a batch publish command.
type ResourceBatchPublishResult struct {
	Requested            int    `json:"requested"`
	Published            int    `json:"published"`
	PublishedResourceIDs []uint `json:"publishedResourceIds,omitempty"`
}

// ResourceBatchDeleteResult holds the result of a batch delete command.
type ResourceBatchDeleteResult struct {
	Requested          int    `json:"requested"`
	Deleted            int    `json:"deleted"`
	DeletedResourceIDs []uint `json:"deletedResourceIds,omitempty"`
}

// ResourceBulkSelectionMode identifies how a bulk command selects resources.
type ResourceBulkSelectionMode string

const (
	ResourceBulkSelectionIDs    ResourceBulkSelectionMode = "ids"
	ResourceBulkSelectionFilter ResourceBulkSelectionMode = "filter"
)

// ResourceBulkSelection describes the resource set for a bulk command.
type ResourceBulkSelection struct {
	Mode        ResourceBulkSelectionMode `json:"mode"`
	ResourceIDs []uint                    `json:"resourceIds,omitempty"`
	Filter      ResourceBulkFilter        `json:"filter,omitempty"`
	// AllowBinding defaults to false so ordinary self-service batches cannot
	// select auxiliary-mailbox domains.
	AllowBinding bool `json:"allowBinding,omitempty"`
	// AdminScope is set only by administrator application services. It lets the
	// Redis batch worker select resources across owners without exposing that
	// authority through the public request DTO.
	AdminScope bool `json:"adminScope,omitempty"`
	// BatchKey optionally makes the live Redis task idempotent while it exists.
	// It is intentionally excluded from the nested selection payload.
	BatchKey string `json:"-"`
}

// ResourceListFilter is the server-side filter shared by resource lists and
// "all matching" bulk commands.
type ResourceListFilter struct {
	ResourceType domain.ResourceType `json:"resourceType"`
	Search       string              `json:"search,omitempty"`
	Suffix       string              `json:"suffix,omitempty"`
	TLD          string              `json:"tld,omitempty"`
	Status       string              `json:"status,omitempty"`
	Purpose      string              `json:"purpose,omitempty"`
	MailServerID uint                `json:"mailServerId,omitempty"`
	OwnerID      uint                `json:"ownerId,omitempty"`
	OwnerIDs     []uint              `json:"ownerIds,omitempty"`
	TokenHealth  string              `json:"tokenHealth,omitempty"`
	AdminSearch  bool                `json:"adminSearch,omitempty"`
	// ExcludeBinding is an internal visibility guard for supplier/self-service
	// resource queries. Auxiliary-mailbox domains are operational platform
	// resources and must not contribute list items, totals, or facets there.
	ExcludeBinding bool       `json:"excludeBinding,omitempty"`
	ForSale        *bool      `json:"forSale,omitempty"`
	LongLived      *bool      `json:"longLived,omitempty"`
	GraphAvailable *bool      `json:"graphAvailable,omitempty"`
	CreatedFrom    *time.Time `json:"createdFrom,omitempty"`
	CreatedTo      *time.Time `json:"createdTo,omitempty"`
}

// ResourceBulkFilter keeps bulk command APIs on the same filter semantics as
// the resource list endpoint.
type ResourceBulkFilter = ResourceListFilter

// MicrosoftStatusResult holds minimal API-safe status for a Microsoft resource.
type MicrosoftStatusResult struct {
	ID             uint
	EmailAddress   string
	ForSale        bool
	LongLived      bool
	GraphAvailable bool
	Status         string
	LastSafeError  string
}

// DomainStatusResult holds minimal API-safe status for a domain resource.
type DomainStatusResult struct {
	ID              uint
	Domain          string
	DomainTLD       string
	MailServerID    uint
	Purpose         string
	Status          string
	LastSafeError   string
	MailboxCount    int
	LastAllocatedAt *time.Time
	UpdatedAt       time.Time
}

const (
	defaultResourceListLimit = 20
	maxResourceListLimit     = 10000
	defaultInboundServerName = "Remail Inbound"
	defaultInboundMXRecord   = "mx.aishop6.com"
)

// List returns the user's resources.
func (uc *ResourceUseCase) List(ctx context.Context, ownerUserID uint, scope string, filter ResourceListFilter, offset, limit int, afterID uint) (*ResourceListResult, error) {
	if limit <= 0 {
		limit = defaultResourceListLimit
	}
	if limit > maxResourceListLimit {
		limit = maxResourceListLimit
	}
	if offset < 0 {
		offset = 0
	}
	filter, err := normalizeResourceListFilter(filter)
	if err != nil {
		return nil, err
	}
	// Auxiliary-mailbox domains are only visible in an administrative all-scope
	// query. In particular, do not rely on the web client to hide them after
	// receiving the response: doing that leaks their counts and TLD facets.
	if scope != "all" {
		filter.ExcludeBinding = true
	}

	var resources []domain.EmailResource
	var total int64
	var facets *ResourceListFacets

	if scope == "all" {
		resources, err = uc.resources.ListAll(ctx, filter, offset, limit, afterID)
		if err != nil {
			return nil, err
		}
		total, err = uc.resources.CountAll(ctx, filter)
		if err != nil {
			return nil, err
		}
		facets, err = uc.resources.Facets(ctx, 0, filter)
		if err != nil {
			return nil, err
		}
	} else {
		resources, err = uc.resources.List(ctx, ownerUserID, filter, offset, limit, afterID)
		if err != nil {
			return nil, err
		}
		total, err = uc.resources.Count(ctx, ownerUserID, filter)
		if err != nil {
			return nil, err
		}
		facets, err = uc.resources.Facets(ctx, ownerUserID, filter)
		if err != nil {
			return nil, err
		}
	}

	// Batch-fetch sub-table status info to avoid N+1
	var msIDs, domainIDs []uint
	for _, r := range resources {
		switch r.Type {
		case domain.ResourceTypeMicrosoft:
			msIDs = append(msIDs, r.ID)
		case domain.ResourceTypeDomain:
			domainIDs = append(domainIDs, r.ID)
		}
	}

	msStatusMap := make(map[uint]*MicrosoftStatusResult)
	if len(msIDs) > 0 {
		msStatuses, err := uc.resources.ListMicrosoftStatus(ctx, msIDs)
		if err != nil {
			return nil, err
		}
		for i := range msStatuses {
			msStatusMap[msStatuses[i].ID] = &msStatuses[i]
		}
	}

	domainStatusMap := make(map[uint]*DomainStatusResult)
	if len(domainIDs) > 0 {
		domainStatuses, err := uc.resources.ListDomainStatus(ctx, domainIDs)
		if err != nil {
			return nil, err
		}
		for i := range domainStatuses {
			domainStatusMap[domainStatuses[i].ID] = &domainStatuses[i]
		}
	}

	items := make([]ResourceItem, len(resources))
	for i, r := range resources {
		item := ResourceItem{
			ID:        r.ID,
			Type:      r.Type,
			OwnerID:   r.OwnerUserID,
			CreatedAt: r.CreatedAt,
			UpdatedAt: r.UpdatedAt,
		}
		switch r.Type {
		case domain.ResourceTypeMicrosoft:
			if s, ok := msStatusMap[r.ID]; ok {
				item.Status = s.Status
				item.Email = s.EmailAddress
				item.LastSafeError = s.LastSafeError
				forSale := s.ForSale
				item.ForSale = &forSale
				longLived := s.LongLived
				item.LongLived = &longLived
				graphAvailable := s.GraphAvailable
				item.GraphAvailable = &graphAvailable
			} else {
				return nil, fmt.Errorf("resource invariant violation: microsoft resource %d has no subtable status", r.ID)
			}
		case domain.ResourceTypeDomain:
			if s, ok := domainStatusMap[r.ID]; ok {
				item.Status = s.Status
				item.Domain = s.Domain
				item.DomainTLD = s.DomainTLD
				item.MailServerID = s.MailServerID
				item.Purpose = s.Purpose
				item.LastSafeError = s.LastSafeError
				item.MailboxCount = s.MailboxCount
				item.LastAllocatedAt = s.LastAllocatedAt
				item.UpdatedAt = s.UpdatedAt
			} else {
				return nil, fmt.Errorf("resource invariant violation: domain resource %d has no subtable status", r.ID)
			}
		default:
			return nil, domain.ErrInvalidResourceType
		}
		items[i] = item
	}

	return &ResourceListResult{Items: items, Total: total, Offset: offset, Limit: limit, NextAfterID: resourceListNextAfterID(resources, limit), Facets: facets}, nil
}

func resourceListNextAfterID(resources []domain.EmailResource, limit int) *uint {
	if len(resources) < limit || len(resources) == 0 {
		return nil
	}
	next := resources[len(resources)-1].ID
	return &next
}

// GetDetail returns the detailed view of a single resource.
func (uc *ResourceUseCase) GetDetail(ctx context.Context, resourceID, userID uint) (interface{}, error) {
	resource, err := uc.resources.FindByID(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	if resource == nil {
		return nil, domain.ErrResourceNotFound
	}

	// Supplier detail is owner-only; admin resource management uses separate admin routes.
	if resource.OwnerUserID != userID {
		return nil, domain.ErrForbiddenResource
	}

	switch resource.Type {
	case domain.ResourceTypeMicrosoft:
		ms, err := uc.resources.FindMicrosoftByID(ctx, resourceID)
		if err != nil {
			return nil, err
		}
		if ms == nil {
			return nil, domain.ErrResourceNotFound
		}
		if ms.Status == domain.MicrosoftStatusDeleted {
			return nil, domain.ErrResourceNotFound
		}
		return &MicrosoftResourceDetail{
			ID:              ms.ID,
			EmailAddress:    ms.EmailAddress,
			ForSale:         ms.ForSale,
			LongLived:       ms.LongLived,
			GraphAvailable:  ms.GraphAvailable,
			Status:          string(ms.Status),
			QualityScore:    ms.QualityScore,
			LastSafeError:   ms.LastSafeError,
			LastAllocatedAt: ms.LastAllocatedAt,
			CreatedAt:       ms.CreatedAt,
		}, nil

	case domain.ResourceTypeDomain:
		dr, err := uc.resources.FindDomainByID(ctx, resourceID)
		if err != nil {
			return nil, err
		}
		if dr == nil {
			return nil, domain.ErrResourceNotFound
		}
		if dr.Status == domain.DomainStatusDeleted {
			return nil, domain.ErrResourceNotFound
		}
		// This is the supplier/self-service detail API. Binding domains are
		// auxiliary-mailbox infrastructure and are deliberately not exposed here.
		if dr.Purpose == domain.PurposeBinding {
			return nil, domain.ErrForbiddenResource
		}
		return &DomainResourceDetail{
			ID:              dr.ID,
			Domain:          dr.Domain,
			MailServerID:    dr.MailServerID,
			Purpose:         string(dr.Purpose),
			Status:          string(dr.Status),
			LastSafeError:   dr.LastSafeError,
			LastAllocatedAt: dr.LastAllocatedAt,
			CreatedAt:       dr.CreatedAt,
		}, nil
	}

	return nil, domain.ErrInvalidResourceType
}

// PublishMicrosoftForSale publishes an owned Microsoft resource into the public supply pool.
// The API layer enforces supplier/admin/super_admin role. This use case preserves
// owner-only access and keeps the command one-way: private -> public supply.
func (uc *ResourceUseCase) PublishMicrosoftForSale(ctx context.Context, resourceID, userID uint, requestID, path string) (*MicrosoftResourceDetail, error) {
	resource, err := uc.resources.FindByID(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	if resource == nil {
		return nil, domain.ErrResourceNotFound
	}
	if resource.OwnerUserID != userID {
		return nil, domain.ErrForbiddenResource
	}
	if resource.Type != domain.ResourceTypeMicrosoft {
		return nil, domain.ErrInvalidResourceType
	}

	ms, err := uc.resources.FindMicrosoftByID(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	if ms == nil {
		return nil, domain.ErrResourceNotFound
	}
	if ms.Status == domain.MicrosoftStatusDeleted {
		return nil, domain.ErrResourceNotFound
	}

	if _, err := uc.resources.PublishMicrosoftWithLog(ctx, userID, resourceID, governancedomain.OperationLog{
		OperatorUserID: userID,
		OperationType:  "core.microsoft_resource.publish",
		ResourceType:   "microsoft_resource",
		ResourceID:     fmt.Sprintf("%d", ms.ID),
		Path:           path,
		Result:         "success",
		SafeSummary:    "Microsoft resource published for sale.",
		RequestID:      requestID,
	}); err != nil {
		return nil, err
	}
	ms.ForSale = true

	return &MicrosoftResourceDetail{
		ID:              ms.ID,
		EmailAddress:    ms.EmailAddress,
		ForSale:         ms.ForSale,
		LongLived:       ms.LongLived,
		GraphAvailable:  ms.GraphAvailable,
		Status:          string(ms.Status),
		QualityScore:    ms.QualityScore,
		LastSafeError:   ms.LastSafeError,
		LastAllocatedAt: ms.LastAllocatedAt,
		CreatedAt:       ms.CreatedAt,
	}, nil
}

// PublishResourceForSale publishes an owned resource into the public supply pool.
// The API layer enforces supplier/admin/super_admin role. This command is one-way:
// private -> public supply.
func (uc *ResourceUseCase) PublishResourceForSale(ctx context.Context, resourceID, userID uint, requestID, path string) (interface{}, error) {
	resource, err := uc.resources.FindByID(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	if resource == nil {
		return nil, domain.ErrResourceNotFound
	}
	if resource.OwnerUserID != userID {
		return nil, domain.ErrForbiddenResource
	}

	switch resource.Type {
	case domain.ResourceTypeMicrosoft:
		return uc.PublishMicrosoftForSale(ctx, resourceID, userID, requestID, path)
	case domain.ResourceTypeDomain:
		return uc.PublishDomainForSale(ctx, resourceID, userID, requestID, path)
	default:
		return nil, domain.ErrInvalidResourceType
	}
}

// PublishDomainForSale publishes an owned private domain resource into the public supply pool.
func (uc *ResourceUseCase) PublishDomainForSale(ctx context.Context, resourceID, userID uint, requestID, path string) (*DomainResourceDetail, error) {
	resource, err := uc.resources.FindByID(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	if resource == nil {
		return nil, domain.ErrResourceNotFound
	}
	if resource.OwnerUserID != userID {
		return nil, domain.ErrForbiddenResource
	}
	if resource.Type != domain.ResourceTypeDomain {
		return nil, domain.ErrInvalidResourceType
	}

	dr, err := uc.resources.FindDomainByID(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	if dr == nil {
		return nil, domain.ErrResourceNotFound
	}
	if dr.Status == domain.DomainStatusDeleted {
		return nil, domain.ErrResourceNotFound
	}
	if dr.Purpose == domain.PurposeBinding {
		return nil, domain.ErrResourceNotPrivate
	}
	if dr.Purpose != domain.PurposeNotSale && dr.Purpose != domain.PurposeSale {
		return nil, domain.ErrResourceNotPrivate
	}

	if _, err := uc.resources.PublishDomainWithLog(ctx, userID, resourceID, governancedomain.OperationLog{
		OperatorUserID: userID,
		OperationType:  "core.domain_resource.publish",
		ResourceType:   "domain_resource",
		ResourceID:     fmt.Sprintf("%d", dr.ID),
		Path:           path,
		Result:         "success",
		SafeSummary:    "Domain resource published for sale.",
		RequestID:      requestID,
	}); err != nil {
		return nil, err
	}
	dr.Purpose = domain.PurposeSale

	return &DomainResourceDetail{
		ID:              dr.ID,
		Domain:          dr.Domain,
		MailServerID:    dr.MailServerID,
		Purpose:         string(dr.Purpose),
		Status:          string(dr.Status),
		LastSafeError:   dr.LastSafeError,
		LastAllocatedAt: dr.LastAllocatedAt,
		CreatedAt:       dr.CreatedAt,
	}, nil
}

// PublishResourcesForSaleBatch publishes owned resources into the public supply pool.
func (uc *ResourceUseCase) PublishResourcesForSaleBatch(ctx context.Context, selection ResourceBulkSelection, userID uint, requestID, path string) (*ResourceBatchPublishResult, error) {
	microsoftLog, domainLog := publishBatchLogs(userID, requestID, path)

	switch selection.Mode {
	case ResourceBulkSelectionIDs:
		ids := uniqueResourceIDs(selection.ResourceIDs)
		if len(ids) == 0 {
			return nil, domain.ErrResourceNotFound
		}

		publishedIDs, err := uc.resources.PublishResourcesBatchWithLog(ctx, userID, ids, microsoftLog, domainLog)
		if err != nil {
			return nil, err
		}

		return &ResourceBatchPublishResult{
			Requested:            len(ids),
			Published:            len(publishedIDs),
			PublishedResourceIDs: publishedIDs,
		}, nil
	case ResourceBulkSelectionFilter:
		filter, err := normalizeResourceBulkFilter(selection.Filter)
		if err != nil {
			return nil, err
		}

		published, err := uc.resources.PublishResourcesByFilterWithLog(ctx, userID, filter, microsoftLog, domainLog)
		if err != nil {
			return nil, err
		}
		return &ResourceBatchPublishResult{Requested: published, Published: published}, nil
	default:
		return nil, domain.ErrInvalidResourceType
	}
}

// DeletePrivateMicrosoft removes one owner-owned Microsoft resource while it is still private.
func (uc *ResourceUseCase) DeletePrivateMicrosoft(ctx context.Context, resourceID, userID uint, requestID, path string) error {
	resource, err := uc.resources.FindByID(ctx, resourceID)
	if err != nil {
		return err
	}
	if resource == nil {
		return domain.ErrResourceNotFound
	}
	if resource.OwnerUserID != userID {
		return domain.ErrForbiddenResource
	}
	if resource.Type != domain.ResourceTypeMicrosoft {
		return domain.ErrInvalidResourceType
	}

	ms, err := uc.resources.FindMicrosoftByID(ctx, resourceID)
	if err != nil {
		return err
	}
	if ms == nil {
		return domain.ErrResourceNotFound
	}
	if ms.Status == domain.MicrosoftStatusDeleted {
		return domain.ErrResourceNotFound
	}
	if ms.ForSale {
		return domain.ErrResourceNotPrivate
	}

	return uc.resources.DeletePrivateMicrosoftWithLog(ctx, userID, resourceID, governancedomain.OperationLog{
		OperatorUserID: userID,
		OperationType:  "core.microsoft_resource.delete_private",
		ResourceType:   "microsoft_resource",
		ResourceID:     fmt.Sprintf("%d", ms.ID),
		Path:           path,
		Result:         "success",
		SafeSummary:    "Private Microsoft resource deleted.",
		RequestID:      requestID,
	})
}

// DeletePrivateResource removes one owner-owned private resource.
func (uc *ResourceUseCase) DeletePrivateResource(ctx context.Context, resourceID, userID uint, requestID, path string) error {
	resource, err := uc.resources.FindByID(ctx, resourceID)
	if err != nil {
		return err
	}
	if resource == nil {
		return domain.ErrResourceNotFound
	}
	if resource.OwnerUserID != userID {
		return domain.ErrForbiddenResource
	}

	switch resource.Type {
	case domain.ResourceTypeMicrosoft:
		return uc.DeletePrivateMicrosoft(ctx, resourceID, userID, requestID, path)
	case domain.ResourceTypeDomain:
		return uc.DeletePrivateDomain(ctx, resourceID, userID, requestID, path)
	default:
		return domain.ErrInvalidResourceType
	}
}

// DeletePrivateResourcesBatch deletes owned private resources in one command.
func (uc *ResourceUseCase) DeletePrivateResourcesBatch(ctx context.Context, selection ResourceBulkSelection, userID uint, requestID, path string) (*ResourceBatchDeleteResult, error) {
	microsoftLog, domainLog := deleteBatchLogs(userID, requestID, path)

	switch selection.Mode {
	case ResourceBulkSelectionIDs:
		ids := uniqueResourceIDs(selection.ResourceIDs)
		if len(ids) == 0 {
			return nil, domain.ErrResourceNotFound
		}

		deletedIDs, err := uc.resources.DeleteResourcesBatchWithLog(ctx, userID, ids, microsoftLog, domainLog)
		if err != nil {
			return nil, err
		}
		return &ResourceBatchDeleteResult{
			Requested:          len(ids),
			Deleted:            len(deletedIDs),
			DeletedResourceIDs: deletedIDs,
		}, nil
	case ResourceBulkSelectionFilter:
		filter, err := normalizeResourceBulkFilter(selection.Filter)
		if err != nil {
			return nil, err
		}

		deleted, err := uc.resources.DeleteResourcesByFilterWithLog(ctx, userID, filter, microsoftLog, domainLog)
		if err != nil {
			return nil, err
		}
		return &ResourceBatchDeleteResult{Requested: deleted, Deleted: deleted}, nil
	default:
		return nil, domain.ErrInvalidResourceType
	}
}

// DeletePrivateDomain removes one owner-owned domain resource while it is still private.
func (uc *ResourceUseCase) DeletePrivateDomain(ctx context.Context, resourceID, userID uint, requestID, path string) error {
	resource, err := uc.resources.FindByID(ctx, resourceID)
	if err != nil {
		return err
	}
	if resource == nil {
		return domain.ErrResourceNotFound
	}
	if resource.OwnerUserID != userID {
		return domain.ErrForbiddenResource
	}
	if resource.Type != domain.ResourceTypeDomain {
		return domain.ErrInvalidResourceType
	}

	dr, err := uc.resources.FindDomainByID(ctx, resourceID)
	if err != nil {
		return err
	}
	if dr == nil {
		return domain.ErrResourceNotFound
	}
	if dr.Status == domain.DomainStatusDeleted {
		return domain.ErrResourceNotFound
	}
	if dr.Purpose != domain.PurposeNotSale {
		return domain.ErrResourceNotPrivate
	}

	return uc.resources.DeletePrivateDomainWithLog(ctx, userID, resourceID, governancedomain.OperationLog{
		OperatorUserID: userID,
		OperationType:  "core.domain_resource.delete_private",
		ResourceType:   "domain_resource",
		ResourceID:     fmt.Sprintf("%d", dr.ID),
		Path:           path,
		Result:         "success",
		SafeSummary:    "Private domain resource deleted.",
		RequestID:      requestID,
	})
}

func publishBatchLogs(userID uint, requestID, path string) (governancedomain.OperationLog, governancedomain.OperationLog) {
	return governancedomain.OperationLog{
			OperatorUserID: userID,
			OperationType:  "core.microsoft_resource.publish_batch",
			ResourceType:   "microsoft_resource",
			Path:           path,
			Result:         "success",
			SafeSummary:    "Microsoft resources published for sale.",
			RequestID:      requestID,
		}, governancedomain.OperationLog{
			OperatorUserID: userID,
			OperationType:  "core.domain_resource.publish_batch",
			ResourceType:   "domain_resource",
			Path:           path,
			Result:         "success",
			SafeSummary:    "Domain resources published for sale.",
			RequestID:      requestID,
		}
}

func deleteBatchLogs(userID uint, requestID, path string) (governancedomain.OperationLog, governancedomain.OperationLog) {
	return governancedomain.OperationLog{
			OperatorUserID: userID,
			OperationType:  "core.microsoft_resource.delete_batch",
			ResourceType:   "microsoft_resource",
			Path:           path,
			Result:         "success",
			SafeSummary:    "Private Microsoft resources deleted.",
			RequestID:      requestID,
		}, governancedomain.OperationLog{
			OperatorUserID: userID,
			OperationType:  "core.domain_resource.delete_batch",
			ResourceType:   "domain_resource",
			Path:           path,
			Result:         "success",
			SafeSummary:    "Private domain resources deleted.",
			RequestID:      requestID,
		}
}

func normalizeResourceListFilter(filter ResourceListFilter) (ResourceListFilter, error) {
	filter.Search = strings.ToLower(strings.TrimSpace(filter.Search))
	filter.Suffix = strings.ToLower(strings.TrimSpace(filter.Suffix))
	filter.TLD = strings.ToLower(strings.TrimSpace(filter.TLD))
	filter.Status = strings.TrimSpace(filter.Status)
	filter.Purpose = strings.TrimSpace(filter.Purpose)
	if filter.ResourceType == domain.ResourceType("all") {
		filter.ResourceType = ""
	}
	if filter.Status == "all" {
		filter.Status = ""
	}
	if filter.Purpose == "all" {
		filter.Purpose = ""
	}
	if filter.CreatedFrom != nil && filter.CreatedTo != nil && filter.CreatedFrom.After(*filter.CreatedTo) {
		return ResourceBulkFilter{}, domain.ErrInvalidResourceFilter
	}

	switch filter.ResourceType {
	case "":
		if filter.Suffix != "" || filter.TLD != "" || filter.Purpose != "" || filter.MailServerID != 0 || filter.ForSale != nil || filter.LongLived != nil || filter.GraphAvailable != nil {
			return ResourceListFilter{}, domain.ErrInvalidResourceFilter
		}
		if filter.Status != "" && !domain.IsValidMicrosoftStatus(filter.Status) && !domain.IsValidDomainStatus(filter.Status) {
			return ResourceListFilter{}, domain.ErrInvalidResourceStatus
		}
		if filter.Status == string(domain.MicrosoftStatusDeleted) || filter.Status == string(domain.DomainStatusDeleted) {
			return ResourceListFilter{}, domain.ErrInvalidResourceStatus
		}
	case domain.ResourceTypeMicrosoft:
		filter.Purpose = ""
		filter.TLD = ""
		filter.MailServerID = 0
		if filter.Suffix != "" {
			suffix := strings.TrimPrefix(filter.Suffix, "@")
			normalized, err := domain.NormalizeDomainSuffix(suffix)
			if err != nil {
				return ResourceBulkFilter{}, domain.ErrInvalidResourceFilter
			}
			filter.Suffix = strings.TrimPrefix(normalized, ".")
		}
		if filter.Status != "" && !domain.IsValidMicrosoftStatus(filter.Status) {
			return ResourceBulkFilter{}, domain.ErrInvalidResourceStatus
		}
		if filter.Status == string(domain.MicrosoftStatusDeleted) {
			return ResourceBulkFilter{}, domain.ErrInvalidResourceStatus
		}
	case domain.ResourceTypeDomain:
		filter.ForSale = nil
		filter.LongLived = nil
		filter.GraphAvailable = nil
		filter.Suffix = ""
		if filter.Purpose != "" && !domain.IsValidPurpose(domain.ResourcePurpose(filter.Purpose)) {
			return ResourceBulkFilter{}, domain.ErrInvalidPurpose
		}
		if filter.TLD != "" {
			normalized, err := domain.NormalizeDomainSuffix(filter.TLD)
			if err != nil {
				return ResourceBulkFilter{}, domain.ErrInvalidResourceFilter
			}
			filter.TLD = normalized
		}
		if filter.Status != "" && !domain.IsValidDomainStatus(filter.Status) {
			return ResourceBulkFilter{}, domain.ErrInvalidResourceStatus
		}
		if filter.Status == string(domain.DomainStatusDeleted) {
			return ResourceBulkFilter{}, domain.ErrInvalidResourceStatus
		}
	default:
		return ResourceBulkFilter{}, domain.ErrInvalidResourceType
	}

	return filter, nil
}

func normalizeResourceBulkFilter(filter ResourceBulkFilter) (ResourceBulkFilter, error) {
	return normalizeResourceListFilter(filter)
}

func uniqueResourceIDs(resourceIDs []uint) []uint {
	seen := make(map[uint]struct{}, len(resourceIDs))
	ids := make([]uint, 0, len(resourceIDs))
	for _, id := range resourceIDs {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// DomainUseCase handles domain resource management.
type DomainUseCase struct {
	resources EmailResourceRepository
	servers   MailServerRepository
	mailboxes GeneratedMailboxRepository
}

// NewDomainUseCase creates a new DomainUseCase.
func NewDomainUseCase(resources EmailResourceRepository, servers MailServerRepository, mailboxes GeneratedMailboxRepository) *DomainUseCase {
	return &DomainUseCase{resources: resources, servers: servers, mailboxes: mailboxes}
}

// CreateDomainRequest contains the fields for creating a domain resource.
type CreateDomainRequest struct {
	Domain       string
	MailServerID uint
	Purpose      string
	AllowBinding bool
}

// Create creates a self-hosted domain resource. P1 defaults to the local
// inbound server and keeps public sale as a separate publish command.
func (uc *DomainUseCase) Create(ctx context.Context, ownerUserID uint, req *CreateDomainRequest) (*domain.MailDomainResource, error) {
	domainName, err := domain.NormalizeDomainName(req.Domain)
	if err != nil {
		return nil, err
	}

	purpose := domain.ResourcePurpose(req.Purpose)
	if purpose == "" {
		purpose = domain.PurposeNotSale
	}
	if !domain.IsValidPurpose(purpose) {
		return nil, domain.ErrInvalidPurpose
	}
	if purpose == domain.PurposeSale {
		return nil, domain.ErrInvalidPurpose
	}
	if purpose == domain.PurposeBinding && !req.AllowBinding {
		return nil, domain.ErrForbiddenPurpose
	}

	server, err := uc.resolveMailServer(ctx, ownerUserID, req.MailServerID)
	if err != nil {
		return nil, err
	}
	if server == nil {
		return nil, domain.ErrMailServerNotFound
	}
	if server.OwnerUserID != ownerUserID {
		return nil, domain.ErrMailServerNotFound
	}

	resource := &domain.EmailResource{
		Type:        domain.ResourceTypeDomain,
		OwnerUserID: ownerUserID,
	}

	dr := &domain.MailDomainResource{
		Domain:       domainName,
		MailServerID: req.MailServerID,
		Purpose:      purpose,
		Status:       domain.DomainStatusAbnormal,
	}
	if server != nil {
		dr.MailServerID = server.ID
	}

	if err := uc.resources.CreateDomain(ctx, resource, dr); err != nil {
		return nil, err
	}

	return dr, nil
}

func (uc *DomainUseCase) resolveMailServer(ctx context.Context, ownerUserID uint, mailServerID uint) (*domain.MailServer, error) {
	if mailServerID != 0 {
		server, err := uc.servers.FindByID(ctx, mailServerID)
		if err != nil {
			return nil, err
		}
		return server, nil
	}

	return uc.servers.GetOrCreateDefaultInbound(ctx, ownerUserID, defaultInboundServerName, defaultInboundMXRecord, defaultInboundMXRecord)
}

// ServerUseCase handles mail server management.
type ServerUseCase struct {
	servers MailServerRepository
}

// NewServerUseCase creates a new ServerUseCase.
func NewServerUseCase(servers MailServerRepository) *ServerUseCase {
	return &ServerUseCase{servers: servers}
}

// CreateServerRequest contains the fields for creating a mail server.
type CreateServerRequest struct {
	Name          string
	ServerAddress string
	MXRecord      string
	SPFRecord     string
	DKIMRecord    string
	DMARCRecord   string
	PTRRecord     string
}

// Create creates a new mail server owned by the user.
func (uc *ServerUseCase) Create(ctx context.Context, ownerUserID uint, req *CreateServerRequest) (*domain.MailServer, error) {
	server := &domain.MailServer{
		OwnerUserID:   ownerUserID,
		Name:          req.Name,
		ServerAddress: req.ServerAddress,
		MXRecord:      req.MXRecord,
		SPFRecord:     req.SPFRecord,
		DKIMRecord:    req.DKIMRecord,
		DMARCRecord:   req.DMARCRecord,
		PTRRecord:     req.PTRRecord,
		Status:        domain.MailServerOnline,
	}

	if err := uc.servers.Create(ctx, server); err != nil {
		return nil, err
	}

	return server, nil
}

// ServerListResult holds paginated mail server results.
type ServerListResult struct {
	Items  []domain.MailServer `json:"items"`
	Total  int64               `json:"total"`
	Offset int                 `json:"offset"`
	Limit  int                 `json:"limit"`
}

// List returns mail servers accessible by the user.
func (uc *ServerUseCase) List(ctx context.Context, ownerUserID uint, scope string, offset, limit int) (*ServerListResult, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	var servers []domain.MailServer
	var total int64
	var err error

	if scope == "all" {
		servers, err = uc.servers.ListAll(ctx, offset, limit)
		if err != nil {
			return nil, err
		}
		total, err = uc.servers.CountAll(ctx)
		if err != nil {
			return nil, err
		}
	} else {
		servers, err = uc.servers.List(ctx, ownerUserID, offset, limit)
		if err != nil {
			return nil, err
		}
		total, err = uc.servers.Count(ctx, ownerUserID)
		if err != nil {
			return nil, err
		}
	}

	return &ServerListResult{Items: servers, Total: total, Offset: offset, Limit: limit}, nil
}

// MailboxListResult holds paginated mailbox results.
type MailboxListResult struct {
	Items  []domain.GeneratedMailbox `json:"items"`
	Total  int64                     `json:"total"`
	Offset int                       `json:"offset"`
	Limit  int                       `json:"limit"`
}

// DomainMailboxUseCase handles generated mailbox queries for domain resources.
type DomainMailboxUseCase struct {
	mailboxes GeneratedMailboxRepository
	resources EmailResourceRepository
}

// NewDomainMailboxUseCase creates a new DomainMailboxUseCase.
func NewDomainMailboxUseCase(mailboxes GeneratedMailboxRepository, resources EmailResourceRepository) *DomainMailboxUseCase {
	return &DomainMailboxUseCase{mailboxes: mailboxes, resources: resources}
}

// List returns paginated mailboxes for a domain resource that the user owns.
// Non-admin users can only see their own domain resource's mailboxes.
// Unauthorized access returns ErrForbiddenResource to prevent enumeration.
func (uc *DomainMailboxUseCase) List(ctx context.Context, domainResourceID, userID uint, isAdmin bool, offset, limit int) (*MailboxListResult, error) {
	if limit <= 0 || limit > 10000 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	// Verify the domain resource exists and the user has access
	resource, err := uc.resources.FindDomainByID(ctx, domainResourceID)
	if err != nil {
		return nil, err
	}
	if resource == nil {
		return nil, domain.ErrForbiddenResource
	}
	if resource.Status == domain.DomainStatusDeleted && !isAdmin {
		return nil, domain.ErrResourceNotFound
	}
	if !isAdmin && resource.Purpose == domain.PurposeBinding {
		return nil, domain.ErrForbiddenResource
	}

	root, err := uc.resources.FindByID(ctx, domainResourceID)
	if err != nil {
		return nil, err
	}
	if root == nil || root.Type != domain.ResourceTypeDomain {
		return nil, domain.ErrForbiddenResource
	}

	// Check ownership: only the owner or admin can view mailboxes
	if !isAdmin {
		if root.OwnerUserID != userID {
			return nil, domain.ErrForbiddenResource
		}
	}

	mailboxes, err := uc.mailboxes.List(ctx, domainResourceID, root.OwnerUserID, offset, limit)
	if err != nil {
		return nil, err
	}

	total, err := uc.mailboxes.Count(ctx, domainResourceID, root.OwnerUserID)
	if err != nil {
		return nil, err
	}

	return &MailboxListResult{Items: mailboxes, Total: total, Offset: offset, Limit: limit}, nil
}

// DisableAdmin prevents one generated mailbox from future allocation.
func (uc *DomainMailboxUseCase) DisableAdmin(ctx context.Context, mailboxID, operatorUserID uint, requestID, requestPath string) error {
	return uc.mailboxes.DisableWithLog(ctx, mailboxID, &governancedomain.OperationLog{
		OperatorUserID: operatorUserID,
		OperationType:  "core.generated_mailbox.disable",
		ResourceType:   "generated_mailbox",
		ResourceID:     fmt.Sprintf("%d", mailboxID),
		Path:           requestPath,
		Result:         "success",
		SafeSummary:    "Generated mailbox disabled by administrator.",
		RequestID:      requestID,
	})
}
