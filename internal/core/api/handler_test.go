package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

type mockProjectRepo struct {
	details   map[uint]*coredomain.ProjectDetail
	summaries []coreapp.ProjectSummary
	nextID    uint
	logs      []*governancedomain.OperationLog
}

func newMockProjectRepo() *mockProjectRepo {
	return &mockProjectRepo{
		details: make(map[uint]*coredomain.ProjectDetail),
		nextID:  100,
	}
}

func (r *mockProjectRepo) CreateWithLog(_ context.Context, detail *coredomain.ProjectDetail, log *governancedomain.OperationLog) error {
	r.nextID++
	detail.Project.ID = r.nextID
	for i := range detail.Products {
		detail.Products[i].ID = uint(i + 1)
		detail.Products[i].ProjectID = detail.Project.ID
	}
	for i := range detail.MailRules {
		detail.MailRules[i].ID = uint(i + 1)
		detail.MailRules[i].ProjectID = detail.Project.ID
	}
	assignMockProjectAccesses(detail)
	r.details[detail.Project.ID] = detail
	r.summaries = append(r.summaries, coreapp.ProjectSummary{
		Project:       detail.Project,
		Products:      detail.Products,
		ProductCount:  len(detail.Products),
		MailRuleCount: len(detail.MailRules),
	})
	if log != nil {
		r.logs = append(r.logs, log)
	}
	return nil
}

func (r *mockProjectRepo) ResubmitWithLog(_ context.Context, applicantUserID uint, detail *coredomain.ProjectDetail, log *governancedomain.OperationLog) error {
	existing := r.details[detail.Project.ID]
	if existing == nil {
		return coredomain.ErrProjectNotFound
	}
	if existing.Project.ApplicantUserID == nil || *existing.Project.ApplicantUserID != applicantUserID {
		return coredomain.ErrForbiddenProject
	}
	if existing.Project.Status != coredomain.ProjectStatusDelisted {
		return coredomain.ErrInvalidProjectStatus
	}
	detail.Project.CreatedAt = existing.Project.CreatedAt
	for i := range detail.MailRules {
		detail.MailRules[i].ID = uint(i + 1)
		detail.MailRules[i].ProjectID = detail.Project.ID
	}
	detail.Products = existing.Products
	r.details[detail.Project.ID] = detail
	for i := range r.summaries {
		if r.summaries[i].Project.ID == detail.Project.ID {
			r.summaries[i] = coreapp.ProjectSummary{
				Project:       detail.Project,
				Products:      detail.Products,
				ProductCount:  len(detail.Products),
				MailRuleCount: len(detail.MailRules),
			}
			break
		}
	}
	if log != nil {
		r.logs = append(r.logs, log)
	}
	return nil
}

func (r *mockProjectRepo) UpdateWithLog(_ context.Context, detail *coredomain.ProjectDetail, log *governancedomain.OperationLog) error {
	existing := r.details[detail.Project.ID]
	if existing == nil {
		return coredomain.ErrProjectNotFound
	}
	if existing.Project.Status == coredomain.ProjectStatusReviewing {
		return coredomain.ErrInvalidProjectStatus
	}
	detail.Project.Status = existing.Project.Status
	detail.Project.ApplicantUserID = existing.Project.ApplicantUserID
	detail.Project.ReviewReason = existing.Project.ReviewReason
	detail.Project.CreatedAt = existing.Project.CreatedAt
	for i := range detail.Products {
		detail.Products[i].ID = uint(i + 1)
		detail.Products[i].ProjectID = detail.Project.ID
	}
	for i := range detail.MailRules {
		detail.MailRules[i].ID = uint(i + 1)
		detail.MailRules[i].ProjectID = detail.Project.ID
	}
	assignMockProjectAccesses(detail)
	r.details[detail.Project.ID] = detail
	r.upsertSummary(detail)
	if log != nil {
		r.logs = append(r.logs, log)
	}
	return nil
}

func (r *mockProjectRepo) ApproveWithConfigAndLog(_ context.Context, detail *coredomain.ProjectDetail, log *governancedomain.OperationLog) error {
	existing := r.details[detail.Project.ID]
	if existing == nil {
		return coredomain.ErrProjectNotFound
	}
	if existing.Project.Status != coredomain.ProjectStatusReviewing {
		return coredomain.ErrInvalidProjectStatus
	}
	detail.Project.Status = coredomain.ProjectStatusListed
	detail.Project.ApplicantUserID = existing.Project.ApplicantUserID
	detail.Project.ReviewReason = ""
	detail.Project.CreatedAt = existing.Project.CreatedAt
	for i := range detail.Products {
		detail.Products[i].ID = uint(i + 1)
		detail.Products[i].ProjectID = detail.Project.ID
	}
	for i := range detail.MailRules {
		detail.MailRules[i].ID = uint(i + 1)
		detail.MailRules[i].ProjectID = detail.Project.ID
	}
	assignMockProjectAccesses(detail)
	r.details[detail.Project.ID] = detail
	r.upsertSummary(detail)
	if log != nil {
		r.logs = append(r.logs, log)
	}
	return nil
}

func (r *mockProjectRepo) TransitionWithLog(_ context.Context, projectID uint, from coredomain.ProjectStatus, to coredomain.ProjectStatus, reviewReason string, log *governancedomain.OperationLog) (*coredomain.ProjectDetail, error) {
	detail := r.details[projectID]
	if detail == nil {
		return nil, coredomain.ErrProjectNotFound
	}
	if detail.Project.Status != from {
		return nil, coredomain.ErrInvalidProjectStatus
	}
	detail.Project.Status = to
	detail.Project.ReviewReason = reviewReason
	r.upsertSummary(detail)
	if log != nil {
		r.logs = append(r.logs, log)
	}
	return detail, nil
}

func (r *mockProjectRepo) DeleteWithLog(_ context.Context, projectID uint, log *governancedomain.OperationLog) error {
	detail := r.details[projectID]
	if detail == nil {
		return coredomain.ErrProjectNotFound
	}
	if detail.Project.Status == coredomain.ProjectStatusReviewing {
		return coredomain.ErrInvalidProjectStatus
	}
	delete(r.details, projectID)
	next := r.summaries[:0]
	for _, summary := range r.summaries {
		if summary.Project.ID != projectID {
			next = append(next, summary)
		}
	}
	r.summaries = next
	if log != nil {
		r.logs = append(r.logs, log)
	}
	return nil
}

func (r *mockProjectRepo) BulkTransitionWithLog(_ context.Context, filter coreapp.ProjectListFilter, from coredomain.ProjectStatus, to coredomain.ProjectStatus, log *governancedomain.OperationLog) (int, error) {
	affected := 0
	for _, detail := range r.details {
		if !mockProjectMatchesFilter(detail.Project, filter) || detail.Project.Status != from {
			continue
		}
		detail.Project.Status = to
		detail.Project.ReviewReason = ""
		r.upsertSummary(detail)
		affected++
	}
	if log != nil {
		r.logs = append(r.logs, log)
	}
	return affected, nil
}

func (r *mockProjectRepo) BulkDeleteWithLog(_ context.Context, filter coreapp.ProjectListFilter, log *governancedomain.OperationLog) (int, error) {
	affected := 0
	for projectID, detail := range r.details {
		if !mockProjectMatchesFilter(detail.Project, filter) || detail.Project.Status == coredomain.ProjectStatusReviewing {
			continue
		}
		delete(r.details, projectID)
		affected++
	}
	if affected > 0 {
		next := r.summaries[:0]
		for _, summary := range r.summaries {
			if _, ok := r.details[summary.Project.ID]; ok {
				next = append(next, summary)
			}
		}
		r.summaries = next
	}
	if log != nil {
		r.logs = append(r.logs, log)
	}
	return affected, nil
}

func (r *mockProjectRepo) ListAccesses(_ context.Context, projectID uint) ([]coredomain.ProjectAccess, error) {
	detail := r.details[projectID]
	if detail == nil {
		return nil, coredomain.ErrProjectNotFound
	}
	if detail.Project.AccessType != coredomain.ProjectAccessPrivate {
		return []coredomain.ProjectAccess{}, nil
	}
	return detail.Accesses, nil
}

func (r *mockProjectRepo) GrantAccessWithLog(_ context.Context, projectID, userID, grantedBy uint, log *governancedomain.OperationLog) (*coredomain.ProjectAccess, error) {
	detail := r.details[projectID]
	if detail == nil {
		return nil, coredomain.ErrProjectNotFound
	}
	if detail.Project.AccessType != coredomain.ProjectAccessPrivate {
		return nil, coredomain.ErrInvalidProject
	}
	for i := range detail.Accesses {
		if detail.Accesses[i].UserID == userID {
			detail.Accesses[i].GrantedBy = grantedBy
			if log != nil {
				r.logs = append(r.logs, log)
			}
			return &detail.Accesses[i], nil
		}
	}
	access := coredomain.ProjectAccess{
		ID:        uint(len(detail.Accesses) + 1),
		ProjectID: projectID,
		UserID:    userID,
		GrantedBy: grantedBy,
		CreatedAt: time.Now().UTC(),
	}
	detail.Accesses = append(detail.Accesses, access)
	if log != nil {
		r.logs = append(r.logs, log)
	}
	return &detail.Accesses[len(detail.Accesses)-1], nil
}

func (r *mockProjectRepo) RevokeAccessWithLog(_ context.Context, projectID, userID uint, log *governancedomain.OperationLog) error {
	detail := r.details[projectID]
	if detail == nil {
		return coredomain.ErrProjectNotFound
	}
	if detail.Project.AccessType != coredomain.ProjectAccessPrivate {
		return coredomain.ErrInvalidProject
	}
	next := detail.Accesses[:0]
	for _, access := range detail.Accesses {
		if access.UserID != userID {
			next = append(next, access)
		}
	}
	detail.Accesses = next
	if log != nil {
		r.logs = append(r.logs, log)
	}
	return nil
}

func (r *mockProjectRepo) upsertSummary(detail *coredomain.ProjectDetail) {
	summary := coreapp.ProjectSummary{
		Project:       detail.Project,
		Products:      detail.Products,
		ProductCount:  len(detail.Products),
		MailRuleCount: len(detail.MailRules),
	}
	for i := range r.summaries {
		if r.summaries[i].Project.ID == detail.Project.ID {
			r.summaries[i] = summary
			return
		}
	}
	r.summaries = append(r.summaries, summary)
}

func assignMockProjectAccesses(detail *coredomain.ProjectDetail) {
	if detail.Project.AccessType != coredomain.ProjectAccessPrivate {
		detail.Accesses = nil
		return
	}
	for i := range detail.Accesses {
		detail.Accesses[i].ID = uint(i + 1)
		detail.Accesses[i].ProjectID = detail.Project.ID
	}
}

