package app

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/core/domain"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/google/uuid"
)

// EmailResourceRepository defines the persistence contract for email resources.
type EmailResourceRepository interface {
	// CreateMicrosoft creates a new Microsoft resource within a transaction.
	CreateMicrosoft(ctx context.Context, resource *domain.EmailResource, ms *domain.MicrosoftResource) error

	// CreateDomain creates a new Domain resource within a transaction.
	CreateDomain(ctx context.Context, resource *domain.EmailResource, dr *domain.DomainResource) error

	// CreateMicrosoftBatch creates multiple Microsoft resources in a single transaction.
	CreateMicrosoftBatch(ctx context.Context, resources []domain.EmailResource, ms []domain.MicrosoftResource) error

	// FindByID looks up a resource by ID. Returns nil, nil if not found.
	FindByID(ctx context.Context, id uint) (*domain.EmailResource, error)

	// FindMicrosoftByID looks up a Microsoft resource by resource ID. Returns nil, nil if not found.
	FindMicrosoftByID(ctx context.Context, resourceID uint) (*domain.MicrosoftResource, error)

	// FindDomainByID looks up a domain resource by resource ID. Returns nil, nil if not found.
	FindDomainByID(ctx context.Context, resourceID uint) (*domain.DomainResource, error)

	// FindMicrosoftByEmail looks up a Microsoft resource by email address.
	FindMicrosoftByEmail(ctx context.Context, email string) (*domain.MicrosoftResource, error)

	// List returns paginated resources owned by a user.
	List(ctx context.Context, ownerUserID uint, resourceType string, offset, limit int) ([]domain.EmailResource, error)

	// ListAll returns paginated resources (admin).
	ListAll(ctx context.Context, resourceType string, offset, limit int) ([]domain.EmailResource, error)

	// Count returns the total count of resources for a user.
	Count(ctx context.Context, ownerUserID uint, resourceType string) (int64, error)

	// CountAll returns the total count of all resources.
	CountAll(ctx context.Context, resourceType string) (int64, error)

	// UpdateMicrosoft updates non-credential Microsoft resource fields and writes OperationLog.
	UpdateMicrosoftWithLog(ctx context.Context, resource *domain.MicrosoftResource, log *governancedomain.OperationLog) error

	// UpdateDomain updates a domain resource and writes OperationLog.
	UpdateDomainWithLog(ctx context.Context, resource *domain.DomainResource, log *governancedomain.OperationLog) error

	// ListMicrosoftStatus returns API-safe status for a batch of Microsoft resources.
	ListMicrosoftStatus(ctx context.Context, ids []uint) ([]MicrosoftStatusResult, error)

	// ListDomainStatus returns API-safe status for a batch of domain resources.
	ListDomainStatus(ctx context.Context, ids []uint) ([]DomainStatusResult, error)
}

// ResourceImportRepository persists safe import artifact metadata.
type ResourceImportRepository interface {
	Create(ctx context.Context, item *domain.ResourceImport) error
	MarkSucceeded(ctx context.Context, id uint, importedCount int) error
	MarkFailed(ctx context.Context, id uint, failureObjectKey string, safeError string) error
}

// MailServerRepository defines the persistence contract for mail servers.
type MailServerRepository interface {
	Create(ctx context.Context, server *domain.MailServer) error
	FindByID(ctx context.Context, id uint) (*domain.MailServer, error)
	List(ctx context.Context, ownerUserID uint, offset, limit int) ([]domain.MailServer, error)
	ListAll(ctx context.Context, offset, limit int) ([]domain.MailServer, error)
	Count(ctx context.Context, ownerUserID uint) (int64, error)
	CountAll(ctx context.Context) (int64, error)
}

// GeneratedMailboxRepository defines the persistence contract for generated mailboxes.
type GeneratedMailboxRepository interface {
	List(ctx context.Context, domainResourceID uint, offset, limit int) ([]domain.GeneratedMailbox, error)
	Count(ctx context.Context, domainResourceID uint) (int64, error)
}

// TXTParser parses resource import TXT files.
type TXTParser interface {
	ParseMicrosoftImport(content string) ([]domain.MicrosoftImportLine, error)
}

// ImportUseCase handles supplier resource import operations.
type ImportUseCase struct {
	resources EmailResourceRepository
	imports   ResourceImportRepository
	parser    TXTParser
	files     governanceapp.FilePort
}

// NewImportUseCase creates a new ImportUseCase.
func NewImportUseCase(resources EmailResourceRepository, imports ResourceImportRepository, parser TXTParser, files governanceapp.FilePort) *ImportUseCase {
	return &ImportUseCase{resources: resources, imports: imports, parser: parser, files: files}
}

