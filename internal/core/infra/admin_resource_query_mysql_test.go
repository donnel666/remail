package infra

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type adminQueryOwnerPort struct {
	owners map[uint]coreapp.AdminOwnerSummary
}

func (p adminQueryOwnerPort) GetByIDs(_ context.Context, ids []uint) (map[uint]coreapp.AdminOwnerSummary, error) {
	result := make(map[uint]coreapp.AdminOwnerSummary, len(ids))
	for _, id := range ids {
		if owner, ok := p.owners[id]; ok {
			result[id] = owner
		}
	}
	return result, nil
}

func (p adminQueryOwnerPort) SearchAdminOwners(_ context.Context, search string, _ int) ([]coreapp.AdminOwnerSummary, error) {
	search = strings.ToLower(strings.TrimSpace(search))
	result := make([]coreapp.AdminOwnerSummary, 0)
	for _, owner := range p.owners {
		if strings.Contains(strings.ToLower(owner.Email), search) || strings.Contains(strings.ToLower(owner.Nickname), search) {
			result = append(result, owner)
		}
	}
	return result, nil
}

func (p adminQueryOwnerPort) ValidateTargetOwner(_ context.Context, id uint) (*coreapp.AdminOwnerSummary, error) {
	owner, ok := p.owners[id]
	if !ok {
		return nil, nil
	}
	return &owner, nil
}

type adminQueryBindingPort map[uint]coreapp.AdminBindingSummary

func (p adminQueryBindingPort) GetByResourceIDs(_ context.Context, ids []uint) (map[uint]coreapp.AdminBindingSummary, error) {
	result := make(map[uint]coreapp.AdminBindingSummary, len(ids))
	for _, id := range ids {
		if binding, ok := p[id]; ok {
			result[id] = binding
		}
	}
	return result, nil
}

func (adminQueryBindingPort) CountActiveByDomains(context.Context, []string) (map[string]int64, error) {
	return map[string]int64{}, nil
}

type adminQueryAliasSchedulePort struct {
	summary coreapp.AdminAliasScheduleSummary
}

func (p adminQueryAliasSchedulePort) GetAdminAliasSchedule(context.Context, uint) (*coreapp.AdminAliasScheduleSummary, error) {
	result := p.summary
	return &result, nil
}

func TestAdminResourceQueryListFacetsDetailAndAliasesMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	adminRepo := NewAdminResourceRepo(db)
	ctx := context.Background()

	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, nickname, role, status)