func mockProjectMatchesFilter(project coredomain.Project, filter coreapp.ProjectListFilter) bool {
	if len(filter.IDs) > 0 {
		found := false
		for _, id := range filter.IDs {
			if id == project.ID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if filter.Status != "" && project.Status != filter.Status {
		return false
	}
	if filter.AccessType != "" && project.AccessType != filter.AccessType {
		return false
	}
	return true
}

func (r *mockProjectRepo) List(_ context.Context, _ coreapp.ProjectListFilter, offset, limit int) ([]coreapp.ProjectSummary, error) {
	if offset >= len(r.summaries) {
		return []coreapp.ProjectSummary{}, nil
	}
	end := offset + limit
	if end > len(r.summaries) {
		end = len(r.summaries)
	}
	return r.summaries[offset:end], nil
}

func (r *mockProjectRepo) Count(_ context.Context, _ coreapp.ProjectListFilter) (int64, error) {
	return int64(len(r.summaries)), nil
}

func (r *mockProjectRepo) Facets(_ context.Context, _ coreapp.ProjectListFilter) (*coreapp.ProjectListFacets, error) {
	facets := &coreapp.ProjectListFacets{}
	facets.Status.All = int64(len(r.summaries))
	facets.Access.All = int64(len(r.summaries))
	facets.Match.All = int64(len(r.summaries))
	facets.ProductType.All = int64(len(r.summaries))
	for _, summary := range r.summaries {
		switch summary.Project.Status {
		case coredomain.ProjectStatusListed:
			facets.Status.Listed++
		case coredomain.ProjectStatusReviewing:
			facets.Status.Reviewing++
		case coredomain.ProjectStatusDelisted:
			facets.Status.Delisted++
		}
		switch summary.Project.AccessType {
		case coredomain.ProjectAccessPublic:
			facets.Access.Public++
		case coredomain.ProjectAccessPrivate:
			facets.Access.Private++
		}
		if summary.Project.LooseMatch {
			facets.Match.Loose++
		} else {
			facets.Match.Strict++
		}
		seenTypes := map[coredomain.ProductType]struct{}{}
		for _, product := range summary.Products {
			seenTypes[product.Type] = struct{}{}
		}
		if _, ok := seenTypes[coredomain.ProductTypeMicrosoft]; ok {
			facets.ProductType.Microsoft++
		}
		if _, ok := seenTypes[coredomain.ProductTypeDomain]; ok {
			facets.ProductType.Domain++
		}
	}
	return facets, nil
}

func (r *mockProjectRepo) FindDetail(_ context.Context, projectID uint, _ uint, _ bool) (*coredomain.ProjectDetail, error) {
	return r.details[projectID], nil
}

func (r *mockResourceRepo) CreateMicrosoft(_ context.Context, resource *coredomain.EmailResource, ms *coredomain.MicrosoftResource) error {
	r.seq++
	resource.ID = r.seq
	resource.CreatedAt = time.Now()
	resource.UpdatedAt = time.Now()
	ms.ID = resource.ID
	if ms.ValidationGeneration == 0 {
		ms.ValidationGeneration = 1
	}
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
		existing.ValidationGeneration++
		existing.ValidationFailures = 0
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
	if dr.ValidationGeneration == 0 {
		dr.ValidationGeneration = 1
	}
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

func (r *mockResourceRepo) List(_ context.Context, ownerUserID uint, filter coreapp.ResourceListFilter, offset, limit int, _ uint) ([]coredomain.EmailResource, error) {
	return r.listResources(ownerUserID, filter, offset, limit), nil
}

func (r *mockResourceRepo) ListAll(_ context.Context, filter coreapp.ResourceListFilter, offset, limit int, _ uint) ([]coredomain.EmailResource, error) {
	return r.listResources(0, filter, offset, limit), nil
}

func (r *mockResourceRepo) Count(_ context.Context, ownerUserID uint, filter coreapp.ResourceListFilter) (int64, error) {
	return int64(len(r.listResources(ownerUserID, filter, 0, 0))), nil
}

func (r *mockResourceRepo) CountAll(_ context.Context, filter coreapp.ResourceListFilter) (int64, error) {
	return int64(len(r.listResources(0, filter, 0, 0))), nil
}

func (r *mockResourceRepo) Facets(_ context.Context, ownerUserID uint, filter coreapp.ResourceListFilter) (*coreapp.ResourceListFacets, error) {
	facets := &coreapp.ResourceListFacets{}
	statusBase := filter
	statusBase.Status = ""
	facets.Status.All = int64(len(r.listResources(ownerUserID, statusBase, 0, 0)))
	for _, status := range []string{"normal", "pending", "validating", "abnormal", "disabled"} {
		next := statusBase
		next.Status = status
		count := int64(len(r.listResources(ownerUserID, next, 0, 0)))
		switch status {
		case "normal":
			facets.Status.Normal = count
		case "pending":
			facets.Status.Pending = count
		case "validating":
			facets.Status.Validating = count
		case "abnormal":
			facets.Status.Abnormal = count
		case "disabled":
			facets.Status.Disabled = count
		}
	}
	if filter.ResourceType == coredomain.ResourceTypeMicrosoft {
		privateBase := filter
		privateBase.ForSale = nil
		facets.Private.All = int64(len(r.listResources(ownerUserID, privateBase, 0, 0)))
		privateYes := privateBase
		privateYes.ForSale = boolTestPtr(false)
		facets.Private.Yes = int64(len(r.listResources(ownerUserID, privateYes, 0, 0)))
		privateNo := privateBase
		privateNo.ForSale = boolTestPtr(true)
		facets.Private.No = int64(len(r.listResources(ownerUserID, privateNo, 0, 0)))

		longBase := filter
		longBase.LongLived = nil
		facets.LongLived.All = int64(len(r.listResources(ownerUserID, longBase, 0, 0)))
		longYes := longBase
		longYes.LongLived = boolTestPtr(true)
		facets.LongLived.Yes = int64(len(r.listResources(ownerUserID, longYes, 0, 0)))
		longNo := longBase
		longNo.LongLived = boolTestPtr(false)
		facets.LongLived.No = int64(len(r.listResources(ownerUserID, longNo, 0, 0)))

		graphBase := filter
		graphBase.GraphAvailable = nil
		facets.GraphAvailable.All = int64(len(r.listResources(ownerUserID, graphBase, 0, 0)))
		graphYes := graphBase
		graphYes.GraphAvailable = boolTestPtr(true)
		facets.GraphAvailable.Yes = int64(len(r.listResources(ownerUserID, graphYes, 0, 0)))
		graphNo := graphBase
		graphNo.GraphAvailable = boolTestPtr(false)
		facets.GraphAvailable.No = int64(len(r.listResources(ownerUserID, graphNo, 0, 0)))

		suffixBase := filter
		suffixBase.Suffix = ""
		facets.Suffixes = r.groupResourceFacets(ownerUserID, suffixBase, func(id uint) string {
			ms := r.microsoft[id]
			if ms == nil {
				return ""
			}
			return emailDomainForTest(ms.EmailAddress)
		})
	}
	if filter.ResourceType == coredomain.ResourceTypeDomain {
		privateBase := filter
		privateBase.Purpose = ""
		facets.Private.All = int64(len(r.listResources(ownerUserID, privateBase, 0, 0)))
		privateYes := privateBase
		privateYes.Purpose = string(coredomain.PurposeNotSale)
		facets.Private.Yes = int64(len(r.listResources(ownerUserID, privateYes, 0, 0)))
		privateNo := privateBase
		privateNo.Purpose = string(coredomain.PurposeSale)
		facets.Private.No = int64(len(r.listResources(ownerUserID, privateNo, 0, 0)))

		tldBase := filter
		tldBase.TLD = ""
		facets.TLDs = r.groupResourceFacets(ownerUserID, tldBase, func(id uint) string {
			dr := r.domains[id]
			if dr == nil {
				return ""
			}
			return coredomain.TLD(dr.Domain)
		})
	}
	return facets, nil
}

func (r *mockResourceRepo) listResources(ownerUserID uint, filter coreapp.ResourceListFilter, offset, limit int) []coredomain.EmailResource {
	var result []coredomain.EmailResource
	for _, res := range r.resources {
		if ownerUserID > 0 && res.OwnerUserID != ownerUserID {
			continue
		}
		if r.resourceMatchesFilter(res, filter) {
			result = append(result, *res)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID > result[j].ID
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	if offset < 0 {
		offset = 0
	}
	if offset >= len(result) {
		return nil
	}
	if limit <= 0 {
		return result[offset:]
	}
	end := offset + limit
	if end > len(result) {
		end = len(result)
	}
	return result[offset:end]
}

func (r *mockResourceRepo) groupResourceFacets(ownerUserID uint, filter coreapp.ResourceListFilter, keyOf func(uint) string) []coreapp.ResourceKeyFacet {
	counts := make(map[string]int64)
	for _, item := range r.listResources(ownerUserID, filter, 0, 0) {
		key := keyOf(item.ID)
		if key == "" {
			continue
		}
		counts[key]++
	}
	result := make([]coreapp.ResourceKeyFacet, 0, len(counts))
	for key, count := range counts {
		result = append(result, coreapp.ResourceKeyFacet{Key: key, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count == result[j].Count {
			return result[i].Key < result[j].Key
		}
		return result[i].Count > result[j].Count
	})
	return result
}

func (r *mockResourceRepo) resourceMatchesFilter(res *coredomain.EmailResource, filter coreapp.ResourceListFilter) bool {
	if filter.ResourceType != "" && filter.ResourceType != coredomain.ResourceType("all") && res.Type != filter.ResourceType {
		return false
	}
	if filter.CreatedFrom != nil && res.CreatedAt.Before(*filter.CreatedFrom) {
		return false
	}
	if filter.CreatedTo != nil && res.CreatedAt.After(*filter.CreatedTo) {
		return false
	}
	switch res.Type {
	case coredomain.ResourceTypeMicrosoft:
		ms := r.microsoft[res.ID]
		if ms == nil || ms.Status == coredomain.MicrosoftStatusDeleted {
			return false
		}
		if filter.ResourceType == "" || filter.ResourceType == coredomain.ResourceType("all") || filter.ResourceType == coredomain.ResourceTypeMicrosoft {
			if filter.Status != "" && string(ms.Status) != filter.Status {
				return false
			}
			if filter.ForSale != nil && ms.ForSale != *filter.ForSale {
				return false
			}
			if filter.LongLived != nil && ms.LongLived != *filter.LongLived {
				return false
			}
			if filter.GraphAvailable != nil && ms.GraphAvailable != *filter.GraphAvailable {
				return false
			}
			domain := emailDomainForTest(ms.EmailAddress)
			if filter.Suffix != "" && domain != filter.Suffix {
				return false
			}
			if filter.Search != "" && !strings.Contains(strings.ToLower(ms.EmailAddress), filter.Search) && !strings.Contains(domain, filter.Search) {
				return false
			}
			return true
		}
	case coredomain.ResourceTypeDomain:
		dr := r.domains[res.ID]
		if dr == nil || dr.Status == coredomain.DomainStatusDeleted {
			return false
		}
		if filter.ExcludeBinding && dr.Purpose == coredomain.PurposeBinding {
			return false
		}
		if filter.ResourceType == "" || filter.ResourceType == coredomain.ResourceType("all") || filter.ResourceType == coredomain.ResourceTypeDomain {
			if filter.Status != "" && string(dr.Status) != filter.Status {
				return false
			}
			if filter.Purpose != "" && string(dr.Purpose) != filter.Purpose {
				return false
			}
			if filter.MailServerID != 0 && dr.MailServerID != filter.MailServerID {
				return false
			}
			tld := coredomain.TLD(dr.Domain)
			if filter.TLD != "" && tld != filter.TLD {
				return false
			}
			if filter.Search != "" && !strings.Contains(strings.ToLower(dr.Domain), filter.Search) && !strings.Contains(tld, filter.Search) {
				return false
			}
			return true
		}
	}
	return false
}

func emailDomainForTest(email string) string {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(email)), "@")
	if len(parts) != 2 {
		return ""
	}
	return parts[1]
}

func boolTestPtr(value bool) *bool {
	return &value
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
			if filter.GraphAvailable != nil && ms.GraphAvailable != *filter.GraphAvailable {
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

func (r *mockResourceRepo) ListMicrosoftStatus(_ context.Context, ids []uint) ([]coreapp.MicrosoftStatusResult, error) {
	var result []coreapp.MicrosoftStatusResult
	for _, id := range ids {
		if ms, ok := r.microsoft[id]; ok {
			result = append(result, coreapp.MicrosoftStatusResult{
				ID:             ms.ID,
				EmailAddress:   ms.EmailAddress,
				ForSale:        ms.ForSale,
				LongLived:      ms.LongLived,
				GraphAvailable: ms.GraphAvailable,
				Status:         string(ms.Status),
				LastSafeError:  ms.LastSafeError,
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
				ID:              dr.ID,
				Domain:          dr.Domain,
				DomainTLD:       coredomain.TLD(dr.Domain),
				MailServerID:    dr.MailServerID,
				Purpose:         string(dr.Purpose),
				Status:          string(dr.Status),
				MailboxCount:    0,
				LastAllocatedAt: dr.LastAllocatedAt,
				UpdatedAt:       dr.UpdatedAt,
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

func (r *mockGeneratedMailboxRepo) DisableWithLog(_ context.Context, mailboxID uint, _ *governancedomain.OperationLog) error {
	mailbox := r.mailboxes[mailboxID]
	if mailbox == nil {
		return coredomain.ErrResourceNotFound
	}
	if mailbox.Status != coredomain.GeneratedMailboxNormal && mailbox.Status != coredomain.GeneratedMailboxDisabled {
		return coredomain.ErrInvalidResourceStatus
	}
	mailbox.Status = coredomain.GeneratedMailboxDisabled
	return nil
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
	if item.Generation == 0 {
		item.Generation = 1
	}
	if item.DispatchStatus == "" || item.DispatchStatus == "queued" {
		item.DispatchStatus = "pending"
	}
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

func (r *mockImportRepo) ClaimAdminImportDispatchable(_ context.Context, limit int, _ time.Time, _ time.Time) ([]coreapp.AdminResourceImportDispatchItem, error) {
	items := make([]coreapp.AdminResourceImportDispatchItem, 0)
	for id := uint(1); id <= r.seq && len(items) < limit; id++ {
		item := r.imports[id]
		if item == nil || item.Status != coredomain.ResourceImportProcessing || item.DispatchStatus != "pending" {
			continue
		}
		items = append(items, coreapp.AdminResourceImportDispatchItem{
			ImportID: item.ID, OwnerUserID: item.OwnerUserID, LongLived: item.LongLived,
			ErrorStrategy: item.ErrorStrategy, RequestID: item.RequestID,
			Generation: item.Generation,
		})
	}
	return items, nil
}

func (r *mockImportRepo) MarkAdminImportDispatched(_ context.Context, id uint, generation uint64) (bool, error) {
	item := r.imports[id]
	if item == nil || item.DispatchStatus != "pending" || item.Generation != generation {
		return false, nil
	}
	item.DispatchStatus = "queued"
	return true, nil
}

func (r *mockImportRepo) MarkAdminImportRunning(_ context.Context, id uint, generation uint64) (string, bool, error) {
	item := r.imports[id]
	if item == nil || (item.DispatchStatus != "pending" && item.DispatchStatus != "queued") || item.Generation != generation {
		return "", false, nil
	}
	item.DispatchStatus = "running"
	item.ClaimToken = fmt.Sprintf("claim-%d", id)
	return item.ClaimToken, true, nil
}

func (r *mockImportRepo) MarkAdminImportPending(_ context.Context, id uint, generation uint64, safeError string) error {
	item := r.imports[id]
	if item != nil && item.Generation == generation && (item.DispatchStatus == "queued" || item.DispatchStatus == "running") {
		item.DispatchStatus = "pending"
		item.Generation++
		item.ClaimToken = ""
		item.LastSafeError = safeError
	}
	return nil
}

func (r *mockImportRepo) MarkAdminImportFailed(_ context.Context, id uint, claimToken, failureObjectKey, safeError string) error {
	item := r.imports[id]
	if item == nil || item.DispatchStatus != "running" || item.ClaimToken != claimToken {
		return coredomain.ErrResourceImportInvalidClaim
	}
	item.Attempts++
	item.Generation++
	item.DispatchStatus = "pending"
	item.ClaimToken = ""
	item.FailureObjectKey = failureObjectKey
	item.LastSafeError = safeError
	if item.Attempts >= item.MaxAttempts {
		item.Status = coredomain.ResourceImportFailed
		item.DispatchStatus = "failed"
	}
	return nil
}

func (r *mockImportRepo) CreateMicrosoftResourcesAndMarkSucceeded(ctx context.Context, id uint, _ string, _ []coredomain.MicrosoftImportLine, resources []coredomain.EmailResource, ms []coredomain.MicrosoftResource, _ []coreapp.AdminResourceImportSkippedItem, failureObjectKey string, safeSummary string, afterCreate func(context.Context, []coredomain.MicrosoftResource, []uint) error) ([]uint, error) {
	item := r.imports[id]
	if item == nil {
		return nil, coredomain.ErrResourceNotFound
	}
	if item.Status != coredomain.ResourceImportProcessing {
		return nil, nil
	}
	if err := r.resources.createMicrosoftBatch(ctx, resources, ms); err != nil {
		return nil, err
	}
	ids := make([]uint, 0, len(ms))
	for _, row := range r.resources.microsoft {
		for _, imported := range ms {
			if row.EmailAddress == imported.EmailAddress {
				ids = append(ids, row.ID)
				break
			}
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if afterCreate != nil {
		if err := afterCreate(ctx, ms, ids); err != nil {
			return nil, err
		}
	}
	item.Status = coredomain.ResourceImportImported
	item.DispatchStatus = "succeeded"
	item.ClaimToken = ""
	item.ImportedCount = len(ms)
	item.FailureObjectKey = failureObjectKey
	item.LastSafeError = safeSummary
	item.UpdatedAt = time.Now()
	return ids, nil
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

func (s *mockFileStore) SavePrivateStream(_ context.Context, file governancedomain.PrivateFileStream) (*governancedomain.StoredPrivateFile, error) {
	content, err := io.ReadAll(file.Content)
	if err != nil {
		return nil, err
	}
	s.files[file.ObjectKey] = governancedomain.PrivateFile{
		ObjectKey:    file.ObjectKey,
		FileName:     file.FileName,
		ContentType:  file.ContentType,
		ContentBytes: content,
	}
	return &governancedomain.StoredPrivateFile{
		ObjectKey:   file.ObjectKey,
		FileName:    file.FileName,
		ContentType: file.ContentType,
		Size:        int64(len(content)),
	}, nil
}

func (s *mockFileStore) ReadPrivate(_ context.Context, objectKey string) (*governancedomain.PrivateFile, error) {
	file := s.files[objectKey]
	return &file, nil
}

func (s *mockFileStore) DeletePrivate(_ context.Context, objectKey string) error {
	delete(s.files, objectKey)
	return nil
}

func (s *mockFileStore) ListPrivate(_ context.Context, prefix string, startAfter string, limit int) ([]governancedomain.PrivateObject, error) {
	items := make([]governancedomain.PrivateObject, 0)
	for objectKey := range s.files {
		if strings.HasPrefix(objectKey, prefix) && (startAfter == "" || objectKey > startAfter) {
			items = append(items, governancedomain.PrivateObject{ObjectKey: objectKey})
		}
	}
	if limit > 0 && len(items) > limit {
		return items[:limit], nil
	}
	return items, nil
}

type mockImportQueue struct {
	tasks     []coreapp.MicrosoftImportTask
	duplicate bool
	err       error
}

func (q *mockImportQueue) EnqueueMicrosoftImport(_ context.Context, task coreapp.MicrosoftImportTask) (bool, error) {
	if q.err != nil {
		return false, q.err
	}
	if q.duplicate {
		return false, nil
	}
	q.tasks = append(q.tasks, task)
	return true, nil
}

type mockValidationRepo struct {
	resources               *mockResourceRepo
	batches                 []coreapp.ResourceValidationBatchTask
	appliedMicrosoftResults []coreapp.MicrosoftValidationResult
	markDispatchedErr       error
	applyMicrosoftErr       error
}

func newMockValidationRepo(resources *mockResourceRepo) *mockValidationRepo {
	return &mockValidationRepo{resources: resources}
}

func (r *mockValidationRepo) MarkResourcePendingWithLog(_ context.Context, resourceID uint, resourceType coredomain.ResourceType, _ uint, _ *governancedomain.OperationLog) error {
	if r.resources == nil {
		return nil
	}
	switch resourceType {
	case coredomain.ResourceTypeMicrosoft:
		resource := r.resources.microsoft[resourceID]
		if resource == nil || resource.Status == coredomain.MicrosoftStatusDeleted {
			return coredomain.ErrResourceNotFound
		}
		if resource.Status == coredomain.MicrosoftStatusDisabled {
			return coredomain.ErrInvalidResourceStatus
		}
		resource.Status = coredomain.MicrosoftStatusPending
		resource.ValidationGeneration++
		resource.ValidationFailures = 0
		resource.LastSafeError = ""
	case coredomain.ResourceTypeDomain:
		resource := r.resources.domains[resourceID]
		if resource == nil || resource.Status == coredomain.DomainStatusDeleted {
			return coredomain.ErrResourceNotFound
		}
		if resource.Status == coredomain.DomainStatusDisabled {
			return coredomain.ErrInvalidResourceStatus
		}
		resource.Status = coredomain.DomainStatusPending
		resource.ValidationGeneration++
		resource.ValidationFailures = 0
		resource.LastSafeError = ""
	}
	return nil
}

func (r *mockValidationRepo) MarkValidationBatchPending(_ context.Context, task coreapp.ResourceValidationBatchTask, _ int) (*coreapp.ResourceValidationBatchPageResult, error) {
	r.batches = append(r.batches, task)
	return &coreapp.ResourceValidationBatchPageResult{Processed: len(task.Selection.ResourceIDs), Done: true}, nil
}

func (r *mockValidationRepo) ClaimPendingValidations(_ context.Context, limit int) ([]coreapp.ResourceValidationTask, error) {
	result := make([]coreapp.ResourceValidationTask, 0)
	if r.resources == nil {
		return result, nil
	}
	for id, resource := range r.resources.microsoft {
		if resource.Status != coredomain.MicrosoftStatusPending {
			continue
		}
		root := r.resources.resources[id]
		result = append(result, coreapp.ResourceValidationTask{
			ResourceID: id, ResourceType: coredomain.ResourceTypeMicrosoft,
			OwnerUserID: root.OwnerUserID, ValidationGeneration: resource.ValidationGeneration,
			ExpectedCredentialRevision: resource.CredentialRevision,
		})
		if limit > 0 && len(result) >= limit {
			return result, nil
		}
	}
	for id, resource := range r.resources.domains {
		if resource.Status != coredomain.DomainStatusPending {
			continue
		}
		root := r.resources.resources[id]
		result = append(result, coreapp.ResourceValidationTask{ResourceID: id, ResourceType: coredomain.ResourceTypeDomain, OwnerUserID: root.OwnerUserID, ValidationGeneration: resource.ValidationGeneration})
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (r *mockValidationRepo) MarkValidationDispatched(_ context.Context, task coreapp.ResourceValidationTask) (bool, error) {
	if r.markDispatchedErr != nil {
		return false, r.markDispatchedErr
	}
	if r.resources == nil {
		return false, nil
	}
	switch task.ResourceType {
	case coredomain.ResourceTypeMicrosoft:
		resource := r.resources.microsoft[task.ResourceID]
		if resource == nil || resource.Status != coredomain.MicrosoftStatusPending || resource.ValidationGeneration != task.ValidationGeneration || resource.CredentialRevision != task.ExpectedCredentialRevision {
			return false, nil
		}
		resource.Status = coredomain.MicrosoftStatusValidating
	case coredomain.ResourceTypeDomain:
		resource := r.resources.domains[task.ResourceID]
		if resource == nil || resource.Status != coredomain.DomainStatusPending || resource.ValidationGeneration != task.ValidationGeneration {
			return false, nil
		}
		resource.Status = coredomain.DomainStatusValidating
	default:
		return false, coredomain.ErrInvalidResourceType
	}
	return true, nil
}

func (r *mockValidationRepo) ReleaseValidation(_ context.Context, task coreapp.ResourceValidationTask) error {
	if r.resources == nil {
		return nil
	}
	switch task.ResourceType {
	case coredomain.ResourceTypeMicrosoft:
		if resource := r.resources.microsoft[task.ResourceID]; resource != nil && resource.Status == coredomain.MicrosoftStatusValidating && resource.ValidationGeneration == task.ValidationGeneration && resource.CredentialRevision == task.ExpectedCredentialRevision {
			resource.Status = coredomain.MicrosoftStatusPending
			resource.ValidationGeneration++
		}
	case coredomain.ResourceTypeDomain:
		if resource := r.resources.domains[task.ResourceID]; resource != nil && resource.Status == coredomain.DomainStatusValidating && resource.ValidationGeneration == task.ValidationGeneration {
			resource.Status = coredomain.DomainStatusPending
			resource.ValidationGeneration++
		}
	}
	return nil
}

func (r *mockValidationRepo) ApplyMicrosoftResult(_ context.Context, task coreapp.ResourceValidationTask, result coreapp.MicrosoftValidationResult, _ *governancedomain.SystemLog) error {
	if r.applyMicrosoftErr != nil {
		return r.applyMicrosoftErr
	}
	resource := r.resources.microsoft[task.ResourceID]
	if resource == nil || resource.Status != coredomain.MicrosoftStatusValidating || resource.ValidationGeneration != task.ValidationGeneration || resource.CredentialRevision != task.ExpectedCredentialRevision {
		return coreapp.ErrValidationResultStale
	}
	r.appliedMicrosoftResults = append(r.appliedMicrosoftResults, result)
	if result.Valid || result.CredentialsAuthoritative {
		if value := strings.TrimSpace(result.ClientID); value != "" {
			resource.ClientID = value
		}
		if value := strings.TrimSpace(result.RefreshToken); value != "" {
			resource.RefreshToken = value
		}
	}
	if result.Valid {
		resource.Status = coredomain.MicrosoftStatusNormal
		resource.ValidationFailures = 0
		resource.LastSafeError = ""
		resource.QualityScore = 100
		resource.GraphAvailable = result.GraphAvailable
	} else {
		resource.ValidationFailures++
		if result.Retryable && resource.ValidationFailures < coreapp.ResourceValidationMaxFailures {
			resource.Status = coredomain.MicrosoftStatusPending
			resource.ValidationGeneration++
		} else {
			resource.Status = coredomain.MicrosoftStatusAbnormal
		}
		resource.LastSafeError = result.SafeMessage
		resource.QualityScore = 0
		resource.GraphAvailable = false
	}
	return nil
}

func (r *mockValidationRepo) ApplyDomainResult(_ context.Context, task coreapp.ResourceValidationTask, result coreapp.DomainValidationResult, _ *governancedomain.SystemLog) error {
	resource := r.resources.domains[task.ResourceID]
	if resource == nil || resource.Status != coredomain.DomainStatusValidating || resource.ValidationGeneration != task.ValidationGeneration {
		return coreapp.ErrValidationResultStale
	}
	if result.Valid {
		resource.Status = coredomain.DomainStatusNormal
		resource.ValidationFailures = 0
		resource.LastSafeError = ""
	} else {
		resource.ValidationFailures++
		if result.Retryable && resource.ValidationFailures < coreapp.ResourceValidationMaxFailures {
			resource.Status = coredomain.DomainStatusPending
			resource.ValidationGeneration++
		} else {
			resource.Status = coredomain.DomainStatusAbnormal
		}
		resource.LastSafeError = result.SafeMessage
	}
	return nil
}

func mockValidationTask(repo *mockValidationRepo, resourceID uint) coreapp.ResourceValidationTask {
	root := repo.resources.resources[resourceID]
	if resource := repo.resources.microsoft[resourceID]; resource != nil {
		resource.Status = coredomain.MicrosoftStatusValidating
		return coreapp.ResourceValidationTask{
			ResourceID: resourceID, ResourceType: coredomain.ResourceTypeMicrosoft,
			OwnerUserID: root.OwnerUserID, ValidationGeneration: resource.ValidationGeneration,
			ExpectedCredentialRevision: resource.CredentialRevision,
		}
	}
	if resource := repo.resources.domains[resourceID]; resource != nil {
		resource.Status = coredomain.DomainStatusValidating
		return coreapp.ResourceValidationTask{ResourceID: resourceID, ResourceType: coredomain.ResourceTypeDomain, OwnerUserID: root.OwnerUserID, ValidationGeneration: resource.ValidationGeneration}
	}
	return coreapp.ResourceValidationTask{ResourceID: resourceID}
}

type mockValidationQueue struct {
	tasks       []coreapp.ResourceValidationTask
	batches     []coreapp.ResourceValidationBatchTask
	dispatchers int
	enqueueErr  error
	duplicate   bool
}

func (q *mockValidationQueue) EnqueueResourceValidation(_ context.Context, task coreapp.ResourceValidationTask) (bool, error) {
	if q.enqueueErr != nil {
		return false, q.enqueueErr
	}
	if q.duplicate {
		return false, nil
	}
	q.tasks = append(q.tasks, task)
	return true, nil
}

func (q *mockValidationQueue) EnqueueResourceValidationBatch(_ context.Context, task coreapp.ResourceValidationBatchTask) error {
	if q.enqueueErr != nil {
		return q.enqueueErr
	}
	q.batches = append(q.batches, task)
	return nil
}

func (q *mockValidationQueue) EnqueueResourceValidationDispatcher(_ context.Context, _ time.Duration) error {
	q.dispatchers++
	return nil
}

type mockResourceValidator struct {
	msResult     coreapp.MicrosoftValidationResult
	msErr        error
	domainResult coreapp.DomainValidationResult
	domainErr    error
}

type validationAliasTrigger struct {
	resourceIDs []uint
	err         error
}

func (t *validationAliasTrigger) EnsureForValidatedMicrosoftResource(_ context.Context, resourceID uint) error {
	t.resourceIDs = append(t.resourceIDs, resourceID)
	return t.err
}

type validationHistoryTrigger struct {
	tasks []struct {
		resourceID uint
		requestID  string
	}
	err error
}

func (t *validationHistoryTrigger) ScheduleValidatedMicrosoftHistory(_ context.Context, resourceID uint, requestID string) error {
	t.tasks = append(t.tasks, struct {
		resourceID uint
		requestID  string
	}{resourceID: resourceID, requestID: requestID})
	return t.err
}

func (v mockResourceValidator) ValidateMicrosoft(_ context.Context, _ coreapp.MicrosoftValidationRequest) (coreapp.MicrosoftValidationResult, error) {
	if v.msErr != nil {
		return v.msResult, v.msErr
	}
	if v.msResult.SafeMessage != "" || v.msResult.Valid || v.msResult.ClientID != "" || v.msResult.RefreshToken != "" {
		return v.msResult, nil
	}
	return coreapp.MicrosoftValidationResult{Valid: true, ClientID: "client-id", RefreshToken: "refresh-token"}, nil
}

func (v mockResourceValidator) ValidateDomain(_ context.Context, _ coreapp.DomainValidationRequest) (coreapp.DomainValidationResult, error) {
	if v.domainErr != nil {
		return coreapp.DomainValidationResult{}, v.domainErr
	}
	if v.domainResult.SafeMessage != "" || v.domainResult.Valid || v.domainResult.Category != "" {
		return v.domainResult, nil
	}
	return coreapp.DomainValidationResult{Valid: true}, nil
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
	validationRepo := newMockValidationRepo(resourceRepo)
	validationQueue := &mockValidationQueue{}
	mailServerRepo := newMockMailServerRepo()
	mailboxRepo := newMockGeneratedMailboxRepo()
	projectRepo := newMockProjectRepo()
	fileStore := newMockFileStore()

	mod := &CoreModule{
		ImportUseCase:     coreapp.NewImportUseCase(resourceRepo, importRepo, txtParser, fileStore, importQueue),
		ResourceUseCase:   coreapp.NewResourceUseCase(resourceRepo),
		ValidationUseCase: coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, validationQueue, mockResourceValidator{}),
		DomainUseCase:     coreapp.NewDomainUseCase(resourceRepo, mailServerRepo, mailboxRepo),
		ServerUseCase:     coreapp.NewServerUseCase(mailServerRepo),
		MailboxUseCase:    coreapp.NewDomainMailboxUseCase(mailboxRepo, resourceRepo),
		ProjectUseCase:    coreapp.NewProjectUseCase(projectRepo),
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

func setAuthContext(c *gin.Context, userID uint, role iamdomain.Role) {
	middleware.SetCurrentUser(c, userID, role, "test@example.com", "test-session-id")
}

type mockPermissionChecker struct {
	allowed bool
	err     error
}

func (m mockPermissionChecker) Check(_ context.Context, _ uint, _ iamdomain.Role, _, _ string) (bool, error) {
	return m.allowed, m.err
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
			setAuthContext(c, 1, iamdomain.RoleUser) // role=user (10), below supplier (20)

			tt.call(h, c)

			if w.Code != http.StatusForbidden {
				t.Errorf("expected 403, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestImportDispatcherChangesStateOnlyAfterAcceptedEnqueue(t *testing.T) {
	resources := newMockResourceRepo()
	imports := newMockImportRepo(resources)
	item := &coredomain.ResourceImport{
		OwnerUserID: 1, ResourceType: coredomain.ResourceTypeMicrosoft,
		Status: coredomain.ResourceImportProcessing, DispatchStatus: "pending", Generation: 1,
	}
	require.NoError(t, imports.Create(context.Background(), item))
	queue := &mockImportQueue{err: errors.New("redis unavailable")}
	useCase := coreapp.NewImportUseCase(resources, imports, nil, nil, queue)

	queued, err := useCase.DispatchAdminImports(context.Background(), 10)
	require.Error(t, err)
	require.Zero(t, queued)
	require.Equal(t, "pending", imports.imports[item.ID].DispatchStatus)

	queue.err = nil
	queue.duplicate = true
	queued, err = useCase.DispatchAdminImports(context.Background(), 10)
	require.NoError(t, err)
	require.Zero(t, queued)
	require.Equal(t, "pending", imports.imports[item.ID].DispatchStatus)

	queue.duplicate = false
	queued, err = useCase.DispatchAdminImports(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 1, queued)
	require.Equal(t, "queued", imports.imports[item.ID].DispatchStatus)
	require.Equal(t, uint64(1), queue.tasks[0].Generation)
}

func TestImportWorkerActivatesPendingGeneration(t *testing.T) {
	resources := newMockResourceRepo()
	imports := newMockImportRepo(resources)
	files := newMockFileStore()
	const sourceObjectKey = "imports/microsoft/source/pending-generation.txt"
	files.files[sourceObjectKey] = governancedomain.PrivateFile{
		ObjectKey: sourceObjectKey, ContentBytes: []byte("pending@example.com----pass123"),
	}
	item := &coredomain.ResourceImport{
		OwnerUserID: 1, ResourceType: coredomain.ResourceTypeMicrosoft,
		SourceObjectKey: sourceObjectKey, Status: coredomain.ResourceImportProcessing,
		DispatchStatus: "pending", Generation: 2, ErrorStrategy: coredomain.ImportErrorStrategySkip,
	}
	require.NoError(t, imports.Create(context.Background(), item))
	useCase := coreapp.NewImportUseCase(resources, imports, &mockTXTParser{}, files, nil)

	result, err := useCase.ProcessMicrosoftImport(context.Background(), coreapp.MicrosoftImportTask{
		ImportID: item.ID, OwnerUserID: 1, Generation: 2, ErrorStrategy: coredomain.ImportErrorStrategySkip,
	})
	require.NoError(t, err)
	require.Equal(t, 1, result.Imported)
	require.Equal(t, "succeeded", imports.imports[item.ID].DispatchStatus)
	require.Equal(t, coredomain.ResourceImportImported, imports.imports[item.ID].Status)
	require.Zero(t, imports.imports[item.ID].Attempts)
}

func TestCoreHandler_ImportSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, _, _, importQueue, _, _ := setupCoreTestModuleWithImportMocks()
	h := NewCoreHandler(mod)

	body, contentType := multipartImportBody(t, "resources.txt", "user@outlook.com----pass123\nuser2@hotmail.com----pass456----aux@example.net")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/resources/imports", body)
	c.Request.Header.Set("Content-Type", contentType)
	setAuthContext(c, 1, iamdomain.RoleUser) // regular users can import private Microsoft resources

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
	result, err := mod.ImportUseCase.ProcessMicrosoftImport(context.Background(), importQueue.tasks[0])
	require.NoError(t, err)
	require.Len(t, result.ImportedResourceIDs, 2)
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
	setAuthContext(c, 1, iamdomain.RoleUser)

	h.PostResourceImport(c)

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	require.Len(t, importQueue.tasks, 1)
	result, err := mod.ImportUseCase.ProcessMicrosoftImport(context.Background(), importQueue.tasks[0])
	require.NoError(t, err)
	require.Empty(t, result.ImportedResourceIDs)
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
	setAuthContext(c, 1, iamdomain.RoleUser)

	h.PostResourceImport(c)

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	require.Len(t, importQueue.tasks, 1)
	for attempt := 1; attempt <= 3; attempt++ {
		task := importQueue.tasks[len(importQueue.tasks)-1]
		require.Equal(t, coredomain.ImportErrorStrategyAbort, task.ErrorStrategy)
		result, err := mod.ImportUseCase.ProcessMicrosoftImport(context.Background(), task)
		require.NoError(t, err)
		require.Empty(t, result.ImportedResourceIDs)
		if attempt < 3 {
			require.Equal(t, coredomain.ResourceImportProcessing, importRepo.imports[task.ImportID].Status)
			require.Equal(t, "pending", importRepo.imports[task.ImportID].DispatchStatus)
			_, err = mod.ImportUseCase.DispatchAdminImports(context.Background(), 10)
			require.NoError(t, err)
		}
	}
	require.Equal(t, coredomain.ResourceImportFailed, importRepo.imports[importQueue.tasks[0].ImportID].Status)
	require.Equal(t, 3, importRepo.imports[importQueue.tasks[0].ImportID].Attempts)
}

func TestCoreHandler_ImportDuplicateDefaultSkips(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, _, _, importQueue, importRepo, _ := setupCoreTestModuleWithImportMocks()
	h := NewCoreHandler(mod)
	require.NoError(t, resourceRepo.CreateMicrosoft(
		context.Background(),
		&coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1},
		&coredomain.MicrosoftResource{
			EmailAddress: "duplicate@outlook.com",
			Password:     "secret",
			Status:       coredomain.MicrosoftStatusPending,
		},
	))

	body, contentType := multipartImportBody(t, "resources.txt", "duplicate@outlook.com----pass123\nnew@outlook.com----pass456")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/resources/imports", body)
	c.Request.Header.Set("Content-Type", contentType)
	setAuthContext(c, 1, iamdomain.RoleUser)

	h.PostResourceImport(c)

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	require.Len(t, importQueue.tasks, 1)
	result, err := mod.ImportUseCase.ProcessMicrosoftImport(context.Background(), importQueue.tasks[0])
	require.NoError(t, err)
	require.Len(t, result.ImportedResourceIDs, 1)
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
	setAuthContext(c, 1, iamdomain.RoleUser)

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
	setAuthContext(c, 2, iamdomain.RoleUser)

	h.GetResourceDetail(c)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for non-owner, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCoreHandler_ValidateMarksPendingAndReturnsAcceptance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mod, resourceRepo, _, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &coredomain.MicrosoftResource{EmailAddress: "test@example.com", Password: "secret", Status: coredomain.MicrosoftStatusNormal}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, resource))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/resources/1/validate", nil)
	c.Params = []gin.Param{{Key: "resourceId", Value: "1"}}
	setAuthContext(c, 1, iamdomain.RoleUser)
	h.PostResourceValidate(c)

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	var response ResourceValidationsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, ResourceValidationsResponse{Requested: 1, Queued: 1}, response)
	require.Equal(t, coredomain.MicrosoftStatusPending, resourceRepo.microsoft[root.ID].Status)
	require.NotContains(t, w.Body.String(), "validationId")
}

func TestResourceValidationUseCase_CreateBatchEnqueuesOnlyRedisCursor(t *testing.T) {
	resourceRepo := newMockResourceRepo()
	validationRepo := newMockValidationRepo(resourceRepo)
	queue := &mockValidationQueue{}
	uc := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, queue, mockResourceValidator{})

	result, err := uc.CreateBatch(context.Background(), coreapp.ResourceBulkSelection{
		Mode: coreapp.ResourceBulkSelectionIDs, ResourceIDs: []uint{3, 2, 2},
	}, 1, false, "request-batch", "/v1/resources/validations")
	require.NoError(t, err)
	require.Equal(t, &coreapp.ResourceBatchValidationResult{Requested: 2, Queued: 2}, result)
	require.Len(t, queue.batches, 1)
	require.Equal(t, []uint{2, 3}, queue.batches[0].Selection.ResourceIDs)
	require.Empty(t, queue.tasks)
}

func TestResourceValidationUseCase_CreateBatchRejectsTooManyExplicitIDs(t *testing.T) {
	uc := coreapp.NewResourceValidationUseCase(newMockResourceRepo(), newMockValidationRepo(nil), &mockValidationQueue{}, mockResourceValidator{})
	_, err := uc.CreateBatch(context.Background(), coreapp.ResourceBulkSelection{
		Mode: coreapp.ResourceBulkSelectionIDs, ResourceIDs: make([]uint, coreapp.ResourceValidationMaxExplicitIDs+1),
	}, 1, false, "req-too-large", "/v1/resources/validations")
	require.ErrorIs(t, err, coredomain.ErrResourceSelectionTooLarge)
}

func TestResourceValidationUseCase_NonAdminBatchExcludesBindingDomains(t *testing.T) {
	queue := &mockValidationQueue{}
	uc := coreapp.NewResourceValidationUseCase(newMockResourceRepo(), newMockValidationRepo(nil), queue, mockResourceValidator{})
	_, err := uc.CreateBatch(context.Background(), coreapp.ResourceBulkSelection{
		Mode:   coreapp.ResourceBulkSelectionFilter,
		Filter: coreapp.ResourceBulkFilter{ResourceType: coredomain.ResourceTypeDomain},
	}, 1, false, "req-binding-validation-batch", "/v1/resources/validations")
	require.NoError(t, err)
	require.Len(t, queue.batches, 1)
	require.True(t, queue.batches[0].Selection.Filter.ExcludeBinding)
}

func TestQueueMicrosoftImportValidationsWakesDispatcher(t *testing.T) {
	mod, resourceRepo, _, _, _, _, _ := setupCoreTestModuleWithImportMocks()
	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, &coredomain.MicrosoftResource{
		EmailAddress: "import@example.com", Password: "secret", Status: coredomain.MicrosoftStatusPending,
	}))
	queued, err := queueMicrosoftImportValidations(context.Background(), mod, &coreapp.MicrosoftImportProcessResult{ImportedResourceIDs: []uint{root.ID}})
	require.NoError(t, err)
	require.Equal(t, 1, queued)
}

func TestResourceValidationUseCase_DispatchClaimsOnlyPendingResources(t *testing.T) {
	resourceRepo := newMockResourceRepo()
	validationRepo := newMockValidationRepo(resourceRepo)
	queue := &mockValidationQueue{}
	uc := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, queue, mockResourceValidator{})

	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &coredomain.MicrosoftResource{EmailAddress: "pending@example.com", Password: "secret", Status: coredomain.MicrosoftStatusPending, CredentialRevision: 7}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, resource))

	result, err := uc.DispatchPending(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, result.Queued)
	require.Equal(t, coredomain.MicrosoftStatusValidating, resourceRepo.microsoft[root.ID].Status)
	require.Equal(t, uint64(7), queue.tasks[0].ExpectedCredentialRevision)
}

func TestResourceValidationUseCase_DispatchFailureReturnsResourcePending(t *testing.T) {
	resourceRepo := newMockResourceRepo()
	validationRepo := newMockValidationRepo(resourceRepo)
	queue := &mockValidationQueue{enqueueErr: errors.New("redis unavailable")}
	uc := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, queue, mockResourceValidator{})

	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeDomain, OwnerUserID: 1}
	require.NoError(t, resourceRepo.CreateDomain(context.Background(), root, &coredomain.MailDomainResource{
		Domain: "example.com", MailServerID: 1, Purpose: coredomain.PurposeNotSale, Status: coredomain.DomainStatusPending,
	}))
	result, err := uc.DispatchPending(context.Background(), 1)
	require.Error(t, err)
	require.Equal(t, 1, result.Failed)
	require.Equal(t, coredomain.DomainStatusPending, resourceRepo.domains[root.ID].Status)
}

func TestResourceValidationUseCase_DuplicateDispatchLeavesResourcePending(t *testing.T) {
	resourceRepo := newMockResourceRepo()
	validationRepo := newMockValidationRepo(resourceRepo)
	queue := &mockValidationQueue{duplicate: true}
	uc := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, queue, mockResourceValidator{})

	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeDomain, OwnerUserID: 1}
	require.NoError(t, resourceRepo.CreateDomain(context.Background(), root, &coredomain.MailDomainResource{
		Domain: "duplicate.example.com", MailServerID: 1, Purpose: coredomain.PurposeNotSale, Status: coredomain.DomainStatusPending,
	}))
	result, err := uc.DispatchPending(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, result.Attempted)
	require.Zero(t, result.Queued)
	require.Zero(t, result.Failed)
	require.Equal(t, coredomain.DomainStatusPending, resourceRepo.domains[root.ID].Status)
}

func TestResourceValidationUseCase_ActivationFailureLeavesResourcePending(t *testing.T) {
	resourceRepo := newMockResourceRepo()
	validationRepo := newMockValidationRepo(resourceRepo)
	validationRepo.markDispatchedErr = errors.New("database unavailable")
	queue := &mockValidationQueue{}
	uc := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, queue, mockResourceValidator{})

	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, &coredomain.MicrosoftResource{
		EmailAddress: "activation@example.com", Password: "secret", Status: coredomain.MicrosoftStatusPending, CredentialRevision: 2,
	}))
	result, err := uc.DispatchPending(context.Background(), 1)
	require.Error(t, err)
	require.Equal(t, 1, result.Failed)
	require.Zero(t, result.Queued)
	require.Len(t, queue.tasks, 1, "the accepted Redis task remains available to retry the activation race")
	require.Equal(t, coredomain.MicrosoftStatusPending, resourceRepo.microsoft[root.ID].Status)
}

func TestResourceValidationUseCase_WorkerActivatesPendingGeneration(t *testing.T) {
	t.Run("microsoft", func(t *testing.T) {
		resourceRepo := newMockResourceRepo()
		validationRepo := newMockValidationRepo(resourceRepo)
		uc := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, &mockValidationQueue{}, mockResourceValidator{
			msResult: coreapp.MicrosoftValidationResult{Valid: true},
		})
		root := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
		require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, &coredomain.MicrosoftResource{
			EmailAddress: "pending-race@example.com", Password: "secret", Status: coredomain.MicrosoftStatusPending, CredentialRevision: 3,
		}))
		task := coreapp.ResourceValidationTask{
			ResourceID: root.ID, ResourceType: coredomain.ResourceTypeMicrosoft,
			OwnerUserID: 1, ValidationGeneration: resourceRepo.microsoft[root.ID].ValidationGeneration, ExpectedCredentialRevision: 3,
		}
		require.NoError(t, uc.Process(context.Background(), task, false))
		require.Equal(t, coredomain.MicrosoftStatusNormal, resourceRepo.microsoft[root.ID].Status)
	})

	t.Run("domain", func(t *testing.T) {
		resourceRepo := newMockResourceRepo()
		validationRepo := newMockValidationRepo(resourceRepo)
		uc := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, &mockValidationQueue{}, mockResourceValidator{
			domainResult: coreapp.DomainValidationResult{Valid: true},
		})
		root := &coredomain.EmailResource{Type: coredomain.ResourceTypeDomain, OwnerUserID: 1}
		require.NoError(t, resourceRepo.CreateDomain(context.Background(), root, &coredomain.MailDomainResource{
			Domain: "pending-race.example.com", MailServerID: 1, Purpose: coredomain.PurposeNotSale, Status: coredomain.DomainStatusPending,
		}))
		task := coreapp.ResourceValidationTask{ResourceID: root.ID, ResourceType: coredomain.ResourceTypeDomain, OwnerUserID: 1, ValidationGeneration: resourceRepo.domains[root.ID].ValidationGeneration}
		require.NoError(t, uc.Process(context.Background(), task, false))
		require.Equal(t, coredomain.DomainStatusNormal, resourceRepo.domains[root.ID].Status)
	})
}

