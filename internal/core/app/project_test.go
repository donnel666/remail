package app

import (
	"context"
	"errors"
	"testing"

	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/stretchr/testify/require"
)

type fakeProjectRepo struct {
	detail           *domain.ProjectDetail
	bulkReviewReason string
	items            []ProjectSummary
	log              *governancedomain.OperationLog
}

type projectOwnerQueryStub struct {
	owner AdminOwnerSummary
}

func (s projectOwnerQueryStub) GetByIDs(_ context.Context, ids []uint) (map[uint]AdminOwnerSummary, error) {
	result := make(map[uint]AdminOwnerSummary, len(ids))
	for _, id := range ids {
		if id == s.owner.ID {
			result[id] = s.owner
		}
	}
	return result, nil
}

func (projectOwnerQueryStub) SearchAdminOwners(context.Context, string, int) ([]AdminOwnerSummary, error) {
	return nil, nil
}

func (s projectOwnerQueryStub) ValidateTargetOwner(_ context.Context, id uint) (*AdminOwnerSummary, error) {
	if id != s.owner.ID {
		return nil, nil
	}
	owner := s.owner
	return &owner, nil
}

func (r *fakeProjectRepo) CreateWithLog(_ context.Context, detail *domain.ProjectDetail, log *governancedomain.OperationLog) error {
	detail.Project.ID = 101
	for i := range detail.Products {
		detail.Products[i].ID = uint(i + 1)
		detail.Products[i].ProjectID = detail.Project.ID
	}
	for i := range detail.MailRules {
		detail.MailRules[i].ID = uint(i + 1)
		detail.MailRules[i].ProjectID = detail.Project.ID
	}
	assignProjectAccessesForTest(detail)
	r.detail = detail
	r.log = log
	return nil
}

func (r *fakeProjectRepo) ResubmitWithLog(_ context.Context, _ uint, detail *domain.ProjectDetail, log *governancedomain.OperationLog) error {
	for i := range detail.MailRules {
		detail.MailRules[i].ID = uint(i + 1)
		detail.MailRules[i].ProjectID = detail.Project.ID
	}
	r.detail = detail
	r.log = log
	return nil
}

func (r *fakeProjectRepo) UpdateWithLog(_ context.Context, detail *domain.ProjectDetail, log *governancedomain.OperationLog) error {
	assignProjectAccessesForTest(detail)
	r.detail = detail
	r.log = log
	return nil
}

func (r *fakeProjectRepo) ApproveWithConfigAndLog(_ context.Context, detail *domain.ProjectDetail, log *governancedomain.OperationLog) error {
	assignProjectAccessesForTest(detail)
	r.detail = detail
	r.log = log
	return nil
}

func (r *fakeProjectRepo) TransitionWithLog(_ context.Context, projectID uint, from domain.ProjectStatus, to domain.ProjectStatus, reviewReason string, log *governancedomain.OperationLog) (*domain.ProjectDetail, error) {
	if r.detail == nil {
		r.detail = validProjectDetailForUseCase()
	}
	if r.detail.Project.Status != from {
		return nil, domain.ErrInvalidProjectStatus
	}
	r.detail.Project.ID = projectID
	r.detail.Project.Status = to
	r.detail.Project.ReviewReason = reviewReason
	r.log = log
	return r.detail, nil
}

func (r *fakeProjectRepo) DeleteWithLog(_ context.Context, _ uint, log *governancedomain.OperationLog) error {
	r.detail = nil
	r.log = log
	return nil
}

func (r *fakeProjectRepo) BulkTransitionWithLog(_ context.Context, _ ProjectListFilter, _ domain.ProjectStatus, _ domain.ProjectStatus, reviewReason string, log *governancedomain.OperationLog) (int, error) {
	r.bulkReviewReason = reviewReason
	r.log = log
	return 2, nil
}

func (r *fakeProjectRepo) BulkDeleteWithLog(_ context.Context, _ ProjectListFilter, log *governancedomain.OperationLog) (int, error) {
	r.log = log
	return 2, nil
}

func (r *fakeProjectRepo) ListAccesses(_ context.Context, _ uint) ([]domain.ProjectAccess, error) {
	return nil, nil
}

func (r *fakeProjectRepo) GrantAccessWithLog(_ context.Context, projectID, userID, grantedBy uint, log *governancedomain.OperationLog) (*domain.ProjectAccess, error) {
	r.log = log
	return &domain.ProjectAccess{ID: 1, ProjectID: projectID, UserID: userID, GrantedBy: grantedBy}, nil
}

func (r *fakeProjectRepo) RevokeAccessWithLog(_ context.Context, _ uint, _ uint, log *governancedomain.OperationLog) error {
	r.log = log
	return nil
}

