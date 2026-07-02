package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	coreapp "github.com/donnel666/remail/internal/core/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// --- In-memory mock repositories for testing ---

type mockResourceRepo struct {
	resources map[uint]*coredomain.EmailResource
	microsoft map[uint]*coredomain.MicrosoftResource
	domains   map[uint]*coredomain.MailDomainResource
	seq       uint
}

func newMockResourceRepo() *mockResourceRepo {
	return &mockResourceRepo{
		resources: make(map[uint]*coredomain.EmailResource),
		microsoft: make(map[uint]*coredomain.MicrosoftResource),
		domains:   make(map[uint]*coredomain.MailDomainResource),
	}
}

func (r *mockResourceRepo) CreateMicrosoft(_ context.Context, resource *coredomain.EmailResource, ms *coredomain.MicrosoftResource) error {
	r.seq++
	resource.ID = r.seq
	resource.CreatedAt = time.Now()
	resource.UpdatedAt = time.Now()
	ms.ID = resource.ID
	ms.CreatedAt = resource.CreatedAt
	r.resources[resource.ID] = resource
	r.microsoft[resource.ID] = ms
	return nil
}

func (r *mockResourceRepo) CreateDomain(_ context.Context, resource *coredomain.EmailResource, dr *coredomain.MailDomainResource) error {
	for id, existing := range r.domains {
		if existing.Domain != dr.Domain {
			continue
		}
		root := r.resources[id]
		if existing.Status != coredomain.DomainStatusDeleted || root == nil {
			return coredomain.ErrDuplicateDomain
		}
		root.OwnerUserID = resource.OwnerUserID
		root.UpdatedAt = time.Now()
		resource.ID = id
		resource.CreatedAt = root.CreatedAt
		resource.UpdatedAt = root.UpdatedAt
		existing.MailServerID = dr.MailServerID
		existing.Purpose = dr.Purpose
		existing.Status = dr.Status
		existing.LastAllocatedAt = nil
		existing.UpdatedAt = resource.UpdatedAt
		*dr = *existing
		return nil
	}
	r.seq++
	resource.ID = r.seq
	resource.CreatedAt = time.Now()
	resource.UpdatedAt = time.Now()
	dr.ID = resource.ID
	dr.CreatedAt = resource.CreatedAt
	r.resources[resource.ID] = resource
	r.domains[resource.ID] = dr
	return nil
}

func (r *mockResourceRepo) createMicrosoftBatch(ctx context.Context, resources []coredomain.EmailResource, ms []coredomain.MicrosoftResource) error {
	for i := range resources {
		_ = r.CreateMicrosoft(ctx, &resources[i], &ms[i])
	}
	return nil
}

func (r *mockResourceRepo) FindByID(_ context.Context, id uint) (*coredomain.EmailResource, error) {
	if res, ok := r.resources[id]; ok {
		return res, nil
	}
	return nil, nil
}

func (r *mockResourceRepo) FindMicrosoftByID(_ context.Context, id uint) (*coredomain.MicrosoftResource, error) {
	if ms, ok := r.microsoft[id]; ok {
		return ms, nil
	}
	return nil, nil
}

func (r *mockResourceRepo) FindDomainByID(_ context.Context, id uint) (*coredomain.MailDomainResource, error) {
	if dr, ok := r.domains[id]; ok {
		return dr, nil
	}
	return nil, nil
}

func (r *mockResourceRepo) FindMicrosoftByEmail(_ context.Context, email string) (*coredomain.MicrosoftResource, error) {
	for _, ms := range r.microsoft {
		if ms.EmailAddress == email {
			return ms, nil
		}
	}
	return nil, nil
}

func (r *mockResourceRepo) FindExistingMicrosoftEmails(_ context.Context, emails []string) (map[string]struct{}, error) {
	wanted := make(map[string]struct{}, len(emails))
	for _, email := range emails {
		wanted[email] = struct{}{}
	}
	result := make(map[string]struct{})
	for _, ms := range r.microsoft {
		if ms.Status == coredomain.MicrosoftStatusDeleted {
			continue
		}
		if _, ok := wanted[ms.EmailAddress]; ok {
			result[ms.EmailAddress] = struct{}{}
		}
	}
	return result, nil
}

func (r *mockResourceRepo) List(_ context.Context, ownerUserID uint, resourceType string, _, _ int) ([]coredomain.EmailResource, error) {
	var result []coredomain.EmailResource
	for _, res := range r.resources {
		if res.OwnerUserID == ownerUserID && resourceMatchesType(res.Type, resourceType) {
			if r.isDeletedResource(res.ID) {
				continue
			}
			result = append(result, *res)
		}
	}
	return result, nil
}

func (r *mockResourceRepo) ListAll(_ context.Context, resourceType string, _, _ int) ([]coredomain.EmailResource, error) {
	var result []coredomain.EmailResource
	for _, res := range r.resources {
		if resourceMatchesType(res.Type, resourceType) {
			if r.isDeletedResource(res.ID) {
				continue
			}
			result = append(result, *res)
		}
	}
	return result, nil
}

func (r *mockResourceRepo) Count(_ context.Context, ownerUserID uint, resourceType string) (int64, error) {
	var count int64
	for _, res := range r.resources {
		if res.OwnerUserID == ownerUserID && resourceMatchesType(res.Type, resourceType) {
			if r.isDeletedResource(res.ID) {
				continue
			}
			count++
		}
	}
	return count, nil
}

func (r *mockResourceRepo) CountAll(_ context.Context, resourceType string) (int64, error) {
	var count int64
	for _, res := range r.resources {
		if resourceMatchesType(res.Type, resourceType) {
			if r.isDeletedResource(res.ID) {
				continue
			}
			count++
		}
	}
	return count, nil
}

func resourceMatchesType(actual coredomain.ResourceType, filter string) bool {
	return filter == "" || filter == "all" || string(actual) == filter
}

func (r *mockResourceRepo) isDeletedMicrosoft(id uint) bool {
	ms, ok := r.microsoft[id]
	return ok && ms.Status == coredomain.MicrosoftStatusDeleted
}

func (r *mockResourceRepo) isDeletedDomain(id uint) bool {
	dr, ok := r.domains[id]
	return ok && dr.Status == coredomain.DomainStatusDeleted
}

func (r *mockResourceRepo) isDeletedResource(id uint) bool {
	return r.isDeletedMicrosoft(id) || r.isDeletedDomain(id)
}

func (r *mockResourceRepo) UpdateMicrosoftWithLog(_ context.Context, resource *coredomain.MicrosoftResource, _ *governancedomain.OperationLog) error {
	stored, ok := r.microsoft[resource.ID]
	if !ok {
		return coredomain.ErrResourceNotFound
	}
	stored.ForSale = resource.ForSale
	stored.Status = resource.Status
	stored.QualityScore = resource.QualityScore
	stored.LastSafeError = resource.LastSafeError
	stored.LastAllocatedAt = resource.LastAllocatedAt
	return nil
}

func (r *mockResourceRepo) PublishMicrosoftWithLog(_ context.Context, ownerUserID uint, resourceID uint, _ governancedomain.OperationLog) (bool, error) {
	root, ok := r.resources[resourceID]
	if !ok || root.OwnerUserID != ownerUserID || root.Type != coredomain.ResourceTypeMicrosoft {
		return false, coredomain.ErrForbiddenResource
	}
	ms, ok := r.microsoft[resourceID]
	if !ok {
		return false, coredomain.ErrResourceNotFound
	}
	if ms.Status == coredomain.MicrosoftStatusDeleted {
		return false, coredomain.ErrResourceNotFound
	}
	if ms.ForSale {
		return false, nil
	}
	ms.ForSale = true
	return true, nil
}

func (r *mockResourceRepo) PublishDomainWithLog(_ context.Context, ownerUserID uint, resourceID uint, _ governancedomain.OperationLog) (bool, error) {
	root, ok := r.resources[resourceID]
	if !ok || root.OwnerUserID != ownerUserID || root.Type != coredomain.ResourceTypeDomain {
		return false, coredomain.ErrForbiddenResource
	}
	dr, ok := r.domains[resourceID]
	if !ok {
		return false, coredomain.ErrResourceNotFound
	}
	if dr.Status == coredomain.DomainStatusDeleted {
		return false, coredomain.ErrResourceNotFound
	}
	if dr.Purpose == coredomain.PurposeSale {
		return false, nil
	}
	if dr.Purpose != coredomain.PurposeNotSale {
		return false, coredomain.ErrResourceNotPrivate
	}
	dr.Purpose = coredomain.PurposeSale
	return true, nil
}

