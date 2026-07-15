package infra

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/stretchr/testify/require"
)

func TestProjectRepoCreateWithLogEnforcesListedNameUniqueMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewProjectRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (?, ?, ?, ?)",
		1,
		"admin@test.local",
		"hash",
		"admin",
	).Error)

	first := validListedProjectDetail("Unique Listed Project")
	err := repo.CreateWithLog(context.Background(), first, projectTestLog("req-project-unique-1"))
	require.NoError(t, err)
	require.NotZero(t, first.Project.ID)
	require.Len(t, first.Products, 1)
	require.Len(t, first.MailRules, 2)

	second := validListedProjectDetail("Unique Listed Project")
	err = repo.CreateWithLog(context.Background(), second, projectTestLog("req-project-unique-2"))
	require.ErrorIs(t, err, domain.ErrDuplicateProject)

	var count int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM projects WHERE name = ?", "Unique Listed Project").Scan(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestProjectRepoCreateWithLogRollsBackOnDuplicateProductTypeMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewProjectRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (?, ?, ?, ?)",
		1,
		"admin@test.local",
		"hash",
		"admin",
	).Error)

	detail := validListedProjectDetail("Duplicate Product Type Project")
	detail.Products = append(detail.Products, detail.Products[0])

	err := repo.CreateWithLog(context.Background(), detail, projectTestLog("req-project-product-duplicate"))
	require.True(t, errors.Is(err, domain.ErrInvalidProduct), "got %v", err)

	var projectCount int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM projects WHERE name = ?", "Duplicate Product Type Project").Scan(&projectCount).Error)
	require.Zero(t, projectCount)

	var logCount int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM operation_logs WHERE request_id = ?", "req-project-product-duplicate").Scan(&logCount).Error)
	require.Zero(t, logCount)
}

func TestProjectRepoListSearchUsesFullTextIndexMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewProjectRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (?, ?, ?, ?)",
		1,
		"admin@test.local",
		"hash",
		"admin",
	).Error)

	github := validListedProjectDetail("GitHub Codes")
	github.Project.TargetPlatform = "github.com"
	require.NoError(t, repo.CreateWithLog(context.Background(), github, projectTestLog("req-project-search-github")))

	google := validListedProjectDetail("Google Auth")
	google.Project.TargetPlatform = "accounts.google.com"
	require.NoError(t, repo.CreateWithLog(context.Background(), google, projectTestLog("req-project-search-google")))

	items, err := repo.List(context.Background(), coreapp.ProjectListFilter{
		Scope:   coreapp.ProjectListScopeAll,
		IsAdmin: true,
		Search:  "github",
	}, 0, 20)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "GitHub Codes", items[0].Project.Name)

	var plan []projectExplainRow
	require.NoError(t, db.Raw(
		"EXPLAIN SELECT id FROM projects WHERE MATCH(name, target_platform) AGAINST (? IN BOOLEAN MODE) LIMIT 20",
		projectSearchBooleanQuery("github"),
	).Scan(&plan).Error)
	require.NotEmpty(t, plan)
	require.Equal(t, "fulltext", plan[0].Type)
	require.Equal(t, "idx_projects_search", plan[0].Key.String)
}

func TestProjectRepoListOrderUsesUpdatedIndexMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewProjectRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (?, ?, ?, ?)",
		1,
		"admin@test.local",
		"hash",
		"admin",
	).Error)
	require.NoError(t, repo.CreateWithLog(context.Background(), validListedProjectDetail("Ordered Project"), projectTestLog("req-project-order")))

	var plan []projectExplainRow
	require.NoError(t, db.Raw(
		"EXPLAIN SELECT id FROM projects WHERE status = ? ORDER BY updated_at DESC, id DESC LIMIT 20",
		string(domain.ProjectStatusListed),
	).Scan(&plan).Error)
	require.NotEmpty(t, plan)
	require.Equal(t, "idx_projects_status_updated", plan[0].Key.String)
}