func (r *fakeProjectRepo) List(_ context.Context, _ ProjectListFilter, _, _ int) ([]ProjectSummary, error) {
	return r.items, nil
}

func (r *fakeProjectRepo) Count(_ context.Context, _ ProjectListFilter) (int64, error) {
	return 0, nil
}

func (r *fakeProjectRepo) Facets(_ context.Context, _ ProjectListFilter) (*ProjectListFacets, error) {
	return &ProjectListFacets{}, nil
}

func (r *fakeProjectRepo) FindDetail(_ context.Context, _ uint, _ uint, _ bool) (*domain.ProjectDetail, error) {
	return r.detail, nil
}

func assignProjectAccessesForTest(detail *domain.ProjectDetail) {
	if detail.Project.AccessType != domain.ProjectAccessPrivate {
		detail.Accesses = nil
		return
	}
	for i := range detail.Accesses {
		detail.Accesses[i].ID = uint(i + 1)
		detail.Accesses[i].ProjectID = detail.Project.ID
	}
}

func TestProjectUseCaseAdminCreateListedRejectsInvalidEnums(t *testing.T) {
	uc := NewProjectUseCase(&fakeProjectRepo{})

	req := validProjectCreateRequest()
	req.AccessType = "internal"
	_, err := uc.AdminCreateListed(context.Background(), 1, req, "req-1", "/v1/admin/projects")
	require.ErrorIs(t, err, domain.ErrInvalidProject)

	req = validProjectCreateRequest()
	req.Products[0].Status = "archived"
	_, err = uc.AdminCreateListed(context.Background(), 1, req, "req-2", "/v1/admin/projects")
	require.ErrorIs(t, err, domain.ErrInvalidProduct)
}

func TestProjectUseCaseListEnrichesAdminProjectOwners(t *testing.T) {
	ownerID := uint(7)
	repo := &fakeProjectRepo{items: []ProjectSummary{{
		Project: domain.Project{ApplicantUserID: &ownerID},
	}}}
	uc := NewProjectUseCase(repo)
	uc.SetOwnerQueryPort(projectOwnerQueryStub{owner: AdminOwnerSummary{
		ID:       ownerID,
		Email:    "owner@example.com",
		Nickname: "Owner",
	}})

	result, err := uc.List(context.Background(), ProjectListFilter{Scope: ProjectListScopeAll, IsAdmin: true}, 0, 20)

	require.NoError(t, err)
	require.Equal(t, "owner@example.com", result.Items[0].Owner.Email)
}

func TestNormalizeOrderingAmountPreservesLedgerPrecision(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "10", want: "10.00"},
		{input: "0", want: "0.00"},
		{input: "0.008000", want: "0.008"},
		{input: "0.005000", want: "0.005"},
		{input: "0.007000", want: "0.007"},
	} {
		t.Run(test.input, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeOrderingAmount(test.input)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}

	_, err := normalizeOrderingAmount("0.0000001")
	require.ErrorIs(t, err, domain.ErrInvalidProduct)
}

func TestProjectUseCaseAdminCreateListedRejectsIncompleteRulesAndInvalidWeights(t *testing.T) {
	uc := NewProjectUseCase(&fakeProjectRepo{})

	req := validProjectCreateRequest()
	req.MailRules = req.MailRules[:1]
	_, err := uc.AdminCreateListed(context.Background(), 1, req, "req-1", "/v1/admin/projects")
	require.ErrorIs(t, err, domain.ErrInvalidMailRule)

	req = validProjectCreateRequest()
	req.Products[0].MainWeight = 0
	req.Products[0].DotWeight = 0
	req.Products[0].PlusWeight = 0
	_, err = uc.AdminCreateListed(context.Background(), 1, req, "req-2", "/v1/admin/projects")
	require.ErrorIs(t, err, domain.ErrInvalidProduct)

	req = validProjectCreateRequest()
	req.Products[0].MainWeight = -1
	_, err = uc.AdminCreateListed(context.Background(), 1, req, "req-3", "/v1/admin/projects")
	require.ErrorIs(t, err, domain.ErrInvalidProduct)
}