func (r *mockResourceRepo) PublishResourcesBatchWithLog(_ context.Context, ownerUserID uint, resourceIDs []uint, _, _ governancedomain.OperationLog) ([]uint, error) {
	var microsoftIDs []uint
	var domainIDs []uint
	for _, id := range resourceIDs {
		root, ok := r.resources[id]
		if !ok || root.OwnerUserID != ownerUserID {
			return nil, coredomain.ErrForbiddenResource
		}
		switch root.Type {
		case coredomain.ResourceTypeMicrosoft:
			ms, ok := r.microsoft[id]
			if !ok || ms.Status == coredomain.MicrosoftStatusDeleted {
				return nil, coredomain.ErrResourceNotFound
			}
			if !ms.ForSale {
				microsoftIDs = append(microsoftIDs, id)
			}
		case coredomain.ResourceTypeDomain:
			dr, ok := r.domains[id]
			if !ok || dr.Status == coredomain.DomainStatusDeleted {
				return nil, coredomain.ErrResourceNotFound
			}
			switch dr.Purpose {
			case coredomain.PurposeNotSale:
				domainIDs = append(domainIDs, id)
			case coredomain.PurposeSale:
			case coredomain.PurposeBinding:
				return nil, coredomain.ErrResourceNotPrivate
			default:
				return nil, coredomain.ErrInvalidPurpose
			}
		default:
			return nil, coredomain.ErrInvalidResourceType
		}
	}

	publishedIDs := make([]uint, 0, len(microsoftIDs)+len(domainIDs))
	for _, id := range microsoftIDs {
		r.microsoft[id].ForSale = true
		publishedIDs = append(publishedIDs, id)
	}
	for _, id := range domainIDs {
		r.domains[id].Purpose = coredomain.PurposeSale
		publishedIDs = append(publishedIDs, id)
	}
	return publishedIDs, nil
}

func (r *mockResourceRepo) PublishResourcesByFilterWithLog(_ context.Context, ownerUserID uint, filter coreapp.ResourceBulkFilter, microsoftLog governancedomain.OperationLog, domainLog governancedomain.OperationLog) (int, error) {
	ids := r.filteredPrivateIDs(ownerUserID, filter)
	publishedIDs, err := r.PublishResourcesBatchWithLog(context.Background(), ownerUserID, ids, microsoftLog, domainLog)
	if err != nil {
		return 0, err
	}
	return len(publishedIDs), nil
}

func (r *mockResourceRepo) DeletePrivateMicrosoftWithLog(_ context.Context, ownerUserID uint, resourceID uint, _ governancedomain.OperationLog) error {
	root, ok := r.resources[resourceID]
	if !ok || root.OwnerUserID != ownerUserID || root.Type != coredomain.ResourceTypeMicrosoft {
		return coredomain.ErrForbiddenResource
	}
	ms, ok := r.microsoft[resourceID]
	if !ok {
		return coredomain.ErrResourceNotFound
	}
	if ms.ForSale {
		return coredomain.ErrResourceNotPrivate
	}
	if ms.Status == coredomain.MicrosoftStatusDeleted {
		return coredomain.ErrResourceNotFound
	}
	ms.Status = coredomain.MicrosoftStatusDeleted
	return nil
}

func (r *mockResourceRepo) DeletePrivateDomainWithLog(_ context.Context, ownerUserID uint, resourceID uint, _ governancedomain.OperationLog) error {
	root, ok := r.resources[resourceID]
	if !ok || root.OwnerUserID != ownerUserID || root.Type != coredomain.ResourceTypeDomain {
		return coredomain.ErrForbiddenResource
	}
	dr, ok := r.domains[resourceID]
	if !ok {
		return coredomain.ErrResourceNotFound
	}
	if dr.Purpose != coredomain.PurposeNotSale {
		return coredomain.ErrResourceNotPrivate
	}
	if dr.Status == coredomain.DomainStatusDeleted {
		return coredomain.ErrResourceNotFound
	}
	dr.Status = coredomain.DomainStatusDeleted
	return nil
}

func (r *mockResourceRepo) DeleteResourcesBatchWithLog(_ context.Context, ownerUserID uint, resourceIDs []uint, _, _ governancedomain.OperationLog) ([]uint, error) {
	var microsoftIDs []uint
	var domainIDs []uint
	for _, id := range resourceIDs {
		root, ok := r.resources[id]
		if !ok || root.OwnerUserID != ownerUserID {
			return nil, coredomain.ErrForbiddenResource
		}
		switch root.Type {
		case coredomain.ResourceTypeMicrosoft:
			ms, ok := r.microsoft[id]
			if !ok {
				return nil, coredomain.ErrResourceNotFound
			}
			if ms.Status != coredomain.MicrosoftStatusDeleted && !ms.ForSale {
				microsoftIDs = append(microsoftIDs, id)
			}
		case coredomain.ResourceTypeDomain:
			dr, ok := r.domains[id]
			if !ok {
				return nil, coredomain.ErrResourceNotFound
			}
			if dr.Status != coredomain.DomainStatusDeleted && dr.Purpose == coredomain.PurposeNotSale {
				domainIDs = append(domainIDs, id)
			}
		default:
			return nil, coredomain.ErrInvalidResourceType
		}
	}

	deletedIDs := make([]uint, 0, len(microsoftIDs)+len(domainIDs))
	for _, id := range microsoftIDs {
		r.microsoft[id].Status = coredomain.MicrosoftStatusDeleted
		deletedIDs = append(deletedIDs, id)
	}
	for _, id := range domainIDs {
		r.domains[id].Status = coredomain.DomainStatusDeleted
		deletedIDs = append(deletedIDs, id)
	}
	return deletedIDs, nil
}

func (r *mockResourceRepo) DeleteResourcesByFilterWithLog(_ context.Context, ownerUserID uint, filter coreapp.ResourceBulkFilter, microsoftLog governancedomain.OperationLog, domainLog governancedomain.OperationLog) (int, error) {
	ids := r.filteredPrivateIDs(ownerUserID, filter)
	deletedIDs, err := r.DeleteResourcesBatchWithLog(context.Background(), ownerUserID, ids, microsoftLog, domainLog)
	if err != nil {
		return 0, err
	}
	return len(deletedIDs), nil
}

