package infra

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ProjectModel struct {
	ID              uint      `gorm:"primaryKey;autoIncrement"`
	Name            string    `gorm:"type:varchar(120);not null"`
	TargetPlatform  string    `gorm:"type:varchar(120);not null;column:target_platform"`
	LogoURL         string    `gorm:"type:varchar(500);not null;default:'';column:logo_url"`
	Description     string    `gorm:"type:varchar(1000);not null;default:''"`
	Status          string    `gorm:"type:varchar(32);not null;default:'reviewing'"`
	AccessType      string    `gorm:"type:varchar(32);not null;default:'public';column:access_type"`
	ApplicantUserID *uint     `gorm:"column:applicant_user_id"`
	ReviewReason    string    `gorm:"type:varchar(500);not null;default:'';column:review_reason"`
	LooseMatch      bool      `gorm:"not null;column:loose_match"`
	CreatedAt       time.Time `gorm:"not null;autoCreateTime"`
	UpdatedAt       time.Time `gorm:"not null;autoUpdateTime"`
}

func (ProjectModel) TableName() string {
	return "projects"
}

func (m *ProjectModel) toDomain() domain.Project {
	return domain.Project{
		ID:              m.ID,
		Name:            m.Name,
		TargetPlatform:  m.TargetPlatform,
		LogoURL:         m.LogoURL,
		Description:     m.Description,
		Status:          domain.ProjectStatus(m.Status),
		AccessType:      domain.ProjectAccessType(m.AccessType),
		ApplicantUserID: m.ApplicantUserID,
		ReviewReason:    m.ReviewReason,
		LooseMatch:      m.LooseMatch,
		CreatedAt:       m.CreatedAt,
		UpdatedAt:       m.UpdatedAt,
	}
}

func projectModelFromDomain(project domain.Project) *ProjectModel {
	return &ProjectModel{
		ID:              project.ID,
		Name:            project.Name,
		TargetPlatform:  project.TargetPlatform,
		LogoURL:         project.LogoURL,
		Description:     project.Description,
		Status:          string(project.Status),
		AccessType:      string(project.AccessType),
		ApplicantUserID: project.ApplicantUserID,
		ReviewReason:    project.ReviewReason,
		LooseMatch:      project.LooseMatch,
		CreatedAt:       project.CreatedAt,
		UpdatedAt:       project.UpdatedAt,
	}
}

type ProjectProductModel struct {
	ID                      uint      `gorm:"primaryKey;autoIncrement"`
	ProjectID               uint      `gorm:"not null;column:project_id"`
	Type                    string    `gorm:"type:varchar(32);not null"`
	Status                  string    `gorm:"type:varchar(32);not null;default:'enabled'"`
	CodeEnabled             bool      `gorm:"not null;column:code_enabled"`
	PurchaseEnabled         bool      `gorm:"not null;column:purchase_enabled"`
	CodePrice               string    `gorm:"type:decimal(18,6);not null;default:0;column:code_price"`
	PurchasePrice           string    `gorm:"type:decimal(18,6);not null;default:0;column:purchase_price"`
	CodeSupplierPrice       string    `gorm:"type:decimal(18,6);not null;default:0;column:code_supplier_price"`
	PurchaseSupplierPrice   string    `gorm:"type:decimal(18,6);not null;default:0;column:purchase_supplier_price"`
	CodeWindowMinutes       int       `gorm:"not null;column:code_window_minutes"`
	ActivationWindowMinutes int       `gorm:"not null;column:activation_window_minutes"`
	WarrantyMinutes         int       `gorm:"not null;column:warranty_minutes"`
	MainWeight              int       `gorm:"not null;column:main_weight"`
	DotWeight               int       `gorm:"not null;column:dot_weight"`
	PlusWeight              int       `gorm:"not null;column:plus_weight"`
	CreatedAt               time.Time `gorm:"not null;autoCreateTime"`
	UpdatedAt               time.Time `gorm:"not null;autoUpdateTime"`
}

func (ProjectProductModel) TableName() string {
	return "project_products"
}

func (m *ProjectProductModel) toDomain() domain.Product {
	return domain.Product{
		ID:                      m.ID,
		ProjectID:               m.ProjectID,
		Type:                    domain.ProductType(m.Type),
		Status:                  domain.ProductStatus(m.Status),
		CodeEnabled:             m.CodeEnabled,
		PurchaseEnabled:         m.PurchaseEnabled,
		CodePrice:               m.CodePrice,
		PurchasePrice:           m.PurchasePrice,
		CodeSupplierPrice:       m.CodeSupplierPrice,
		PurchaseSupplierPrice:   m.PurchaseSupplierPrice,
		CodeWindowMinutes:       m.CodeWindowMinutes,
		ActivationWindowMinutes: m.ActivationWindowMinutes,
		WarrantyMinutes:         m.WarrantyMinutes,
		MainWeight:              m.MainWeight,
		DotWeight:               m.DotWeight,
		PlusWeight:              m.PlusWeight,
		CreatedAt:               m.CreatedAt,
		UpdatedAt:               m.UpdatedAt,
	}
}