func TestResourceValidationUseCase_ProcessMicrosoftSuccessUpdatesResource(t *testing.T) {
	resourceRepo := newMockResourceRepo()
	validationRepo := newMockValidationRepo(resourceRepo)
	trigger := &validationAliasTrigger{}
	historyTrigger := &validationHistoryTrigger{}
	uc := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, &mockValidationQueue{}, mockResourceValidator{
		msResult: coreapp.MicrosoftValidationResult{Valid: true, ClientID: "rotated-client", RefreshToken: "rotated-rt", GraphAvailable: true},
	})
	uc.SetMicrosoftAliasScheduleTrigger(trigger)
	uc.SetMicrosoftHistoryScanTrigger(historyTrigger)

	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &coredomain.MicrosoftResource{EmailAddress: "success@example.com", Password: "secret", Status: coredomain.MicrosoftStatusPending, CredentialRevision: 1}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, resource))
	task := mockValidationTask(validationRepo, root.ID)
	require.NoError(t, uc.Process(context.Background(), task, false))
	require.Equal(t, coredomain.MicrosoftStatusNormal, resourceRepo.microsoft[root.ID].Status)
	require.Equal(t, "rotated-rt", resourceRepo.microsoft[root.ID].RefreshToken)
	require.Equal(t, []uint{root.ID}, trigger.resourceIDs)
	require.Len(t, historyTrigger.tasks, 1)
	require.Equal(t, root.ID, historyTrigger.tasks[0].resourceID)
	require.Equal(t, task.RequestID, historyTrigger.tasks[0].requestID)
}