VALUES
    (1, 'alpha-owner@test.local', 'hash', 'Alpha Supplier', 'supplier', 'active'),
    (2, 'beta-owner@test.local', 'hash', 'Beta Supplier', 'supplier', 'active')`).Error)

	now := time.Now().UTC().Truncate(time.Millisecond)
	validExpiry := now.Add(30 * 24 * time.Hour)
	r1Root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	r1 := &domain.MicrosoftResource{
		EmailAddress: "alpha@outlook.com", Password: "secret-alpha", ClientID: "client-alpha", RefreshToken: "refresh-alpha",
		Status: domain.MicrosoftStatusNormal, ForSale: true, LongLived: true, GraphAvailable: true, QualityScore: 95, RTExpireAt: &validExpiry,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(ctx, r1Root, r1))
	r2Root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 2}
	r2 := &domain.MicrosoftResource{
		EmailAddress: "beta@hotmail.com", Password: "secret-beta", Status: domain.MicrosoftStatusAbnormal,
		ForSale: false, LongLived: false, GraphAvailable: false, QualityScore: 40, LastSafeError: "Safe validation failure.",
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(ctx, r2Root, r2))
	r3Root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	r3 := &domain.MicrosoftResource{EmailAddress: "deleted@outlook.com", Password: "secret-deleted", Status: domain.MicrosoftStatusDeleted}
	require.NoError(t, resourceRepo.CreateMicrosoft(ctx, r3Root, r3))

	require.NoError(t, db.Exec(
		"INSERT INTO explicit_aliases(resource_id, owner_user_id, email, status) VALUES (?, ?, ?, ?), (?, ?, ?, ?)",
		r1Root.ID, 1, "explicit-one@outlook.com", "normal", r1Root.ID, 1, "explicit-two@outlook.com", "normal",
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO dot_aliases(resource_id, email, status) VALUES (?, ?, ?)",
		r1Root.ID, "a.l.p.h.a@outlook.com", "normal",
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO plus_aliases(resource_id, email, status) VALUES (?, ?, ?)",
		r1Root.ID, "alpha+one@outlook.com", "normal",
	).Error)

	owners := adminQueryOwnerPort{owners: map[uint]coreapp.AdminOwnerSummary{
		1: {ID: 1, Email: "alpha-owner@test.local", Nickname: "Alpha Supplier", GroupName: "Supplier", Role: "supplier", Enabled: true},
		2: {ID: 2, Email: "beta-owner@test.local", Nickname: "Beta Supplier", GroupName: "Supplier", Role: "supplier", Enabled: true},
	}}
	bindings := adminQueryBindingPort{
		r1Root.ID: {ResourceID: r1Root.ID, EmailAddress: "alpha-aux@example.net", Status: "verified", UpdatedAt: now},
	}
	nextRun := now.Add(time.Hour)
	query := coreapp.NewAdminResourceQuery(adminRepo)
	query.SetPorts(owners, bindings, nil, adminQueryAliasSchedulePort{summary: coreapp.AdminAliasScheduleSummary{
		WeekCreated: 1, WeekLimit: 2, YearCreated: 4, YearLimit: 10, NextRunAt: &nextRun,
	}})

	list, err := query.List(ctx, coreapp.AdminMicrosoftListFilter{}, 0, 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 2, list.Total)
	require.Len(t, list.Items, 2)
	require.Equal(t, r2Root.ID, list.Items[0].ID, "admin list is stable id DESC")
	require.Equal(t, r1Root.ID, list.Items[1].ID)
	require.EqualValues(t, 2, list.Facets.Status.All)
	require.EqualValues(t, 1, list.Facets.Status.Normal)
	require.EqualValues(t, 1, list.Facets.Status.Abnormal)
	require.EqualValues(t, 1, list.Facets.Status.Deleted)
	require.EqualValues(t, 1, list.Facets.ForSale.Yes)
	require.EqualValues(t, 1, list.Facets.ForSale.No)
	require.EqualValues(t, 1, list.Facets.TokenHealth.Valid)
	require.EqualValues(t, 1, list.Facets.TokenHealth.Missing)
	require.Equal(t, "alpha-aux@example.net", *list.Items[1].BindingAddress)
	require.Equal(t, "graph", list.Items[1].MailProtocol)
	require.Equal(t, "missing", list.Items[0].TokenHealth)

	ownerSearch, err := query.List(ctx, coreapp.AdminMicrosoftListFilter{Search: "beta-owner"}, 0, 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, ownerSearch.Total)
	require.Equal(t, r2Root.ID, ownerSearch.Items[0].ID)

	for _, search := range []string{"explicit-two@outlook.com", "a.l.p.h.a@outlook.com", "alpha+one@outlook.com"} {
		aliasSearch, err := query.List(ctx, coreapp.AdminMicrosoftListFilter{Search: search}, 0, 20, 0)
		require.NoError(t, err)
		require.EqualValues(t, 1, aliasSearch.Total, search)
		require.Equal(t, r1Root.ID, aliasSearch.Items[0].ID, search)
		require.Equal(t, "alpha@outlook.com", aliasSearch.Items[0].EmailAddress, search)
	}

	suffixList, err := query.List(ctx, coreapp.AdminMicrosoftListFilter{Suffix: "@outlook.com"}, 0, 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, suffixList.Total)
	require.Equal(t, r1Root.ID, suffixList.Items[0].ID)

	detail, err := query.Get(ctx, r1Root.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, detail.AliasCounts.Explicit)
	require.EqualValues(t, 1, detail.AliasCounts.Dot)
	require.EqualValues(t, 1, detail.AliasCounts.Plus)
	require.True(t, detail.Credentials.PasswordConfigured)
	require.True(t, detail.Credentials.ClientIDConfigured)
	require.True(t, detail.Credentials.RefreshTokenConfigured)
	require.EqualValues(t, 1, detail.Credentials.Revision)
	require.Equal(t, "valid", detail.Token.Health)
	require.Equal(t, []string{
		"https://graph.microsoft.com/Mail.Read",
		"https://graph.microsoft.com/Mail.Send",
		"User.Read",
		"offline_access",
	}, detail.Token.Scopes)

	missingOAuthDetail, err := query.Get(ctx, r2Root.ID)
	require.NoError(t, err)
	require.NotNil(t, missingOAuthDetail.Token.Scopes)
	require.Empty(t, missingOAuthDetail.Token.Scopes,
		"the ACL request allowlist is only applicable when both OAuth inputs are configured")

	explicit, err := query.ListAliases(ctx, r1Root.ID, "explicit", 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 2, explicit.Total)
	require.Len(t, explicit.Items, 2)
	require.NotNil(t, explicit.Schedule)
	require.Equal(t, 2, explicit.Schedule.WeekLimit)
	other, err := query.ListAliases(ctx, r1Root.ID, "other", 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 2, other.Total)
	require.Len(t, other.Items, 2)
	require.Nil(t, other.Schedule)
}

func TestAdminMicrosoftIdentifyingStatusFilterAndFacetMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, nickname, role, status)
VALUES (9101, 'identifying-owner@test.local', 'hash', 'Identifying Owner', 'supplier', 'active')`).Error)

	resources := NewResourceRepo(db)
	identifyingRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 9101}
	require.NoError(t, resources.CreateMicrosoft(ctx, identifyingRoot, &domain.MicrosoftResource{
		EmailAddress: "identifying@outlook.com", Password: "secret", ClientID: "client", RefreshToken: "refresh",
		Status: domain.MicrosoftStatusIdentifying,
	}))
	normalRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 9101}
	require.NoError(t, resources.CreateMicrosoft(ctx, normalRoot, &domain.MicrosoftResource{
		EmailAddress: "normal@outlook.com", Password: "secret", Status: domain.MicrosoftStatusNormal,
	}))

	repo := NewAdminResourceRepo(db)
	items, total, err := repo.ListAdminMicrosoft(ctx, coreapp.AdminMicrosoftListFilter{
		Status: domain.MicrosoftStatusIdentifying,
	}, 0, 20, 0, time.Now().UTC())
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, items, 1)
	require.Equal(t, identifyingRoot.ID, items[0].ID)

	facets, err := repo.AdminMicrosoftFacets(ctx, coreapp.AdminMicrosoftListFilter{}, time.Now().UTC())
	require.NoError(t, err)
	require.EqualValues(t, 2, facets.Status.All)
	require.EqualValues(t, 1, facets.Status.Identifying)
	require.EqualValues(t, 1, facets.Status.Normal)
}