func (r *mockResourceRepo) filteredPrivateIDs(ownerUserID uint, filter coreapp.ResourceBulkFilter) []uint {
	ids := make([]uint, 0)
	for id, root := range r.resources {
		if root.OwnerUserID != ownerUserID || root.Type != filter.ResourceType {
			continue
		}
		if filter.CreatedFrom != nil && root.CreatedAt.Before(*filter.CreatedFrom) {
			continue
		}
		if filter.CreatedTo != nil && root.CreatedAt.After(*filter.CreatedTo) {
			continue
		}
		switch filter.ResourceType {
		case coredomain.ResourceTypeMicrosoft:
			ms := r.microsoft[id]
			if ms == nil || ms.ForSale || ms.Status == coredomain.MicrosoftStatusDeleted {
				continue
			}
			if filter.Status != "" && string(ms.Status) != filter.Status {
				continue
			}
			if filter.LongLived != nil && ms.LongLived != *filter.LongLived {
				continue
			}
			if filter.Suffix != "" {
				parts := strings.Split(ms.EmailAddress, "@")
				if len(parts) != 2 || strings.ToLower(parts[1]) != filter.Suffix {
					continue
				}
			}
			if filter.Search != "" && !strings.Contains(strings.ToLower(ms.EmailAddress), filter.Search) {
				continue
			}
			ids = append(ids, id)
		case coredomain.ResourceTypeDomain:
			dr := r.domains[id]
			if dr == nil || dr.Purpose != coredomain.PurposeNotSale || dr.Status == coredomain.DomainStatusDeleted {
				continue
			}
			if filter.Status != "" && string(dr.Status) != filter.Status {
				continue
			}
			if filter.TLD != "" && coredomain.TLD(dr.Domain) != filter.TLD {
				continue
			}
			if filter.Search != "" && !strings.Contains(strings.ToLower(dr.Domain), filter.Search) {
				continue
			}
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (r *mockResourceRepo) UpdateDomainWithLog(_ context.Context, _ *coredomain.MailDomainResource, _ *governancedomain.OperationLog) error {
	return nil
}

func (r *mockResourceRepo) ListMicrosoftStatus(_ context.Context, ids []uint) ([]coreapp.MicrosoftStatusResult, error) {
	var result []coreapp.MicrosoftStatusResult
	for _, id := range ids {
		if ms, ok := r.microsoft[id]; ok {
			result = append(result, coreapp.MicrosoftStatusResult{
				ID:            ms.ID,
				EmailAddress:  ms.EmailAddress,
				ForSale:       ms.ForSale,
				LongLived:     ms.LongLived,
				Status:        string(ms.Status),
				LastSafeError: ms.LastSafeError,
			})
		}
	}
	return result, nil
}

func (r *mockResourceRepo) ListDomainStatus(_ context.Context, ids []uint) ([]coreapp.DomainStatusResult, error) {
	var result []coreapp.DomainStatusResult
	for _, id := range ids {
		if dr, ok := r.domains[id]; ok {
			if dr.Status == coredomain.DomainStatusDeleted {
				continue
			}
			result = append(result, coreapp.DomainStatusResult{
				ID:           dr.ID,
				Domain:       dr.Domain,
				DomainTLD:    coredomain.TLD(dr.Domain),
				MailServerID: dr.MailServerID,
				Purpose:      string(dr.Purpose),
				Status:       string(dr.Status),
				MailboxCount: 0,
			})
		}
	}
	return result, nil
}

type mockMailServerRepo struct {
	servers map[uint]*coredomain.MailServer
	seq     uint
}

func newMockMailServerRepo() *mockMailServerRepo {
	return &mockMailServerRepo{servers: make(map[uint]*coredomain.MailServer)}
}

func (r *mockMailServerRepo) Create(_ context.Context, server *coredomain.MailServer) error {
	r.seq++
	server.ID = r.seq
	r.servers[server.ID] = server
	return nil
}

func (r *mockMailServerRepo) FindByID(_ context.Context, id uint) (*coredomain.MailServer, error) {
	if s, ok := r.servers[id]; ok {
		return s, nil
	}
	return nil, nil
}

func (r *mockMailServerRepo) GetOrCreateDefaultInbound(_ context.Context, ownerUserID uint, name, serverAddress, mxRecord string) (*coredomain.MailServer, error) {
	for _, server := range r.servers {
		if server.OwnerUserID == ownerUserID && server.ServerAddress == serverAddress && server.MXRecord == mxRecord {
			return server, nil
		}
	}
	server := &coredomain.MailServer{
		OwnerUserID:   ownerUserID,
		Name:          name,
		ServerAddress: serverAddress,
		MXRecord:      mxRecord,
		Status:        coredomain.MailServerOnline,
	}
	if err := r.Create(context.Background(), server); err != nil {
		return nil, err
	}
	return server, nil
}

func (r *mockMailServerRepo) List(_ context.Context, _ uint, _, _ int) ([]coredomain.MailServer, error) {
	return nil, nil
}

func (r *mockMailServerRepo) ListAll(_ context.Context, _, _ int) ([]coredomain.MailServer, error) {
	return nil, nil
}

func (r *mockMailServerRepo) Count(_ context.Context, _ uint) (int64, error) {
	return 0, nil
}

func (r *mockMailServerRepo) CountAll(_ context.Context) (int64, error) {
	return 0, nil
}

type mockGeneratedMailboxRepo struct {
	mailboxes map[uint]*coredomain.GeneratedMailbox
}

func newMockGeneratedMailboxRepo() *mockGeneratedMailboxRepo {
	return &mockGeneratedMailboxRepo{mailboxes: make(map[uint]*coredomain.GeneratedMailbox)}
}

func (r *mockGeneratedMailboxRepo) List(_ context.Context, resourceID uint, ownerUserID uint, _, _ int) ([]coredomain.GeneratedMailbox, error) {
	var result []coredomain.GeneratedMailbox
	for _, mb := range r.mailboxes {
		if mb.ResourceID == resourceID && mb.OwnerUserID == ownerUserID {
			result = append(result, *mb)
		}
	}
	return result, nil
}

func (r *mockGeneratedMailboxRepo) Count(_ context.Context, resourceID uint, ownerUserID uint) (int64, error) {
	var count int64
	for _, mb := range r.mailboxes {
		if mb.ResourceID == resourceID && mb.OwnerUserID == ownerUserID {
			count++
		}
	}
	return count, nil
}

type mockImportRepo struct {
	imports   map[uint]*coredomain.ResourceImport
	resources *mockResourceRepo
	seq       uint
}

func newMockImportRepo(resources *mockResourceRepo) *mockImportRepo {
	return &mockImportRepo{imports: make(map[uint]*coredomain.ResourceImport), resources: resources}
}

func (r *mockImportRepo) Create(_ context.Context, item *coredomain.ResourceImport) error {
	r.seq++
	item.ID = r.seq
	item.CreatedAt = time.Now()
	item.UpdatedAt = item.CreatedAt
	snapshot := *item
	r.imports[item.ID] = &snapshot
	return nil
}

func (r *mockImportRepo) FindByID(_ context.Context, id uint) (*coredomain.ResourceImport, error) {
	item := r.imports[id]
	if item == nil {
		return nil, nil
	}
	snapshot := *item
	return &snapshot, nil
}

func (r *mockImportRepo) MarkFailed(_ context.Context, id uint, failureObjectKey string, safeError string) error {
	item := r.imports[id]
	if item == nil || item.Status != coredomain.ResourceImportProcessing {
		return nil
	}
	item.Status = coredomain.ResourceImportFailed
	item.FailureObjectKey = failureObjectKey
	item.LastSafeError = safeError
	item.UpdatedAt = time.Now()
	return nil
}

func (r *mockImportRepo) CreateMicrosoftResourcesAndMarkSucceeded(ctx context.Context, id uint, resources []coredomain.EmailResource, ms []coredomain.MicrosoftResource, failureObjectKey string, safeSummary string) error {
	item := r.imports[id]
	if item == nil {
		return coredomain.ErrResourceNotFound
	}
	if item.Status != coredomain.ResourceImportProcessing {
		return nil
	}
	if err := r.resources.createMicrosoftBatch(ctx, resources, ms); err != nil {
		return err
	}
	item.Status = coredomain.ResourceImportImported
	item.ImportedCount = len(ms)
	item.FailureObjectKey = failureObjectKey
	item.LastSafeError = safeSummary
	item.UpdatedAt = time.Now()
	return nil
}

type mockFileStore struct {
	files map[string]governancedomain.PrivateFile
}

func newMockFileStore() *mockFileStore {
	return &mockFileStore{files: make(map[string]governancedomain.PrivateFile)}
}

func (s *mockFileStore) SavePrivate(_ context.Context, file governancedomain.PrivateFile) (*governancedomain.StoredPrivateFile, error) {
	s.files[file.ObjectKey] = file
	return &governancedomain.StoredPrivateFile{
		ObjectKey:   file.ObjectKey,
		FileName:    file.FileName,
		ContentType: file.ContentType,
		Size:        int64(len(file.ContentBytes)),
	}, nil
}

func (s *mockFileStore) ReadPrivate(_ context.Context, objectKey string) (*governancedomain.PrivateFile, error) {
	file := s.files[objectKey]
	return &file, nil
}

type mockImportQueue struct {
	tasks []coreapp.MicrosoftImportTask
}

func (q *mockImportQueue) EnqueueMicrosoftImport(_ context.Context, task coreapp.MicrosoftImportTask) error {
	q.tasks = append(q.tasks, task)
	return nil
}

// --- Test setup ---

func setupCoreTestModule() (*CoreModule, *mockResourceRepo, *mockMailServerRepo, *mockGeneratedMailboxRepo) {
	mod, resourceRepo, mailServerRepo, mailboxRepo, _, _, _ := setupCoreTestModuleWithImportMocks()
	return mod, resourceRepo, mailServerRepo, mailboxRepo
}

func setupCoreTestModuleWithImportMocks() (*CoreModule, *mockResourceRepo, *mockMailServerRepo, *mockGeneratedMailboxRepo, *mockImportQueue, *mockImportRepo, *mockFileStore) {
	txtParser := &mockTXTParser{}
	resourceRepo := newMockResourceRepo()
	importRepo := newMockImportRepo(resourceRepo)
	importQueue := &mockImportQueue{}
	mailServerRepo := newMockMailServerRepo()
	mailboxRepo := newMockGeneratedMailboxRepo()
	fileStore := newMockFileStore()

	mod := &CoreModule{
		ImportUseCase:   coreapp.NewImportUseCase(resourceRepo, importRepo, txtParser, fileStore, importQueue),
		ResourceUseCase: coreapp.NewResourceUseCase(resourceRepo),
		DomainUseCase:   coreapp.NewDomainUseCase(resourceRepo, mailServerRepo, mailboxRepo),
		ServerUseCase:   coreapp.NewServerUseCase(mailServerRepo),
		MailboxUseCase:  coreapp.NewDomainMailboxUseCase(mailboxRepo, resourceRepo),
	}
	return mod, resourceRepo, mailServerRepo, mailboxRepo, importQueue, importRepo, fileStore
}

type mockTXTParser struct{}

func (p *mockTXTParser) ParseMicrosoftImport(content string, strategy coredomain.ImportErrorStrategy) ([]coredomain.MicrosoftImportLine, []coredomain.ImportLineError, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, nil, coredomain.ErrInvalidImportFormat
	}
	lines := strings.Split(content, "\n")
	var result []coredomain.MicrosoftImportLine
	var failures []coredomain.ImportLineError
	for i, line := range lines {
		lineNumber := i + 1
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "----")
		if len(parts) != 2 && len(parts) != 3 && len(parts) != 4 && len(parts) != 5 {
			if strategy == coredomain.ImportErrorStrategyAbort {
				return nil, nil, coredomain.ErrInvalidImportFormat
			}
			failures = append(failures, coredomain.ImportLineError{Line: lineNumber, Category: "invalid_format", SafeMessage: "Invalid import format."})
			continue
		}
		email := strings.TrimSpace(parts[0])
		password := strings.TrimSpace(parts[1])
		if email == "" || password == "" {
			if strategy == coredomain.ImportErrorStrategyAbort {
				return nil, nil, coredomain.ErrInvalidImportFormat
			}
			failures = append(failures, coredomain.ImportLineError{Line: lineNumber, Email: email, Category: "invalid_format", SafeMessage: "Invalid import format."})
			continue
		}
		item := coredomain.MicrosoftImportLine{
			LineNumber: lineNumber,
			Email:      email,
			Password:   password,
		}
		switch len(parts) {
		case 3:
			item.BindingAddress = strings.TrimSpace(parts[2])
			if item.BindingAddress == "" {
				if strategy == coredomain.ImportErrorStrategyAbort {
					return nil, nil, coredomain.ErrInvalidImportFormat
				}
				failures = append(failures, coredomain.ImportLineError{Line: lineNumber, Email: email, Category: "invalid_format", SafeMessage: "Invalid import format."})
				continue
			}
		case 4:
			item.ClientID = strings.TrimSpace(parts[2])
			item.RefreshToken = strings.TrimSpace(parts[3])
			if item.ClientID == "" || item.RefreshToken == "" {
				if strategy == coredomain.ImportErrorStrategyAbort {
					return nil, nil, coredomain.ErrInvalidImportFormat
				}
				failures = append(failures, coredomain.ImportLineError{Line: lineNumber, Email: email, Category: "invalid_format", SafeMessage: "Invalid import format."})
				continue
			}
		case 5:
			item.ClientID = strings.TrimSpace(parts[2])
			item.RefreshToken = strings.TrimSpace(parts[3])
			item.BindingAddress = strings.TrimSpace(parts[4])
			if item.ClientID == "" || item.RefreshToken == "" || item.BindingAddress == "" {
				if strategy == coredomain.ImportErrorStrategyAbort {
					return nil, nil, coredomain.ErrInvalidImportFormat
				}
				failures = append(failures, coredomain.ImportLineError{Line: lineNumber, Email: email, Category: "invalid_format", SafeMessage: "Invalid import format."})
				continue
			}
		}
		result = append(result, item)
	}
	if len(result) == 0 && len(failures) == 0 {
		return nil, nil, coredomain.ErrInvalidImportFormat
	}
	return result, failures, nil
}

func setAuthContext(c *gin.Context, userID uint, roleLevel int) {
	middleware.SetCurrentUser(c, userID, iamdomain.RoleLevel(roleLevel), "test@example.com", "test-session-id")
}

func multipartImportBody(t *testing.T, fileName string, content string) (*bytes.Buffer, string) {
	return multipartImportBodyWithStrategy(t, fileName, content, "")
}

func multipartImportBodyWithStrategy(t *testing.T, fileName string, content string, errorStrategy string) (*bytes.Buffer, string) {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatalf("create multipart file: %v", err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatalf("write multipart file: %v", err)
	}
	if err := writer.WriteField("longLived", "true"); err != nil {
		t.Fatalf("write longLived field: %v", err)
	}
	if errorStrategy != "" {
		if err := writer.WriteField("errorStrategy", errorStrategy); err != nil {
			t.Fatalf("write errorStrategy field: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return body, writer.FormDataContentType()
}

func TestCoreHandler_RequiresAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	endpoints := []struct {
		method string
		path   string
		body   string
	}{
		{"GET", "/v1/resources", ""},
		{"GET", "/v1/resources/1", ""},
		{"DELETE", "/v1/resources/1", ""},
		{"POST", "/v1/resources/imports", `{"content":"a@b----c"}`},
		{"POST", "/v1/resources/delete", `{"selection":{"mode":"ids","resourceIds":[1]}}`},
		{"POST", "/v1/resources/publish", `{"selection":{"mode":"ids","resourceIds":[1]}}`},
		{"POST", "/v1/resources/1/publish", ""},
		{"GET", "/v1/servers", ""},
		{"POST", "/v1/servers", `{"serverAddress":"smtp.example.com"}`},
		{"POST", "/v1/domains", `{"domain":"example.com","mailServerId":1,"purpose":"sale"}`},
		{"GET", "/v1/domains/1/mailboxes", ""},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			mod, _, _, _ := setupCoreTestModule()
			h := NewCoreHandler(mod)

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			req := httptest.NewRequest(ep.method, ep.path, strings.NewReader(ep.body))
			if ep.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			c.Request = req

			// Set path params for parameterized routes
			switch ep.path {
			case "/v1/resources/1", "/v1/resources/1/publish":
				c.Params = []gin.Param{{Key: "resourceId", Value: "1"}}
			case "/v1/domains/1/mailboxes":
				c.Params = []gin.Param{{Key: "domainId", Value: "1"}}
			}

			// Route to the appropriate handler
			switch {
			case ep.method == "GET" && ep.path == "/v1/resources":
				h.GetResources(c)
			case ep.method == "POST" && ep.path == "/v1/resources/1/publish":
				h.PostResourcePublish(c)
			case ep.method == "POST" && ep.path == "/v1/resources/publish":
				h.PostResourcePublishBatch(c)
			case ep.method == "POST" && ep.path == "/v1/resources/delete":
				h.PostResourceDeleteBatch(c)
			case ep.method == "DELETE" && ep.path == "/v1/resources/1":
				h.DeleteResource(c)
			case ep.method == "GET" && len(ep.path) >= 14 && ep.path[:14] == "/v1/resources/":
				h.GetResourceDetail(c)
			case ep.method == "POST" && ep.path == "/v1/resources/imports":
				req.Header.Set("Content-Type", "application/json")
				h.PostResourceImport(c)
			case ep.method == "GET" && ep.path == "/v1/servers":
				h.GetServers(c)
			case ep.method == "POST" && ep.path == "/v1/servers":
				h.PostServer(c)
			case ep.method == "POST" && ep.path == "/v1/domains":
				h.PostDomain(c)
			case ep.method == "GET" && len(ep.path) >= 12 && ep.path[:12] == "/v1/domains/":
				h.GetDomainMailboxes(c)
			}

			if w.Code != http.StatusUnauthorized {
				t.Errorf("expected 401, got %d for %s %s", w.Code, ep.method, ep.path)
			}
		})
	}
}

func TestCoreHandler_RequiresSupplierRoleForPrivilegedResourceCommands(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		params []gin.Param
		call   func(*CoreHandler, *gin.Context)
	}{
		{
			name:   "publish resource",
			method: "POST",
			path:   "/v1/resources/1/publish",
			params: []gin.Param{{Key: "resourceId", Value: "1"}},
			call:   (*CoreHandler).PostResourcePublish,
		},
		{
			name:   "publish resources batch",
			method: "POST",
			path:   "/v1/resources/publish",
			body:   `{"selection":{"mode":"ids","resourceIds":[1]}}`,
			call:   (*CoreHandler).PostResourcePublishBatch,
		},
		{
			name:   "list servers",
			method: "GET",
			path:   "/v1/servers",
			call:   (*CoreHandler).GetServers,
		},
		{
			name:   "create server",
			method: "POST",
			path:   "/v1/servers",
			body:   `{"serverAddress":"smtp.example.com"}`,
			call:   (*CoreHandler).PostServer,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mod, _, _, _ := setupCoreTestModule()
			h := NewCoreHandler(mod)

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			if tt.body != "" {
				c.Request.Header.Set("Content-Type", "application/json")
			}
			c.Params = tt.params
			setAuthContext(c, 1, 10) // roleLevel=user (10), below supplier (20)

			tt.call(h, c)

			if w.Code != http.StatusForbidden {
				t.Errorf("expected 403, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestCoreHandler_ImportSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, _, _, importQueue, _, _ := setupCoreTestModuleWithImportMocks()
	h := NewCoreHandler(mod)

	body, contentType := multipartImportBody(t, "resources.txt", "user@example.com----pass123\nuser2@test.com----pass456----aux@example.net")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/resources/imports", body)
	c.Request.Header.Set("Content-Type", contentType)
	setAuthContext(c, 1, 10) // regular users can import private Microsoft resources

	h.PostResourceImport(c)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	var resp ImportResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Imported != 0 {
		t.Errorf("expected imported 0 before async processing, got %d", resp.Imported)
	}
	if resp.ImportID == 0 {
		t.Errorf("expected importId > 0, got %d", resp.ImportID)
	}
	require.Len(t, importQueue.tasks, 1)
	require.Equal(t, resp.ImportID, importQueue.tasks[0].ImportID)
	require.Equal(t, coredomain.ImportErrorStrategySkip, importQueue.tasks[0].ErrorStrategy)
	require.NoError(t, mod.ImportUseCase.ProcessMicrosoftImport(context.Background(), importQueue.tasks[0]))
	require.Len(t, resourceRepo.microsoft, 2)

	for _, ms := range resourceRepo.microsoft {
		if ms.ForSale {
			t.Fatalf("expected imported Microsoft resource to be private by default")
		}
		if !ms.LongLived {
			t.Fatalf("expected imported Microsoft resource to inherit longLived batch option")
		}
	}
}

func TestCoreHandler_ImportInvalidFormatDefaultSkips(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, _, _, _, importQueue, importRepo, _ := setupCoreTestModuleWithImportMocks()
	h := NewCoreHandler(mod)

	body, contentType := multipartImportBody(t, "resources.txt", "invalid")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/resources/imports", body)
	c.Request.Header.Set("Content-Type", contentType)
	setAuthContext(c, 1, 10)

	h.PostResourceImport(c)

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	require.Len(t, importQueue.tasks, 1)
	require.NoError(t, mod.ImportUseCase.ProcessMicrosoftImport(context.Background(), importQueue.tasks[0]))
	importRecord := importRepo.imports[importQueue.tasks[0].ImportID]
	require.Equal(t, coredomain.ResourceImportImported, importRecord.Status)
	require.Zero(t, importRecord.ImportedCount)
	require.NotEmpty(t, importRecord.FailureObjectKey)
	require.Equal(t, "Skipped 1 import entry.", importRecord.LastSafeError)
}

func TestCoreHandler_ImportInvalidFormatAbortFails(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, _, _, _, importQueue, importRepo, _ := setupCoreTestModuleWithImportMocks()
	h := NewCoreHandler(mod)

	body, contentType := multipartImportBodyWithStrategy(t, "resources.txt", "invalid", "abort")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/resources/imports", body)
	c.Request.Header.Set("Content-Type", contentType)
	setAuthContext(c, 1, 10)

	h.PostResourceImport(c)

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	require.Len(t, importQueue.tasks, 1)
	require.Equal(t, coredomain.ImportErrorStrategyAbort, importQueue.tasks[0].ErrorStrategy)
	require.NoError(t, mod.ImportUseCase.ProcessMicrosoftImport(context.Background(), importQueue.tasks[0]))
	require.Equal(t, coredomain.ResourceImportFailed, importRepo.imports[importQueue.tasks[0].ImportID].Status)
}

func TestCoreHandler_ImportDuplicateDefaultSkips(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, _, _, importQueue, importRepo, _ := setupCoreTestModuleWithImportMocks()
	h := NewCoreHandler(mod)
	require.NoError(t, resourceRepo.CreateMicrosoft(
		context.Background(),
		&coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1},
		&coredomain.MicrosoftResource{
			EmailAddress: "duplicate@example.com",
			Password:     "secret",
			Status:       coredomain.MicrosoftStatusPending,
		},
	))

	body, contentType := multipartImportBody(t, "resources.txt", "duplicate@example.com----pass123\nnew@example.com----pass456")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/resources/imports", body)
	c.Request.Header.Set("Content-Type", contentType)
	setAuthContext(c, 1, 10)

	h.PostResourceImport(c)

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	require.Len(t, importQueue.tasks, 1)
	require.NoError(t, mod.ImportUseCase.ProcessMicrosoftImport(context.Background(), importQueue.tasks[0]))
	importRecord := importRepo.imports[importQueue.tasks[0].ImportID]
	require.Equal(t, coredomain.ResourceImportImported, importRecord.Status)
	require.Equal(t, 1, importRecord.ImportedCount)
	require.Equal(t, "Skipped 1 import entry.", importRecord.LastSafeError)
	require.Len(t, resourceRepo.microsoft, 2)
}

func TestCoreHandler_ResourceDetail_OwnerAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, _, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	// Create a Microsoft resource owned by user 1
	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	ms := &coredomain.MicrosoftResource{
		EmailAddress: "test@example.com",
		Password:     "secret",
		Status:       coredomain.MicrosoftStatusNormal,
		ForSale:      true,
	}
	if err := resourceRepo.CreateMicrosoft(context.Background(), root, ms); err != nil {
		t.Fatalf("create resource: %v", err)
	}

	// Owner (userID=1) should see detail
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/v1/resources/1", nil)
	c.Params = []gin.Param{{Key: "resourceId", Value: "1"}}
	setAuthContext(c, 1, 10)

	h.GetResourceDetail(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for owner, got %d: %s", w.Code, w.Body.String())
	}

	// Verify no credentials in response
	body := w.Body.String()
	if strings.Contains(body, "secret") {
		t.Error("response contains password!")
	}
}

func TestCoreHandler_ResourceDetail_NonOwnerDenied(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, _, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	// Create a resource owned by user 1
	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	ms := &coredomain.MicrosoftResource{EmailAddress: "test@example.com", Password: "secret"}
	_ = resourceRepo.CreateMicrosoft(context.Background(), root, ms)

	// Non-owner (userID=2) should get 404 (ErrForbiddenResource → Resource not found)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/v1/resources/1", nil)
	c.Params = []gin.Param{{Key: "resourceId", Value: "1"}}
	setAuthContext(c, 2, 10)

	h.GetResourceDetail(c)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for non-owner, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCoreHandler_ValidateStubReturns501(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, _, _, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/resources/1/validate", nil)
	c.Params = []gin.Param{{Key: "resourceId", Value: "1"}}
	setAuthContext(c, 1, 10)

	h.PostResourceValidate(c)

	if w.Code != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCoreHandler_ResourceListIncludesStatusFields(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, mailServerRepo, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	msRoot := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	ms := &coredomain.MicrosoftResource{
		EmailAddress:  "ms@example.com",
		Password:      "secret",
		Status:        coredomain.MicrosoftStatusNormal,
		ForSale:       true,
		LongLived:     true,
		LastSafeError: "safe diagnostic",
	}
	if err := resourceRepo.CreateMicrosoft(context.Background(), msRoot, ms); err != nil {
		t.Fatalf("create microsoft resource: %v", err)
	}

	server := &coredomain.MailServer{OwnerUserID: 1, ServerAddress: "mail.example.com", Status: coredomain.MailServerOnline}
	if err := mailServerRepo.Create(context.Background(), server); err != nil {
		t.Fatalf("create mail server: %v", err)
	}
	domainRoot := &coredomain.EmailResource{Type: coredomain.ResourceTypeDomain, OwnerUserID: 1}
	dr := &coredomain.MailDomainResource{
		Domain:       "example.com",
		MailServerID: server.ID,
		Purpose:      coredomain.PurposeSale,
		Status:       coredomain.DomainStatusNormal,
	}
	if err := resourceRepo.CreateDomain(context.Background(), domainRoot, dr); err != nil {
		t.Fatalf("create domain resource: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/v1/resources", nil)
	setAuthContext(c, 1, 20)

	h.GetResources(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp ResourceListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Total != 2 || len(resp.Items) != 2 {
		t.Fatalf("expected 2 resources, got total=%d len=%d", resp.Total, len(resp.Items))
	}

	var sawMicrosoft, sawDomain bool
	for _, item := range resp.Items {
		switch item.Type {
		case string(coredomain.ResourceTypeMicrosoft):
			sawMicrosoft = true
			if item.Status != string(coredomain.MicrosoftStatusNormal) {
				t.Errorf("expected microsoft status normal, got %q", item.Status)
			}
			if item.Email != "ms@example.com" {
				t.Errorf("expected microsoft email, got %q", item.Email)
			}
			if item.ForSale == nil || !*item.ForSale {
				t.Errorf("expected microsoft forSale true, got %v", item.ForSale)
			}
			if item.LongLived == nil || !*item.LongLived {
				t.Errorf("expected microsoft longLived true, got %v", item.LongLived)
			}
			if item.LastSafeError != "safe diagnostic" {
				t.Errorf("expected lastSafeError safe diagnostic, got %q", item.LastSafeError)
			}
		case string(coredomain.ResourceTypeDomain):
			sawDomain = true
			if item.Status != string(coredomain.DomainStatusNormal) {
				t.Errorf("expected domain status normal, got %q", item.Status)
			}
			if item.Domain != "example.com" {
				t.Errorf("expected domain example.com, got %q", item.Domain)
			}
			if item.DomainTLD != ".com" {
				t.Errorf("expected domainTld .com, got %q", item.DomainTLD)
			}
			if item.MailServerID != server.ID {
				t.Errorf("expected mailServerId %d, got %d", server.ID, item.MailServerID)
			}
			if item.Purpose != string(coredomain.PurposeSale) {
				t.Errorf("expected purpose sale, got %q", item.Purpose)
			}
		}
	}
	if !sawMicrosoft || !sawDomain {
		t.Fatalf("expected both microsoft and domain resources, got %+v", resp.Items)
	}
}

func TestCoreHandler_ResourceListScopeAllFallsBackToOwnedForNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, _, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	ownerRoot := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	ownerMs := &coredomain.MicrosoftResource{
		EmailAddress: "owner@example.com",
		Password:     "secret",
		Status:       coredomain.MicrosoftStatusNormal,
		ForSale:      true,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), ownerRoot, ownerMs))
	otherRoot := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 2}
	otherMs := &coredomain.MicrosoftResource{
		EmailAddress: "other@example.com",
		Password:     "secret",
		Status:       coredomain.MicrosoftStatusNormal,
		ForSale:      true,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), otherRoot, otherMs))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/v1/resources?scope=all", nil)
	setAuthContext(c, 1, 10)

	h.GetResources(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp ResourceListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, 1, len(resp.Items))
	require.Equal(t, int64(1), resp.Total)
	require.Equal(t, "owner@example.com", resp.Items[0].Email)
}

