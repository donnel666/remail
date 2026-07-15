package app

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	moneyfmt "github.com/donnel666/remail/internal/money"
)

const (
	projectNameMax           = 120
	projectTargetPlatformMax = 120
	projectLogoURLMax        = 500
	projectDescriptionMax    = 1000
	projectRulePatternMax    = 500
)

// ProjectRepository persists Project aggregates.
type ProjectRepository interface {
	CreateWithLog(ctx context.Context, detail *domain.ProjectDetail, log *governancedomain.OperationLog) error
	ResubmitWithLog(ctx context.Context, applicantUserID uint, detail *domain.ProjectDetail, log *governancedomain.OperationLog) error
	UpdateWithLog(ctx context.Context, detail *domain.ProjectDetail, log *governancedomain.OperationLog) error
	ApproveWithConfigAndLog(ctx context.Context, detail *domain.ProjectDetail, log *governancedomain.OperationLog) error
	TransitionWithLog(ctx context.Context, projectID uint, from domain.ProjectStatus, to domain.ProjectStatus, reviewReason string, log *governancedomain.OperationLog) (*domain.ProjectDetail, error)
	DeleteWithLog(ctx context.Context, projectID uint, log *governancedomain.OperationLog) error
	BulkTransitionWithLog(ctx context.Context, filter ProjectListFilter, from domain.ProjectStatus, to domain.ProjectStatus, log *governancedomain.OperationLog) (int, error)
	BulkDeleteWithLog(ctx context.Context, filter ProjectListFilter, log *governancedomain.OperationLog) (int, error)
	ListAccesses(ctx context.Context, projectID uint) ([]domain.ProjectAccess, error)
	GrantAccessWithLog(ctx context.Context, projectID, userID, grantedBy uint, log *governancedomain.OperationLog) (*domain.ProjectAccess, error)
	RevokeAccessWithLog(ctx context.Context, projectID, userID uint, log *governancedomain.OperationLog) error
	List(ctx context.Context, filter ProjectListFilter, offset, limit int) ([]ProjectSummary, error)
	Count(ctx context.Context, filter ProjectListFilter) (int64, error)
	Facets(ctx context.Context, filter ProjectListFilter) (*ProjectListFacets, error)
	FindDetail(ctx context.Context, projectID uint, userID uint, isAdmin bool) (*domain.ProjectDetail, error)
}

type ProjectListScope string

const (
	ProjectListScopeVisible ProjectListScope = "visible"
	ProjectListScopeMine    ProjectListScope = "mine"
	ProjectListScopeAll     ProjectListScope = "all"
)

type ProjectListFilter struct {
	Scope          ProjectListScope
	UserID         uint
	IsAdmin        bool
	IDs            []uint
	Status         domain.ProjectStatus
	AccessType     domain.ProjectAccessType
	LooseMatch     *bool
	ProductType    domain.ProductType
	CreatedFrom    *time.Time
	CreatedTo      *time.Time
	Search         string
	TargetPlatform string
}

type ProjectStatusFacets struct {
	All       int64
	Listed    int64
	Reviewing int64
	Delisted  int64
}

type ProjectAccessFacets struct {
	All     int64
	Public  int64
	Private int64
}

type ProjectMatchFacets struct {
	All    int64
	Loose  int64
	Strict int64
}

type ProjectProductTypeFacets struct {
	All       int64
	Microsoft int64
	Domain    int64
}

type ProjectListFacets struct {
	Status      ProjectStatusFacets
	Access      ProjectAccessFacets
	Match       ProjectMatchFacets
	ProductType ProjectProductTypeFacets
}

type ProjectSelectionMode string

const (
	ProjectSelectionModeIDs    ProjectSelectionMode = "ids"
	ProjectSelectionModeFilter ProjectSelectionMode = "filter"
)

type ProjectBulkSelection struct {
	Mode       ProjectSelectionMode
	ProjectIDs []uint
	Filter     ProjectListFilter
}

type ProjectBulkResult struct {
	Affected int
}

type ProjectSummary struct {
	Project       domain.Project
	Products      []domain.Product
	ProductCount  int
	MailRuleCount int
}