func TestResourceValidationUseCase_HistoryTaskFailureKeepsMicrosoftValidating(t *testing.T) {
	resourceRepo := newMockResourceRepo()
	validationRepo := newMockValidationRepo(resourceRepo)
	historyTrigger := &validationHistoryTrigger{err: errors.New("redis unavailable")}
	uc := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, &mockValidationQueue{}, mockResourceValidator{
		msResult: coreapp.MicrosoftValidationResult{Valid: true, ClientID: "rotated-client", RefreshToken: "rotated-rt", GraphAvailable: true},
	})
	uc.SetMicrosoftHistoryScanTrigger(historyTrigger)

	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &coredomain.MicrosoftResource{EmailAddress: "history-retry@example.com", Password: "secret", Status: coredomain.MicrosoftStatusPending, CredentialRevision: 1}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, resource))
	task := mockValidationTask(validationRepo, root.ID)

	require.ErrorIs(t, uc.Process(context.Background(), task, false), coreapp.ErrValidationTemporaryUnavailable)
	require.Equal(t, coredomain.MicrosoftStatusValidating, resourceRepo.microsoft[root.ID].Status)
	require.Len(t, historyTrigger.tasks, 1)
}

func TestResourceValidationUseCase_TemporaryFailureStaysValidatingUntilRedisRetryEnds(t *testing.T) {
	resourceRepo := newMockResourceRepo()
	validationRepo := newMockValidationRepo(resourceRepo)
	uc := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, &mockValidationQueue{}, mockResourceValidator{msErr: errors.New("temporary")})

	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, &coredomain.MicrosoftResource{
		EmailAddress: "retry@example.com", Password: "secret", Status: coredomain.MicrosoftStatusPending, CredentialRevision: 1,
	}))
	task := mockValidationTask(validationRepo, root.ID)
	require.ErrorIs(t, uc.Process(context.Background(), task, false), coreapp.ErrValidationTemporaryUnavailable)
	require.Equal(t, coredomain.MicrosoftStatusValidating, resourceRepo.microsoft[root.ID].Status)
	require.NoError(t, uc.ReleaseDispatch(context.Background(), task))
	require.Equal(t, coredomain.MicrosoftStatusPending, resourceRepo.microsoft[root.ID].Status)
}

