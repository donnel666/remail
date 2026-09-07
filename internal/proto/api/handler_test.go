package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/donnel666/remail/api/middleware"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/proto/infra"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type protoTestRoot struct {
	ID          uint `gorm:"primaryKey"`
	Type        string
	OwnerUserID uint
	Version     uint64 `gorm:"default:1"`
}

func (protoTestRoot) TableName() string { return "email_resources" }

func TestProtoListNeverReturnsPassword(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:proto-api-list?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&protoTestRoot{}, &infra.Resource{}))
	require.NoError(t, db.Create(&protoTestRoot{ID: 1, Type: "proto", OwnerUserID: 7}).Error)
	require.NoError(t, db.Create(&infra.Resource{ID: 1, ResourceType: "proto", OwnerUserID: 7, EmailAddress: "safe@proto.test", Password: "secret", Status: "pending", ValidationGeneration: 1, CredentialRevision: 1}).Error)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/proto/resources?limit=20", nil)
	middleware.SetCurrentUser(c, 7, iamdomain.RoleSupplier, "owner@example.test", "session")
	(&handler{module: &Module{Service: infra.NewService(db)}}).listOwned(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "secret")
	require.Contains(t, recorder.Body.String(), `"passwordConfigured":true`)
}

func TestProtoGetNeverReturnsPassword(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:proto-api-get?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&protoTestRoot{}, &infra.Resource{}))
	require.NoError(t, db.Create(&protoTestRoot{ID: 1, Type: "proto", OwnerUserID: 7}).Error)
	require.NoError(t, db.Create(&infra.Resource{ID: 1, ResourceType: "proto", OwnerUserID: 7, EmailAddress: "safe@proto.test", Password: "secret", Status: "pending", Version: 1, ValidationGeneration: 1, CredentialRevision: 1}).Error)

	item, err := infra.NewService(db).GetResource(context.Background(), 1, nil)
	require.NoError(t, err)
	require.NotNil(t, item)
	require.Empty(t, item.Password)
	require.True(t, item.PasswordConfigured)
}

func TestProtoRouteSurfaceIsProviderScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterRoutes(r.Group("/v1"), NewModuleWithQueue(infra.NewService(nil), nil), middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
		return 1, iamdomain.RoleAdmin, "a@b.test", true
	}), allowProtoPermission{})
	seenImport := false
	for _, route := range r.Routes() {
		if route.Method == http.MethodPost && route.Path == "/v1/proto/resources/imports" {
			seenImport = true
		}
		if strings.Contains(route.Path, "microsoft") || strings.Contains(route.Path, "gmail") || strings.Contains(route.Path, "icloud") {
			t.Fatalf("provider leak in route %s", route.Path)
		}
	}
	if !seenImport {
		t.Fatal("Proto import route is missing")
	}
}

type allowProtoPermission struct{}

func (allowProtoPermission) Check(context.Context, uint, iamdomain.Role, string, string) (bool, error) {
	return true, nil
}