func TestCoreHandler_PublishMicrosoftResource(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, _, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	ms := &coredomain.MicrosoftResource{
		EmailAddress: "private@example.com",
		Password:     "secret",
		Status:       coredomain.MicrosoftStatusPending,
		ForSale:      false,
	}
	if err := resourceRepo.CreateMicrosoft(context.Background(), root, ms); err != nil {
		t.Fatalf("create resource: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/resources/1/publish", nil)
	c.Params = []gin.Param{{Key: "resourceId", Value: "1"}}
	setAuthContext(c, 1, 20)

	h.PostResourcePublish(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !resourceRepo.microsoft[1].ForSale {
		t.Fatalf("expected resource to be published for sale")
	}

	var resp MicrosoftResourceDetailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if !resp.ForSale {
		t.Fatalf("expected response forSale true")
	}
}

func TestCoreHandler_PublishMicrosoftResourcesBatch(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, _, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	firstRoot := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	first := &coredomain.MicrosoftResource{
		EmailAddress: "private1@example.com",
		Password:     "secret",
		Status:       coredomain.MicrosoftStatusPending,
		ForSale:      false,
	}
	if err := resourceRepo.CreateMicrosoft(context.Background(), firstRoot, first); err != nil {
		t.Fatalf("create first resource: %v", err)
	}

	secondRoot := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	second := &coredomain.MicrosoftResource{
		EmailAddress: "public@example.com",
		Password:     "secret",
		Status:       coredomain.MicrosoftStatusPending,
		ForSale:      true,
	}
	if err := resourceRepo.CreateMicrosoft(context.Background(), secondRoot, second); err != nil {
		t.Fatalf("create second resource: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/resources/publish", strings.NewReader(`{"selection":{"mode":"ids","resourceIds":[1,2]}}`))
	c.Request.Header.Set("Content-Type", "application/json")
	setAuthContext(c, 1, 20)

	h.PostResourcePublishBatch(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !resourceRepo.microsoft[1].ForSale || !resourceRepo.microsoft[2].ForSale {
		t.Fatalf("expected resources to be for sale")
	}

	var resp PublishResourcesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Requested != 2 || resp.Published != 1 {
		t.Fatalf("expected requested=2 published=1, got %+v", resp)
	}
	require.ElementsMatch(t, []uint{first.ID}, resp.PublishedResourceIDs)
}

func TestCoreHandler_PublishResourcesBatchRejectsBindingWithoutPartialPublish(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, _, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	msRoot := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	ms := &coredomain.MicrosoftResource{
		EmailAddress: "private-mixed@example.com",
		Password:     "secret",
		Status:       coredomain.MicrosoftStatusPending,
		ForSale:      false,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), msRoot, ms))

	domainRoot := &coredomain.EmailResource{Type: coredomain.ResourceTypeDomain, OwnerUserID: 1}
	dr := &coredomain.MailDomainResource{
		Domain:       "binding.example.com",
		MailServerID: 1,
		Purpose:      coredomain.PurposeBinding,
		Status:       coredomain.DomainStatusNormal,
	}
	require.NoError(t, resourceRepo.CreateDomain(context.Background(), domainRoot, dr))

	body := fmt.Sprintf(`{"selection":{"mode":"ids","resourceIds":[%d,%d]}}`, ms.ID, dr.ID)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/resources/publish", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	setAuthContext(c, 1, 20)

	h.PostResourcePublishBatch(c)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	require.False(t, resourceRepo.microsoft[ms.ID].ForSale)
	require.Equal(t, coredomain.PurposeBinding, resourceRepo.domains[dr.ID].Purpose)
}

func TestCoreHandler_PublishResourcesByFilterOmitsResourceIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, _, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	longLived := &coredomain.MicrosoftResource{
		EmailAddress: "filter-long@example.com",
		Password:     "secret",
		Status:       coredomain.MicrosoftStatusNormal,
		LongLived:    true,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}, longLived))
	shortLived := &coredomain.MicrosoftResource{
		EmailAddress: "filter-short@example.com",
		Password:     "secret",
		Status:       coredomain.MicrosoftStatusNormal,
		LongLived:    false,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}, shortLived))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/resources/publish", strings.NewReader(`{"selection":{"mode":"filter","filter":{"resourceType":"microsoft","status":"normal","longLived":true}}}`))
	c.Request.Header.Set("Content-Type", "application/json")
	setAuthContext(c, 1, 20)

	h.PostResourcePublishBatch(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.True(t, resourceRepo.microsoft[longLived.ID].ForSale)
	require.False(t, resourceRepo.microsoft[shortLived.ID].ForSale)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.EqualValues(t, 1, resp["requested"])
	require.EqualValues(t, 1, resp["published"])
	require.NotContains(t, resp, "publishedResourceIds")
}