type ProjectListResult struct {
	Items  []ProjectSummary
	Total  int64
	Offset int
	Limit  int
	Facets *ProjectListFacets
}

type CreateProjectRequest struct {
	Name           string
	TargetPlatform string
	LogoURL        string
	Description    string
	AccessType     string
	AccessUserIDs  []uint
	LooseMatch     bool
	Products       []ProjectProductRequest
	MailRules      []ProjectMailRuleRequest
}

type ProjectProductRequest struct {
	Type                    string
	Status                  string
	CodeEnabled             bool
	PurchaseEnabled         bool
	CodePrice               string
	PurchasePrice           string
	CodeSupplierPrice       string
	PurchaseSupplierPrice   string
	CodeWindowMinutes       int
	ActivationWindowMinutes int
	WarrantyMinutes         int
	MainWeight              int
	DotWeight               int
	PlusWeight              int
}

type OrderingQuote struct {
	ProjectID               uint
	ProductID               uint
	ProductType             domain.ProductType
	PayAmount               string
	SupplierAmount          string
	CodeWindowMinutes       int
	ActivationWindowMinutes int
	WarrantyMinutes         int
}

type ProjectMailRuleRequest struct {
	RuleType string
	Pattern  string
	Enabled  bool
}

// ProjectUseCase handles project/product/rule commands.
type ProjectUseCase struct {
	projects ProjectRepository
}

func NewProjectUseCase(projects ProjectRepository) *ProjectUseCase {
	return &ProjectUseCase{projects: projects}
}

func (uc *ProjectUseCase) List(ctx context.Context, filter ProjectListFilter, offset, limit int) (*ProjectListResult, error) {
	normalized, err := normalizeProjectListFilter(filter)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	total, err := uc.projects.Count(ctx, normalized)
	if err != nil {
		return nil, err
	}
	items, err := uc.projects.List(ctx, normalized, offset, limit)
	if err != nil {
		return nil, err
	}
	facets, err := uc.projects.Facets(ctx, projectListFacetBaseFilter(normalized))
	if err != nil {
		return nil, err
	}
	return &ProjectListResult{Items: items, Total: total, Offset: offset, Limit: limit, Facets: facets}, nil
}

func (uc *ProjectUseCase) Get(ctx context.Context, projectID uint, userID uint, isAdmin bool) (*domain.ProjectDetail, error) {
	if projectID == 0 {
		return nil, domain.ErrProjectNotFound
	}
	detail, err := uc.projects.FindDetail(ctx, projectID, userID, isAdmin)
	if err != nil {
		return nil, err
	}
	if detail == nil {
		return nil, domain.ErrProjectNotFound
	}
	return detail, nil
}

func (uc *ProjectUseCase) GetOrderingQuote(ctx context.Context, projectID uint, productID uint, buyerUserID uint, serviceMode string) (*OrderingQuote, error) {
	if projectID == 0 || productID == 0 || buyerUserID == 0 {
		return nil, domain.ErrInvalidProject
	}
	mode := strings.ToLower(strings.TrimSpace(serviceMode))
	if mode != "code" && mode != "purchase" {
		return nil, domain.ErrInvalidProduct
	}
	detail, err := uc.Get(ctx, projectID, buyerUserID, false)
	if err != nil {
		return nil, err
	}
	if detail == nil {
		return nil, domain.ErrForbiddenProject
	}
	if detail.Project.Status != domain.ProjectStatusListed {
		return nil, domain.ErrInvalidProjectStatus
	}

	var product *domain.Product
	for i := range detail.Products {
		if detail.Products[i].ID == productID {
			product = &detail.Products[i]
			break
		}
	}
	if product == nil || product.ProjectID != projectID || product.Status != domain.ProductStatusEnabled {
		return nil, domain.ErrInvalidProduct
	}

	quote := &OrderingQuote{
		ProjectID:               projectID,
		ProductID:               product.ID,
		ProductType:             product.Type,
		CodeWindowMinutes:       product.CodeWindowMinutes,
		ActivationWindowMinutes: product.ActivationWindowMinutes,
		WarrantyMinutes:         product.WarrantyMinutes,
	}
	switch mode {
	case "code":
		if !product.CodeEnabled || product.CodeWindowMinutes <= 0 {
			return nil, domain.ErrInvalidProduct
		}
		payAmount, err := normalizeOrderingAmount(product.CodePrice)
		if err != nil {
			return nil, err
		}
		supplierAmount, err := normalizeOrderingAmount(product.CodeSupplierPrice)
		if err != nil {
			return nil, err
		}
		quote.PayAmount = payAmount
		quote.SupplierAmount = supplierAmount
	case "purchase":
		if !product.PurchaseEnabled || product.ActivationWindowMinutes <= 0 || product.WarrantyMinutes <= 0 {
			return nil, domain.ErrInvalidProduct
		}
		payAmount, err := normalizeOrderingAmount(product.PurchasePrice)
		if err != nil {
			return nil, err
		}
		supplierAmount, err := normalizeOrderingAmount(product.PurchaseSupplierPrice)
		if err != nil {
			return nil, err
		}
		quote.PayAmount = payAmount
		quote.SupplierAmount = supplierAmount
	}
	return quote, nil
}