func productModelFromDomain(product domain.Product) ProjectProductModel {
	return ProjectProductModel{
		ID:                      product.ID,
		ProjectID:               product.ProjectID,
		Type:                    string(product.Type),
		Status:                  string(product.Status),
		CodeEnabled:             product.CodeEnabled,
		PurchaseEnabled:         product.PurchaseEnabled,
		CodePrice:               product.CodePrice,
		PurchasePrice:           product.PurchasePrice,
		CodeSupplierPrice:       product.CodeSupplierPrice,
		PurchaseSupplierPrice:   product.PurchaseSupplierPrice,
		CodeWindowMinutes:       product.CodeWindowMinutes,
		ActivationWindowMinutes: product.ActivationWindowMinutes,
		WarrantyMinutes:         product.WarrantyMinutes,
		MainWeight:              product.MainWeight,
		DotWeight:               product.DotWeight,
		PlusWeight:              product.PlusWeight,
		CreatedAt:               product.CreatedAt,
		UpdatedAt:               product.UpdatedAt,
	}
}

type ProjectMailRuleModel struct {
	ID        uint      `gorm:"primaryKey;autoIncrement"`
	ProjectID uint      `gorm:"not null;column:project_id"`
	RuleType  string    `gorm:"type:varchar(32);not null;column:rule_type"`
	Pattern   string    `gorm:"type:varchar(500);not null"`
	Enabled   bool      `gorm:"not null"`
	CreatedAt time.Time `gorm:"not null;autoCreateTime"`
	UpdatedAt time.Time `gorm:"not null;autoUpdateTime"`
}

func (ProjectMailRuleModel) TableName() string {
	return "project_mail_rules"
}