func TestCoreHandler_BulkSelectionShapeValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name      string
		method    string
		path      string
		body      string
		roleLevel int
		call      func(*CoreHandler, *gin.Context)
		field     string
	}{
		{
			name:      "publish ids requires ids",
			method:    "POST",
			path:      "/v1/resources/publish",
			body:      `{"selection":{"mode":"ids"}}`,
			roleLevel: 20,
			call:      (*CoreHandler).PostResourcePublishBatch,
			field:     "selection.resourceIds",
		},
		{
			name:      "publish filter requires resource type",
			method:    "POST",
			path:      "/v1/resources/publish",
			body:      `{"selection":{"mode":"filter","filter":{"status":"normal"}}}`,
			roleLevel: 20,
			call:      (*CoreHandler).PostResourcePublishBatch,
			field:     "selection.filter.resourceType",
		},
		{
			name:      "delete ids requires ids",
			method:    "POST",
			path:      "/v1/resources/delete",
			body:      `{"selection":{"mode":"ids"}}`,
			roleLevel: 10,
			call:      (*CoreHandler).PostResourceDeleteBatch,
			field:     "selection.resourceIds",
		},
		{
			name:      "delete ids rejects zero",
			method:    "POST",
			path:      "/v1/resources/delete",
			body:      `{"selection":{"mode":"ids","resourceIds":[0]}}`,
			roleLevel: 10,
			call:      (*CoreHandler).PostResourceDeleteBatch,
			field:     "selection.resourceIds",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mod, _, _, _ := setupCoreTestModule()
			h := NewCoreHandler(mod)

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			c.Request.Header.Set("Content-Type", "application/json")
			setAuthContext(c, 1, tt.roleLevel)

			tt.call(h, c)

			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			var resp struct {
				Fields map[string]string `json:"fields"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			require.Contains(t, resp.Fields, tt.field)
		})
	}
}

func TestCoreHandler_PublishMicrosoftResourceNonOwnerDenied(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, _, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	ms := &coredomain.MicrosoftResource{EmailAddress: "private@example.com", Password: "secret"}
	if err := resourceRepo.CreateMicrosoft(context.Background(), root, ms); err != nil {
		t.Fatalf("create resource: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/resources/1/publish", nil)
	c.Params = []gin.Param{{Key: "resourceId", Value: "1"}}
	setAuthContext(c, 2, 20)

	h.PostResourcePublish(c)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for non-owner, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCoreHandler_DeletePrivateMicrosoftResource(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, _, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	ms := &coredomain.MicrosoftResource{
		EmailAddress: "private-delete@example.com",
		Password:     "secret",
		Status:       coredomain.MicrosoftStatusPending,
		ForSale:      false,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, ms))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("DELETE", "/v1/resources/1", nil)
	c.Params = []gin.Param{{Key: "resourceId", Value: "1"}}
	setAuthContext(c, 1, 10)

	h.DeleteResource(c)

	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	require.Contains(t, resourceRepo.resources, uint(1))
	require.Equal(t, coredomain.MicrosoftStatusDeleted, resourceRepo.microsoft[1].Status)
}

func TestCoreHandler_DeleteResourcesBatchDeletesPrivateMixedResources(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, _, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	privateMSRoot := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	privateMS := &coredomain.MicrosoftResource{
		EmailAddress: "private-batch@example.com",
		Password:     "secret",
		Status:       coredomain.MicrosoftStatusPending,
		ForSale:      false,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), privateMSRoot, privateMS))

	publicMSRoot := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	publicMS := &coredomain.MicrosoftResource{
		EmailAddress: "public-batch@example.com",
		Password:     "secret",
		Status:       coredomain.MicrosoftStatusNormal,
		ForSale:      true,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), publicMSRoot, publicMS))

	privateDomainRoot := &coredomain.EmailResource{Type: coredomain.ResourceTypeDomain, OwnerUserID: 1}
	privateDomain := &coredomain.MailDomainResource{
		Domain:       "private-batch.example.com",
		MailServerID: 1,
		Purpose:      coredomain.PurposeNotSale,
		Status:       coredomain.DomainStatusNormal,
	}
	require.NoError(t, resourceRepo.CreateDomain(context.Background(), privateDomainRoot, privateDomain))

	body := fmt.Sprintf(`{"selection":{"mode":"ids","resourceIds":[%d,%d,%d]}}`, privateMS.ID, publicMS.ID, privateDomain.ID)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/resources/delete", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	setAuthContext(c, 1, 10)

	h.PostResourceDeleteBatch(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, coredomain.MicrosoftStatusDeleted, resourceRepo.microsoft[privateMS.ID].Status)
	require.Equal(t, coredomain.MicrosoftStatusNormal, resourceRepo.microsoft[publicMS.ID].Status)
	require.Equal(t, coredomain.DomainStatusDeleted, resourceRepo.domains[privateDomain.ID].Status)

	var resp DeleteResourcesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, 3, resp.Requested)
	require.Equal(t, 2, resp.Deleted)
	require.ElementsMatch(t, []uint{privateMS.ID, privateDomain.ID}, resp.DeletedResourceIDs)
}

func TestCoreHandler_DeleteResourcesBatchNonOwnerDeniedWithoutPartialDelete(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, _, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	ownerRoot := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	ownerMS := &coredomain.MicrosoftResource{
		EmailAddress: "owner-batch-delete@example.com",
		Password:     "secret",
		Status:       coredomain.MicrosoftStatusPending,
		ForSale:      false,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), ownerRoot, ownerMS))

	otherRoot := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 2}
	otherMS := &coredomain.MicrosoftResource{
		EmailAddress: "other-batch-delete@example.com",
		Password:     "secret",
		Status:       coredomain.MicrosoftStatusPending,
		ForSale:      false,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), otherRoot, otherMS))

	body := fmt.Sprintf(`{"selection":{"mode":"ids","resourceIds":[%d,%d]}}`, ownerMS.ID, otherMS.ID)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/resources/delete", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	setAuthContext(c, 1, 10)

	h.PostResourceDeleteBatch(c)

	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	require.Equal(t, coredomain.MicrosoftStatusPending, resourceRepo.microsoft[ownerMS.ID].Status)
	require.Equal(t, coredomain.MicrosoftStatusPending, resourceRepo.microsoft[otherMS.ID].Status)
}

func TestCoreHandler_DeleteResourcesByFilterOmitsResourceIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, _, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	privateDomain := &coredomain.MailDomainResource{
		Domain:       "delete-filter.example.com",
		MailServerID: 1,
		Purpose:      coredomain.PurposeNotSale,
		Status:       coredomain.DomainStatusNormal,
	}
	require.NoError(t, resourceRepo.CreateDomain(context.Background(), &coredomain.EmailResource{Type: coredomain.ResourceTypeDomain, OwnerUserID: 1}, privateDomain))
	disabledDomain := &coredomain.MailDomainResource{
		Domain:       "delete-disabled.example.com",
		MailServerID: 1,
		Purpose:      coredomain.PurposeNotSale,
		Status:       coredomain.DomainStatusDisabled,
	}
	require.NoError(t, resourceRepo.CreateDomain(context.Background(), &coredomain.EmailResource{Type: coredomain.ResourceTypeDomain, OwnerUserID: 1}, disabledDomain))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/resources/delete", strings.NewReader(`{"selection":{"mode":"filter","filter":{"resourceType":"domain","status":"normal"}}}`))
	c.Request.Header.Set("Content-Type", "application/json")
	setAuthContext(c, 1, 10)

	h.PostResourceDeleteBatch(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, coredomain.DomainStatusDeleted, resourceRepo.domains[privateDomain.ID].Status)
	require.Equal(t, coredomain.DomainStatusDisabled, resourceRepo.domains[disabledDomain.ID].Status)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.EqualValues(t, 1, resp["requested"])
	require.EqualValues(t, 1, resp["deleted"])
	require.NotContains(t, resp, "deletedResourceIds")
}

func TestCoreHandler_DeletePublishedMicrosoftResourceDenied(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, _, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	ms := &coredomain.MicrosoftResource{
		EmailAddress: "public-delete@example.com",
		Password:     "secret",
		Status:       coredomain.MicrosoftStatusNormal,
		ForSale:      true,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, ms))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("DELETE", "/v1/resources/1", nil)
	c.Params = []gin.Param{{Key: "resourceId", Value: "1"}}
	setAuthContext(c, 1, 10)

	h.DeleteResource(c)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	require.Contains(t, resourceRepo.resources, uint(1))
	require.Contains(t, resourceRepo.microsoft, uint(1))
}

func TestCoreHandler_DeleteMicrosoftResourceNonOwnerDenied(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, _, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	ms := &coredomain.MicrosoftResource{EmailAddress: "private-non-owner@example.com", Password: "secret"}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, ms))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("DELETE", "/v1/resources/1", nil)
	c.Params = []gin.Param{{Key: "resourceId", Value: "1"}}
	setAuthContext(c, 2, 10)

	h.DeleteResource(c)

	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	require.Contains(t, resourceRepo.resources, uint(1))
	require.Contains(t, resourceRepo.microsoft, uint(1))
}

func TestCoreHandler_CreateDomainDefaultsToLocalInboundServer(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, mailServerRepo, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/domains", strings.NewReader(`{"domain":"Example.COM"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	setAuthContext(c, 1, 10)

	h.PostDomain(c)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Len(t, resourceRepo.domains, 1)
	require.Equal(t, "example.com", resourceRepo.domains[1].Domain)
	require.Equal(t, coredomain.PurposeNotSale, resourceRepo.domains[1].Purpose)
	require.Equal(t, coredomain.DomainStatusAbnormal, resourceRepo.domains[1].Status)
	require.Equal(t, "mx.aishop6.com", mailServerRepo.servers[1].ServerAddress)
	require.Equal(t, "mx.aishop6.com", mailServerRepo.servers[1].MXRecord)
}

func TestCoreHandler_CreateDomainHidesForeignMailServerID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, _, mailServerRepo, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)
	require.NoError(t, mailServerRepo.Create(context.Background(), &coredomain.MailServer{
		OwnerUserID:   2,
		Name:          "other",
		ServerAddress: "mx.other.test",
		MXRecord:      "mx.other.test",
		Status:        coredomain.MailServerOnline,
	}))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/domains", strings.NewReader(`{"domain":"example.com","mailServerId":1}`))
	c.Request.Header.Set("Content-Type", "application/json")
	setAuthContext(c, 1, 10)

	h.PostDomain(c)

	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), "owner")
	require.NotContains(t, w.Body.String(), "mismatch")
}