func (uc *ProjectUseCase) Apply(ctx context.Context, applicantUserID uint, req CreateProjectRequest, requestID, path string) (*domain.ProjectDetail, error) {
	project, err := normalizeProject(req, domain.ProjectStatusReviewing)
	if err != nil {
		return nil, err
	}
	project.ApplicantUserID = &applicantUserID

	rules, err := normalizeMailRuleRequests(req.MailRules, false, project.LooseMatch)
	if err != nil {
		return nil, err
	}
	detail := &domain.ProjectDetail{
		Project:   project,
		MailRules: rules,
	}
	log := projectOperationLog(applicantUserID, requestID, path, "core.project.apply", "project", "new", "success", "Project application created.")
	if err := uc.projects.CreateWithLog(ctx, detail, log); err != nil {
		return nil, err
	}
	return detail, nil
}

func (uc *ProjectUseCase) Resubmit(ctx context.Context, applicantUserID, projectID uint, req CreateProjectRequest, requestID, path string) (*domain.ProjectDetail, error) {
	if projectID == 0 {
		return nil, domain.ErrProjectNotFound
	}
	project, err := normalizeProject(req, domain.ProjectStatusReviewing)
	if err != nil {
		return nil, err
	}
	project.ID = projectID
	project.ApplicantUserID = &applicantUserID
	project.ReviewReason = ""

	rules, err := normalizeMailRuleRequests(req.MailRules, false, project.LooseMatch)
	if err != nil {
		return nil, err
	}
	detail := &domain.ProjectDetail{
		Project:   project,
		MailRules: rules,
	}
	log := projectOperationLog(applicantUserID, requestID, path, "core.project.resubmit", "project", strconv.FormatUint(uint64(projectID), 10), "success", "Project application resubmitted.")
	if err := uc.projects.ResubmitWithLog(ctx, applicantUserID, detail, log); err != nil {
		return nil, err
	}
	return detail, nil
}

func (uc *ProjectUseCase) AdminCreateListed(ctx context.Context, operatorUserID uint, req CreateProjectRequest, requestID, path string) (*domain.ProjectDetail, error) {
	project, err := normalizeProject(req, domain.ProjectStatusListed)
	if err != nil {
		return nil, err
	}
	products, err := normalizeProductRequests(req.Products, true)
	if err != nil {
		return nil, err
	}
	rules, err := normalizeMailRuleRequests(req.MailRules, true, project.LooseMatch)
	if err != nil {
		return nil, err
	}
	accesses, err := normalizeProjectAccessRequests(project.AccessType, req.AccessUserIDs, operatorUserID)
	if err != nil {
		return nil, err
	}

	detail := &domain.ProjectDetail{
		Project:   project,
		Products:  products,
		MailRules: rules,
		Accesses:  accesses,
	}
	log := projectOperationLog(operatorUserID, requestID, path, "core.project.create", "project", "new", "success", "Listed project created.")
	if err := uc.projects.CreateWithLog(ctx, detail, log); err != nil {
		return nil, err
	}
	return detail, nil
}