func TestResourceValidationUseCase_ReleasesRecoveryLeaseOnValidatorError(t *testing.T) {
	resourceRepo := newMockResourceRepo()
	validationRepo := newMockValidationRepo(resourceRepo)
	released := false
	releaseContextActive := false
	uc := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, &mockValidationQueue{}, mockResourceValidator{
		msResult: coreapp.MicrosoftValidationResult{ReleaseRecoveryLease: func(ctx context.Context) error {
			released = true
			releaseContextActive = ctx.Err() == nil
			return nil
		}},
		msErr: errors.New("temporary"),
	})

	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, &coredomain.MicrosoftResource{
		EmailAddress: "lease-error@example.com", Password: "secret", Status: coredomain.MicrosoftStatusPending, CredentialRevision: 1,
	}))
	task := mockValidationTask(validationRepo, root.ID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.ErrorIs(t, uc.Process(ctx, task, false), coreapp.ErrValidationTemporaryUnavailable)
	require.True(t, released)
	require.True(t, releaseContextActive, "lease cleanup must survive worker cancellation")
}

func TestResourceValidationUseCase_ProcessDomainFailureWritesDiagnostic(t *testing.T) {
	resourceRepo := newMockResourceRepo()
	validationRepo := newMockValidationRepo(resourceRepo)
	uc := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, &mockValidationQueue{}, mockResourceValidator{
		domainResult: coreapp.DomainValidationResult{Valid: false, Category: "dns", SafeMessage: "Domain MX record is not configured correctly."},
	})

	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeDomain, OwnerUserID: 1}
	require.NoError(t, resourceRepo.CreateDomain(context.Background(), root, &coredomain.MailDomainResource{
		Domain: "example.com", MailServerID: 1, Purpose: coredomain.PurposeNotSale, Status: coredomain.DomainStatusPending,
	}))
	require.NoError(t, uc.Process(context.Background(), mockValidationTask(validationRepo, root.ID), false))
	require.Equal(t, coredomain.DomainStatusAbnormal, resourceRepo.domains[root.ID].Status)
	require.Equal(t, "Domain MX record is not configured correctly.", resourceRepo.domains[root.ID].LastSafeError)
}