// ImportMicrosoftTXTFile imports Microsoft resources from an uploaded TXT file.
// Each line uses the P1-I2 Microsoft TXT import format documented in docs/14.
func (uc *ImportUseCase) ImportMicrosoftTXTFile(ctx context.Context, ownerUserID uint, fileName string, content []byte, requestID string) (*ImportResult, error) {
	if len(content) == 0 {
		return nil, domain.ErrInvalidImportFormat
	}

	now := time.Now().UTC()
	importID := strings.TrimSpace(requestID)
	if importID == "" {
		importID = uuid.NewString()
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
		SourceObjectKey: storedSource.ObjectKey,
		Status:          domain.ResourceImportProcessing,
	}
	if err := uc.imports.Create(ctx, importRecord); err != nil {
		return nil, err
	}

	lines, err := uc.parser.ParseMicrosoftImport(string(content))
	if err != nil {
		return nil, uc.failImport(ctx, importRecord.ID, ownerUserID, now, importID, importFailureFromError(err))
	}
	if len(lines) == 0 {
		return nil, uc.failImport(ctx, importRecord.ID, ownerUserID, now, importID, importFailure{
			Line:        0,
			Category:    "invalid_format",
			SafeMessage: "Invalid import format.",
		})
	}

	if failure, ok := uc.duplicateInFile(lines); ok {
		return nil, uc.failImport(ctx, importRecord.ID, ownerUserID, now, importID, failure)
	}

	for _, line := range lines {
		existing, err := uc.resources.FindMicrosoftByEmail(ctx, line.Email)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return nil, uc.failImport(ctx, importRecord.ID, ownerUserID, now, importID, importFailure{
				Line:        line.LineNumber,
				Email:       line.Email,
				Category:    "duplicate_email",
				SafeMessage: "Email address already exists.",
				Err:         domain.ErrDuplicateEmail,
			})
		}
	}

	resources := make([]domain.EmailResource, 0, len(lines))
	msResources := make([]domain.MicrosoftResource, 0, len(lines))

	for _, line := range lines {
		resources = append(resources, domain.EmailResource{
			Type:        domain.ResourceTypeMicrosoft,
			OwnerUserID: ownerUserID,
			CreatedAt:   now,
			UpdatedAt:   now,
		})
		msResources = append(msResources, domain.MicrosoftResource{
			EmailAddress: line.Email,
			Password:     line.Password,
			ClientID:     line.ClientID,
			RefreshToken: line.RefreshToken,
			ForSale:      true,
			Status:       domain.MicrosoftStatusPending,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
	}

	if err := uc.resources.CreateMicrosoftBatch(ctx, resources, msResources); err != nil {
		if errors.Is(err, domain.ErrDuplicateEmail) {
			return nil, uc.failImport(ctx, importRecord.ID, ownerUserID, now, importID, importFailure{
				Line:        0,
				Category:    "duplicate_email",
				SafeMessage: "An email address in the import already exists.",
				Err:         domain.ErrDuplicateEmail,
			})
		}
		return nil, err
	}

	if err := uc.imports.MarkSucceeded(ctx, importRecord.ID, len(lines)); err != nil {
		return nil, err
	}

	return &ImportResult{ImportID: importRecord.ID, Imported: len(lines)}, nil
}

// ImportResult holds the result of an import operation.
type ImportResult struct {
	ImportID uint `json:"importId"`
	Imported int  `json:"imported"`
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
		key := strings.ToLower(line.Email)
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

func (uc *ImportUseCase) failImport(ctx context.Context, importRecordID uint, ownerUserID uint, now time.Time, importID string, failure importFailure) error {
	if failure.Err == nil {
		failure.Err = domain.ErrInvalidImportFormat
	}
	detail := importFailureDetail(failure)
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
	if err := uc.imports.MarkFailed(ctx, importRecordID, storedFailure.ObjectKey, failure.SafeMessage); err != nil {
		return err
	}
	return failure.Err
}

func importFailureDetail(failure importFailure) string {
	return "line,email,category,message\n" +
		fmt.Sprintf("%d,%s,%s,%s\n",
			failure.Line,
			csvSafe(failure.Email),
			csvSafe(failure.Category),
			csvSafe(failure.SafeMessage),
		)
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
		return uuid.NewString()
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
	ID        uint                `json:"id"`
	Type      domain.ResourceType `json:"type"`
	OwnerID   uint                `json:"ownerId"`
	Status    string              `json:"status"`
	ForSale   *bool               `json:"forSale,omitempty"`
	Email     string              `json:"email,omitempty"`
	Domain    string              `json:"domain,omitempty"`
	Purpose   string              `json:"purpose,omitempty"`
	CreatedAt time.Time           `json:"createdAt"`
}

// MicrosoftResourceDetail is the API-safe view of a Microsoft resource (no credentials).
type MicrosoftResourceDetail struct {
	ID              uint       `json:"id"`
	EmailAddress    string     `json:"emailAddress"`
	ForSale         bool       `json:"forSale"`
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
	LastAllocatedAt *time.Time `json:"lastAllocatedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
}

// ResourceListResult holds paginated resource results.
type ResourceListResult struct {
	Items  []ResourceItem `json:"items"`
	Total  int64          `json:"total"`
	Offset int            `json:"offset"`
	Limit  int            `json:"limit"`
}

// MicrosoftStatusResult holds minimal API-safe status for a Microsoft resource.
type MicrosoftStatusResult struct {
	ID           uint
	EmailAddress string
	ForSale      bool
	Status       string
}

// DomainStatusResult holds minimal API-safe status for a domain resource.
type DomainStatusResult struct {
	ID      uint
	Domain  string
	Purpose string
	Status  string
}

// List returns the user's resources.
func (uc *ResourceUseCase) List(ctx context.Context, ownerUserID uint, scope string, resourceType string, offset, limit int) (*ResourceListResult, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	var resources []domain.EmailResource
	var total int64
	var err error

	if scope == "all" {
		resources, err = uc.resources.ListAll(ctx, resourceType, offset, limit)
		if err != nil {
			return nil, err
		}
		total, err = uc.resources.CountAll(ctx, resourceType)
		if err != nil {
			return nil, err
		}
	} else {
		resources, err = uc.resources.List(ctx, ownerUserID, resourceType, offset, limit)
		if err != nil {
			return nil, err
		}
		total, err = uc.resources.Count(ctx, ownerUserID, resourceType)
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
		}
		switch r.Type {
		case domain.ResourceTypeMicrosoft:
			if s, ok := msStatusMap[r.ID]; ok {
				item.Status = s.Status
				item.Email = s.EmailAddress
				forSale := s.ForSale
				item.ForSale = &forSale
			} else {
				return nil, fmt.Errorf("resource invariant violation: microsoft resource %d has no subtable status", r.ID)
			}
		case domain.ResourceTypeDomain:
			if s, ok := domainStatusMap[r.ID]; ok {
				item.Status = s.Status
				item.Domain = s.Domain
				item.Purpose = s.Purpose
			} else {
				return nil, fmt.Errorf("resource invariant violation: domain resource %d has no subtable status", r.ID)
			}
		default:
			return nil, domain.ErrInvalidResourceType
		}
		items[i] = item
	}

	return &ResourceListResult{Items: items, Total: total, Offset: offset, Limit: limit}, nil
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
		return &MicrosoftResourceDetail{
			ID:              ms.ID,
			EmailAddress:    ms.EmailAddress,
			ForSale:         ms.ForSale,
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
		return &DomainResourceDetail{
			ID:              dr.ID,
			Domain:          dr.Domain,
			MailServerID:    dr.MailServerID,
			Purpose:         string(dr.Purpose),
			Status:          string(dr.Status),
			LastAllocatedAt: dr.LastAllocatedAt,
			CreatedAt:       dr.CreatedAt,
		}, nil
	}

	return nil, domain.ErrInvalidResourceType
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
}

// Create creates a new self-hosted domain resource (P1-I2 supplier self-service).
func (uc *DomainUseCase) Create(ctx context.Context, ownerUserID uint, req *CreateDomainRequest) (*domain.DomainResource, error) {
	if !domain.IsValidPurpose(domain.ResourcePurpose(req.Purpose)) {
		return nil, domain.ErrInvalidPurpose
	}

	server, err := uc.servers.FindByID(ctx, req.MailServerID)
	if err != nil {
		return nil, err
	}
	if server == nil {
		return nil, domain.ErrMailServerNotFound
	}
	if server.OwnerUserID != ownerUserID {
		return nil, domain.ErrMailServerOwnerMismatch
	}

	resource := &domain.EmailResource{
		Type:        domain.ResourceTypeDomain,
		OwnerUserID: ownerUserID,
	}

	dr := &domain.DomainResource{
		Domain:       req.Domain,
		MailServerID: req.MailServerID,
		Purpose:      domain.ResourcePurpose(req.Purpose),
		Status:       domain.DomainStatusDNSAbnormal,
	}

	if err := uc.resources.CreateDomain(ctx, resource, dr); err != nil {
		return nil, err
	}

	return dr, nil
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
	if limit <= 0 || limit > 100 {
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

	// Check ownership: only the owner or admin can view mailboxes
	if !isAdmin {
		root, err := uc.resources.FindByID(ctx, domainResourceID)
		if err != nil {
			return nil, err
		}
		if root == nil || root.OwnerUserID != userID {
			return nil, domain.ErrForbiddenResource
		}
	}

	mailboxes, err := uc.mailboxes.List(ctx, domainResourceID, offset, limit)
	if err != nil {
		return nil, err
	}

	total, err := uc.mailboxes.Count(ctx, domainResourceID)
	if err != nil {
		return nil, err
	}

	return &MailboxListResult{Items: mailboxes, Total: total, Offset: offset, Limit: limit}, nil
}