func (uc *ProjectUseCase) AdminUpdate(ctx context.Context, operatorUserID, projectID uint, req CreateProjectRequest, requestID, path string) (*domain.ProjectDetail, error) {
	if projectID == 0 {
		return nil, domain.ErrProjectNotFound
	}
	project, err := normalizeProject(req, domain.ProjectStatusDelisted)
	if err != nil {
		return nil, err
	}
	project.ID = projectID
	products, err := normalizeProductRequests(req.Products, true)
	if err != nil {
		return nil, err
	}
	rules, err := normalizeMailRuleRequests(req.MailRules, true, project.LooseMatch)
	if err != nil {
		return nil, err
	}
	accesses, err := normalizeProjectAccessRequests(project.AccessType, req.AccessUserIDs, operatorUserID)
	if err != nil {
		return nil, err
	}

	detail := &domain.ProjectDetail{
		Project:   project,
		Products:  products,
		MailRules: rules,
		Accesses:  accesses,
	}
	log := projectOperationLog(operatorUserID, requestID, path, "core.project.update", "project", strconv.FormatUint(uint64(projectID), 10), "success", "Project updated.")
	if err := uc.projects.UpdateWithLog(ctx, detail, log); err != nil {
		return nil, err
	}
	return detail, nil
}

func (uc *ProjectUseCase) AdminApprove(ctx context.Context, operatorUserID, projectID uint, requestID, path string) (*domain.ProjectDetail, error) {
	log := projectOperationLog(operatorUserID, requestID, path, "core.project.approve", "project", strconv.FormatUint(uint64(projectID), 10), "success", "Project approved.")
	return uc.projects.TransitionWithLog(ctx, projectID, domain.ProjectStatusReviewing, domain.ProjectStatusListed, "", log)
}

func (uc *ProjectUseCase) AdminApproveWithConfig(ctx context.Context, operatorUserID, projectID uint, req CreateProjectRequest, requestID, path string) (*domain.ProjectDetail, error) {
	if projectID == 0 {
		return nil, domain.ErrProjectNotFound
	}
	project, err := normalizeProject(req, domain.ProjectStatusListed)
	if err != nil {
		return nil, err
	}
	project.ID = projectID
	products, err := normalizeProductRequests(req.Products, true)
	if err != nil {
		return nil, err
	}
	rules, err := normalizeMailRuleRequests(req.MailRules, true, project.LooseMatch)
	if err != nil {
		return nil, err
	}
	accesses, err := normalizeProjectAccessRequests(project.AccessType, req.AccessUserIDs, operatorUserID)
	if err != nil {
		return nil, err
	}
	detail := &domain.ProjectDetail{
		Project:   project,
		Products:  products,
		MailRules: rules,
		Accesses:  accesses,
	}
	log := projectOperationLog(operatorUserID, requestID, path, "core.project.approve", "project", strconv.FormatUint(uint64(projectID), 10), "success", "Project approved with configuration.")
	if err := uc.projects.ApproveWithConfigAndLog(ctx, detail, log); err != nil {
		return nil, err
	}
	return detail, nil
}

func (uc *ProjectUseCase) AdminReject(ctx context.Context, operatorUserID, projectID uint, reviewReason, requestID, path string) (*domain.ProjectDetail, error) {
	reason := strings.TrimSpace(reviewReason)
	if reason == "" || len([]rune(reason)) > 500 {
		return nil, domain.ErrInvalidProject
	}
	log := projectOperationLog(operatorUserID, requestID, path, "core.project.reject", "project", strconv.FormatUint(uint64(projectID), 10), "success", "Project rejected.")
	return uc.projects.TransitionWithLog(ctx, projectID, domain.ProjectStatusReviewing, domain.ProjectStatusDelisted, reason, log)
}

func (uc *ProjectUseCase) AdminDuplicate(ctx context.Context, operatorUserID, projectID uint, reviewReason, requestID, path string) (*domain.ProjectDetail, error) {
	reason := strings.TrimSpace(reviewReason)
	if reason == "" || len([]rune(reason)) > 500 {
		return nil, domain.ErrInvalidProject
	}
	log := projectOperationLog(operatorUserID, requestID, path, "core.project.duplicate", "project", strconv.FormatUint(uint64(projectID), 10), "success", "Project marked as duplicate.")
	return uc.projects.TransitionWithLog(ctx, projectID, domain.ProjectStatusReviewing, domain.ProjectStatusDelisted, reason, log)
}