func TestProtoBulkResponseHidesCoordinationAndUsesReasonArray(t *testing.T) {
	encoded, err := json.Marshal(toBulkResponse(&infra.BulkStatus{TaskID: "proto_bulk:4", AfterID: 33, ThroughID: 88, Fingerprint: "private-fingerprint", ReasonCounts: map[string]int{"not_private": 2}}))
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "afterId")
	require.NotContains(t, string(encoded), "throughId")
	require.NotContains(t, string(encoded), "private-fingerprint")
	require.Contains(t, string(encoded), `"reasonCounts":[{"reason":"not_private","count":2}]`)
	encoded, err = json.Marshal(toBulkResponse(&infra.BulkStatus{}))
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"reasonCounts":[]`)
}

func TestProtoOpenRoutesRemainOwnedAndRejectPublish(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	open := r.Group("/v1/open")
	open.Use(func(c *gin.Context) {
		middleware.SetCurrentUser(c, 7, iamdomain.RoleSupplier, "owner@example.test", "api-key")
		c.Next()
	})
	RegisterOpenRoutes(open, NewModuleWithQueue(infra.NewService(nil), nil), allowProtoPermission{})
	for _, route := range r.Routes() {
		require.NotContains(t, route.Path, "admin")
		require.NotContains(t, route.Path, "publish")
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/open/proto/resources/bulk/publish", strings.NewReader(`{"resourceIds":[1]}`))
	request.Header.Set("Idempotency-Key", "key")
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

type onlyProtoPermission string

func (p onlyProtoPermission) Check(_ context.Context, _ uint, _ iamdomain.Role, _, action string) (bool, error) {
	return action == string(p), nil
}

func TestProtoAdminCommandPermissions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name, allowed, method, path, body string
		status                            int
	}{
		{"write cannot replace credentials", "write", http.MethodPut, "/1/credentials", `{"version":1,"password":"new-password"}`, http.StatusForbidden},
		{"write cannot replace credentials through edit", "write", http.MethodPatch, "/1", `{"version":1,"password":"new-password"}`, http.StatusForbidden},
		{"operate cannot edit", "operate", http.MethodPatch, "/1", `{"version":1,"qualityScore":50}`, http.StatusForbidden},
		{"no wildcard edit", "operate", http.MethodPost, "/1/edit", `{"version":1,"qualityScore":50}`, http.StatusNotFound},
		{"no wildcard credentials", "operate", http.MethodPost, "/1/credentials", `{"version":1,"password":"new-password"}`, http.StatusNotFound},
		{"write reaches ordinary edit", "write", http.MethodPatch, "/1", `{"version":1,"qualityScore":50}`, http.StatusServiceUnavailable},
		{"operate reaches credential replacement", "operate", http.MethodPut, "/1/credentials", `{"version":1,"password":"new-password"}`, http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := gin.New()
			RegisterRoutes(r.Group("/v1"), NewModuleWithQueue(infra.NewService(nil), nil), middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
				return 7, iamdomain.RoleAdmin, "admin@proto.test", true
			}), onlyProtoPermission(test.allowed))
			request := httptest.NewRequest(test.method, "/v1/admin/proto/resources"+test.path, strings.NewReader(test.body))
			request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "test-session"})
			request.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "test-csrf"})
			request.Header.Set(middleware.CSRFHeaderName, "test-csrf")
			request.Header.Set("Idempotency-Key", "permission-test")
			recorder := httptest.NewRecorder()
			r.ServeHTTP(recorder, request)
			require.Equal(t, test.status, recorder.Code, recorder.Body.String())
		})
	}
}

func TestProtoListVisibilityTotalsAndAdminSearch(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:proto-list-contract?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&protoTestRoot{}, &infra.Resource{}))
	for _, row := range []infra.Resource{
		{ID: 1, OwnerUserID: 7, EmailAddress: "one@proto.test", EmailDomain: "proto.test", Status: "pending"},
		{ID: 2, OwnerUserID: 7, EmailAddress: "deleted@proto.test", EmailDomain: "proto.test", Status: "deleted"},
		{ID: 3, OwnerUserID: 8, EmailAddress: "supplier@other.test", EmailDomain: "other.test", Status: "normal"},
		{ID: 4, OwnerUserID: 7, EmailAddress: "one-more@proto.test", EmailDomain: "proto.test", Status: "normal"},
		{ID: 5, OwnerUserID: 7, EmailAddress: "local_part@proto.test", EmailDomain: "proto.test", Status: "normal"},
	} {
		row.ResourceType = "proto"
		require.NoError(t, db.Create(&protoTestRoot{ID: row.ID, Type: "proto", OwnerUserID: row.OwnerUserID}).Error)
		require.NoError(t, db.Create(&row).Error)
	}
	for _, test := range []struct {
		name, query string
		owned       bool
		ids         []uint
	}{
		{"admin default hides deleted", "", false, []uint{5, 4, 3, 1}},
		{"admin can inspect deleted", "status=deleted", false, []uint{2}},
		{"owned default hides deleted", "", true, []uint{5, 4, 1}},
		{"owned explicit deleted is empty", "status=deleted", true, []uint{}},
		{"skipped total is omitted", "includeTotal=false&includeFacets=false&limit=1", false, []uint{5}},
		{"resource ID", "search=1", false, []uint{1}},
		{"owner match", "search=OwnerName", false, []uint{3}},
		{"domain match", "search=" + url.QueryEscape("@proto.test"), false, []uint{5, 4, 1}},
		{"empty domain matches nothing", "search=" + url.QueryEscape("@"), false, []uint{}},
		{"full address", "search=" + url.QueryEscape("one@proto.test"), false, []uint{1}},
		{"exact local part", "search=one", false, []uint{1}},
		{"literal local part underscore", "search=local_part", false, []uint{5}},
		{"owned never searches other owners", "search=OwnerName", true, []uint{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ownerSearches := 0
			h := &handler{module: &Module{Service: infra.NewService(db), SearchOwners: func(_ context.Context, search string, limit int) ([]uint, error) {
				ownerSearches++
				require.Equal(t, 1000, limit)
				if search == "OwnerName" {
					return []uint{8}, nil
				}
				return nil, nil
			}}}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/proto/resources?"+test.query, nil)
			var owner *uint
			if test.owned {
				id := uint(7)
				owner = &id
			}
			h.list(c, owner)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			var response struct {
				Items  []protoResourceResponse `json:"items"`
				Total  *int64                  `json:"total"`
				Facets infra.ResourceFacets    `json:"facets"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			ids := make([]uint, 0, len(response.Items))
			for _, item := range response.Items {
				ids = append(ids, item.ID)
			}
			require.Equal(t, test.ids, ids)
			if strings.Contains(test.query, "includeTotal=false") {
				require.NotContains(t, recorder.Body.String(), `"total"`)
				require.Contains(t, recorder.Body.String(), `"suffixes":[]`)
			} else {
				require.NotNil(t, response.Total)
				require.EqualValues(t, len(test.ids), *response.Total)
			}
			if test.owned {
				require.Zero(t, ownerSearches)
				require.Zero(t, response.Facets.Deleted)
			}
		})
	}
}