func (m *ProjectMailRuleModel) toDomain() domain.MailRule {
	return domain.MailRule{
		ID:        m.ID,
		ProjectID: m.ProjectID,
		RuleType:  domain.MailRuleType(m.RuleType),
		Pattern:   m.Pattern,
		Enabled:   m.Enabled,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

func mailRuleModelFromDomain(rule domain.MailRule) ProjectMailRuleModel {
	return ProjectMailRuleModel{
		ID:        rule.ID,
		ProjectID: rule.ProjectID,
		RuleType:  string(rule.RuleType),
		Pattern:   rule.Pattern,
		Enabled:   rule.Enabled,
		CreatedAt: rule.CreatedAt,
		UpdatedAt: rule.UpdatedAt,
	}
}

type ProjectAccessModel struct {
	ID        uint      `gorm:"primaryKey;autoIncrement"`
	ProjectID uint      `gorm:"not null;column:project_id"`
	UserID    uint      `gorm:"not null;column:user_id"`
	GrantedBy uint      `gorm:"not null;column:granted_by"`
	CreatedAt time.Time `gorm:"not null;autoCreateTime"`
}

func (ProjectAccessModel) TableName() string {
	return "project_accesses"
}

func (m *ProjectAccessModel) toDomain() domain.ProjectAccess {
	return domain.ProjectAccess{
		ID:        m.ID,
		ProjectID: m.ProjectID,
		UserID:    m.UserID,
		GrantedBy: m.GrantedBy,
		CreatedAt: m.CreatedAt,
	}
}

type ProjectSummaryModel struct {
	ProjectModel
	ProductCount  int `gorm:"column:product_count"`
	MailRuleCount int `gorm:"column:mail_rule_count"`
}

type ProjectRepo struct {
	db            *gorm.DB
	operationLogs *governanceinfra.OperationLogRepo
}

func NewProjectRepo(db *gorm.DB) *ProjectRepo {
	return &ProjectRepo{
		db:            db,
		operationLogs: governanceinfra.NewOperationLogRepo(db),
	}
}

func (r *ProjectRepo) CreateWithLog(ctx context.Context, detail *domain.ProjectDetail, log *governancedomain.OperationLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		projectModel := projectModelFromDomain(detail.Project)
		if err := tx.Create(projectModel).Error; err != nil {
			if isDuplicateKeyError(err) {
				return domain.ErrDuplicateProject
			}
			return fmt.Errorf("create project: %w", err)
		}
		detail.Project = projectModel.toDomain()
		if log != nil {
			log.ResourceID = fmt.Sprintf("%d", projectModel.ID)
		}

		if len(detail.Products) > 0 {
			productModels := make([]ProjectProductModel, 0, len(detail.Products))
			for i := range detail.Products {
				detail.Products[i].ProjectID = projectModel.ID
				productModels = append(productModels, productModelFromDomain(detail.Products[i]))
			}
			if err := tx.Create(&productModels).Error; err != nil {
				if isDuplicateKeyError(err) {
					return domain.ErrInvalidProduct
				}
				return fmt.Errorf("create project products: %w", err)
			}
			for i := range productModels {
				detail.Products[i] = productModels[i].toDomain()
			}
		}

		if len(detail.MailRules) > 0 {
			ruleModels := make([]ProjectMailRuleModel, 0, len(detail.MailRules))
			for i := range detail.MailRules {
				detail.MailRules[i].ProjectID = projectModel.ID
				ruleModels = append(ruleModels, mailRuleModelFromDomain(detail.MailRules[i]))
			}
			if err := tx.Create(&ruleModels).Error; err != nil {
				return fmt.Errorf("create project mail rules: %w", err)
			}
			for i := range ruleModels {
				detail.MailRules[i] = ruleModels[i].toDomain()
			}
		}
		if err := r.replaceProjectAccesses(ctx, tx, projectModel.ID, detail); err != nil {
			return err
		}

		if log != nil {
			if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *ProjectRepo) ResubmitWithLog(ctx context.Context, applicantUserID uint, detail *domain.ProjectDetail, log *governancedomain.OperationLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var projectModel ProjectModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", detail.Project.ID).
			First(&projectModel).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrProjectNotFound
			}
			return fmt.Errorf("find project for resubmit: %w", err)
		}
		if projectModel.ApplicantUserID == nil || *projectModel.ApplicantUserID != applicantUserID {
			return domain.ErrForbiddenProject
		}
		if domain.ProjectStatus(projectModel.Status) != domain.ProjectStatusDelisted {
			return domain.ErrInvalidProjectStatus
		}

		projectModel.Name = detail.Project.Name
		projectModel.TargetPlatform = detail.Project.TargetPlatform
		projectModel.LogoURL = detail.Project.LogoURL
		projectModel.Description = detail.Project.Description
		projectModel.Status = string(domain.ProjectStatusReviewing)
		projectModel.AccessType = string(detail.Project.AccessType)
		projectModel.ReviewReason = ""
		projectModel.LooseMatch = detail.Project.LooseMatch
		if err := tx.Save(&projectModel).Error; err != nil {
			if isDuplicateKeyError(err) {
				return domain.ErrDuplicateProject
			}
			return fmt.Errorf("resubmit project: %w", err)
		}
		detail.Project = projectModel.toDomain()

		if err := tx.Where("project_id = ?", projectModel.ID).Delete(&ProjectMailRuleModel{}).Error; err != nil {
			return fmt.Errorf("replace project mail rules: %w", err)
		}
		if len(detail.MailRules) > 0 {
			ruleModels := make([]ProjectMailRuleModel, 0, len(detail.MailRules))
			for i := range detail.MailRules {
				detail.MailRules[i].ProjectID = projectModel.ID
				ruleModels = append(ruleModels, mailRuleModelFromDomain(detail.MailRules[i]))
			}
			if err := tx.Create(&ruleModels).Error; err != nil {
				return fmt.Errorf("create resubmitted project mail rules: %w", err)
			}
			for i := range ruleModels {
				detail.MailRules[i] = ruleModels[i].toDomain()
			}
		}

		var productModels []ProjectProductModel
		if err := tx.Where("project_id = ?", projectModel.ID).Order("id ASC").Find(&productModels).Error; err != nil {
			return fmt.Errorf("list resubmitted project products: %w", err)
		}
		detail.Products = make([]domain.Product, len(productModels))
		for i := range productModels {
			detail.Products[i] = productModels[i].toDomain()
		}

		if log != nil {
			log.ResourceID = fmt.Sprintf("%d", projectModel.ID)
			if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *ProjectRepo) UpdateWithLog(ctx context.Context, detail *domain.ProjectDetail, log *governancedomain.OperationLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var projectModel ProjectModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", detail.Project.ID).
			First(&projectModel).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrProjectNotFound
			}
			return fmt.Errorf("find project for update: %w", err)
		}
		if domain.ProjectStatus(projectModel.Status) == domain.ProjectStatusReviewing {
			return domain.ErrInvalidProjectStatus
		}

		projectModel.Name = detail.Project.Name
		projectModel.TargetPlatform = detail.Project.TargetPlatform
		projectModel.LogoURL = detail.Project.LogoURL
		projectModel.Description = detail.Project.Description
		projectModel.AccessType = string(detail.Project.AccessType)
		projectModel.LooseMatch = detail.Project.LooseMatch
		if err := tx.Save(&projectModel).Error; err != nil {
			if isDuplicateKeyError(err) {
				return domain.ErrDuplicateProject
			}
			return fmt.Errorf("update project: %w", err)
		}
		detail.Project = projectModel.toDomain()

		if err := r.replaceProductsAndRules(ctx, tx, projectModel.ID, detail); err != nil {
			return err
		}
		if err := r.replaceProjectAccesses(ctx, tx, projectModel.ID, detail); err != nil {
			return err
		}
		if log != nil {
			log.ResourceID = fmt.Sprintf("%d", projectModel.ID)
			if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *ProjectRepo) ApproveWithConfigAndLog(ctx context.Context, detail *domain.ProjectDetail, log *governancedomain.OperationLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var projectModel ProjectModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", detail.Project.ID).
			First(&projectModel).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrProjectNotFound
			}
			return fmt.Errorf("find project for approve with config: %w", err)
		}
		if domain.ProjectStatus(projectModel.Status) != domain.ProjectStatusReviewing {
			return domain.ErrInvalidProjectStatus
		}

		projectModel.Name = detail.Project.Name
		projectModel.TargetPlatform = detail.Project.TargetPlatform
		projectModel.LogoURL = detail.Project.LogoURL
		projectModel.Description = detail.Project.Description
		projectModel.Status = string(domain.ProjectStatusListed)
		projectModel.AccessType = string(detail.Project.AccessType)
		projectModel.ReviewReason = ""
		projectModel.LooseMatch = detail.Project.LooseMatch
		if err := tx.Save(&projectModel).Error; err != nil {
			if isDuplicateKeyError(err) {
				return domain.ErrDuplicateProject
			}
			return fmt.Errorf("approve project with config: %w", err)
		}
		detail.Project = projectModel.toDomain()
		if err := r.replaceProductsAndRules(ctx, tx, projectModel.ID, detail); err != nil {
			return err
		}
		if err := r.replaceProjectAccesses(ctx, tx, projectModel.ID, detail); err != nil {
			return err
		}
		if err := r.ensureProjectListable(ctx, tx, projectModel.ID); err != nil {
			return err
		}
		if log != nil {
			log.ResourceID = fmt.Sprintf("%d", projectModel.ID)
			if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *ProjectRepo) TransitionWithLog(ctx context.Context, projectID uint, from domain.ProjectStatus, to domain.ProjectStatus, reviewReason string, log *governancedomain.OperationLog) (*domain.ProjectDetail, error) {
	if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var projectModel ProjectModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", projectID).
			First(&projectModel).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrProjectNotFound
			}
			return fmt.Errorf("find project for transition: %w", err)
		}
		if domain.ProjectStatus(projectModel.Status) != from {
			return domain.ErrInvalidProjectStatus
		}
		if to == domain.ProjectStatusListed {
			if err := r.ensureProjectListable(ctx, tx, projectID); err != nil {
				return err
			}
		}
		projectModel.Status = string(to)
		projectModel.ReviewReason = reviewReason
		if err := tx.Save(&projectModel).Error; err != nil {
			if isDuplicateKeyError(err) {
				return domain.ErrDuplicateProject
			}
			return fmt.Errorf("transition project: %w", err)
		}
		if log != nil {
			log.ResourceID = fmt.Sprintf("%d", projectID)
			if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	detail, err := r.FindDetail(ctx, projectID, 0, true)
	if err != nil {
		return nil, err
	}
	if detail == nil {
		return nil, domain.ErrProjectNotFound
	}
	return detail, nil
}