func (uc *ProjectUseCase) AdminRelist(ctx context.Context, operatorUserID, projectID uint, requestID, path string) (*domain.ProjectDetail, error) {
	log := projectOperationLog(operatorUserID, requestID, path, "core.project.relist", "project", strconv.FormatUint(uint64(projectID), 10), "success", "Project relisted.")
	return uc.projects.TransitionWithLog(ctx, projectID, domain.ProjectStatusDelisted, domain.ProjectStatusListed, "", log)
}

func (uc *ProjectUseCase) AdminDelist(ctx context.Context, operatorUserID, projectID uint, requestID, path string) (*domain.ProjectDetail, error) {
	log := projectOperationLog(operatorUserID, requestID, path, "core.project.delist", "project", strconv.FormatUint(uint64(projectID), 10), "success", "Project delisted.")
	return uc.projects.TransitionWithLog(ctx, projectID, domain.ProjectStatusListed, domain.ProjectStatusDelisted, "", log)
}

func (uc *ProjectUseCase) AdminDelete(ctx context.Context, operatorUserID, projectID uint, requestID, path string) error {
	if projectID == 0 {
		return domain.ErrProjectNotFound
	}
	log := projectOperationLog(operatorUserID, requestID, path, "core.project.delete", "project", strconv.FormatUint(uint64(projectID), 10), "success", "Project deleted.")
	return uc.projects.DeleteWithLog(ctx, projectID, log)
}

func (uc *ProjectUseCase) AdminBulkRelist(ctx context.Context, operatorUserID uint, selection ProjectBulkSelection, requestID, path string) (*ProjectBulkResult, error) {
	filter, err := uc.normalizeBulkSelection(selection)
	if err != nil {
		return nil, err
	}
	log := projectOperationLog(operatorUserID, requestID, path, "core.project.bulk_relist", "project", "bulk", "success", "Projects relisted.")
	affected, err := uc.projects.BulkTransitionWithLog(ctx, filter, domain.ProjectStatusDelisted, domain.ProjectStatusListed, log)
	if err != nil {
		return nil, err
	}
	return &ProjectBulkResult{Affected: affected}, nil
}

func (uc *ProjectUseCase) AdminBulkDelist(ctx context.Context, operatorUserID uint, selection ProjectBulkSelection, requestID, path string) (*ProjectBulkResult, error) {
	filter, err := uc.normalizeBulkSelection(selection)
	if err != nil {
		return nil, err
	}
	log := projectOperationLog(operatorUserID, requestID, path, "core.project.bulk_delist", "project", "bulk", "success", "Projects delisted.")
	affected, err := uc.projects.BulkTransitionWithLog(ctx, filter, domain.ProjectStatusListed, domain.ProjectStatusDelisted, log)
	if err != nil {
		return nil, err
	}
	return &ProjectBulkResult{Affected: affected}, nil
}

func (uc *ProjectUseCase) AdminBulkDelete(ctx context.Context, operatorUserID uint, selection ProjectBulkSelection, requestID, path string) (*ProjectBulkResult, error) {
	filter, err := uc.normalizeBulkSelection(selection)
	if err != nil {
		return nil, err
	}
	log := projectOperationLog(operatorUserID, requestID, path, "core.project.bulk_delete", "project", "bulk", "success", "Projects deleted.")
	affected, err := uc.projects.BulkDeleteWithLog(ctx, filter, log)
	if err != nil {
		return nil, err
	}
	return &ProjectBulkResult{Affected: affected}, nil
}

func (uc *ProjectUseCase) AdminListAccesses(ctx context.Context, projectID uint) ([]domain.ProjectAccess, error) {
	if projectID == 0 {
		return nil, domain.ErrProjectNotFound
	}
	return uc.projects.ListAccesses(ctx, projectID)
}