func TestProjectRepoAccessGrantListAndRevokeMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewProjectRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (?, ?, ?, ?), (?, ?, ?, ?)",
		1,
		"admin@test.local",
		"hash",
		"admin",
		2,
		"user@test.local",
		"hash",
		"user",
	).Error)

	detail := validListedProjectDetail("Private Access Project")
	detail.Project.AccessType = domain.ProjectAccessPrivate
	require.NoError(t, repo.CreateWithLog(context.Background(), detail, projectTestLog("req-project-access-create")))

	access, err := repo.GrantAccessWithLog(context.Background(), detail.Project.ID, 2, 1, projectTestLog("req-project-access-grant"))
	require.NoError(t, err)
	require.Equal(t, detail.Project.ID, access.ProjectID)
	require.Equal(t, uint(2), access.UserID)

	accesses, err := repo.ListAccesses(context.Background(), detail.Project.ID)
	require.NoError(t, err)
	require.Len(t, accesses, 1)

	require.NoError(t, repo.RevokeAccessWithLog(context.Background(), detail.Project.ID, 2, projectTestLog("req-project-access-revoke")))
	accesses, err = repo.ListAccesses(context.Background(), detail.Project.ID)
	require.NoError(t, err)
	require.Empty(t, accesses)

	var logCount int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM operation_logs WHERE request_id IN (?, ?)", "req-project-access-grant", "req-project-access-revoke").Scan(&logCount).Error)
	require.Equal(t, int64(2), logCount)
}

func TestProjectRepoCompleteConfigReplacesAccessesMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewProjectRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (?, ?, ?, ?), (?, ?, ?, ?), (?, ?, ?, ?)",
		1,
		"admin-access-replace@test.local",
		"hash",
		"admin",
		2,
		"access-user-2@test.local",
		"hash",
		"user",
		3,
		"access-user-3@test.local",
		"hash",
		"user",
	).Error)

	detail := validListedProjectDetail("Private Access Replacement Project")
	detail.Project.AccessType = domain.ProjectAccessPrivate
	detail.Accesses = []domain.ProjectAccess{
		{UserID: 2, GrantedBy: 1},
		{UserID: 3, GrantedBy: 1},
	}
	require.NoError(t, repo.CreateWithLog(context.Background(), detail, projectTestLog("req-project-access-replace-create")))

	accesses, err := repo.ListAccesses(context.Background(), detail.Project.ID)
	require.NoError(t, err)
	require.Len(t, accesses, 2)

	publicDetail := validListedProjectDetail("Private Access Replacement Project")
	publicDetail.Project.ID = detail.Project.ID
	publicDetail.Project.AccessType = domain.ProjectAccessPublic
	require.NoError(t, repo.UpdateWithLog(context.Background(), publicDetail, projectTestLog("req-project-access-replace-public")))

	var accessCount int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM project_accesses WHERE project_id = ?", detail.Project.ID).Scan(&accessCount).Error)
	require.Zero(t, accessCount)

	adminDetail, err := repo.FindDetail(context.Background(), detail.Project.ID, 0, true)
	require.NoError(t, err)
	require.Empty(t, adminDetail.Accesses)
}

func TestProjectRepoVisiblePrivateProjectRequiresAccessMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewProjectRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (?, ?, ?, ?), (?, ?, ?, ?)",
		1,
		"admin@test.local",
		"hash",
		"admin",
		2,
		"applicant@test.local",
		"hash",
		"user",
	).Error)

	applicantID := uint(2)
	detail := validListedProjectDetail("Private Listed Applicant Project")
	detail.Project.AccessType = domain.ProjectAccessPrivate
	detail.Project.ApplicantUserID = &applicantID
	require.NoError(t, repo.CreateWithLog(context.Background(), detail, projectTestLog("req-project-private-visible-create")))

	visible, err := repo.List(context.Background(), coreapp.ProjectListFilter{
		Scope:  coreapp.ProjectListScopeVisible,
		UserID: applicantID,
	}, 0, 20)
	require.NoError(t, err)
	require.Empty(t, visible)

	mine, err := repo.List(context.Background(), coreapp.ProjectListFilter{
		Scope:  coreapp.ProjectListScopeMine,
		UserID: applicantID,
	}, 0, 20)
	require.NoError(t, err)
	require.Empty(t, mine)

	found, err := repo.FindDetail(context.Background(), detail.Project.ID, applicantID, false)
	require.NoError(t, err)
	require.Nil(t, found)

	_, err = repo.GrantAccessWithLog(context.Background(), detail.Project.ID, applicantID, 1, projectTestLog("req-project-private-visible-grant"))
	require.NoError(t, err)

	visible, err = repo.List(context.Background(), coreapp.ProjectListFilter{
		Scope:  coreapp.ProjectListScopeVisible,
		UserID: applicantID,
	}, 0, 20)
	require.NoError(t, err)
	require.Len(t, visible, 1)

	found, err = repo.FindDetail(context.Background(), detail.Project.ID, applicantID, false)
	require.NoError(t, err)
	require.NotNil(t, found)
}

func TestProjectRepoApproveWithConfigMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewProjectRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (?, ?, ?, ?)",
		1,
		"admin@test.local",
		"hash",
		"admin",
	).Error)

	reviewing := validListedProjectDetail("Reviewing Project")
	reviewing.Project.Status = domain.ProjectStatusReviewing
	reviewing.Products = nil
	require.NoError(t, repo.CreateWithLog(context.Background(), reviewing, projectTestLog("req-project-reviewing-create")))

	configured := validListedProjectDetail("Reviewing Project Approved")
	configured.Project.ID = reviewing.Project.ID
	require.NoError(t, repo.ApproveWithConfigAndLog(context.Background(), configured, projectTestLog("req-project-reviewing-approve")))

	detail, err := repo.FindDetail(context.Background(), reviewing.Project.ID, 0, true)
	require.NoError(t, err)
	require.Equal(t, domain.ProjectStatusListed, detail.Project.Status)
	require.Equal(t, "Reviewing Project Approved", detail.Project.Name)
	require.Len(t, detail.Products, 1)
	require.Len(t, detail.MailRules, 2)
}

func TestProjectRepoUpdateWithLogRejectsReviewingProjectMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewProjectRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (?, ?, ?, ?)",
		1,
		"admin@test.local",
		"hash",
		"admin",
	).Error)

	reviewing := validListedProjectDetail("Reviewing Update Guard Project")
	reviewing.Project.Status = domain.ProjectStatusReviewing
	require.NoError(t, repo.CreateWithLog(context.Background(), reviewing, projectTestLog("req-project-update-guard-create")))

	update := validListedProjectDetail("Reviewing Update Guard Project Updated")
	update.Project.ID = reviewing.Project.ID
	err := repo.UpdateWithLog(context.Background(), update, projectTestLog("req-project-update-guard-update"))
	require.ErrorIs(t, err, domain.ErrInvalidProjectStatus)
}

