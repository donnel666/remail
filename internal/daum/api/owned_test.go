package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/daum/domain"
	"github.com/donnel666/remail/internal/daum/infra"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type ownedImportItemRow struct {
	ID            uint64 `gorm:"primaryKey"`
	ImportID      uint64 `gorm:"uniqueIndex:uq_daum_import_line"`
	LineNumber    int    `gorm:"uniqueIndex:uq_daum_import_line"`
	ResourceID    *uint
	Outcome       string
	Category      string
	LastSafeError string
	CreatedAt     time.Time
}

func (ownedImportItemRow) TableName() string { return "daum_resource_import_items" }

type ownedFiles struct {
	governanceapp.FilePort
	objects map[string][]byte
	reads   int
}

func (f *ownedFiles) SavePrivate(_ context.Context, file governancedomain.PrivateFile) (*governancedomain.StoredPrivateFile, error) {
	f.objects[file.ObjectKey] = append([]byte(nil), file.ContentBytes...)
	return &governancedomain.StoredPrivateFile{ObjectKey: file.ObjectKey, FileName: file.FileName}, nil
}
func (f *ownedFiles) ReadPrivate(_ context.Context, key string) (*governancedomain.PrivateFile, error) {
	f.reads++
	return &governancedomain.PrivateFile{ObjectKey: key, ContentBytes: f.objects[key]}, nil
}
func (f *ownedFiles) DeletePrivate(_ context.Context, key string) error {
	delete(f.objects, key)
	return nil
}

func ownedTestModule(t *testing.T) *Module {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&testRoot{}, &infra.Resource{}, &infra.ExplicitAlias{}, &infra.BindingMailbox{}, &infra.MaintenanceRun{}, &infra.ImportStatus{}, &ownedImportItemRow{}))
	require.NoError(t, db.Exec(`CREATE TABLE daum_allocations (id INTEGER PRIMARY KEY, resource_id INTEGER, status TEXT);
CREATE TABLE daum_command_receipts (id INTEGER PRIMARY KEY, operator_user_id INTEGER, resource_id INTEGER, command TEXT, idempotency_key TEXT, request_fingerprint TEXT, reservation_token TEXT, status TEXT, result_json TEXT, created_at DATETIME, updated_at DATETIME, UNIQUE(operator_user_id, idempotency_key));`).Error)
	files := &ownedFiles{objects: map[string][]byte{}}
	module := NewModuleWithQueue(infra.NewService(db, files), nil)
	module.Files = files
	module.ValidateOwner = func(context.Context, uint) (bool, error) { return true, nil }
	module.ValidateSupplierOwner = func(context.Context, uint) (bool, error) { return true, nil }
	module.OwnerSummaries = func(context.Context, []uint) (map[uint]OwnerSummary, error) {
		t.Fatal("the owned console must never query user profiles")
		return nil, nil
	}
	return module
}

func ownedTestRouter(module *Module, user uint, role iamdomain.Role, guards ...gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterRoutes(router.Group("/v1"), module, middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
		return user, role, "", true
	}), daumPermissionCheckerFunc(func(context.Context, uint, iamdomain.Role, string, string) (bool, error) { return false, nil }), guards...)
	return router
}