func (r *ProjectRepo) DeleteWithLog(ctx context.Context, projectID uint, log *governancedomain.OperationLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var projectModel ProjectModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", projectID).
			First(&projectModel).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrProjectNotFound
			}
			return fmt.Errorf("find project for delete: %w", err)
		}
		if domain.ProjectStatus(projectModel.Status) == domain.ProjectStatusReviewing {
			return domain.ErrInvalidProjectStatus
		}
		if err := tx.Delete(&projectModel).Error; err != nil {
			return fmt.Errorf("delete project: %w", err)
		}
		if log != nil {
			log.ResourceID = fmt.Sprintf("%d", projectID)
			if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *ProjectRepo) BulkTransitionWithLog(ctx context.Context, filter coreapp.ProjectListFilter, from domain.ProjectStatus, to domain.ProjectStatus, reviewReason string, log *governancedomain.OperationLog) (int, error) {
	var affected int64
	if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := r.projectListQueryWithDB(ctx, tx, filter).Where("projects.status = ?", string(from))
		if to == domain.ProjectStatusListed {
			query = r.applyListableProjectConditions(query)
		}
		result := query.Updates(map[string]any{
			"status":        string(to),
			"review_reason": reviewReason,
			"updated_at":    time.Now(),
		})
		if result.Error != nil {
			if isDuplicateKeyError(result.Error) {
				return domain.ErrDuplicateProject
			}
			return fmt.Errorf("bulk transition projects: %w", result.Error)
		}
		affected = result.RowsAffected
		if log != nil {
			log.SafeSummary = projectBulkSummary(log.SafeSummary, affected)
			if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return 0, err
	}
	return int(affected), nil
}

func (r *ProjectRepo) BulkDeleteWithLog(ctx context.Context, filter coreapp.ProjectListFilter, log *governancedomain.OperationLog) (int, error) {
	var affected int64
	if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := r.projectListQueryWithDB(ctx, tx, filter).
			Where("projects.status <> ?", string(domain.ProjectStatusReviewing)).
			Delete(&ProjectModel{})
		if result.Error != nil {
			return fmt.Errorf("bulk delete projects: %w", result.Error)
		}
		affected = result.RowsAffected
		if log != nil {
			log.SafeSummary = projectBulkSummary(log.SafeSummary, affected)
			if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return 0, err
	}
	return int(affected), nil
}

func (r *ProjectRepo) List(ctx context.Context, filter coreapp.ProjectListFilter, offset, limit int) ([]coreapp.ProjectSummary, error) {
	var rows []ProjectSummaryModel
	q := r.projectListQuery(ctx, filter).Select("projects.*")
	q = applyProjectListOrder(q, filter).
		Offset(offset).
		Limit(limit)
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}

	projectIDs := make([]uint, 0, len(rows))
	for i := range rows {
		projectIDs = append(projectIDs, rows[i].ID)
	}
	productsByProjectID, err := r.listProductsByProjectIDs(ctx, projectIDs)
	if err != nil {
		return nil, err
	}
	mailRuleCounts, err := r.countMailRulesByProjectIDs(ctx, projectIDs)
	if err != nil {
		return nil, err
	}

	result := make([]coreapp.ProjectSummary, len(rows))
	for i := range rows {
		products := productsByProjectID[rows[i].ID]
		result[i] = coreapp.ProjectSummary{
			Project:       rows[i].toDomain(),
			Products:      products,
			ProductCount:  len(products),
			MailRuleCount: mailRuleCounts[rows[i].ID],
		}
	}
	return result, nil
}

func (r *ProjectRepo) Count(ctx context.Context, filter coreapp.ProjectListFilter) (int64, error) {
	var count int64
	if err := r.projectListQuery(ctx, filter).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count projects: %w", err)
	}
	return count, nil
}