func TestAdminResourceQueryPlansKeepListBoundedAndUseResourceIndexesMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	rareResourceID := seedAdminResourceQueryPlanFacts(t, db, 256)

	for _, item := range []struct {
		table string
		index string
	}{
		{"microsoft_resources", "idx_microsoft_bulk_domain"},
		{"microsoft_resources", "idx_microsoft_status"},
		{"explicit_aliases", "idx_explicit_aliases_resource_created_id"},
		{"dot_aliases", "idx_dot_aliases_resource_created_id"},
		{"plus_aliases", "idx_plus_aliases_resource_created_id"},
	} {
		requireIndexExists(t, db, item.table, item.index)
	}

	// The list query must stay a resource-only page. Alias counts and recent
	// tasks are single-resource detail concerns and must not add list joins.
	require.NotContains(t, strings.ToLower(adminMicrosoftListSelect), "select count(")
	require.Contains(t, strings.ToLower(adminMicrosoftDetailSelect), "from explicit_aliases")
	require.Contains(t, strings.ToLower(adminMicrosoftDetailSelect), "from dot_aliases")
	require.Contains(t, strings.ToLower(adminMicrosoftDetailSelect), "from plus_aliases")

	listExplain := `EXPLAIN SELECT ` + adminMicrosoftListSelect + `
FROM email_resources AS er
JOIN microsoft_resources AS mr ON mr.id = er.id AND er.type = 'microsoft'
WHERE mr.status <> 'deleted' AND mr.email_domain = 'rare-admin-plan.example'
ORDER BY er.id DESC
LIMIT 20`
	requireAdminExplainTableUsesOneOf(t, db, listExplain, "mr", 8,
		"idx_microsoft_bulk_domain")
	requireAdminExplainTableUsesOneOf(t, db, listExplain, "er", 8, "PRIMARY")

	facetExplain := `EXPLAIN SELECT mr.email_domain AS suffix, COUNT(*) AS count
FROM microsoft_resources AS mr
WHERE mr.status <> 'deleted'
GROUP BY mr.email_domain
ORDER BY count DESC, suffix ASC`
	requireAdminExplainTableUsesOneOf(t, db, facetExplain, "mr", 512,
		"idx_microsoft_bulk_domain", "idx_microsoft_status")

	detailExplain := fmt.Sprintf(`EXPLAIN SELECT %s
FROM email_resources AS er
JOIN microsoft_resources AS mr ON mr.id = er.id AND er.type = 'microsoft'
WHERE er.id = %d
LIMIT 1`, adminMicrosoftDetailSelect, rareResourceID)
	requireAdminExplainTableUsesOneOf(t, db, detailExplain, "er", 1, "PRIMARY")
	requireAdminExplainTableUsesOneOf(t, db, detailExplain, "mr", 1, "PRIMARY")
	requireAdminExplainTableUsesOneOf(t, db, detailExplain, "ea", 64,
		"idx_explicit_aliases_resource_created_id", "idx_explicit_aliases_resource_email", "idx_explicit_aliases_alloc_reuse")
	requireAdminExplainTableUsesOneOf(t, db, detailExplain, "da", 64,
		"idx_dot_aliases_resource_created_id", "idx_dot_aliases_resource_email", "idx_dot_aliases_alloc_reuse")
	requireAdminExplainTableUsesOneOf(t, db, detailExplain, "pa", 64,
		"idx_plus_aliases_resource_created_id", "idx_plus_aliases_resource_email", "idx_plus_aliases_alloc_reuse")

	explicitExplain := fmt.Sprintf(`EXPLAIN SELECT id, email, created_at
FROM explicit_aliases
WHERE resource_id = %d
ORDER BY created_at DESC, id DESC
LIMIT 20 OFFSET 0`, rareResourceID)
	requireAdminExplainTableUsesOneOf(t, db, explicitExplain, "explicit_aliases", 64,
		"idx_explicit_aliases_resource_created_id", "idx_explicit_aliases_resource_email", "idx_explicit_aliases_alloc_reuse")

	otherExplain := fmt.Sprintf(`EXPLAIN SELECT * FROM (
    SELECT id, email, created_at FROM dot_aliases AS da WHERE resource_id = %d
    UNION ALL
    SELECT id, email, created_at FROM plus_aliases AS pa WHERE resource_id = %d
) AS aliases
ORDER BY created_at DESC, id DESC
LIMIT 20 OFFSET 0`, rareResourceID, rareResourceID)
	requireAdminExplainTableUsesOneOf(t, db, otherExplain, "da", 64,
		"idx_dot_aliases_resource_created_id", "idx_dot_aliases_resource_email", "idx_dot_aliases_alloc_reuse")
	requireAdminExplainTableUsesOneOf(t, db, otherExplain, "pa", 64,
		"idx_plus_aliases_resource_created_id", "idx_plus_aliases_resource_email", "idx_plus_aliases_alloc_reuse")
}