func ownedRequest(router *gin.Engine, method, path, body, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	setDaumTestMutationHeaders(request)
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func assertOwnedResponseHasNoPrivateDetails(t *testing.T, body string) {
	t.Helper()
	for _, secret := range []string{"hanmail", "alias", "password", "Password", "ownerUserId", "ownerId", "binding", "kakao", "other@daum.net", "fixture-secret", "private-token"} {
		require.NotContains(t, body, secret)
	}
}

func TestOwnedDaumListHasIndependentSafeContract(t *testing.T) {
	module := ownedTestModule(t)
	for i, email := range []string{"owned@daum.net", "other@daum.net"} {
		_, _, err := module.Service.ImportLine(context.Background(), uint(7+i), domain.ImportLine{Email: email, Password: "fixture-secret", AppPassword: "fixture-secret"})
		require.NoError(t, err)
	}
	require.NoError(t, module.Service.DB.Model(&infra.Resource{}).Where("owner_user_id = 7").Updates(map[string]any{"status": "abnormal", "last_safe_error": "hidden@hanmail.net fixture-secret; stage=connect_tls; reason=network_unreachable"}).Error)
	router := ownedTestRouter(module, 7, iamdomain.RoleUser)
	response := ownedRequest(router, http.MethodGet, "/v1/daum/resources", "", "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assertOwnedResponseHasNoPrivateDetails(t, response.Body.String())
	var list ownedDaumResourceListResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &list))
	require.EqualValues(t, 1, *list.Total)
	require.Len(t, list.Items, 1)
	require.Equal(t, "owned@daum.net", list.Items[0].Email)
	require.Equal(t, "Daum mailbox connection failed. Please retry validation.", list.Items[0].LastSafeError)
	require.Equal(t, []infra.SuffixFacet{{Key: "daum.net", Count: 1}}, list.Facets.Suffixes)
	for _, query := range []string{"search=owned%40hanmail.net", "search=owned%40hanmail2.net", "search=other"} {
		response = ownedRequest(router, http.MethodGet, "/v1/daum/resources?"+query, "", "")
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		assertOwnedResponseHasNoPrivateDetails(t, response.Body.String())
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &list))
		require.Empty(t, list.Items)
		require.Zero(t, *list.Total)
	}
	for _, query := range []string{"suffix=hanmail.net", "suffix=hanmail2.net", "ownerId=8"} {
		response = ownedRequest(router, http.MethodGet, "/v1/daum/resources?"+query, "", "")
		require.Equal(t, http.StatusBadRequest, response.Code)
		assertOwnedResponseHasNoPrivateDetails(t, response.Body.String())
	}
	for _, path := range []string{"1", "1/aliases", "1/maintenance", "1/credentials", "1/history"} {
		response = ownedRequest(router, http.MethodGet, "/v1/daum/resources/"+path, "", "")
		require.Equal(t, http.StatusNotFound, response.Code)
	}
}