func (r *ProjectRepo) Facets(ctx context.Context, filter coreapp.ProjectListFilter) (*coreapp.ProjectListFacets, error) {
	facets := &coreapp.ProjectListFacets{}
	total, err := r.Count(ctx, filter)
	if err != nil {
		return nil, err
	}
	facets.Status.All = total
	facets.Access.All = total
	facets.Match.All = total
	facets.ProductType.All = total

	statusRows, err := r.projectStringFacet(ctx, filter, "projects.status")
	if err != nil {
		return nil, fmt.Errorf("project status facets: %w", err)
	}
	for _, row := range statusRows {
		switch domain.ProjectStatus(row.Value) {
		case domain.ProjectStatusListed:
			facets.Status.Listed = row.Count
		case domain.ProjectStatusReviewing:
			facets.Status.Reviewing = row.Count
		case domain.ProjectStatusDelisted:
			facets.Status.Delisted = row.Count
		}
	}

	accessRows, err := r.projectStringFacet(ctx, filter, "projects.access_type")
	if err != nil {
		return nil, fmt.Errorf("project access facets: %w", err)
	}
	for _, row := range accessRows {
		switch domain.ProjectAccessType(row.Value) {
		case domain.ProjectAccessPublic:
			facets.Access.Public = row.Count
		case domain.ProjectAccessPrivate:
			facets.Access.Private = row.Count
		}
	}

	matchRows, err := r.projectBoolFacet(ctx, filter, "projects.loose_match")
	if err != nil {
		return nil, fmt.Errorf("project match facets: %w", err)
	}
	for _, row := range matchRows {
		if row.Value {
			facets.Match.Loose = row.Count
		} else {
			facets.Match.Strict = row.Count
		}
	}

	productRows, err := r.projectProductTypeFacet(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("project product type facets: %w", err)
	}
	for _, row := range productRows {
		switch domain.ProductType(row.Value) {
		case domain.ProductTypeMicrosoft:
			facets.ProductType.Microsoft = row.Count
		case domain.ProductTypeDomain:
			facets.ProductType.Domain = row.Count
		}
	}
	return facets, nil
}

type projectStringFacetRow struct {
	Value string `gorm:"column:value"`
	Count int64  `gorm:"column:count"`
}

type projectBoolFacetRow struct {
	Value bool  `gorm:"column:value"`
	Count int64 `gorm:"column:count"`
}

func (r *ProjectRepo) projectStringFacet(ctx context.Context, filter coreapp.ProjectListFilter, column string) ([]projectStringFacetRow, error) {
	var rows []projectStringFacetRow
	err := r.projectListQuery(ctx, filter).
		Select(fmt.Sprintf("%s AS value, COUNT(*) AS count", column)).
		Group(column).
		Scan(&rows).Error
	return rows, err
}

func (r *ProjectRepo) projectBoolFacet(ctx context.Context, filter coreapp.ProjectListFilter, column string) ([]projectBoolFacetRow, error) {
	var rows []projectBoolFacetRow
	err := r.projectListQuery(ctx, filter).
		Select(fmt.Sprintf("%s AS value, COUNT(*) AS count", column)).
		Group(column).
		Scan(&rows).Error
	return rows, err
}

func (r *ProjectRepo) projectProductTypeFacet(ctx context.Context, filter coreapp.ProjectListFilter) ([]projectStringFacetRow, error) {
	var rows []projectStringFacetRow
	err := r.projectListQuery(ctx, filter).
		Joins("JOIN project_products ON project_products.project_id = projects.id").
		Where("project_products.status = ?", string(domain.ProductStatusEnabled)).
		Select("project_products.type AS value, COUNT(DISTINCT projects.id) AS count").
		Group("project_products.type").
		Scan(&rows).Error
	return rows, err
}

func (r *ProjectRepo) FindDetail(ctx context.Context, projectID uint, userID uint, isAdmin bool) (*domain.ProjectDetail, error) {
	var project ProjectModel
	query := r.db.WithContext(ctx).Model(&ProjectModel{}).Where("projects.id = ?", projectID)
	if !isAdmin {
		query = query.Where(
			r.db.Where("projects.applicant_user_id = ? AND projects.status IN ?", userID, []string{
				string(domain.ProjectStatusReviewing),
				string(domain.ProjectStatusDelisted),
			}).
				Or("projects.status = ? AND projects.access_type = ?", string(domain.ProjectStatusListed), string(domain.ProjectAccessPublic)).
				Or(`projects.status = ? AND projects.access_type = ? AND EXISTS (
					SELECT 1 FROM project_accesses
					WHERE project_accesses.project_id = projects.id
					AND project_accesses.user_id = ?
				)`, string(domain.ProjectStatusListed), string(domain.ProjectAccessPrivate), userID),
		)
	}
	if err := query.First(&project).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find project detail: %w", err)
	}

	products, err := r.listProducts(ctx, project.ID)
	if err != nil {
		return nil, err
	}
	rules, err := r.listMailRules(ctx, project.ID)
	if err != nil {
		return nil, err
	}
	accesses := []domain.ProjectAccess{}
	if isAdmin && domain.ProjectAccessType(project.AccessType) == domain.ProjectAccessPrivate {
		accesses, err = r.listAccesses(ctx, project.ID)
		if err != nil {
			return nil, err
		}
	}

	return &domain.ProjectDetail{
		Project:   project.toDomain(),
		Products:  products,
		MailRules: rules,
		Accesses:  accesses,
	}, nil
}

func (r *ProjectRepo) ListAccesses(ctx context.Context, projectID uint) ([]domain.ProjectAccess, error) {
	var project ProjectModel
	if err := r.db.WithContext(ctx).Where("id = ?", projectID).First(&project).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrProjectNotFound
		}
		return nil, fmt.Errorf("find project for access list: %w", err)
	}
	if domain.ProjectAccessType(project.AccessType) != domain.ProjectAccessPrivate {
		return []domain.ProjectAccess{}, nil
	}
	return r.listAccesses(ctx, projectID)
}