func TestResourceValidationUseCase_ThirdBusinessFailureCommitsAbnormal(t *testing.T) {
	resourceRepo := newMockResourceRepo()
	validationRepo := newMockValidationRepo(resourceRepo)
	uc := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, &mockValidationQueue{}, mockResourceValidator{
		msResult: coreapp.MicrosoftValidationResult{Valid: false, Category: "auth_timeout", SafeMessage: "Microsoft authorization timed out."},
	})

	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, &coredomain.MicrosoftResource{
		EmailAddress: "auth-timeout@example.com", Password: "secret", Status: coredomain.MicrosoftStatusPending, CredentialRevision: 1,
	}))
	for attempt := 1; attempt <= coreapp.ResourceValidationMaxFailures; attempt++ {
		task := mockValidationTask(validationRepo, root.ID)
		require.NoError(t, uc.Process(context.Background(), task, false))
		require.Equal(t, attempt, resourceRepo.microsoft[root.ID].ValidationFailures)
		if attempt < coreapp.ResourceValidationMaxFailures {
			require.Equal(t, coredomain.MicrosoftStatusPending, resourceRepo.microsoft[root.ID].Status)
		} else {
			require.Equal(t, coredomain.MicrosoftStatusAbnormal, resourceRepo.microsoft[root.ID].Status)
		}
	}
	require.Equal(t, coredomain.MicrosoftStatusAbnormal, resourceRepo.microsoft[root.ID].Status)
	require.Equal(t, "Microsoft authorization timed out.", resourceRepo.microsoft[root.ID].LastSafeError)
	require.Len(t, validationRepo.appliedMicrosoftResults, coreapp.ResourceValidationMaxFailures)
}