func ownedUpload(t *testing.T, router *gin.Engine, content, owner, key string, strategy ...string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if owner != "" {
		require.NoError(t, writer.WriteField("ownerId", owner))
	}
	if len(strategy) > 0 {
		require.NoError(t, writer.WriteField("errorStrategy", strategy[0]))
	}
	part, err := writer.CreateFormFile("file", "accounts.txt")
	require.NoError(t, err)
	_, err = part.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	request := httptest.NewRequest(http.MethodPost, "/v1/daum/resources/imports", &body)
	setDaumTestMutationHeaders(request)
	request.Header.Set("Idempotency-Key", key)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestOwnedDaumImportAcceptsThreeFormatsWithoutAdminPermissions(t *testing.T) {
	module := ownedTestModule(t)
	linked := false
	module.Service.ResolveBinding = func(_ context.Context, input infra.BindingInput) (infra.BindingResolution, error) {
		linked = true
		require.EqualValues(t, 7, input.OwnerUserID)
		require.Equal(t, "helper@outlook.com", input.BindingAddress)
		require.Contains(t, input.PickupURL, "private-token")
		source := uint(77)
		return infra.BindingResolution{Status: "linked", MailboxResourceID: &source, OrderNo: "owned-source-order"}, nil
	}
	router := ownedTestRouter(module, 7, iamdomain.RoleUser)
	content := "two@daum.net----fixture-secret\nthree@daum.net----login----fixture-secret\nfive@daum.net----login----fixture-secret----https://remail.test/pickup?email=helper@outlook.com&token=private-token----identity@kakao.com"
	response := ownedUpload(t, router, content, "", "owned-import")
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	assertOwnedResponseHasNoPrivateDetails(t, response.Body.String())
	var result daumImportResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	ids, err := module.Service.ProcessImportTask(context.Background(), result.ImportID, 1)
	require.NoError(t, err)
	require.Len(t, ids, 3)
	require.True(t, linked)
	status, err := module.Service.GetImport(context.Background(), result.ImportID)
	require.NoError(t, err)
	require.EqualValues(t, 7, status.OwnerUserID)
	require.EqualValues(t, 7, status.OperatorUserID)
	require.Equal(t, 3, status.ImportedCount)
	response = ownedUpload(t, router, content, "", "owned-import")
	require.Equal(t, http.StatusAccepted, response.Code)
	require.Contains(t, response.Body.String(), `"reused":true`)
	for _, path := range []string{"", "/items", "/failures"} {
		response = ownedRequest(ownedTestRouter(module, 8, iamdomain.RoleUser), http.MethodGet, fmt.Sprintf("/v1/daum/resources/imports/%d%s", result.ImportID, path), "", "")
		require.Equal(t, http.StatusNotFound, response.Code)
	}
	for _, email := range []string{"two@hanmail.net", "two@hanmail2.net", " TWO@HANMAIL.NET ", " TWO@HANMAIL2.NET "} {
		response = ownedUpload(t, router, "valid@daum.net----fixture-secret\n"+email+"----fixture-secret", "", "invalid-domain")
		require.Equal(t, http.StatusBadRequest, response.Code)
		assertOwnedResponseHasNoPrivateDetails(t, response.Body.String())
	}
	response = ownedUpload(t, router, content, "8", "forged-owner")
	require.Equal(t, http.StatusBadRequest, response.Code)
	response = ownedUpload(t, ownedTestRouter(module, 7, iamdomain.RoleUser, middleware.TurnstileGuard(nil, nil)), content, "", "missing-turnstile")
	require.Equal(t, http.StatusUnprocessableEntity, response.Code)
}

func TestOwnedDaumImportPreservesSkipAndAbortForOrdinaryInvalidRows(t *testing.T) {
	for _, strategy := range []string{"skip", "abort"} {
		t.Run(strategy, func(t *testing.T) {
			module := ownedTestModule(t)
			router := ownedTestRouter(module, 7, iamdomain.RoleUser)
			content := "ok@daum.net----fixture-secret\nnot-an-email----fixture-secret\ninvalid@example.test----fixture-secret"
			response := ownedUpload(t, router, content, "", "mixed-import", strategy)
			require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
			var accepted daumImportResponse
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &accepted))
			ids, err := module.Service.ProcessImportTask(context.Background(), accepted.ImportID, 1)
			require.NoError(t, err)
			response = ownedRequest(router, http.MethodGet, fmt.Sprintf("/v1/daum/resources/imports/%d", accepted.ImportID), "", "")
			require.Equal(t, http.StatusOK, response.Code)
			var result daumImportResponse
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
			require.True(t, result.FailureAvailable)
			var resources int64
			require.NoError(t, module.Service.DB.Model(&infra.Resource{}).Count(&resources).Error)
			if strategy == "skip" {
				require.Len(t, ids, 1)
				require.EqualValues(t, 1, resources)
				require.Equal(t, "imported", result.Status)
				require.Equal(t, 1, result.Accepted)
				require.Equal(t, 1, result.Imported)
				require.Equal(t, 2, result.Skipped)
				require.Zero(t, result.Failed)
			} else {
				require.Empty(t, ids)
				require.Zero(t, resources)
				require.Equal(t, "failed", result.Status)
				require.Zero(t, result.Accepted)
				require.Zero(t, result.Imported)
				require.Zero(t, result.Skipped)
				require.Equal(t, 1, result.Failed, "abort records the first failure and imports no rows")
			}
			for _, tail := range []string{"/items", "/failures"} {
				response = ownedRequest(router, http.MethodGet, fmt.Sprintf("/v1/daum/resources/imports/%d%s", accepted.ImportID, tail), "", "")
				require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				require.Contains(t, response.Body.String(), "invalid_format")
				assertOwnedResponseHasNoPrivateDetails(t, response.Body.String())
				require.NotContains(t, response.Body.String(), "not-an-email")
				require.NotContains(t, response.Body.String(), "invalid@example.test")
			}
		})
	}
}