func (r *ProjectRepo) GrantAccessWithLog(ctx context.Context, projectID, userID, grantedBy uint, log *governancedomain.OperationLog) (*domain.ProjectAccess, error) {
	var access domain.ProjectAccess
	if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		project, err := r.lockProjectForPrivateAccess(ctx, tx, projectID)
		if err != nil {
			return err
		}
		model := ProjectAccessModel{
			ProjectID: project.ID,
			UserID:    userID,
			GrantedBy: grantedBy,
		}
		if err := tx.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "project_id"}, {Name: "user_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"granted_by"}),
		}).Create(&model).Error; err != nil {
			if isForeignKeyError(err) {
				return domain.ErrInvalidProject
			}
			return fmt.Errorf("grant project access: %w", err)
		}
		if err := tx.WithContext(ctx).
			Where("project_id = ? AND user_id = ?", project.ID, userID).
			First(&model).Error; err != nil {
			return fmt.Errorf("find granted project access: %w", err)
		}
		access = model.toDomain()
		if log != nil {
			log.SafeSummary = fmt.Sprintf("%s userId=%d.", strings.TrimRight(log.SafeSummary, "."), userID)
			if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return &access, nil
}

func (r *ProjectRepo) RevokeAccessWithLog(ctx context.Context, projectID, userID uint, log *governancedomain.OperationLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		project, err := r.lockProjectForPrivateAccess(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).
			Where("project_id = ? AND user_id = ?", project.ID, userID).
			Delete(&ProjectAccessModel{}).Error; err != nil {
			return fmt.Errorf("revoke project access: %w", err)
		}
		if log != nil {
			log.SafeSummary = fmt.Sprintf("%s userId=%d.", strings.TrimRight(log.SafeSummary, "."), userID)
			if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *ProjectRepo) projectListQuery(ctx context.Context, filter coreapp.ProjectListFilter) *gorm.DB {
	return r.projectListQueryWithDB(ctx, r.db, filter)
}

func applyProjectListOrder(q *gorm.DB, filter coreapp.ProjectListFilter) *gorm.DB {
	if filter.Scope == coreapp.ProjectListScopeVisible && filter.UserID > 0 {
		return q.Order(clause.OrderBy{
			Expression: clause.Expr{
				SQL: `CASE
					WHEN projects.status = 'delisted' AND projects.applicant_user_id = ? THEN 0
					WHEN projects.status = 'reviewing' AND projects.applicant_user_id = ? THEN 1
					WHEN projects.status = 'listed' AND projects.access_type = 'private' THEN 2
					WHEN projects.status = 'listed' AND projects.access_type = 'public' THEN 3
					ELSE 4
				END ASC, projects.updated_at DESC, projects.id DESC`,
				Vars:               []any{filter.UserID, filter.UserID},
				WithoutParentheses: true,
			},
		})
	}
	return q.Order("projects.updated_at DESC, projects.id DESC")
}

func (r *ProjectRepo) projectListQueryWithDB(ctx context.Context, db *gorm.DB, filter coreapp.ProjectListFilter) *gorm.DB {
	q := db.WithContext(ctx).Model(&ProjectModel{})
	switch filter.Scope {
	case coreapp.ProjectListScopeAll:
	case coreapp.ProjectListScopeMine:
		q = q.Where("projects.applicant_user_id = ? AND projects.status IN ?", filter.UserID, []string{
			string(domain.ProjectStatusReviewing),
			string(domain.ProjectStatusDelisted),
		})
	default:
		listedVisible := db.Where("projects.status = ?", string(domain.ProjectStatusListed)).
			Where(db.Where("projects.access_type = ?", string(domain.ProjectAccessPublic)).
				Or(`projects.access_type = ? AND EXISTS (
					SELECT 1 FROM project_accesses
					WHERE project_accesses.project_id = projects.id
					AND project_accesses.user_id = ?
				)`, string(domain.ProjectAccessPrivate), filter.UserID))
		ownApplications := db.Where("projects.applicant_user_id = ? AND projects.status IN ?", filter.UserID, []string{
			string(domain.ProjectStatusReviewing),
			string(domain.ProjectStatusDelisted),
		})
		q = q.Where(ownApplications.Or(listedVisible))
	}
	if len(filter.IDs) > 0 {
		q = q.Where("projects.id IN ?", filter.IDs)
	}
	if filter.Status != "" {
		q = q.Where("projects.status = ?", string(filter.Status))
	}
	if filter.AccessType != "" {
		q = q.Where("projects.access_type = ?", string(filter.AccessType))
	}
	if filter.LooseMatch != nil {
		q = q.Where("projects.loose_match = ?", *filter.LooseMatch)
	}
	if filter.ProductType != "" {
		q = q.Where(`EXISTS (
			SELECT 1 FROM project_products
			WHERE project_products.project_id = projects.id
			AND project_products.type = ?
			AND project_products.status = ?
		)`, string(filter.ProductType), string(domain.ProductStatusEnabled))
	}
	if filter.TargetPlatform != "" {
		q = q.Where("projects.target_platform = ?", filter.TargetPlatform)
	}
	if filter.CreatedFrom != nil {
		q = q.Where("projects.created_at >= ?", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		q = q.Where("projects.created_at <= ?", *filter.CreatedTo)
	}
	if filter.Search != "" {
		searchQuery := projectSearchBooleanQuery(filter.Search)
		if searchQuery == "" {
			q = q.Where("1 = 0")
		} else {
			q = q.Where("MATCH(projects.name, projects.target_platform) AGAINST (? IN BOOLEAN MODE)", searchQuery)
		}
	}
	return q
}

func (r *ProjectRepo) lockProjectForPrivateAccess(ctx context.Context, tx *gorm.DB, projectID uint) (*ProjectModel, error) {
	var project ProjectModel
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", projectID).
		First(&project).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrProjectNotFound
		}
		return nil, fmt.Errorf("find project for access command: %w", err)
	}
	if domain.ProjectAccessType(project.AccessType) != domain.ProjectAccessPrivate {
		return nil, domain.ErrInvalidProject
	}
	return &project, nil
}

