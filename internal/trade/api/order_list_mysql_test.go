package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func checkoutListOrder(
	t *testing.T,
	uc *tradeapp.UseCase,
	userID uint,
	projectID uint,
	productID uint,
	serviceMode string,
	idempotencyKey string,
) tradeapp.CheckoutResult {
	t.Helper()
	result, err := uc.Checkout(context.Background(), tradeapp.CheckoutRequest{
		UserID:         userID,
		ProjectID:      projectID,
		ProductID:      productID,
		ServiceMode:    serviceMode,
		SupplyPolicy:   "public_only",
		ClientChannel:  tradedomain.ClientChannelConsole,
		IdempotencyKey: idempotencyKey,
		RequestID:      "req-" + idempotencyKey,
	})
	require.NoError(t, err)
	require.Equal(t, tradedomain.OrderStatusActive, result.Order.Status)
	return *result
}

func setOrderCreatedAt(t *testing.T, db *gorm.DB, orderNo string, createdAt time.Time) {
	t.Helper()
	require.NoError(t, db.Exec(
		"UPDATE orders SET created_at = ?, updated_at = ? WHERE order_no = ?",
		createdAt.UTC(), createdAt.UTC(), orderNo,
	).Error)
}

func TestListOrdersFiltersFacetsAndPagingMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	require.NoError(t, db.Exec(`
INSERT INTO projects(id, name, target_platform, logo_url, status, access_type, loose_match)
VALUES (11, 'Second Project', 'second', '/v1/projects/logos/second-project', 'listed', 'public', TRUE)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_products(
    id, project_id, type, status, code_enabled, purchase_enabled,
    code_price, purchase_price, code_supplier_price, purchase_supplier_price,
    code_window_minutes, activation_window_minutes, warranty_minutes,
    main_weight, dot_weight, plus_weight
)
SELECT 21, 11, type, status, code_enabled, purchase_enabled,
    code_price, purchase_price, code_supplier_price, purchase_supplier_price,
    code_window_minutes, activation_window_minutes, warranty_minutes,
    main_weight, dot_weight, plus_weight
FROM project_products WHERE id = 20`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_mail_rules(project_id, rule_type, pattern, enabled) VALUES
    (11, 'sender', '.*', TRUE),
    (11, 'recipient', 'exact', TRUE)`).Error)
	require.NoError(t, db.Table("users").Where("id = ?", 3).Update("nickname", "Regular Customer").Error)
	creditBuyer(t, db, 2, "50.00")
	creditBuyer(t, db, 3, "50.00")

	uc := newTradeUseCase(db)
	ctx := context.Background()

	// Seed exactly one available resource before each checkout so the
	// delivery email mapping stays deterministic regardless of the
	// allocation picking strategy.
	seedTradeMicrosoftResource(t, db, 1, 1001, "a1@outlook.test", "outlook.test", 100, true)
	first := checkoutListOrder(t, uc, 2, 10, 20, "code", "order-list-1")
	seedTradeMicrosoftResource(t, db, 1, 1002, "a2@outlook.test", "outlook.test", 99, true)
	second := checkoutListOrder(t, uc, 2, 10, 20, "code", "order-list-2")
	seedTradeMicrosoftResource(t, db, 1, 1003, "b1@hotmail.test", "hotmail.test", 98, true)
	third := checkoutListOrder(t, uc, 2, 11, 21, "purchase", "order-list-3")
	seedTradeMicrosoftResource(t, db, 1, 1004, "b2@hotmail.test", "hotmail.test", 97, true)
	fourth := checkoutListOrder(t, uc, 2, 11, 21, "purchase", "order-list-4")
	seedTradeMicrosoftResource(t, db, 1, 1005, "c1@outlook.test", "outlook.test", 96, true)
	other := checkoutListOrder(t, uc, 3, 10, 20, "code", "order-list-other")
	seedTradeMicrosoftResource(t, db, 1, 1006, "history@history.test", "history.test", 95, true)
	matchedAt := time.Now().UTC().Add(-time.Hour)
	require.NoError(t, uc.ImportHistoricalMicrosoftUsage(ctx, []tradeapp.HistoricalMicrosoftUsage{{
		ResourceID: 1006, ProjectID: 10, ProductID: 20, Mailbox: "main", Email: "history@history.test",
		FirstMatchedAt: matchedAt.Add(-time.Hour), LastMatchedAt: matchedAt, EvidenceCount: 1,
	}}))
	var historicalOrder struct {
		OrderNo string `gorm:"column:order_no"`
	}
	require.NoError(t, db.Table("orders").Select("order_no").Where("order_no LIKE 'HIST-%'").Take(&historicalOrder).Error)

	require.Equal(t, "a1@outlook.test", first.Order.DeliveryEmail)
	require.Equal(t, "a2@outlook.test", second.Order.DeliveryEmail)
	require.Equal(t, "b1@hotmail.test", third.Order.DeliveryEmail)
	require.Equal(t, "b2@hotmail.test", fourth.Order.DeliveryEmail)
	require.Equal(t, "c1@outlook.test", other.Order.DeliveryEmail)

	// Second code order matches a code and completes.
	require.NoError(t, uc.NotifyMatchedCode(ctx, tradeapp.MatchCodeResultRequest{
		OrderNo:   second.Order.OrderNo,
		MatchedAt: time.Now().UTC(),
	}))
	// Fourth purchase order is refunded by the admin.
	_, err := uc.AdminRefundOrder(ctx, tradeapp.AdminOrderCommandRequest{
		OrderNo:        fourth.Order.OrderNo,
		Reason:         "test refund",
		IdempotencyKey: "order-list-refund",
		RequestID:      "req-order-list-refund",
		OperatorUserID: 1,
	})
	require.NoError(t, err)

	base := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	orderNos := []string{
		first.Order.OrderNo, second.Order.OrderNo, third.Order.OrderNo, fourth.Order.OrderNo,
	}
	for i, orderNo := range orderNos {
		setOrderCreatedAt(t, db, orderNo, base.Add(time.Duration(i)*24*time.Hour))
	}

	// Unfiltered list: totals, ordering, facets and project names.
	all, err := uc.ListOrders(ctx, tradeapp.OrderListFilter{UserID: 2}, 0, 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 4, all.Total)
	require.Len(t, all.Items, 4)
	require.Equal(t,
		[]string{fourth.Order.OrderNo, third.Order.OrderNo, second.Order.OrderNo, first.Order.OrderNo},
		[]string{all.Items[0].Order.OrderNo, all.Items[1].Order.OrderNo, all.Items[2].Order.OrderNo, all.Items[3].Order.OrderNo},
	)
	require.Nil(t, all.NextAfterID)
	for _, item := range all.Items {
		if item.Order.ProjectID == 10 {
			require.Equal(t, "Trade Project", item.ProjectName)
			require.Equal(t, "/v1/projects/logos/trade-project", item.ProjectLogoURL)
		} else {
			require.Equal(t, uint(11), item.Order.ProjectID)
			require.Equal(t, "Second Project", item.ProjectName)
			require.Equal(t, "/v1/projects/logos/second-project", item.ProjectLogoURL)
		}
	}
	require.NotNil(t, all.Facets)
	require.EqualValues(t, 4, all.Facets.Status.All)
	require.EqualValues(t, 2, all.Facets.Status.Active)
	require.EqualValues(t, 1, all.Facets.Status.Completed)
	require.EqualValues(t, 1, all.Facets.Status.Refunded)
	require.EqualValues(t, 4, all.Facets.ServiceMode.All)
	require.EqualValues(t, 2, all.Facets.ServiceMode.Code)
	require.EqualValues(t, 2, all.Facets.ServiceMode.Purchase)
	require.Len(t, all.Facets.Projects, 2)
	require.Equal(t, uint(10), all.Facets.Projects[0].ProjectID)
	require.Equal(t, "Trade Project", all.Facets.Projects[0].Name)
	require.Equal(t, "/v1/projects/logos/trade-project", all.Facets.Projects[0].LogoURL)
	require.EqualValues(t, 2, all.Facets.Projects[0].Count)
	require.Equal(t, uint(11), all.Facets.Projects[1].ProjectID)
	require.Equal(t, "Second Project", all.Facets.Projects[1].Name)
	require.Equal(t, "/v1/projects/logos/second-project", all.Facets.Projects[1].LogoURL)
	require.EqualValues(t, 2, all.Facets.Projects[1].Count)
	require.Equal(t, []tradeapp.OrderKeyFacet{
		{Key: "hotmail.test", Count: 2},
		{Key: "outlook.test", Count: 2},
	}, all.Facets.Domains)

	// Project filter keeps the full project tab set by excluding its own
	// dimension from the project facets query.
	secondProject, err := uc.ListOrders(ctx, tradeapp.OrderListFilter{UserID: 2, ProjectID: 11}, 0, 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 2, secondProject.Total)
	require.Equal(t,
		[]string{fourth.Order.OrderNo, third.Order.OrderNo},
		[]string{secondProject.Items[0].Order.OrderNo, secondProject.Items[1].Order.OrderNo},
	)
	require.Len(t, secondProject.Facets.Projects, 2)

	// Exercise the HTTP query/JSON boundary used by both consoles.
	handler := NewHandler(&Module{UseCase: uc})
	router := gin.New()
	router.GET("/v1/orders", func(c *gin.Context) {
		middleware.SetCurrentUser(c, 2, iamdomain.RoleUser, "", "")
		handler.GetOrders(c)
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/orders?projectId=11", nil))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var httpList OrderListResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &httpList))
	require.EqualValues(t, 2, httpList.Total)
	require.Len(t, httpList.Items, 2)
	for _, item := range httpList.Items {
		require.Equal(t, uint(11), item.ProjectID)
	}
	require.NotNil(t, httpList.Facets)
	require.Len(t, httpList.Facets.Projects, 2)
	require.Equal(t, "/v1/projects/logos/second-project", httpList.Facets.Projects[1].LogoURL)
	require.Equal(t, []OrderKeyFacetResponse{{Key: "hotmail.test", Count: 2}}, httpList.Facets.Domains)

	// Domain filter, with and without the "@" prefix.
	outlook, err := uc.ListOrders(ctx, tradeapp.OrderListFilter{UserID: 2, Domain: "outlook.test"}, 0, 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 2, outlook.Total)
	require.Len(t, outlook.Items, 2)
	require.EqualValues(t, 1, outlook.Facets.Status.Active)
	require.EqualValues(t, 1, outlook.Facets.Status.Completed)
	require.Len(t, outlook.Facets.Projects, 1)

	prefixed, err := uc.ListOrders(ctx, tradeapp.OrderListFilter{UserID: 2, Domain: "@Outlook.TEST"}, 0, 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 2, prefixed.Total)

	// Service mode filter keeps self-excluded mode facets.
	code, err := uc.ListOrders(ctx, tradeapp.OrderListFilter{UserID: 2, ServiceMode: "code"}, 0, 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 2, code.Total)
	require.EqualValues(t, 4, code.Facets.ServiceMode.All)
	require.EqualValues(t, 2, code.Facets.ServiceMode.Code)
	require.EqualValues(t, 2, code.Facets.ServiceMode.Purchase)

	// Created-at range picks the middle two orders.
	from := base.Add(12 * time.Hour)
	to := base.Add(60 * time.Hour)
	ranged, err := uc.ListOrders(ctx, tradeapp.OrderListFilter{UserID: 2, CreatedFrom: &from, CreatedTo: &to}, 0, 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 2, ranged.Total)
	require.Equal(t,
		[]string{third.Order.OrderNo, second.Order.OrderNo},
		[]string{ranged.Items[0].Order.OrderNo, ranged.Items[1].Order.OrderNo},
	)

	// Cursor and offset paging agree with each other.
	firstPage, err := uc.ListOrders(ctx, tradeapp.OrderListFilter{UserID: 2}, 0, 0, 2)
	require.NoError(t, err)
	require.EqualValues(t, 4, firstPage.Total)
	require.Len(t, firstPage.Items, 2)
	require.NotNil(t, firstPage.NextAfterID)
	require.Equal(t, third.Order.ID, *firstPage.NextAfterID)

	viaCursor, err := uc.ListOrders(ctx, tradeapp.OrderListFilter{UserID: 2}, 0, *firstPage.NextAfterID, 2)
	require.NoError(t, err)
	require.Equal(t,
		[]string{second.Order.OrderNo, first.Order.OrderNo},
		[]string{viaCursor.Items[0].Order.OrderNo, viaCursor.Items[1].Order.OrderNo},
	)

	viaOffset, err := uc.ListOrders(ctx, tradeapp.OrderListFilter{UserID: 2}, 2, 0, 2)
	require.NoError(t, err)
	require.Equal(t,
		[]string{second.Order.OrderNo, first.Order.OrderNo},
		[]string{viaOffset.Items[0].Order.OrderNo, viaOffset.Items[1].Order.OrderNo},
	)
	require.Nil(t, viaOffset.NextAfterID)

	// Search covers order number, delivery email, user email/nickname/ID and
	// project name/platform/ID.
	searched, err := uc.ListOrders(ctx, tradeapp.OrderListFilter{UserID: 2, Search: "b1@hotmail"}, 0, 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 1, searched.Total)
	require.Equal(t, third.Order.OrderNo, searched.Items[0].Order.OrderNo)
	byOrderNo, err := uc.ListOrders(ctx, tradeapp.OrderListFilter{UserID: 2, Search: first.Order.OrderNo}, 0, 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 1, byOrderNo.Total)
	require.Equal(t, first.Order.OrderNo, byOrderNo.Items[0].Order.OrderNo)
	byUser, err := uc.ListOrders(ctx, tradeapp.OrderListFilter{UserID: 1, IsAdmin: true, Scope: "all", Search: "Customer"}, 0, 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 1, byUser.Total)
	require.Equal(t, other.Order.OrderNo, byUser.Items[0].Order.OrderNo)
	byUserID, err := uc.ListOrders(ctx, tradeapp.OrderListFilter{UserID: 1, IsAdmin: true, Scope: "all", Search: "3"}, 0, 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 1, byUserID.Total)
	require.Equal(t, other.Order.OrderNo, byUserID.Items[0].Order.OrderNo)
	byProject, err := uc.ListOrders(ctx, tradeapp.OrderListFilter{UserID: 2, Search: "Project"}, 0, 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 4, byProject.Total)
	byProjectID, err := uc.ListOrders(ctx, tradeapp.OrderListFilter{UserID: 2, Search: "10"}, 0, 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 2, byProjectID.Total)

	// User isolation stays intact.
	otherList, err := uc.ListOrders(ctx, tradeapp.OrderListFilter{UserID: 3}, 0, 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 1, otherList.Total)
	require.Equal(t, other.Order.OrderNo, otherList.Items[0].Order.OrderNo)

	// Historical imports belong to the super administrator but stay out of
	// that administrator's personal order records and facets.
	adminMine, err := uc.ListOrders(ctx, tradeapp.OrderListFilter{UserID: 1, IsAdmin: true, Scope: "mine"}, 0, 0, 20)
	require.NoError(t, err)
	require.Zero(t, adminMine.Total)
	require.Empty(t, adminMine.Items)
	require.NotNil(t, adminMine.Facets)
	require.Zero(t, adminMine.Facets.Status.All)
	require.Zero(t, adminMine.Facets.ServiceMode.All)
	require.Empty(t, adminMine.Facets.Projects)
	require.Empty(t, adminMine.Facets.Domains)

	// The site-wide admin list uses the same historical-order exclusion.
	adminList, err := uc.ListOrders(ctx, tradeapp.OrderListFilter{UserID: 1, IsAdmin: true, Scope: "all"}, 0, 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 5, adminList.Total)
	require.Len(t, adminList.Items, 5)
	for _, item := range adminList.Items {
		require.NotEqual(t, historicalOrder.OrderNo, item.Order.OrderNo)
	}
	require.NotNil(t, adminList.Facets)
	require.EqualValues(t, 5, adminList.Facets.Status.All)
	require.EqualValues(t, 5, adminList.Facets.ServiceMode.All)
	require.Len(t, adminList.Facets.Projects, 2)
	require.Equal(t, uint(10), adminList.Facets.Projects[0].ProjectID)
	require.Equal(t, "Trade Project", adminList.Facets.Projects[0].Name)
	require.EqualValues(t, 3, adminList.Facets.Projects[0].Count)
	require.Equal(t, uint(11), adminList.Facets.Projects[1].ProjectID)
	require.Equal(t, "Second Project", adminList.Facets.Projects[1].Name)
	require.EqualValues(t, 2, adminList.Facets.Projects[1].Count)
}

func TestParseOrderDomainAndOptionalTime(t *testing.T) {
	domainCases := []struct {
		raw      string
		expected string
		ok       bool
	}{
		{"", "", true},
		{"outlook.com", "outlook.com", true},
		{"@Outlook.COM", "outlook.com", true},
		{" @sub.mail-hub.vip ", "sub.mail-hub.vip", true},
		{"bad%like", "", false},
		{"under_score", "", false},
	}
	for _, tc := range domainCases {
		got, ok := parseOrderDomain(tc.raw)
		require.Equal(t, tc.ok, ok, "domain %q", tc.raw)
		if tc.ok {
			require.Equal(t, tc.expected, got, "domain %q", tc.raw)
		}
	}

	parsed, ok := parseOptionalTime("2026-07-10T12:00:00+08:00")
	require.True(t, ok)
	require.NotNil(t, parsed)
	require.Equal(t, time.Date(2026, 7, 10, 4, 0, 0, 0, time.UTC), *parsed)

	empty, ok := parseOptionalTime("  ")
	require.True(t, ok)
	require.Nil(t, empty)

	_, ok = parseOptionalTime("2026-07-10")
	require.False(t, ok)
}