func TestOwnedDaumImportLogsNeverExposeAdministratorInput(t *testing.T) {
	module := ownedTestModule(t)
	row := infra.ImportStatus{OwnerUserID: 7, OperatorUserID: 1, Status: "failed", IdempotencyKey: "admin-import", FailureObjectKey: "sensitive.csv", LastSafeError: "hidden@hanmail.net private-token"}
	require.NoError(t, module.Service.DB.Create(&row).Error)
	foreignID := uint(99)
	require.NoError(t, module.Service.DB.Create(&ownedImportItemRow{ImportID: row.ID, LineNumber: 1, ResourceID: &foreignID, Outcome: "failed", Category: "owner_conflict", LastSafeError: "other@daum.net hidden@hanmail.net private-token"}).Error)
	files := module.Files.(*ownedFiles)
	files.objects["sensitive.csv"] = []byte("hidden@hanmail.net,private-token")
	router := ownedTestRouter(module, 7, iamdomain.RoleUser)
	for _, tail := range []string{"", "/items", "/failures"} {
		response := ownedRequest(router, http.MethodGet, fmt.Sprintf("/v1/daum/resources/imports/%d%s", row.ID, tail), "", "")
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		assertOwnedResponseHasNoPrivateDetails(t, response.Body.String())
		require.NotContains(t, response.Body.String(), "resourceId")
		require.NotContains(t, response.Body.String(), "owner_conflict")
	}
	require.Zero(t, files.reads, "owned failure downloads must not read the raw administrator artifact")
}

func TestOwnedDaumCommandsRequireCurrentOwnerAndSupplierPublishing(t *testing.T) {
	module := ownedTestModule(t)
	id, _, err := module.Service.ImportLine(context.Background(), 7, domain.ImportLine{Email: "command@daum.net", AppPassword: "fixture"})
	require.NoError(t, err)
	other, _, err := module.Service.ImportLine(context.Background(), 8, domain.ImportLine{Email: "other@daum.net", AppPassword: "fixture"})
	require.NoError(t, err)
	user := ownedTestRouter(module, 7, iamdomain.RoleUser)
	response := ownedRequest(user, http.MethodPost, fmt.Sprintf("/v1/daum/resources/%d/publish", id), `{"version":1}`, "publish-denied")
	require.Equal(t, http.StatusForbidden, response.Code)
	response = ownedRequest(user, http.MethodPost, "/v1/daum/resources/bulk/publish", `{"selection":{"mode":"ids","resourceIds":[1]}}`, "bulk-denied")
	require.Equal(t, http.StatusForbidden, response.Code)
	for _, action := range []string{"validate", "publish"} {
		response = ownedRequest(ownedTestRouter(module, 7, iamdomain.RoleSupplier), http.MethodPost, fmt.Sprintf("/v1/daum/resources/%d/%s", other, action), `{"version":1}`, "foreign-"+action)
		require.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
	}
	response = ownedRequest(ownedTestRouter(module, 7, iamdomain.RoleSupplier), http.MethodPost, fmt.Sprintf("/v1/daum/resources/%d/publish", id), `{"version":1}`, "publish-owned")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assertOwnedResponseHasNoPrivateDetails(t, response.Body.String())
	var published infra.CommandResult
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &published))
	response = ownedRequest(user, http.MethodDelete, fmt.Sprintf("/v1/daum/resources/%d", id), fmt.Sprintf(`{"version":%d}`, published.Version), "delete-public")
	require.Equal(t, http.StatusConflict, response.Code)
	response = ownedRequest(user, http.MethodPost, fmt.Sprintf("/v1/daum/resources/%d/validate", id), "", "validate-owned")
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	for _, filter := range []string{`{"ownerId":8}`, `{"suffix":"hanmail.net"}`, `{"suffix":"hanmail2.net"}`} {
		response = ownedRequest(user, http.MethodPost, "/v1/daum/resources/bulk/validate", `{"selection":{"mode":"filter","filter":`+filter+`}}`, "forged-filter")
		require.Equal(t, http.StatusBadRequest, response.Code)
		assertOwnedResponseHasNoPrivateDetails(t, response.Body.String())
	}
}

func TestOwnedDaumRoutesKeepAuthenticationAndCSRF(t *testing.T) {
	router := ownedTestRouter(ownedTestModule(t), 7, iamdomain.RoleUser)
	for _, route := range router.Routes() {
		if !strings.HasPrefix(route.Path, "/v1/daum/") {
			continue
		}
		path := strings.NewReplacer(":resourceId", "1", ":importId", "1", ":taskId", "daum_bulk:1", ":command", "validate").Replace(route.Path)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(route.Method, path, nil))
		require.Equal(t, http.StatusUnauthorized, response.Code, route.Path)
		if route.Method != http.MethodGet {
			request := httptest.NewRequest(route.Method, path, nil)
			request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "session"})
			response = httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, http.StatusForbidden, response.Code, route.Path)
		}
	}
}