func (uc *ProjectUseCase) AdminGrantAccess(ctx context.Context, operatorUserID, projectID, userID uint, requestID, path string) (*domain.ProjectAccess, error) {
	if projectID == 0 {
		return nil, domain.ErrProjectNotFound
	}
	if userID == 0 {
		return nil, domain.ErrInvalidProject
	}
	log := projectOperationLog(
		operatorUserID,
		requestID,
		path,
		"core.project.grant_access",
		"project",
		strconv.FormatUint(uint64(projectID), 10),
		"success",
		"Project access granted.",
	)
	return uc.projects.GrantAccessWithLog(ctx, projectID, userID, operatorUserID, log)
}

func (uc *ProjectUseCase) AdminRevokeAccess(ctx context.Context, operatorUserID, projectID, userID uint, requestID, path string) error {
	if projectID == 0 {
		return domain.ErrProjectNotFound
	}
	if userID == 0 {
		return domain.ErrInvalidProject
	}
	log := projectOperationLog(
		operatorUserID,
		requestID,
		path,
		"core.project.revoke_access",
		"project",
		strconv.FormatUint(uint64(projectID), 10),
		"success",
		"Project access revoked.",
	)
	return uc.projects.RevokeAccessWithLog(ctx, projectID, userID, log)
}

func (uc *ProjectUseCase) normalizeBulkSelection(selection ProjectBulkSelection) (ProjectListFilter, error) {
	filter := selection.Filter
	filter.Scope = ProjectListScopeAll
	filter.IsAdmin = true
	switch selection.Mode {
	case ProjectSelectionModeIDs:
		if len(selection.ProjectIDs) == 0 {
			return ProjectListFilter{}, domain.ErrInvalidProject
		}
		filter.IDs = uniqueProjectIDs(selection.ProjectIDs)
		if len(filter.IDs) == 0 {
			return ProjectListFilter{}, domain.ErrInvalidProject
		}
	case ProjectSelectionModeFilter:
	default:
		return ProjectListFilter{}, domain.ErrInvalidProject
	}
	return normalizeProjectListFilter(filter)
}

func normalizeProjectListFilter(filter ProjectListFilter) (ProjectListFilter, error) {
	filter.Search = strings.TrimSpace(filter.Search)
	filter.TargetPlatform = strings.TrimSpace(filter.TargetPlatform)
	switch filter.Scope {
	case "", ProjectListScopeVisible:
		filter.Scope = ProjectListScopeVisible
	case ProjectListScopeMine:
	case ProjectListScopeAll:
		if !filter.IsAdmin {
			filter.Scope = ProjectListScopeVisible
		}
	default:
		return filter, domain.ErrInvalidProject
	}
	if filter.Status != "" && !domain.IsValidProjectStatus(filter.Status) {
		return filter, domain.ErrInvalidProjectStatus
	}
	if filter.AccessType != "" && !domain.IsValidProjectAccessType(filter.AccessType) {
		return filter, domain.ErrInvalidProject
	}
	if filter.ProductType != "" && !domain.IsValidProductType(filter.ProductType) {
		return filter, domain.ErrInvalidProject
	}
	return filter, nil
}

func projectListFacetBaseFilter(filter ProjectListFilter) ProjectListFilter {
	filter.Status = ""
	filter.AccessType = ""
	filter.LooseMatch = nil
	filter.ProductType = ""
	return filter
}

func normalizeProject(req CreateProjectRequest, status domain.ProjectStatus) (domain.Project, error) {
	name := strings.TrimSpace(req.Name)
	targetPlatform := strings.TrimSpace(req.TargetPlatform)
	logoURL := strings.TrimSpace(req.LogoURL)
	description := strings.TrimSpace(req.Description)
	if name == "" || len([]rune(name)) > projectNameMax {
		return domain.Project{}, domain.ErrInvalidProject
	}
	if targetPlatform == "" || len([]rune(targetPlatform)) > projectTargetPlatformMax {
		return domain.Project{}, domain.ErrInvalidProject
	}
	if len([]rune(logoURL)) > projectLogoURLMax || len([]rune(description)) > projectDescriptionMax {
		return domain.Project{}, domain.ErrInvalidProject
	}
	accessType := domain.ProjectAccessType("")
	if strings.TrimSpace(req.AccessType) == "" {
		accessType = domain.ProjectAccessPublic
	} else {
		normalized, ok := domain.NormalizeProjectAccessType(req.AccessType)
		if !ok {
			return domain.Project{}, domain.ErrInvalidProject
		}
		accessType = normalized
	}
	return domain.Project{
		Name:           name,
		TargetPlatform: targetPlatform,
		LogoURL:        logoURL,
		Description:    description,
		Status:         status,
		AccessType:     accessType,
		LooseMatch:     req.LooseMatch,
	}, nil
}