func TestProjectUseCaseAdminCreateListedCreatesCompleteProjectAndLog(t *testing.T) {
	repo := &fakeProjectRepo{}
	uc := NewProjectUseCase(repo)
	var scannedProjectID uint
	uc.SetHistoryScan(func(_ context.Context, projectID uint, requestID string) error {
		scannedProjectID = projectID
		require.Equal(t, "req-ok", requestID)
		return errors.New("queue unavailable")
	})

	detail, err := uc.AdminCreateListed(context.Background(), 9, validProjectCreateRequest(), "req-ok", "/v1/admin/projects")
	require.NoError(t, err)
	require.Equal(t, uint(101), detail.Project.ID)
	require.Equal(t, domain.ProjectStatusListed, detail.Project.Status)
	require.Len(t, detail.Products, 1)
	require.Equal(t, detail.Project.ID, detail.Products[0].ProjectID)
	require.Len(t, detail.MailRules, 2)
	require.Equal(t, "core.project.create", repo.log.OperationType)
	require.Equal(t, "req-ok", repo.log.RequestID)
	require.Equal(t, uint(9), repo.log.OperatorUserID)
	require.Equal(t, detail.Project.ID, scannedProjectID)
}

func TestProjectUseCaseSkipsHistoryScanWithoutMicrosoftProduct(t *testing.T) {
	repo := &fakeProjectRepo{}
	uc := NewProjectUseCase(repo)
	scanned := false
	uc.SetHistoryScan(func(context.Context, uint, string) error {
		scanned = true
		return nil
	})
	req := validProjectCreateRequest()
	req.Products[0].Type = "domain"

	_, err := uc.AdminCreateListed(context.Background(), 9, req, "req-domain", "/v1/admin/projects")

	require.NoError(t, err)
	require.False(t, scanned)
}

func TestProjectUseCaseAdminUpdatePreservesDisabledHistoricalProduct(t *testing.T) {
	repo := &fakeProjectRepo{}
	uc := NewProjectUseCase(repo)

	req := validProjectCreateRequest()
	req.Products[0].Status = "disabled"
	req.Products = append(req.Products, ProjectProductRequest{
		Type:                    "domain",
		Status:                  "enabled",
		CodeEnabled:             true,
		PurchaseEnabled:         false,
		CodePrice:               "0.200000",
		CodeSupplierPrice:       "0.100000",
		PurchasePrice:           "0",
		PurchaseSupplierPrice:   "0",
		CodeWindowMinutes:       10,
		ActivationWindowMinutes: 60,
		WarrantyMinutes:         60,
	})

	detail, err := uc.AdminUpdate(
		context.Background(),
		9,
		55,
		req,
		"req-update-preserve-disabled-product",
		"/v1/admin/projects/:projectId",
	)
	require.NoError(t, err)
	require.Len(t, detail.Products, 2)
	require.Equal(t, domain.ProductStatusDisabled, detail.Products[0].Status)
	require.Equal(t, domain.ProductStatusEnabled, detail.Products[1].Status)
}

func TestProjectUseCaseAdminCreateListedNormalizesPrivateAccesses(t *testing.T) {
	repo := &fakeProjectRepo{}
	uc := NewProjectUseCase(repo)

	req := validProjectCreateRequest()
	req.AccessType = "private"
	req.AccessUserIDs = []uint{2, 2, 3}
	detail, err := uc.AdminCreateListed(context.Background(), 9, req, "req-access", "/v1/admin/projects")
	require.NoError(t, err)
	require.Equal(t, domain.ProjectAccessPrivate, detail.Project.AccessType)
	require.Len(t, detail.Accesses, 2)
	require.Equal(t, uint(2), detail.Accesses[0].UserID)
	require.Equal(t, uint(9), detail.Accesses[0].GrantedBy)

	req.AccessType = "public"
	detail, err = uc.AdminCreateListed(context.Background(), 9, req, "req-public", "/v1/admin/projects")
	require.NoError(t, err)
	require.Empty(t, detail.Accesses)
}

func TestProjectUseCaseResubmitNormalizesApplicationAndLog(t *testing.T) {
	repo := &fakeProjectRepo{}
	uc := NewProjectUseCase(repo)

	req := CreateProjectRequest{
		Name:           "GitHub Updated",
		TargetPlatform: "github.com",
		AccessType:     "public",
		LooseMatch:     true,
		MailRules: []ProjectMailRuleRequest{
			{RuleType: "sender", Pattern: "noreply@github.com", Enabled: true},
			{RuleType: "recipient", Pattern: "exact", Enabled: true},
		},
	}
	detail, err := uc.Resubmit(context.Background(), 7, 55, req, "req-resubmit", "/v1/projects/:projectId/resubmit")
	require.NoError(t, err)
	require.Equal(t, uint(55), detail.Project.ID)
	require.Equal(t, uint(7), *detail.Project.ApplicantUserID)
	require.Equal(t, domain.ProjectStatusReviewing, detail.Project.Status)
	require.Empty(t, detail.Project.ReviewReason)
	require.Len(t, detail.MailRules, 2)
	require.Equal(t, "core.project.resubmit", repo.log.OperationType)
	require.Equal(t, "55", repo.log.ResourceID)
	require.Equal(t, "req-resubmit", repo.log.RequestID)
}