func TestAdminMicrosoftFilterQueryUsesOneRootJoinShapeMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t).Session(&gorm.Session{DryRun: true})
	repo := NewAdminResourceRepo(db)
	var total int64
	result := repo.adminMicrosoftFilterQuery(
		context.Background(),
		coreapp.AdminMicrosoftListFilter{Search: "owner", OwnerIDs: []uint{9901}},
		time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC),
		"",
	).Count(&total)
	require.NoError(t, result.Error)
	sql := strings.ToLower(strings.ReplaceAll(result.Statement.SQL.String(), string(rune(96)), ""))
	require.Contains(t, sql, "from email_resources as er")
	require.Contains(t, sql, "join microsoft_resources as mr")
	require.Contains(t, sql, "exists (select 1 from explicit_aliases ea")
	require.Contains(t, sql, "exists (select 1 from dot_aliases da")
	require.Contains(t, sql, "exists (select 1 from plus_aliases pa")
	require.Contains(t, sql, "er.owner_user_id in (?)")
}

func seedAdminResourceQueryPlanFacts(t *testing.T, db *gorm.DB, count int) uint {
	t.Helper()
	require.GreaterOrEqual(t, count, 32)
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, nickname, role, status)
VALUES (9901, 'admin-query-plan-owner@test.local', 'hash', 'Query Plan Owner', 'supplier', 'active')`).Error)

	rootValues := make([]string, 0, count)
	rootArgs := make([]any, 0, count*5)
	microsoftValues := make([]string, 0, count)
	microsoftArgs := make([]any, 0, count*7)
	base := time.Date(2026, time.July, 12, 8, 0, 0, 0, time.UTC)
	rareResourceID := uint(0)
	for i := 0; i < count; i++ {
		id := uint(991000 + i)
		domainName := "common-admin-plan.example"
		status := "normal"
		if i%19 == 0 {
			status = "deleted"
		}
		if i == count/2 {
			domainName = "rare-admin-plan.example"
			status = "normal"
			rareResourceID = id
		}
		createdAt := base.Add(time.Duration(i) * time.Second)
		rootValues = append(rootValues, "(?, 'microsoft', 9901, 1, ?, ?)")
		rootArgs = append(rootArgs, id, createdAt, createdAt)
		microsoftValues = append(microsoftValues, "(?, 'microsoft', ?, ?, 'write-only', ?, ?, ?, ?)")
		microsoftArgs = append(microsoftArgs,
			id,
			fmt.Sprintf("query-plan-%d@%s", id, domainName),
			domainName,
			status,
			80,
			createdAt,
			createdAt,
		)
	}
	require.NotZero(t, rareResourceID)
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id, version, created_at, updated_at)
VALUES `+strings.Join(rootValues, ","), rootArgs...).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_resources(
    id, resource_type, email_address, email_domain, password, status, quality_score, created_at, updated_at
)
VALUES `+strings.Join(microsoftValues, ","), microsoftArgs...).Error)

	explicitValues := make([]string, 0, 32)
	explicitArgs := make([]any, 0, 32*4)
	dotValues := make([]string, 0, 32)
	dotArgs := make([]any, 0, 32*3)
	plusValues := make([]string, 0, 32)
	plusArgs := make([]any, 0, 32*3)
	for i := 0; i < 32; i++ {
		createdAt := base.Add(time.Duration(i) * time.Minute)
		explicitValues = append(explicitValues, "(?, 9901, ?, 'normal', ?)")
		explicitArgs = append(explicitArgs, rareResourceID, fmt.Sprintf("explicit-%02d@rare-admin-plan.example", i), createdAt)
		dotValues = append(dotValues, "(?, ?, 'normal', ?)")
		dotArgs = append(dotArgs, rareResourceID, fmt.Sprintf("dot-%02d@rare-admin-plan.example", i), createdAt)
		plusValues = append(plusValues, "(?, ?, 'normal', ?)")
		plusArgs = append(plusArgs, rareResourceID, fmt.Sprintf("plus-%02d@rare-admin-plan.example", i), createdAt)
	}
	for i := 0; i < count; i++ {
		resourceID := uint(991000 + i)
		if resourceID == rareResourceID {
			continue
		}
		createdAt := base.Add(time.Duration(i) * time.Second)
		explicitValues = append(explicitValues, "(?, 9901, ?, 'normal', ?)")
		explicitArgs = append(explicitArgs, resourceID, fmt.Sprintf("explicit-decoy-%d@example.test", resourceID), createdAt)
		dotValues = append(dotValues, "(?, ?, 'normal', ?)")
		dotArgs = append(dotArgs, resourceID, fmt.Sprintf("dot-decoy-%d@example.test", resourceID), createdAt)
		plusValues = append(plusValues, "(?, ?, 'normal', ?)")
		plusArgs = append(plusArgs, resourceID, fmt.Sprintf("plus-decoy-%d@example.test", resourceID), createdAt)
	}
	require.NoError(t, db.Exec(`