func TestProjectRepoUpdateWithLogPreservesReferencedProductWhenEnablingDomainMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewProjectRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (?, ?, ?, ?)",
		1,
		"admin@test.local",
		"hash",
		"admin",
	).Error)

	detail := validListedProjectDetail("Referenced Product Update Project")
	require.NoError(t, repo.CreateWithLog(context.Background(), detail, projectTestLog("req-project-referenced-create")))
	require.Len(t, detail.Products, 1)
	microsoftProductID := detail.Products[0].ID

	// This mirrors a real paid/history-bearing project: orders reference the
	// compound product key and prohibit destructive replacement of that row.
	require.NoError(t, db.Exec(
		`INSERT INTO orders(
			order_no, user_id, project_id, project_product_id, product_type,
			service_mode, pay_amount, idempotency_key, request_fingerprint, client_channel
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"project-product-reference-order",
		1,
		detail.Project.ID,
		microsoftProductID,
		string(domain.ProductTypeMicrosoft),
		"code",
		"0.100000",
		"project-product-reference-key",
		"0123456789012345678901234567890123456789012345678901234567890123",
		"console",
	).Error)

	update := validListedProjectDetail("Referenced Product Update Project")
	update.Project.ID = detail.Project.ID
	update.Products = append(update.Products, domain.Product{
		Type:                    domain.ProductTypeDomain,
		Status:                  domain.ProductStatusEnabled,
		CodeEnabled:             true,
		CodePrice:               "0.200000",
		CodeSupplierPrice:       "0.100000",
		PurchaseEnabled:         false,
		PurchasePrice:           "0.000000",
		PurchaseSupplierPrice:   "0.000000",
		CodeWindowMinutes:       10,
		ActivationWindowMinutes: 60,
		WarrantyMinutes:         60,
	})

	require.NoError(t, repo.UpdateWithLog(context.Background(), update, projectTestLog("req-project-referenced-enable-domain")))

	stored, err := repo.FindDetail(context.Background(), detail.Project.ID, 0, true)
	require.NoError(t, err)
	require.Len(t, stored.Products, 2)
	var sawMicrosoft, sawDomain bool
	for _, product := range stored.Products {
		switch product.Type {
		case domain.ProductTypeMicrosoft:
			sawMicrosoft = true
			require.Equal(t, microsoftProductID, product.ID)
			require.Equal(t, domain.ProductStatusEnabled, product.Status)
		case domain.ProductTypeDomain:
			sawDomain = true
			require.Equal(t, domain.ProductStatusEnabled, product.Status)
		}
	}
	require.True(t, sawMicrosoft)
	require.True(t, sawDomain)

	var orderProductID uint
	require.NoError(t, db.Raw("SELECT project_product_id FROM orders WHERE order_no = ?", "project-product-reference-order").Scan(&orderProductID).Error)
	require.Equal(t, microsoftProductID, orderProductID)

	// Removing the Microsoft type from a later full configuration update must
	// not resurrect the destructive delete path. Its historical row remains
	// referenced by the order but becomes unavailable for new orders.
	domainOnly := validListedProjectDetail("Referenced Product Update Project")
	domainOnly.Project.ID = detail.Project.ID
	domainOnly.Products = []domain.Product{update.Products[1]}
	require.NoError(t, repo.UpdateWithLog(context.Background(), domainOnly, projectTestLog("req-project-referenced-disable-microsoft")))

	stored, err = repo.FindDetail(context.Background(), detail.Project.ID, 0, true)
	require.NoError(t, err)
	require.Len(t, stored.Products, 2)
	for _, product := range stored.Products {
		if product.Type == domain.ProductTypeMicrosoft {
			require.Equal(t, microsoftProductID, product.ID)
			require.Equal(t, domain.ProductStatusDisabled, product.Status)
		}
	}
	require.NoError(t, db.Raw("SELECT project_product_id FROM orders WHERE order_no = ?", "project-product-reference-order").Scan(&orderProductID).Error)
	require.Equal(t, microsoftProductID, orderProductID)
}

func TestProjectRepoFacetsMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewProjectRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (?, ?, ?, ?)",
		1,
		"admin@test.local",
		"hash",
		"admin",
	).Error)

	listed := validListedProjectDetail("Facet Listed Project")
	require.NoError(t, repo.CreateWithLog(context.Background(), listed, projectTestLog("req-project-facet-listed")))

	reviewing := validListedProjectDetail("Facet Reviewing Project")
	reviewing.Project.Status = domain.ProjectStatusReviewing
	reviewing.Project.AccessType = domain.ProjectAccessPrivate
	reviewing.Project.LooseMatch = false
	require.NoError(t, repo.CreateWithLog(context.Background(), reviewing, projectTestLog("req-project-facet-reviewing")))

	domainProject := validListedProjectDetail("Facet Domain Project")
	domainProject.Products = []domain.Product{
		{
			Type:                    domain.ProductTypeDomain,
			Status:                  domain.ProductStatusEnabled,
			CodeEnabled:             true,
			CodePrice:               "0.200000",
			CodeSupplierPrice:       "0.100000",
			PurchaseEnabled:         false,
			PurchasePrice:           "0.000000",
			PurchaseSupplierPrice:   "0.000000",
			CodeWindowMinutes:       10,
			ActivationWindowMinutes: 60,
			WarrantyMinutes:         60,
		},
	}
	require.NoError(t, repo.CreateWithLog(context.Background(), domainProject, projectTestLog("req-project-facet-domain")))

	facets, err := repo.Facets(context.Background(), coreapp.ProjectListFilter{
		Scope:   coreapp.ProjectListScopeAll,
		IsAdmin: true,
	})
	require.NoError(t, err)
	require.Equal(t, int64(3), facets.Status.All)
	require.Equal(t, int64(2), facets.Status.Listed)
	require.Equal(t, int64(1), facets.Status.Reviewing)
	require.Equal(t, int64(2), facets.Access.Public)
	require.Equal(t, int64(1), facets.Access.Private)
	require.Equal(t, int64(2), facets.Match.Loose)
	require.Equal(t, int64(1), facets.Match.Strict)
	require.Equal(t, int64(2), facets.ProductType.Microsoft)
	require.Equal(t, int64(1), facets.ProductType.Domain)
}

func TestProjectRepoListsOnlyCurrentSaleableProductsMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewProjectRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (?, ?, ?, ?)",
		1,
		"admin@test.local",
		"hash",
		"admin",
	).Error)

	microsoft := validListedProjectDetail("Current Microsoft Product")
	require.NoError(t, repo.CreateWithLog(context.Background(), microsoft, projectTestLog("req-project-current-microsoft")))

	withHistoricalMicrosoft := validListedProjectDetail("Domain With Historical Microsoft Product")
	historicalMicrosoft := withHistoricalMicrosoft.Products[0]
	historicalMicrosoft.Status = domain.ProductStatusDisabled
	withHistoricalMicrosoft.Products = []domain.Product{
		historicalMicrosoft,
		{
			Type:                    domain.ProductTypeDomain,
			Status:                  domain.ProductStatusEnabled,
			CodeEnabled:             true,
			CodePrice:               "0.200000",
			CodeSupplierPrice:       "0.100000",
			PurchaseEnabled:         false,
			PurchasePrice:           "0.000000",
			PurchaseSupplierPrice:   "0.000000",
			CodeWindowMinutes:       10,
			ActivationWindowMinutes: 60,
			WarrantyMinutes:         60,
		},
	}
	require.NoError(t, repo.CreateWithLog(context.Background(), withHistoricalMicrosoft, projectTestLog("req-project-historical-microsoft")))

	allFilter := coreapp.ProjectListFilter{
		Scope:   coreapp.ProjectListScopeAll,
		IsAdmin: true,
	}
	items, err := repo.List(context.Background(), allFilter, 0, 20)
	require.NoError(t, err)
	require.Len(t, items, 2)
	var historicalSummary *coreapp.ProjectSummary
	for i := range items {
		if items[i].Project.ID == withHistoricalMicrosoft.Project.ID {
			historicalSummary = &items[i]
			break
		}
	}
	require.NotNil(t, historicalSummary)
	require.Equal(t, 1, historicalSummary.ProductCount)
	require.Len(t, historicalSummary.Products, 1)
	require.Equal(t, domain.ProductTypeDomain, historicalSummary.Products[0].Type)
	require.Equal(t, domain.ProductStatusEnabled, historicalSummary.Products[0].Status)

	microsoftFilter := allFilter
	microsoftFilter.ProductType = domain.ProductTypeMicrosoft
	microsoftItems, err := repo.List(context.Background(), microsoftFilter, 0, 20)
	require.NoError(t, err)
	require.Len(t, microsoftItems, 1)
	require.Equal(t, microsoft.Project.ID, microsoftItems[0].Project.ID)
	microsoftCount, err := repo.Count(context.Background(), microsoftFilter)
	require.NoError(t, err)
	require.Equal(t, int64(1), microsoftCount)

	domainFilter := allFilter
	domainFilter.ProductType = domain.ProductTypeDomain
	domainItems, err := repo.List(context.Background(), domainFilter, 0, 20)
	require.NoError(t, err)
	require.Len(t, domainItems, 1)
	require.Equal(t, withHistoricalMicrosoft.Project.ID, domainItems[0].Project.ID)

	facets, err := repo.Facets(context.Background(), allFilter)
	require.NoError(t, err)
	require.Equal(t, int64(1), facets.ProductType.Microsoft)
	require.Equal(t, int64(1), facets.ProductType.Domain)

	adminDetail, err := repo.FindDetail(context.Background(), withHistoricalMicrosoft.Project.ID, 0, true)
	require.NoError(t, err)
	require.Len(t, adminDetail.Products, 2)
	var foundHistoricalMicrosoft bool
	for _, product := range adminDetail.Products {
		if product.Type == domain.ProductTypeMicrosoft {
			foundHistoricalMicrosoft = true
			require.Equal(t, domain.ProductStatusDisabled, product.Status)
		}
	}
	require.True(t, foundHistoricalMicrosoft)
}

func TestProjectSchemaRejectsInvalidProductRulesMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)

	require.NoError(t, db.Exec(
		"INSERT INTO projects(id, name, target_platform, status, access_type) VALUES (?, ?, ?, ?, ?)",
		1,
		"Constraint Project",
		"constraint.example.com",
		string(domain.ProjectStatusListed),
		string(domain.ProjectAccessPublic),
	).Error)

	require.Error(t, db.Exec(
		`INSERT INTO project_products(
			project_id, type, status, code_enabled, purchase_enabled,
			code_window_minutes, activation_window_minutes, warranty_minutes,
			main_weight, dot_weight, plus_weight
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1,
		string(domain.ProductTypeMicrosoft),
		string(domain.ProductStatusEnabled),
		false,
		false,
		10,
		60,
		60,
		1,
		0,
		0,
	).Error)

	require.Error(t, db.Exec(
		`INSERT INTO project_products(
			project_id, type, status, code_enabled, purchase_enabled,
			code_window_minutes, activation_window_minutes, warranty_minutes,
			main_weight, dot_weight, plus_weight
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1,
		string(domain.ProductTypeMicrosoft),
		string(domain.ProductStatusEnabled),
		true,
		false,
		10,
		60,
		60,
		0,
		0,
		0,
	).Error)

	require.Error(t, db.Exec(
		`INSERT INTO project_products(
			project_id, type, status, code_enabled, purchase_enabled,
			code_window_minutes, activation_window_minutes, warranty_minutes,
			main_weight, dot_weight, plus_weight
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1,
		string(domain.ProductTypeDomain),
		string(domain.ProductStatusEnabled),
		true,
		false,
		10,
		60,
		60,
		1,
		0,
		0,
	).Error)

	require.Error(t, db.Exec(
		`INSERT INTO project_products(
			project_id, type, status, code_enabled, purchase_enabled,
			code_window_minutes, activation_window_minutes, warranty_minutes,
			main_weight, dot_weight, plus_weight
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1,
		string(domain.ProductTypeMicrosoft),
		string(domain.ProductStatusEnabled),
		true,
		false,
		0,
		60,
		60,
		1,
		0,
		0,
	).Error)

	require.Error(t, db.Exec(
		`INSERT INTO project_mail_rules(project_id, rule_type, pattern, enabled) VALUES (?, ?, ?, ?)`,
		1,
		string(domain.MailRuleRecipient),
		"regex-not-allowed",
		true,
	).Error)
}

type projectExplainRow struct {
	Type string         `gorm:"column:type"`
	Key  sql.NullString `gorm:"column:key"`
}

func validListedProjectDetail(name string) *domain.ProjectDetail {
	return &domain.ProjectDetail{
		Project: domain.Project{
			Name:           name,
			TargetPlatform: "example.com",
			Status:         domain.ProjectStatusListed,
			AccessType:     domain.ProjectAccessPublic,
			LooseMatch:     true,
		},
		Products: []domain.Product{
			{
				Type:                    domain.ProductTypeMicrosoft,
				Status:                  domain.ProductStatusEnabled,
				CodeEnabled:             true,
				CodePrice:               "0.100000",
				CodeSupplierPrice:       "0.050000",
				PurchaseEnabled:         false,
				PurchasePrice:           "0.000000",
				PurchaseSupplierPrice:   "0.000000",
				CodeWindowMinutes:       10,
				ActivationWindowMinutes: 60,
				WarrantyMinutes:         60,
				MainWeight:              1,
			},
		},
		MailRules: []domain.MailRule{
			{
				RuleType: domain.MailRuleSender,
				Pattern:  ".*",
				Enabled:  true,
			},
			{
				RuleType: domain.MailRuleRecipient,
				Pattern:  "exact",
				Enabled:  true,
			},
		},
	}
}

func projectTestLog(requestID string) *governancedomain.OperationLog {
	return &governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "core.project.create",
		ResourceType:   "project",
		ResourceID:     "new",
		Path:           "/v1/admin/projects",
		Result:         "success",
		SafeSummary:    "Listed project created.",
		RequestID:      requestID,
	}
}