func TestResourceValidationUseCase_CreateRejectsDisabledResource(t *testing.T) {
	resourceRepo := newMockResourceRepo()
	uc := coreapp.NewResourceValidationUseCase(resourceRepo, newMockValidationRepo(resourceRepo), &mockValidationQueue{}, mockResourceValidator{})
	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, &coredomain.MicrosoftResource{
		EmailAddress: "disabled@example.com", Password: "secret", Status: coredomain.MicrosoftStatusDisabled,
	}))
	_, err := uc.Create(context.Background(), root.ID, 1, true, "req-disabled", "/v1/resources/:resourceId/validate")
	require.ErrorIs(t, err, coredomain.ErrInvalidResourceStatus)
}
func TestCoreHandler_ResourceListIncludesStatusFields(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, mailServerRepo, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	msRoot := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	ms := &coredomain.MicrosoftResource{
		EmailAddress:   "ms@example.com",
		Password:       "secret",
		Status:         coredomain.MicrosoftStatusNormal,
		ForSale:        true,
		LongLived:      true,
		GraphAvailable: true,
		LastSafeError:  "safe diagnostic",
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
	setAuthContext(c, 1, iamdomain.RoleSupplier)

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
			if item.GraphAvailable == nil || !*item.GraphAvailable {
				t.Errorf("expected microsoft graphAvailable true, got %v", item.GraphAvailable)
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
	setAuthContext(c, 1, iamdomain.RoleUser)

	h.GetResources(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp ResourceListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, 1, len(resp.Items))
	require.Equal(t, int64(1), resp.Total)
	require.Equal(t, "owner@example.com", resp.Items[0].Email)
}

func TestCoreHandler_SelfServiceDomainResourcesHideBindingDomainsAndFacets(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, _, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	visibleRoot := &coredomain.EmailResource{Type: coredomain.ResourceTypeDomain, OwnerUserID: 1}
	visibleDomain := &coredomain.MailDomainResource{
		Domain:       "visible.example.com",
		MailServerID: 1,
		Purpose:      coredomain.PurposeNotSale,
		Status:       coredomain.DomainStatusNormal,
	}
	require.NoError(t, resourceRepo.CreateDomain(context.Background(), visibleRoot, visibleDomain))

	bindingRoot := &coredomain.EmailResource{Type: coredomain.ResourceTypeDomain, OwnerUserID: 1}
	bindingDomain := &coredomain.MailDomainResource{
		Domain:       "auxiliary.example.kg",
		MailServerID: 1,
		Purpose:      coredomain.PurposeBinding,
		Status:       coredomain.DomainStatusNormal,
	}
	require.NoError(t, resourceRepo.CreateDomain(context.Background(), bindingRoot, bindingDomain))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/v1/resources?type=domain", nil)
	setAuthContext(c, 1, iamdomain.RoleUser)

	h.GetResources(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var response ResourceListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.EqualValues(t, 1, response.Total)
	require.Len(t, response.Items, 1)
	require.Equal(t, visibleDomain.Domain, response.Items[0].Domain)
	require.NotContains(t, w.Body.String(), bindingDomain.Domain)
	require.NotNil(t, response.Facets)
	require.EqualValues(t, 1, response.Facets.Status.All)
	require.Equal(t, []ResourceKeyFacetResponse{{Key: ".com", Count: 1}}, response.Facets.TLDs)

	detailW := httptest.NewRecorder()
	detailContext, _ := gin.CreateTestContext(detailW)
	detailContext.Request = httptest.NewRequest("GET", "/v1/resources/2", nil)
	detailContext.Params = gin.Params{{Key: "resourceId", Value: "2"}}
	setAuthContext(detailContext, 1, iamdomain.RoleUser)

	h.GetResourceDetail(detailContext)

	require.Equal(t, http.StatusNotFound, detailW.Code, detailW.Body.String())
	require.NotContains(t, detailW.Body.String(), bindingDomain.Domain)
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
	setAuthContext(c, 1, iamdomain.RoleSupplier)

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
	setAuthContext(c, 1, iamdomain.RoleSupplier)

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
	setAuthContext(c, 1, iamdomain.RoleSupplier)

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
	setAuthContext(c, 1, iamdomain.RoleSupplier)

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
		name   string
		method string
		path   string
		body   string
		role   iamdomain.Role
		call   func(*CoreHandler, *gin.Context)
		field  string
	}{
		{
			name:   "publish ids requires ids",
			method: "POST",
			path:   "/v1/resources/publish",
			body:   `{"selection":{"mode":"ids"}}`,
			role:   iamdomain.RoleSupplier,
			call:   (*CoreHandler).PostResourcePublishBatch,
			field:  "selection.resourceIds",
		},
		{
			name:   "publish filter requires resource type",
			method: "POST",
			path:   "/v1/resources/publish",
			body:   `{"selection":{"mode":"filter","filter":{"status":"normal"}}}`,
			role:   iamdomain.RoleSupplier,
			call:   (*CoreHandler).PostResourcePublishBatch,
			field:  "selection.filter.resourceType",
		},
		{
			name:   "delete ids requires ids",
			method: "POST",
			path:   "/v1/resources/delete",
			body:   `{"selection":{"mode":"ids"}}`,
			role:   iamdomain.RoleUser,
			call:   (*CoreHandler).PostResourceDeleteBatch,
			field:  "selection.resourceIds",
		},
		{
			name:   "delete ids rejects zero",
			method: "POST",
			path:   "/v1/resources/delete",
			body:   `{"selection":{"mode":"ids","resourceIds":[0]}}`,
			role:   iamdomain.RoleUser,
			call:   (*CoreHandler).PostResourceDeleteBatch,
			field:  "selection.resourceIds",
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
			setAuthContext(c, 1, tt.role)

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
	setAuthContext(c, 2, iamdomain.RoleSupplier)

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
	setAuthContext(c, 1, iamdomain.RoleUser)

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
	setAuthContext(c, 1, iamdomain.RoleUser)

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
	setAuthContext(c, 1, iamdomain.RoleUser)

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
	setAuthContext(c, 1, iamdomain.RoleUser)

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
	setAuthContext(c, 1, iamdomain.RoleUser)

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
	setAuthContext(c, 2, iamdomain.RoleUser)

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
	setAuthContext(c, 1, iamdomain.RoleUser)

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
	setAuthContext(c, 1, iamdomain.RoleUser)

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
	setAuthContext(c, 1, iamdomain.RoleSupplier)

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
	setAuthContext(c, 1, iamdomain.RoleUser)

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
	setAuthContext(c, 1, iamdomain.RoleAdmin)

	h.PostDomain(c)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Len(t, resourceRepo.domains, 1)
	require.Equal(t, coredomain.PurposeBinding, resourceRepo.domains[1].Purpose)
}

func TestCoreHandler_AdminDomainManagement(t *testing.T) {
	lastError := "MX does not point to the configured inbound server."
	response := toAdminDomainListResponse(&coreapp.AdminDomainListResult{
		Items: []coreapp.AdminDomainItem{{
			ID: 42, Version: 7, Domain: "admin.example.com", DomainTLD: "com",
			Owner:   coreapp.AdminOwnerSummary{ID: 9, Email: "owner@example.com", Nickname: "Owner", Role: "supplier"},
			Purpose: "sale", Status: "deleted", MailServerID: 3, MailboxCount: 11, LastSafeError: &lastError,
			CreatedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC),
		}},
		Total: 1, Offset: 0, Limit: 20,
		Facets: coreapp.AdminDomainFacets{
			Status:  coreapp.AdminDomainStatusFacets{All: 3, Normal: 1, Abnormal: 1, Disabled: 1, Deleted: 1},
			Purpose: coreapp.AdminDomainPurposeFacets{All: 4, NotSale: 1, Sale: 2, Binding: 1},
			TLDs:    []coreapp.AdminKeyFacet{{Key: "com", Count: 4}},
		},
	})

	require.Equal(t, int64(1), response.Total)
	require.Equal(t, uint64(7), response.Items[0].Version)
	require.Equal(t, "owner@example.com", response.Items[0].OwnerEmail)
	require.Equal(t, "deleted", response.Items[0].Status)
	require.Equal(t, int64(1), response.Facets.Status.Deleted)
	require.Equal(t, int64(1), response.Facets.Purpose.Binding)
	require.Equal(t, "com", response.Facets.TLDs[0].Key)
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
	setAuthContext(ownerCtx, 1, iamdomain.RoleUser)

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
	setAuthContext(otherCtx, 2, iamdomain.RoleSupplier)

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
	setAuthContext(c, 1, iamdomain.RoleUser)

	h.GetDomainMailboxes(c)

	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}

func TestCoreHandler_DomainMailboxesHideBindingDomainFromNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, mailServerRepo, mailboxRepo := setupCoreTestModule()
	h := NewCoreHandler(mod)

	server := &coredomain.MailServer{OwnerUserID: 1, ServerAddress: "mail.example.com", Status: coredomain.MailServerOnline}
	require.NoError(t, mailServerRepo.Create(context.Background(), server))
	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeDomain, OwnerUserID: 1}
	domain := &coredomain.MailDomainResource{
		Domain:       "binding.example.com",
		MailServerID: server.ID,
		Purpose:      coredomain.PurposeBinding,
		Status:       coredomain.DomainStatusNormal,
	}
	require.NoError(t, resourceRepo.CreateDomain(context.Background(), root, domain))
	mailboxRepo.mailboxes[1] = &coredomain.GeneratedMailbox{
		ID:          1,
		ResourceID:  domain.ID,
		OwnerUserID: 1,
		Email:       "hidden@binding.example.com",
		Status:      coredomain.GeneratedMailboxNormal,
		CreatedAt:   time.Now(),
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/v1/domains/1/mailboxes", nil)
	c.Params = gin.Params{{Key: "domainId", Value: "1"}}
	setAuthContext(c, 1, iamdomain.RoleUser)

	h.GetDomainMailboxes(c)

	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), "hidden@binding.example.com")
}

func TestCoreHandler_GetProjectDetailHidesInternalProductFieldsForNormalUser(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := newMockProjectRepo()
	repo.details[1] = projectDetailForAPITest()
	mod := &CoreModule{ProjectUseCase: coreapp.NewProjectUseCase(repo)}
	h := NewCoreHandler(mod)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/v1/projects/1", nil)
	c.Params = gin.Params{{Key: "projectId", Value: "1"}}
	setAuthContext(c, 2, iamdomain.RoleUser)

	h.GetProject(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	products := body["products"].([]any)
	product := products[0].(map[string]any)
	require.NotContains(t, product, "codeSupplierPrice")
	require.NotContains(t, product, "purchaseSupplierPrice")
	require.NotContains(t, product, "mainWeight")
	require.NotContains(t, product, "dotWeight")
	require.NotContains(t, product, "plusWeight")
	require.NotContains(t, body, "mailRules")
}

func TestCoreHandler_GetProjectDetailIncludesInternalProductFieldsForProjectAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := newMockProjectRepo()
	repo.details[1] = projectDetailForAPITest()
	mod := &CoreModule{ProjectUseCase: coreapp.NewProjectUseCase(repo)}
	h := NewCoreHandler(mod, mockPermissionChecker{allowed: true})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/v1/projects/1", nil)
	c.Params = gin.Params{{Key: "projectId", Value: "1"}}
	setAuthContext(c, 1, iamdomain.RoleAdmin)

	h.GetProject(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	products := body["products"].([]any)
	product := products[0].(map[string]any)
	require.Equal(t, "0.050000", product["codeSupplierPrice"])
	require.Equal(t, "0.000000", product["purchaseSupplierPrice"])
	require.Equal(t, float64(1), product["mainWeight"])
	require.Len(t, body["mailRules"].([]any), 2)
}

func TestCoreHandler_GetProjectsScopeAllRequiresProjectReadPermission(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := newMockProjectRepo()
	detail := projectDetailForAPITest()
	repo.summaries = []coreapp.ProjectSummary{
		{
			Project:       detail.Project,
			Products:      detail.Products,
			ProductCount:  len(detail.Products),
			MailRuleCount: len(detail.MailRules),
		},
	}
	mod := &CoreModule{ProjectUseCase: coreapp.NewProjectUseCase(repo)}
	h := NewCoreHandler(mod, mockPermissionChecker{allowed: false})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/v1/projects?scope=all", nil)
	setAuthContext(c, 1, iamdomain.RoleAdmin)

	h.GetProjects(c)

	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

func TestCoreHandler_GetProjectsIncludesProductSummaries(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := newMockProjectRepo()
	detail := projectDetailForAPITest()
	repo.summaries = []coreapp.ProjectSummary{
		{
			Project:       detail.Project,
			Products:      detail.Products,
			ProductCount:  len(detail.Products),
			MailRuleCount: len(detail.MailRules),
		},
	}
	mod := &CoreModule{ProjectUseCase: coreapp.NewProjectUseCase(repo)}
	h := NewCoreHandler(mod, mockPermissionChecker{allowed: true})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/v1/projects", nil)
	setAuthContext(c, 1, iamdomain.RoleUser)

	h.GetProjects(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	items := body["items"].([]any)
	require.Len(t, items, 1)
	item := items[0].(map[string]any)
	products := item["products"].([]any)
	require.Len(t, products, 1)
	product := products[0].(map[string]any)
	require.Equal(t, "microsoft", product["type"])
	require.Equal(t, "0.100000", product["codePrice"])
	require.Equal(t, "0.000000", product["purchasePrice"])
	require.NotContains(t, product, "codeSupplierPrice")
	require.NotContains(t, product, "mainWeight")
	facets := body["facets"].(map[string]any)
	statusFacets := facets["status"].(map[string]any)
	require.Equal(t, float64(1), statusFacets["all"])
	require.Equal(t, float64(1), statusFacets["listed"])
	productTypeFacets := facets["productType"].(map[string]any)
	require.Equal(t, float64(1), productTypeFacets["microsoft"])
}

func TestCoreHandler_ProjectOwnerCanSeeRejectReasonAndResubmit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := newMockProjectRepo()
	detail := projectDetailForAPITest()
	applicantID := uint(7)
	detail.Project.ID = 8
	detail.Project.Status = coredomain.ProjectStatusDelisted
	detail.Project.ApplicantUserID = &applicantID
	detail.Project.ReviewReason = "规则不够明确"
	repo.details[8] = detail
	repo.summaries = []coreapp.ProjectSummary{
		{
			Project:       detail.Project,
			Products:      detail.Products,
			ProductCount:  len(detail.Products),
			MailRuleCount: len(detail.MailRules),
		},
	}
	mod := &CoreModule{ProjectUseCase: coreapp.NewProjectUseCase(repo)}
	h := NewCoreHandler(mod)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/v1/projects?scope=mine", nil)
	setAuthContext(c, 7, iamdomain.RoleUser)

	h.GetProjects(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var listBody map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &listBody))
	items := listBody["items"].([]any)
	item := items[0].(map[string]any)
	require.Equal(t, "规则不够明确", item["reviewReason"])
	require.Equal(t, float64(7), item["applicantUserId"])

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/v1/projects/8", nil)
	c.Params = gin.Params{{Key: "projectId", Value: "8"}}
	setAuthContext(c, 7, iamdomain.RoleUser)

	h.GetProject(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var detailBody map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &detailBody))
	require.Len(t, detailBody["mailRules"].([]any), 2)

	body := `{"name":"GitHub Updated","targetPlatform":"github.com","accessType":"public","looseMatch":true,"mailRules":[{"ruleType":"sender","pattern":"noreply@github.com","enabled":true},{"ruleType":"recipient","pattern":"exact","enabled":true}]}`
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/projects/8/resubmit", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "projectId", Value: "8"}}
	setAuthContext(c, 7, iamdomain.RoleUser)

	h.PostProjectResubmit(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resubmitBody map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resubmitBody))
	project := resubmitBody["project"].(map[string]any)
	require.Equal(t, "reviewing", project["status"])
	require.Empty(t, project["reviewReason"])
	require.Equal(t, "GitHub Updated", project["name"])
}