INSERT INTO explicit_aliases(resource_id, owner_user_id, email, status, created_at)
VALUES `+strings.Join(explicitValues, ","), explicitArgs...).Error)
	require.NoError(t, db.Exec(`
INSERT INTO dot_aliases(resource_id, email, status, created_at)
VALUES `+strings.Join(dotValues, ","), dotArgs...).Error)
	require.NoError(t, db.Exec(`
INSERT INTO plus_aliases(resource_id, email, status, created_at)
VALUES `+strings.Join(plusValues, ","), plusArgs...).Error)
	require.NoError(t, db.Exec("ANALYZE TABLE email_resources, microsoft_resources, explicit_aliases, dot_aliases, plus_aliases").Error)
	return rareResourceID
}

func requireAdminExplainTableUsesOneOf(
	t *testing.T,
	db *gorm.DB,
	query string,
	table string,
	maxEstimatedRows int64,
	expectedKeys ...string,
) {
	t.Helper()
	var rows []struct {
		Table      sql.NullString `gorm:"column:table"`
		Key        sql.NullString `gorm:"column:key"`
		Rows       sql.NullInt64  `gorm:"column:rows"`
		AccessType sql.NullString `gorm:"column:type"`
	}
	require.NoError(t, db.Raw(query).Scan(&rows).Error)
	require.NotEmpty(t, rows, "expected EXPLAIN rows for %s", query)
	allowed := make(map[string]struct{}, len(expectedKeys))
	for _, key := range expectedKeys {
		allowed[key] = struct{}{}
	}
	for _, row := range rows {
		if !row.Table.Valid || row.Table.String != table {
			continue
		}
		require.True(t, row.Key.Valid, "expected %s to use an index: %s", table, query)
		_, ok := allowed[row.Key.String]
		require.True(t, ok, "expected %s to use one of %v, saw %s: %s", table, expectedKeys, row.Key.String, query)
		require.NotEqual(t, "ALL", row.AccessType.String, "unexpected full scan of %s: %s", table, query)
		require.True(t, row.Rows.Valid, "expected row estimate for %s: %s", table, query)
		require.LessOrEqual(t, row.Rows.Int64, maxEstimatedRows, "unexpected row estimate for %s: %s", table, query)
		return
	}
	require.Failf(t, "EXPLAIN table missing", "expected table %s in plan: %+v\n%s", table, rows, query)
}