func TestCoreHandler_CreateDomainRejectsDirectSalePurpose(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, _, _, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/domains", strings.NewReader(`{"domain":"example.com","purpose":"sale"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	setAuthContext(c, 1, 20)

	h.PostDomain(c)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
}

func TestCoreHandler_CreateDomainRejectsInvalidDomain(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, _, _, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/domains", strings.NewReader(`{"domain":"https://example.com/path"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	setAuthContext(c, 1, 10)

	h.PostDomain(c)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
}

func TestDomainUseCase_CreateRejectsBindingWithoutAllowBinding(t *testing.T) {
	mod, _, _, _ := setupCoreTestModule()

	_, err := mod.DomainUseCase.Create(context.Background(), 1, &coreapp.CreateDomainRequest{
		Domain:  "binding.example.com",
		Purpose: string(coredomain.PurposeBinding),
	})

	require.ErrorIs(t, err, coredomain.ErrForbiddenPurpose)
}

func TestCoreHandler_CreateDomainAllowsAdminBinding(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, _, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/domains", strings.NewReader(`{"domain":"binding.example.com","purpose":"binding"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	setAuthContext(c, 1, 80)

	h.PostDomain(c)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Len(t, resourceRepo.domains, 1)
	require.Equal(t, coredomain.PurposeBinding, resourceRepo.domains[1].Purpose)
}