func projectBulkSummary(summary string, affected int64) string {
	return fmt.Sprintf("%s affected=%d.", strings.TrimRight(summary, "."), affected)
}

func (r *ProjectRepo) replaceProductsAndRules(ctx context.Context, tx *gorm.DB, projectID uint, detail *domain.ProjectDetail) error {
	// Products are referenced by orders and allocations. Rebuilding this table on
	// every edit invalidates those foreign keys as soon as a project has live or
	// historical orders. Keep an existing (project_id, type) row and its ID,
	// update it in place, and only insert product types that are newly enabled.
	var existingModels []ProjectProductModel
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("project_id = ?", projectID).
		Order("id ASC").
		Find(&existingModels).Error; err != nil {
		return fmt.Errorf("list project products for replacement: %w", err)
	}
	existingByType := make(map[string]ProjectProductModel, len(existingModels))
	for _, existing := range existingModels {
		existingByType[existing.Type] = existing
	}

	requestedTypes := make(map[string]struct{}, len(detail.Products))
	for i := range detail.Products {
		product := detail.Products[i]
		product.ProjectID = projectID
		productType := string(product.Type)
		if _, exists := requestedTypes[productType]; exists {
			return domain.ErrInvalidProduct
		}
		requestedTypes[productType] = struct{}{}

		if existing, ok := existingByType[productType]; ok {
			model := productModelFromDomain(product)
			model.ID = existing.ID
			model.ProjectID = projectID
			// Preserve audit history for the logical product row. Existing orders
			// retain this same product ID and their independently snapshotted price.
			model.CreatedAt = existing.CreatedAt
			if err := tx.WithContext(ctx).Save(&model).Error; err != nil {
				return fmt.Errorf("update project product: %w", err)
			}
			detail.Products[i] = model.toDomain()
			continue
		}

		model := productModelFromDomain(product)
		model.ProjectID = projectID
		if err := tx.WithContext(ctx).Create(&model).Error; err != nil {
			if isDuplicateKeyError(err) {
				return domain.ErrInvalidProduct
			}
			return fmt.Errorf("create project product: %w", err)
		}
		detail.Products[i] = model.toDomain()
	}

	// Toggling a product off is a logical disable rather than a destructive
	// delete. This preserves immutable order/allocation references while keeping
	// the product unavailable to new orders. A later edit can re-enable the same
	// type and will update this row in place.
	for _, existing := range existingModels {
		if _, requested := requestedTypes[existing.Type]; requested {
			continue
		}
		if domain.ProductStatus(existing.Status) != domain.ProductStatusDisabled {
			existing.Status = string(domain.ProductStatusDisabled)
			if err := tx.WithContext(ctx).Save(&existing).Error; err != nil {
				return fmt.Errorf("disable removed project product: %w", err)
			}
		}
		detail.Products = append(detail.Products, existing.toDomain())
	}

	if err := tx.WithContext(ctx).Where("project_id = ?", projectID).Delete(&ProjectMailRuleModel{}).Error; err != nil {
		return fmt.Errorf("replace project mail rules: %w", err)
	}
	if len(detail.MailRules) > 0 {
		ruleModels := make([]ProjectMailRuleModel, 0, len(detail.MailRules))
		for i := range detail.MailRules {
			detail.MailRules[i].ID = 0
			detail.MailRules[i].ProjectID = projectID
			ruleModels = append(ruleModels, mailRuleModelFromDomain(detail.MailRules[i]))
		}
		if err := tx.WithContext(ctx).Create(&ruleModels).Error; err != nil {
			return fmt.Errorf("create replacement project mail rules: %w", err)
		}
		for i := range ruleModels {
			detail.MailRules[i] = ruleModels[i].toDomain()
		}
	}
	return nil
}

func (r *ProjectRepo) replaceProjectAccesses(ctx context.Context, tx *gorm.DB, projectID uint, detail *domain.ProjectDetail) error {
	targetAccesses := uniqueProjectAccesses(detail.Accesses)
	detail.Accesses = nil

	if err := tx.WithContext(ctx).Where("project_id = ?", projectID).Delete(&ProjectAccessModel{}).Error; err != nil {
		return fmt.Errorf("replace project accesses: %w", err)
	}
	if detail.Project.AccessType != domain.ProjectAccessPrivate || len(targetAccesses) == 0 {
		return nil
	}

	models := make([]ProjectAccessModel, 0, len(targetAccesses))
	for i := range targetAccesses {
		models = append(models, ProjectAccessModel{
			ProjectID: projectID,
			UserID:    targetAccesses[i].UserID,
			GrantedBy: targetAccesses[i].GrantedBy,
		})
	}
	if err := tx.WithContext(ctx).Create(&models).Error; err != nil {
		if isForeignKeyError(err) {
			return domain.ErrInvalidProject
		}
		return fmt.Errorf("create replacement project accesses: %w", err)
	}
	detail.Accesses = make([]domain.ProjectAccess, len(models))
	for i := range models {
		detail.Accesses[i] = models[i].toDomain()
	}
	return nil
}