func normalizeProductRequests(requests []ProjectProductRequest, requireEnabled bool) ([]domain.Product, error) {
	if len(requests) == 0 {
		return nil, domain.ErrInvalidProduct
	}
	seenTypes := make(map[domain.ProductType]struct{}, len(requests))
	products := make([]domain.Product, 0, len(requests))
	hasEnabled := false

	for _, req := range requests {
		productType, ok := domain.NormalizeProductType(req.Type)
		if !ok {
			return nil, domain.ErrInvalidProduct
		}
		if _, exists := seenTypes[productType]; exists {
			return nil, domain.ErrInvalidProduct
		}
		seenTypes[productType] = struct{}{}

		status := domain.ProductStatus("")
		if strings.TrimSpace(req.Status) == "" {
			status = domain.ProductStatusEnabled
		} else {
			normalized, ok := domain.NormalizeProductStatus(req.Status)
			if !ok {
				return nil, domain.ErrInvalidProduct
			}
			status = normalized
		}
		if status == domain.ProductStatusEnabled {
			hasEnabled = true
		}
		if !req.CodeEnabled && !req.PurchaseEnabled {
			return nil, domain.ErrInvalidProduct
		}
		if req.CodeWindowMinutes < 0 ||
			req.ActivationWindowMinutes < 0 ||
			req.WarrantyMinutes < 0 ||
			req.MainWeight < 0 ||
			req.DotWeight < 0 ||
			req.PlusWeight < 0 {
			return nil, domain.ErrInvalidProduct
		}

		codePrice, ok := domain.NormalizeMoney(req.CodePrice)
		if !ok {
			return nil, domain.ErrInvalidProduct
		}
		purchasePrice, ok := domain.NormalizeMoney(req.PurchasePrice)
		if !ok {
			return nil, domain.ErrInvalidProduct
		}
		codeSupplierPrice, ok := domain.NormalizeMoney(req.CodeSupplierPrice)
		if !ok {
			return nil, domain.ErrInvalidProduct
		}
		purchaseSupplierPrice, ok := domain.NormalizeMoney(req.PurchaseSupplierPrice)
		if !ok {
			return nil, domain.ErrInvalidProduct
		}

		product := domain.Product{
			Type:                    productType,
			Status:                  status,
			CodeEnabled:             req.CodeEnabled,
			PurchaseEnabled:         req.PurchaseEnabled,
			CodePrice:               codePrice,
			PurchasePrice:           purchasePrice,
			CodeSupplierPrice:       codeSupplierPrice,
			PurchaseSupplierPrice:   purchaseSupplierPrice,
			CodeWindowMinutes:       req.CodeWindowMinutes,
			ActivationWindowMinutes: req.ActivationWindowMinutes,
			WarrantyMinutes:         req.WarrantyMinutes,
			MainWeight:              req.MainWeight,
			DotWeight:               req.DotWeight,
			PlusWeight:              req.PlusWeight,
		}
		if product.CodeEnabled && product.CodeWindowMinutes <= 0 {
			return nil, domain.ErrInvalidProduct
		}
		if product.PurchaseEnabled && (product.ActivationWindowMinutes <= 0 || product.WarrantyMinutes <= 0) {
			return nil, domain.ErrInvalidProduct
		}
		if product.Type == domain.ProductTypeMicrosoft && product.MainWeight+product.DotWeight+product.PlusWeight <= 0 {
			return nil, domain.ErrInvalidProduct
		}
		if product.Type == domain.ProductTypeDomain {
			product.MainWeight = 0
			product.DotWeight = 0
			product.PlusWeight = 0
		}
		products = append(products, product)
	}

	if requireEnabled && !hasEnabled {
		return nil, domain.ErrInvalidProduct
	}
	return products, nil
}