func TestProjectUseCaseAdminReviewTransitions(t *testing.T) {
	repo := &fakeProjectRepo{detail: validProjectDetailForUseCase()}
	uc := NewProjectUseCase(repo)
	scanned := uint(0)
	uc.SetHistoryScan(func(_ context.Context, projectID uint, _ string) error {
		scanned = projectID
		return nil
	})

	approved, err := uc.AdminApprove(context.Background(), 9, 55, "req-approve", "/v1/admin/projects/:projectId/approve")
	require.NoError(t, err)
	require.Equal(t, domain.ProjectStatusListed, approved.Project.Status)
	require.Empty(t, approved.Project.ReviewReason)
	require.Equal(t, "core.project.approve", repo.log.OperationType)
	require.Equal(t, uint(55), scanned)

	repo.detail = validProjectDetailForUseCase()
	rejected, err := uc.AdminReject(context.Background(), 9, 56, "规则不清晰", "req-reject", "/v1/admin/projects/:projectId/reject")
	require.NoError(t, err)
	require.Equal(t, domain.ProjectStatusDelisted, rejected.Project.Status)
	require.Equal(t, "规则不清晰", rejected.Project.ReviewReason)
	require.Equal(t, "core.project.reject", repo.log.OperationType)
}

func TestProjectUseCaseAdminBulkReject(t *testing.T) {
	repo := &fakeProjectRepo{}
	uc := NewProjectUseCase(repo)

	result, err := uc.AdminBulkReject(
		context.Background(),
		9,
		ProjectBulkSelection{Mode: ProjectSelectionModeIDs, ProjectIDs: []uint{5, 6}},
		"  资料不完整  ",
		"req-bulk-reject",
		"/v1/admin/projects/reject",
	)

	require.NoError(t, err)
	require.Equal(t, 2, result.Affected)
	require.Equal(t, "资料不完整", repo.bulkReviewReason)
	require.Equal(t, "core.project.bulk_reject", repo.log.OperationType)
}

func TestProjectUseCaseAdminApproveWithConfig(t *testing.T) {
	repo := &fakeProjectRepo{}
	uc := NewProjectUseCase(repo)
	scanned := uint(0)
	uc.SetHistoryScan(func(_ context.Context, projectID uint, _ string) error {
		scanned = projectID
		return nil
	})

	detail, err := uc.AdminApproveWithConfig(context.Background(), 9, 55, validProjectCreateRequest(), "req-approve-config", "/v1/admin/projects/:projectId/approve")
	require.NoError(t, err)
	require.Equal(t, uint(55), detail.Project.ID)
	require.Equal(t, domain.ProjectStatusListed, detail.Project.Status)
	require.Len(t, detail.Products, 1)
	require.Len(t, detail.MailRules, 2)
	require.Equal(t, "core.project.approve", repo.log.OperationType)
	require.Equal(t, uint(55), scanned)
}

func validProjectCreateRequest() CreateProjectRequest {
	return CreateProjectRequest{
		Name:           "GitHub",
		TargetPlatform: "github.com",
		AccessType:     "public",
		LooseMatch:     true,
		Products: []ProjectProductRequest{
			{
				Type:                    "microsoft",
				Status:                  "enabled",
				CodeEnabled:             true,
				PurchaseEnabled:         false,
				CodePrice:               "0.100000",
				CodeSupplierPrice:       "0.050000",
				PurchasePrice:           "0",
				PurchaseSupplierPrice:   "0",
				CodeWindowMinutes:       10,
				ActivationWindowMinutes: 60,
				WarrantyMinutes:         60,
				MainWeight:              1,
			},
		},
		MailRules: []ProjectMailRuleRequest{
			{RuleType: "sender", Pattern: ".*", Enabled: true},
			{RuleType: "recipient", Pattern: "exact", Enabled: true},
		},
	}
}

func validProjectDetailForUseCase() *domain.ProjectDetail {
	return &domain.ProjectDetail{
		Project: domain.Project{
			ID:             1,
			Name:           "GitHub",
			TargetPlatform: "github.com",
			Status:         domain.ProjectStatusReviewing,
			AccessType:     domain.ProjectAccessPublic,
			LooseMatch:     true,
		},
		Products: []domain.Product{
			{ID: 1, ProjectID: 1, Type: domain.ProductTypeMicrosoft, Status: domain.ProductStatusEnabled},
		},
		MailRules: []domain.MailRule{
			{ID: 1, ProjectID: 1, RuleType: domain.MailRuleSender, Pattern: ".*", Enabled: true},
			{ID: 2, ProjectID: 1, RuleType: domain.MailRuleRecipient, Pattern: "exact", Enabled: true},
		},
	}
}