func uniqueProjectAccesses(accesses []domain.ProjectAccess) []domain.ProjectAccess {
	seen := make(map[uint]struct{}, len(accesses))
	result := make([]domain.ProjectAccess, 0, len(accesses))
	for _, access := range accesses {
		if access.UserID == 0 {
			continue
		}
		if _, exists := seen[access.UserID]; exists {
			continue
		}
		seen[access.UserID] = struct{}{}
		result = append(result, access)
	}
	return result
}

func (r *ProjectRepo) ensureProjectListable(ctx context.Context, tx *gorm.DB, projectID uint) error {
	var count int64
	if err := r.applyListableProjectConditions(tx.WithContext(ctx).Model(&ProjectModel{}).Where("projects.id = ?", projectID)).
		Count(&count).Error; err != nil {
		return fmt.Errorf("check project listable: %w", err)
	}
	if count == 0 {
		return domain.ErrInvalidProject
	}
	return nil
}

func (r *ProjectRepo) applyListableProjectConditions(q *gorm.DB) *gorm.DB {
	return q.
		Where(`EXISTS (
			SELECT 1 FROM project_products
			WHERE project_products.project_id = projects.id
			AND project_products.status = ?
		)`, string(domain.ProductStatusEnabled)).
		Where(`EXISTS (
			SELECT 1 FROM project_mail_rules
			WHERE project_mail_rules.project_id = projects.id
			AND project_mail_rules.rule_type = ?
			AND project_mail_rules.enabled = 1
		)`, string(domain.MailRuleSender)).
		Where(`EXISTS (
			SELECT 1 FROM project_mail_rules
			WHERE project_mail_rules.project_id = projects.id
			AND project_mail_rules.rule_type = ?
			AND project_mail_rules.pattern IN ('exact', 'dot', 'plus')
			AND project_mail_rules.enabled = 1
		)`, string(domain.MailRuleRecipient)).
		Where(`(
			projects.loose_match = 1
			OR (
				EXISTS (
					SELECT 1 FROM project_mail_rules
					WHERE project_mail_rules.project_id = projects.id
					AND project_mail_rules.rule_type = ?
					AND project_mail_rules.enabled = 1
				)
				AND EXISTS (
					SELECT 1 FROM project_mail_rules
					WHERE project_mail_rules.project_id = projects.id
					AND project_mail_rules.rule_type = ?
					AND project_mail_rules.enabled = 1
				)
			)
		)`, string(domain.MailRuleSubject), string(domain.MailRuleBody))
}

func projectSearchBooleanQuery(search string) string {
	parts := strings.FieldsFunc(strings.ToLower(search), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	terms := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		terms = append(terms, "+"+part+"*")
	}
	return strings.Join(terms, " ")
}

func (r *ProjectRepo) listProducts(ctx context.Context, projectID uint) ([]domain.Product, error) {
	var models []ProjectProductModel
	if err := r.db.WithContext(ctx).
		Where("project_id = ?", projectID).
		Order("id ASC").
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list project products: %w", err)
	}
	result := make([]domain.Product, len(models))
	for i := range models {
		result[i] = models[i].toDomain()
	}
	return result, nil
}

func (r *ProjectRepo) listProductsByProjectIDs(ctx context.Context, projectIDs []uint) (map[uint][]domain.Product, error) {
	result := make(map[uint][]domain.Product, len(projectIDs))
	if len(projectIDs) == 0 {
		return result, nil
	}

	var models []ProjectProductModel
	if err := r.db.WithContext(ctx).
		Where("project_id IN ?", projectIDs).
		Where("status = ?", string(domain.ProductStatusEnabled)).
		Order("project_id ASC, id ASC").
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list project summary products: %w", err)
	}
	for i := range models {
		product := models[i].toDomain()
		result[product.ProjectID] = append(result[product.ProjectID], product)
	}
	return result, nil
}

func (r *ProjectRepo) countMailRulesByProjectIDs(ctx context.Context, projectIDs []uint) (map[uint]int, error) {
	result := make(map[uint]int, len(projectIDs))
	if len(projectIDs) == 0 {
		return result, nil
	}

	type countRow struct {
		ProjectID uint `gorm:"column:project_id"`
		Count     int  `gorm:"column:count"`
	}
	rows := make([]countRow, 0)
	if err := r.db.WithContext(ctx).
		Model(&ProjectMailRuleModel{}).
		Select("project_id, COUNT(*) AS count").
		Where("project_id IN ?", projectIDs).
		Group("project_id").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("count project summary mail rules: %w", err)
	}
	for _, row := range rows {
		result[row.ProjectID] = row.Count
	}
	return result, nil
}

func (r *ProjectRepo) listMailRules(ctx context.Context, projectID uint) ([]domain.MailRule, error) {
	var models []ProjectMailRuleModel
	if err := r.db.WithContext(ctx).
		Where("project_id = ?", projectID).
		Order("id ASC").
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list project mail rules: %w", err)
	}
	result := make([]domain.MailRule, len(models))
	for i := range models {
		result[i] = models[i].toDomain()
	}
	return result, nil
}

func (r *ProjectRepo) listAccesses(ctx context.Context, projectID uint) ([]domain.ProjectAccess, error) {
	var models []ProjectAccessModel
	if err := r.db.WithContext(ctx).
		Clauses(clause.OrderBy{Expression: clause.Expr{SQL: "created_at DESC, id DESC"}}).
		Where("project_id = ?", projectID).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list project accesses: %w", err)
	}
	result := make([]domain.ProjectAccess, len(models))
	for i := range models {
		result[i] = models[i].toDomain()
	}
	return result, nil
}