func TestCoreHandler_AdminProjectApproveMovesReviewingToListed(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := newMockProjectRepo()
	detail := projectDetailForAPITest()
	detail.Project.ID = 9
	detail.Project.Status = coredomain.ProjectStatusReviewing
	repo.details[9] = detail
	mod := &CoreModule{ProjectUseCase: coreapp.NewProjectUseCase(repo)}
	h := NewCoreHandler(mod)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/admin/projects/9/approve", nil)
	c.Params = gin.Params{{Key: "projectId", Value: "9"}}
	setAuthContext(c, 1, iamdomain.RoleAdmin)

	h.PostAdminProjectApprove(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body ProjectDetailResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, string(coredomain.ProjectStatusListed), body.Project.Status)
	require.Empty(t, body.Project.ReviewReason)
}

func TestCoreHandler_AdminProjectApproveWithConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := newMockProjectRepo()
	detail := projectDetailForAPITest()
	detail.Project.ID = 11
	detail.Project.Status = coredomain.ProjectStatusReviewing
	detail.Products = nil
	repo.details[11] = detail
	mod := &CoreModule{ProjectUseCase: coreapp.NewProjectUseCase(repo)}
	h := NewCoreHandler(mod)

	body := `{
		"name":"GitHub Configured",
		"targetPlatform":"github.com",
		"accessType":"public",
		"looseMatch":true,
		"products":[{
			"type":"microsoft",
			"status":"enabled",
			"codeEnabled":true,
			"purchaseEnabled":false,
			"codePrice":"0.100000",
			"purchasePrice":"0",
			"codeSupplierPrice":"0.050000",
			"purchaseSupplierPrice":"0",
			"codeWindowMinutes":10,
			"activationWindowMinutes":60,
			"warrantyMinutes":60,
			"mainWeight":1
		}],
		"mailRules":[
			{"ruleType":"sender","pattern":".*","enabled":true},
			{"ruleType":"recipient","pattern":"exact","enabled":true}
		]
	}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/admin/projects/11/approve", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "projectId", Value: "11"}}
	setAuthContext(c, 1, iamdomain.RoleAdmin)

	h.PostAdminProjectApprove(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var response ProjectDetailResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, string(coredomain.ProjectStatusListed), response.Project.Status)
	require.Equal(t, "GitHub Configured", response.Project.Name)
	require.Len(t, response.Products, 1)
}

func TestCoreHandler_AdminProjectCreateWithPrivateAccessUsers(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := newMockProjectRepo()
	mod := &CoreModule{ProjectUseCase: coreapp.NewProjectUseCase(repo)}
	h := NewCoreHandler(mod)

	body := `{
		"name":"Private GitHub",
		"targetPlatform":"github.com",
		"accessType":"private",
		"accessUserIds":[7,7,8],
		"looseMatch":true,
		"products":[{
			"type":"microsoft",
			"status":"enabled",
			"codeEnabled":true,
			"purchaseEnabled":false,
			"codePrice":"0.100000",
			"purchasePrice":"0",
			"codeSupplierPrice":"0.050000",
			"purchaseSupplierPrice":"0",
			"codeWindowMinutes":10,
			"activationWindowMinutes":60,
			"warrantyMinutes":60,
			"mainWeight":1
		}],
		"mailRules":[
			{"ruleType":"sender","pattern":".*","enabled":true},
			{"ruleType":"recipient","pattern":"exact","enabled":true}
		]
	}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/admin/projects", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	setAuthContext(c, 1, iamdomain.RoleAdmin)

	h.PostAdminProject(c)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var response ProjectDetailResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, "private", response.Project.AccessType)
	require.Len(t, response.Accesses, 2)
	require.Equal(t, uint(7), response.Accesses[0].UserID)
	require.Equal(t, uint(1), response.Accesses[0].GrantedBy)
}

func TestCoreHandler_AdminProjectUpdateRejectsReviewingProject(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := newMockProjectRepo()
	detail := projectDetailForAPITest()
	detail.Project.ID = 12
	detail.Project.Status = coredomain.ProjectStatusReviewing
	repo.details[12] = detail
	mod := &CoreModule{ProjectUseCase: coreapp.NewProjectUseCase(repo)}
	h := NewCoreHandler(mod)

	body := `{
		"name":"GitHub Updated",
		"targetPlatform":"github.com",
		"accessType":"public",
		"looseMatch":true,
		"products":[{
			"type":"microsoft",
			"status":"enabled",
			"codeEnabled":true,
			"purchaseEnabled":false,
			"codePrice":"0.100000",
			"purchasePrice":"0",
			"codeSupplierPrice":"0.050000",
			"purchaseSupplierPrice":"0",
			"codeWindowMinutes":10,
			"activationWindowMinutes":60,
			"warrantyMinutes":60,
			"mainWeight":1
		}],
		"mailRules":[
			{"ruleType":"sender","pattern":".*","enabled":true},
			{"ruleType":"recipient","pattern":"exact","enabled":true}
		]
	}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/v1/admin/projects/12", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "projectId", Value: "12"}}
	setAuthContext(c, 1, iamdomain.RoleAdmin)

	h.PutAdminProject(c)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
}

func TestCoreHandler_AdminProjectAccessGrantListAndRevoke(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := newMockProjectRepo()
	detail := projectDetailForAPITest()
	detail.Project.ID = 10
	detail.Project.AccessType = coredomain.ProjectAccessPrivate
	repo.details[10] = detail
	mod := &CoreModule{ProjectUseCase: coreapp.NewProjectUseCase(repo)}
	h := NewCoreHandler(mod)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/admin/projects/10/access", strings.NewReader(`{"userId":7}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "projectId", Value: "10"}}
	setAuthContext(c, 1, iamdomain.RoleAdmin)

	h.PostAdminProjectAccess(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var access ProjectAccessResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &access))
	require.Equal(t, uint(10), access.ProjectID)
	require.Equal(t, uint(7), access.UserID)

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/v1/admin/projects/10/access", nil)
	c.Params = gin.Params{{Key: "projectId", Value: "10"}}
	setAuthContext(c, 1, iamdomain.RoleAdmin)

	h.GetAdminProjectAccess(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var list ProjectAccessListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	require.Equal(t, 1, list.Total)
	require.Len(t, list.Items, 1)

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("DELETE", "/v1/admin/projects/10/access/7", nil)
	c.Params = gin.Params{{Key: "projectId", Value: "10"}, {Key: "userId", Value: "7"}}
	setAuthContext(c, 1, iamdomain.RoleAdmin)

	h.DeleteAdminProjectAccess(c)

	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	require.Empty(t, repo.details[10].Accesses)
}

func TestCoreHandler_AdminProjectBulkSelectionShapeValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := newMockProjectRepo()
	mod := &CoreModule{ProjectUseCase: coreapp.NewProjectUseCase(repo)}
	h := NewCoreHandler(mod)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/admin/projects/delete", strings.NewReader(`{"selection":{"mode":"ids","projectIds":[]}}`))
	c.Request.Header.Set("Content-Type", "application/json")
	setAuthContext(c, 1, iamdomain.RoleAdmin)

	h.PostAdminProjectsDelete(c)

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

func TestCoreHandler_ProjectLogoUploadAndRead(t *testing.T) {
	gin.SetMode(gin.TestMode)

	files := newMockFileStore()
	mod := &CoreModule{ProjectAssets: coreapp.NewProjectAssetUseCase(files)}
	h := NewCoreHandler(mod)
	body, contentType := multipartImportBody(t, "logo.png", "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90wS\xde")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/admin/projects/logos", body)
	c.Request.Header.Set("Content-Type", contentType)
	setAuthContext(c, 1, iamdomain.RoleAdmin)

	h.PostAdminProjectLogo(c)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var upload ProjectLogoUploadResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &upload))
	require.Contains(t, upload.LogoURL, "/v1/projects/logos/")

	logoKey := strings.TrimPrefix(upload.LogoURL, "/v1/projects/logos/")
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", upload.LogoURL, nil)
	c.Params = gin.Params{{Key: "logoKey", Value: logoKey}}
	setAuthContext(c, 2, iamdomain.RoleUser)

	h.GetProjectLogo(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "image/png", w.Header().Get("Content-Type"))
	require.NotEmpty(t, w.Body.Bytes())
}

func projectDetailForAPITest() *coredomain.ProjectDetail {
	now := time.Now().UTC()
	return &coredomain.ProjectDetail{
		Project: coredomain.Project{
			ID:             1,
			Name:           "GitHub",
			TargetPlatform: "github.com",
			Status:         coredomain.ProjectStatusListed,
			AccessType:     coredomain.ProjectAccessPublic,
			LooseMatch:     true,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
		Products: []coredomain.Product{
			{
				ID:                      11,
				ProjectID:               1,
				Type:                    coredomain.ProductTypeMicrosoft,
				Status:                  coredomain.ProductStatusEnabled,
				CodeEnabled:             true,
				PurchaseEnabled:         false,
				CodePrice:               "0.100000",
				PurchasePrice:           "0.000000",
				CodeSupplierPrice:       "0.050000",
				PurchaseSupplierPrice:   "0.000000",
				CodeWindowMinutes:       10,
				ActivationWindowMinutes: 60,
				WarrantyMinutes:         60,
				MainWeight:              1,
			},
		},
		MailRules: []coredomain.MailRule{
			{ID: 21, ProjectID: 1, RuleType: coredomain.MailRuleSender, Pattern: ".*", Enabled: true},
			{ID: 22, ProjectID: 1, RuleType: coredomain.MailRuleRecipient, Pattern: "exact", Enabled: true},
		},
	}
}