func normalizeProjectAccessRequests(accessType domain.ProjectAccessType, userIDs []uint, grantedBy uint) ([]domain.ProjectAccess, error) {
	if accessType != domain.ProjectAccessPrivate {
		return nil, nil
	}
	seen := make(map[uint]struct{}, len(userIDs))
	accesses := make([]domain.ProjectAccess, 0, len(userIDs))
	for _, userID := range userIDs {
		if userID == 0 {
			return nil, domain.ErrInvalidProject
		}
		if _, exists := seen[userID]; exists {
			continue
		}
		seen[userID] = struct{}{}
		accesses = append(accesses, domain.ProjectAccess{
			UserID:    userID,
			GrantedBy: grantedBy,
		})
	}
	return accesses, nil
}

func normalizeMailRuleRequests(requests []ProjectMailRuleRequest, requireComplete bool, looseMatch bool) ([]domain.MailRule, error) {
	if len(requests) == 0 {
		if requireComplete {
			return nil, domain.ErrInvalidMailRule
		}
		return nil, nil
	}
	rules := make([]domain.MailRule, 0, len(requests))
	enabledTypes := make(map[domain.MailRuleType]struct{})
	for _, req := range requests {
		ruleType, ok := domain.NormalizeMailRuleType(req.RuleType)
		if !ok {
			return nil, domain.ErrInvalidMailRule
		}
		pattern := strings.TrimSpace(req.Pattern)
		if pattern == "" || len([]rune(pattern)) > projectRulePatternMax {
			return nil, domain.ErrInvalidMailRule
		}
		if ruleType == domain.MailRuleRecipient && !isValidRecipientPattern(pattern) {
			return nil, domain.ErrInvalidMailRule
		}
		if req.Enabled {
			enabledTypes[ruleType] = struct{}{}
		}
		rules = append(rules, domain.MailRule{
			RuleType: ruleType,
			Pattern:  pattern,
			Enabled:  req.Enabled,
		})
	}
	if requireComplete {
		for _, ruleType := range requiredMailRuleTypes(looseMatch) {
			if _, ok := enabledTypes[ruleType]; !ok {
				return nil, domain.ErrInvalidMailRule
			}
		}
	}
	return rules, nil
}

func isValidRecipientPattern(pattern string) bool {
	switch pattern {
	case "exact", "dot", "plus":
		return true
	default:
		return false
	}
}

func requiredMailRuleTypes(looseMatch bool) []domain.MailRuleType {
	if looseMatch {
		return []domain.MailRuleType{domain.MailRuleSender, domain.MailRuleRecipient}
	}
	return []domain.MailRuleType{domain.MailRuleSender, domain.MailRuleRecipient, domain.MailRuleSubject, domain.MailRuleBody}
}

func normalizeOrderingAmount(value string) (string, error) {
	amount, err := moneyfmt.Parse(value)
	if err != nil || amount.IsNegative() {
		return "", domain.ErrInvalidProduct
	}
	return moneyfmt.Format(amount), nil
}

func projectOperationLog(operatorUserID uint, requestID, path, operationType, resourceType, resourceID, result, summary string) *governancedomain.OperationLog {
	return &governancedomain.OperationLog{
		OperatorUserID: operatorUserID,
		OperationType:  operationType,
		ResourceType:   resourceType,
		ResourceID:     resourceID,
		Path:           path,
		Result:         result,
		SafeSummary:    summary,
		RequestID:      requestID,
	}
}

func uniqueProjectIDs(projectIDs []uint) []uint {
	seen := make(map[uint]struct{}, len(projectIDs))
	result := make([]uint, 0, len(projectIDs))
	for _, projectID := range projectIDs {
		if projectID == 0 {
			continue
		}
		if _, ok := seen[projectID]; ok {
			continue
		}
		seen[projectID] = struct{}{}
		result = append(result, projectID)
	}
	return result
}