func TestCoreHandler_DomainMailboxesOwnerAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, mailServerRepo, mailboxRepo := setupCoreTestModule()
	h := NewCoreHandler(mod)

	server := &coredomain.MailServer{OwnerUserID: 1, ServerAddress: "mail.example.com", Status: coredomain.MailServerOnline}
	if err := mailServerRepo.Create(context.Background(), server); err != nil {
		t.Fatalf("create mail server: %v", err)
	}
	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeDomain, OwnerUserID: 1}
	dr := &coredomain.MailDomainResource{
		Domain:       "example.com",
		MailServerID: server.ID,
		Purpose:      coredomain.PurposeSale,
		Status:       coredomain.DomainStatusNormal,
	}
	if err := resourceRepo.CreateDomain(context.Background(), root, dr); err != nil {
		t.Fatalf("create domain resource: %v", err)
	}
	mailboxRepo.mailboxes[1] = &coredomain.GeneratedMailbox{
		ID:          1,
		ResourceID:  dr.ID,
		OwnerUserID: 1,
		Email:       "box@example.com",
		Status:      coredomain.GeneratedMailboxNormal,
		CreatedAt:   time.Now(),
	}

	ownerW := httptest.NewRecorder()
	ownerCtx, _ := gin.CreateTestContext(ownerW)
	ownerCtx.Request = httptest.NewRequest("GET", "/v1/domains/1/mailboxes", nil)
	ownerCtx.Params = []gin.Param{{Key: "domainId", Value: "1"}}
	setAuthContext(ownerCtx, 1, 10)

	h.GetDomainMailboxes(ownerCtx)

	if ownerW.Code != http.StatusOK {
		t.Fatalf("expected 200 for owner, got %d: %s", ownerW.Code, ownerW.Body.String())
	}

	var ownerResp MailboxListResponse
	if err := json.Unmarshal(ownerW.Body.Bytes(), &ownerResp); err != nil {
		t.Fatalf("failed to parse owner response: %v", err)
	}
	if ownerResp.Total != 1 || len(ownerResp.Items) != 1 || ownerResp.Items[0].Email != "box@example.com" {
		t.Fatalf("unexpected owner mailbox response: %+v", ownerResp)
	}

	otherW := httptest.NewRecorder()
	otherCtx, _ := gin.CreateTestContext(otherW)
	otherCtx.Request = httptest.NewRequest("GET", "/v1/domains/1/mailboxes", nil)
	otherCtx.Params = []gin.Param{{Key: "domainId", Value: "1"}}
	setAuthContext(otherCtx, 2, 20)

	h.GetDomainMailboxes(otherCtx)

	if otherW.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for non-owner, got %d: %s", otherW.Code, otherW.Body.String())
	}
}

func TestCoreHandler_DomainMailboxesDeletedDomainHidden(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, mailServerRepo, mailboxRepo := setupCoreTestModule()
	h := NewCoreHandler(mod)

	server := &coredomain.MailServer{OwnerUserID: 1, ServerAddress: "mail.example.com", Status: coredomain.MailServerOnline}
	require.NoError(t, mailServerRepo.Create(context.Background(), server))
	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeDomain, OwnerUserID: 1}
	dr := &coredomain.MailDomainResource{
		Domain:       "deleted.example.com",
		MailServerID: server.ID,
		Purpose:      coredomain.PurposeNotSale,
		Status:       coredomain.DomainStatusDeleted,
	}
	require.NoError(t, resourceRepo.CreateDomain(context.Background(), root, dr))
	mailboxRepo.mailboxes[1] = &coredomain.GeneratedMailbox{
		ID:          1,
		ResourceID:  dr.ID,
		OwnerUserID: 1,
		Email:       "box@deleted.example.com",
		Status:      coredomain.GeneratedMailboxNormal,
		CreatedAt:   time.Now(),
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/v1/domains/1/mailboxes", nil)
	c.Params = []gin.Param{{Key: "domainId", Value: "1"}}
	setAuthContext(c, 1, 10)

	h.GetDomainMailboxes(c)

	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}
