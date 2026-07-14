package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type adminHandlerReadRepo struct {
	record coreapp.AdminMicrosoftRecord
}

func (r adminHandlerReadRepo) ListAdminMicrosoft(context.Context, coreapp.AdminMicrosoftListFilter, int, int, uint, time.Time) ([]coreapp.AdminMicrosoftRecord, int64, error) {
	return []coreapp.AdminMicrosoftRecord{r.record}, 1, nil
}

func (r adminHandlerReadRepo) AdminMicrosoftFacets(context.Context, coreapp.AdminMicrosoftListFilter, time.Time) (*coreapp.AdminMicrosoftFacets, error) {
	return &coreapp.AdminMicrosoftFacets{
		Status:         coreapp.AdminFacetCounts{All: 1, Normal: 1},
		ForSale:        coreapp.AdminBooleanFacets{All: 1, Yes: 1},
		LongLived:      coreapp.AdminBooleanFacets{All: 1, Yes: 1},
		GraphAvailable: coreapp.AdminBooleanFacets{All: 1, Yes: 1},
		TokenHealth:    coreapp.AdminTokenHealthFacets{All: 1, Valid: 1},
		Suffixes:       []coreapp.AdminKeyFacet{{Key: "@outlook.com", Count: 1}},
	}, nil
}

func (r adminHandlerReadRepo) FindAdminMicrosoft(_ context.Context, resourceID uint) (*coreapp.AdminMicrosoftRecord, error) {
	if resourceID != r.record.ID {
		return nil, nil
	}
	value := r.record
	value.RecentTasks = []coreapp.AdminTaskSummary{}
	return &value, nil
}

func (adminHandlerReadRepo) ListAdminMicrosoftAliases(context.Context, uint, string, int, int) ([]coreapp.AdminMicrosoftAliasItem, int64, error) {
	return []coreapp.AdminMicrosoftAliasItem{}, 0, nil
}

type adminHandlerOwnerPort struct {
	owner coreapp.AdminOwnerSummary
}

func (p adminHandlerOwnerPort) GetByIDs(context.Context, []uint) (map[uint]coreapp.AdminOwnerSummary, error) {
	return map[uint]coreapp.AdminOwnerSummary{p.owner.ID: p.owner}, nil
}
func (adminHandlerOwnerPort) SearchAdminOwners(context.Context, string, int) ([]coreapp.AdminOwnerSummary, error) {
	return nil, nil
}
func (p adminHandlerOwnerPort) ValidateTargetOwner(context.Context, uint) (*coreapp.AdminOwnerSummary, error) {
	value := p.owner
	return &value, nil
}

type adminHandlerBindingPort struct {
	binding coreapp.AdminBindingSummary
}

func (p adminHandlerBindingPort) GetByResourceIDs(context.Context, []uint) (map[uint]coreapp.AdminBindingSummary, error) {
	return map[uint]coreapp.AdminBindingSummary{p.binding.ResourceID: p.binding}, nil
}

type adminHandlerBindingAdminPort struct{}

func (adminHandlerBindingAdminPort) ReplaceAdminInput(context.Context, coreapp.AdminBindingCommand) error {
	return nil
}

type adminHandlerProxyBindingPort struct {
	items map[string][]coreapp.AdminProxyBindingSummary
}

func (p adminHandlerProxyBindingPort) GetByEmailAddresses(_ context.Context, _ []string) (map[string][]coreapp.AdminProxyBindingSummary, error) {
	return p.items, nil
}

type adminHandlerAllocationGuard struct {
	err error
}

func (p adminHandlerAllocationGuard) AssertNoActiveAllocations(context.Context, []uint) error {
	return p.err
}

type adminHandlerOperationLogs struct {
	items []*governancedomain.OperationLog
}

func (p *adminHandlerOperationLogs) Create(_ context.Context, log *governancedomain.OperationLog) error {
	p.items = append(p.items, log)
	return nil
}

type adminHandlerCommandReceipt struct {
	metadata coreapp.AdminResourceCommandReceipt
	result   []byte
}

type adminHandlerCommandRepo struct {
	roots     map[uint]domain.EmailResource
	resources map[uint]domain.MicrosoftResource
	receipts  map[string]adminHandlerCommandReceipt
}

func (r *adminHandlerCommandRepo) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (r *adminHandlerCommandRepo) ReserveAdminCommand(_ context.Context, receipt coreapp.AdminResourceCommandReceipt) ([]byte, bool, error) {
	if r.receipts == nil {
		r.receipts = make(map[string]adminHandlerCommandReceipt)
	}
	key := strconv.FormatUint(uint64(receipt.OperatorUserID), 10) + ":" + receipt.IdempotencyKey
	if stored, ok := r.receipts[key]; ok {
		if stored.metadata.Operation != receipt.Operation || stored.metadata.Subject != receipt.Subject || stored.metadata.RequestFingerprint != receipt.RequestFingerprint {
			return nil, false, domain.ErrResourceIdempotencyConflict
		}
		return append([]byte(nil), stored.result...), true, nil
	}
	r.receipts[key] = adminHandlerCommandReceipt{metadata: receipt}
	return nil, false, nil
}

func (r *adminHandlerCommandRepo) CompleteAdminCommand(_ context.Context, operatorUserID uint, idempotencyKey string, resultJSON []byte) error {
	key := strconv.FormatUint(uint64(operatorUserID), 10) + ":" + idempotencyKey
	receipt := r.receipts[key]
	receipt.result = append([]byte(nil), resultJSON...)
	r.receipts[key] = receipt
	return nil
}

func (r *adminHandlerCommandRepo) LockAdminMicrosoft(_ context.Context, resourceID uint) (*domain.EmailResource, *domain.MicrosoftResource, error) {
	root, rootOK := r.roots[resourceID]
	resource, resourceOK := r.resources[resourceID]
	if !rootOK || !resourceOK {
		return nil, nil, domain.ErrResourceNotFound
	}
	return &root, &resource, nil
}

func (r *adminHandlerCommandRepo) SaveAdminMicrosoft(_ context.Context, root *domain.EmailResource, resource *domain.MicrosoftResource, expectedVersion uint64) error {
	stored := r.roots[root.ID]
	if stored.Version != expectedVersion {
		return domain.ErrResourceVersionConflict
	}
	root.Version = expectedVersion + 1
	r.roots[root.ID] = *root
	r.resources[resource.ID] = *resource
	return nil
}

type adminHandlerValidationRepo struct {
	nextID uint
}

func (r *adminHandlerValidationRepo) CreateWithLog(_ context.Context, job *domain.ResourceValidation, _ *governancedomain.OperationLog) (bool, error) {
	if r.nextID == 0 {
		r.nextID = 100
	}
	job.ID = r.nextID
	r.nextID++
	job.ExpectedCredentialRevision = 2
	job.CreatedAt = time.Now().UTC()
	job.UpdatedAt = job.CreatedAt
	return true, nil
}

func (*adminHandlerValidationRepo) CreateBatchWithLog(context.Context, uint, coreapp.ResourceBulkSelection, *governancedomain.OperationLog, string, string) (*coreapp.ResourceBatchValidationResult, error) {
	return nil, nil
}
func (*adminHandlerValidationRepo) CreateDeferredBatchWithLog(context.Context, uint, coreapp.ResourceBulkSelection, *governancedomain.OperationLog, string, string) (*coreapp.ResourceBatchValidationResult, error) {
	return nil, nil
}
func (*adminHandlerValidationRepo) FindByID(context.Context, uint) (*domain.ResourceValidation, error) {
	return nil, nil
}
func (*adminHandlerValidationRepo) ClaimDispatchable(context.Context, int, time.Time, time.Time) ([]domain.ResourceValidation, error) {
	return nil, nil
}
func (*adminHandlerValidationRepo) ResumeValidationBatches(context.Context, int) (int, error) {
	return 0, nil
}
func (*adminHandlerValidationRepo) MarkRunning(context.Context, uint, string) (string, bool, error) {
	return "", false, nil
}
func (*adminHandlerValidationRepo) ReleaseDispatch(context.Context, uint, string) error { return nil }
func (*adminHandlerValidationRepo) MarkFailed(context.Context, uint, string, string) error {
	return nil
}
func (*adminHandlerValidationRepo) MarkRetryableFailure(context.Context, uint, string, string) (bool, error) {
	return false, nil
}
func (*adminHandlerValidationRepo) SaveMicrosoftProgress(context.Context, uint, uint, string, coreapp.MicrosoftValidationResult) error {
	return nil
}
func (*adminHandlerValidationRepo) ApplyMicrosoftResult(context.Context, uint, uint, string, coreapp.MicrosoftValidationResult, *governancedomain.SystemLog) error {
	return nil
}
func (*adminHandlerValidationRepo) ApplyDomainResult(context.Context, uint, uint, string, coreapp.DomainValidationResult, *governancedomain.SystemLog) error {
	return nil
}
func (*adminHandlerValidationRepo) MarkDispatchFailed(context.Context, uint, string, string) error {
	return nil
}

type adminHandlerValidationQueue struct{}

func (adminHandlerValidationQueue) EnqueueResourceValidation(context.Context, coreapp.ResourceValidationTask) error {
	return nil
}
func (adminHandlerValidationQueue) EnqueueResourceValidationDispatcher(context.Context, time.Duration) error {
	return nil
}

type adminHandlerPermissionChecker struct {
	allowed map[string]bool
	calls   []string
}

func (p *adminHandlerPermissionChecker) Check(_ context.Context, _ uint, _ iamdomain.Role, resource, action string) (bool, error) {
	key := resource + "/" + action
	p.calls = append(p.calls, key)
	return p.allowed[key], nil
}

type adminHandlerBulkRepo struct{}

func (adminHandlerBulkRepo) CreateWithLog(_ context.Context, command *coreapp.AdminResourceBulkCommand, _ *governancedomain.OperationLog) (bool, error) {
	command.ID = 700
	if command.Selection.Mode == coreapp.AdminResourceBulkIDs {
		command.MatchedCount = len(command.Selection.ResourceIDs)
	}
	command.CreatedAt = time.Now().UTC()
	command.UpdatedAt = command.CreatedAt
	return true, nil
}
func (adminHandlerBulkRepo) FindByID(context.Context, uint64) (*coreapp.AdminResourceBulkCommand, error) {
	return nil, nil
}
func (adminHandlerBulkRepo) ClaimDispatchable(context.Context, int, time.Time, time.Time) ([]coreapp.AdminResourceBulkCommand, error) {
	return nil, nil
}
func (adminHandlerBulkRepo) MarkRunning(context.Context, uint64, string) (*coreapp.AdminResourceBulkCommand, bool, error) {
	return nil, false, nil
}
func (adminHandlerBulkRepo) ListCandidateIDs(context.Context, *coreapp.AdminResourceBulkCommand, int, time.Time) ([]uint, error) {
	return nil, nil
}
func (adminHandlerBulkRepo) CompletePage(context.Context, uint64, string, uint, int, int, int, int, map[string]int64, bool) error {
	return nil
}
func (adminHandlerBulkRepo) MarkRetryableFailure(context.Context, uint64, string, string) (bool, error) {
	return false, nil
}
func (adminHandlerBulkRepo) MarkDispatchFailed(context.Context, uint64, string, string) error {
	return nil
}

type adminHandlerBulkQueue struct{}

func (adminHandlerBulkQueue) EnqueueAdminResourceBulk(context.Context, coreapp.AdminResourceBulkTask) error {
	return nil
}
func (adminHandlerBulkQueue) EnqueueAdminResourceBulkDispatcher(context.Context, time.Duration) error {
	return nil
}

func TestAdminMicrosoftReadHandlersMatchSafeFlatContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now().UTC()
	expireAt := now.Add(30 * 24 * time.Hour)
	repo := adminHandlerReadRepo{record: coreapp.AdminMicrosoftRecord{
		ID: 42, OwnerUserID: 7, Version: 3,
		EmailAddress: "safe-mailbox@outlook.com", EmailDomain: "outlook.com",
		Status: domain.MicrosoftStatusNormal, ForSale: true, LongLived: true, GraphAvailable: true, QualityScore: 99,
		RefreshTokenConfigured: true, PasswordConfigured: true, ClientIDConfigured: true,
		CredentialRevision: 4, CredentialUpdatedAt: now, RTExpireAt: &expireAt,
		ExplicitAliasCount: 2, DotAliasCount: 1, PlusAliasCount: 3,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}}
	query := coreapp.NewAdminResourceQuery(repo)
	query.SetPorts(
		adminHandlerOwnerPort{owner: coreapp.AdminOwnerSummary{ID: 7, Email: "owner@example.com", Nickname: "Owner", GroupName: "Supplier", Role: "supplier", Enabled: true}},
		adminHandlerBindingPort{binding: coreapp.AdminBindingSummary{ResourceID: 42, EmailAddress: "aux@example.net", Status: "verified", UpdatedAt: now}},
		nil,
		nil,
	)
	query.SetProxyBindings(adminHandlerProxyBindingPort{items: map[string][]coreapp.AdminProxyBindingSummary{
		"safe-mailbox@outlook.com": {{
			ProxyID: 9, Host: "proxy.example.net", OutboundIP: "203.0.113.9", Country: "US",
			IPVersion: "ipv4", Status: "normal", ExpireAt: expireAt,
		}},
	}})
	handler := NewCoreHandler(&CoreModule{AdminResourceQuery: query})
	router := gin.New()
	router.Use(middleware.RequestID())
	router.GET("/v1/admin/resources", handler.GetAdminMicrosoftResources)
	router.GET("/v1/admin/resources/:resourceId", handler.GetAdminMicrosoftResource)

	list := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/admin/resources?type=microsoft&limit=20&offset=0", nil)
	router.ServeHTTP(list, request)
	require.Equal(t, http.StatusOK, list.Code)
	var listPayload map[string]any
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &listPayload))
	require.NotContains(t, listPayload, "data", "the project keeps its established flat response style")
	items, ok := listPayload["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	item := items[0].(map[string]any)
	require.Equal(t, "microsoft", item["type"])
	require.EqualValues(t, 3, item["version"])
	require.Equal(t, "aux@example.net", item["bindingAddress"])
	require.Nil(t, item["activeTask"])

	detail := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/v1/admin/resources/42", nil)
	router.ServeHTTP(detail, request)
	require.Equal(t, http.StatusOK, detail.Code)
	body := detail.Body.String()
	for _, forbidden := range []string{"\"password\":", "\"clientId\":", "\"refreshToken\":", "\"accessToken\":", "\"objectKey\":", "\"claimToken\":", "\"dispatchToken\":"} {
		require.NotContains(t, body, forbidden)
	}
	var detailPayload map[string]any
	require.NoError(t, json.Unmarshal(detail.Body.Bytes(), &detailPayload))
	require.EqualValues(t, 4, detailPayload["credentials"].(map[string]any)["revision"])
	require.Equal(t, "valid", detailPayload["token"].(map[string]any)["health"])
	require.EqualValues(t, 2, detailPayload["aliasCounts"].(map[string]any)["explicit"])
	require.NotNil(t, detailPayload["recentTasks"])
	proxyBindings := detailPayload["proxyBindings"].([]any)
	require.Len(t, proxyBindings, 1)
	proxyBinding := proxyBindings[0].(map[string]any)
	require.Equal(t, "proxy.example.net", proxyBinding["host"])
	require.Equal(t, "203.0.113.9", proxyBinding["outboundIp"])
	require.NotContains(t, body, "proxy-user")
}

func TestAdminMicrosoftListRejectsWrongTypeAndOversizedLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	query := coreapp.NewAdminResourceQuery(adminHandlerReadRepo{})
	query.SetPorts(adminHandlerOwnerPort{}, adminHandlerBindingPort{}, nil, nil)
	handler := NewCoreHandler(&CoreModule{AdminResourceQuery: query})
	router := gin.New()
	router.Use(middleware.RequestID())
	router.GET("/v1/admin/resources", handler.GetAdminMicrosoftResources)

	for _, path := range []string{
		"/v1/admin/resources?type=domain",
		"/v1/admin/resources?type=microsoft&limit=101",
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, strings.NewReader("")))
		require.Contains(t, []int{http.StatusBadRequest, http.StatusUnprocessableEntity}, response.Code)
		require.Contains(t, response.Body.String(), "requestId")
	}
}

func TestAdminMicrosoftWriteHandlersRequireIdempotencyKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewCoreHandler(&CoreModule{})
	tests := []struct {
		name   string
		method string
		path   string
		call   func(*gin.Context)
	}{
		{name: "patch", method: http.MethodPatch, path: "/v1/admin/resources/42", call: handler.PatchAdminMicrosoftResource},
		{name: "credentials", method: http.MethodPut, path: "/v1/admin/resources/42/credentials", call: handler.PutAdminMicrosoftResourceCredentials},
		{name: "validate", method: http.MethodPost, path: "/v1/admin/resources/42/validate", call: handler.PostAdminMicrosoftResourceValidate},
		{name: "enable", method: http.MethodPost, path: "/v1/admin/resources/42/enable?version=1", call: handler.PostAdminMicrosoftResourceEnable},
		{name: "disable", method: http.MethodPost, path: "/v1/admin/resources/42/disable?version=1", call: handler.PostAdminMicrosoftResourceDisable},
		{name: "publish", method: http.MethodPost, path: "/v1/admin/resources/42/publish?version=1", call: handler.PostAdminMicrosoftResourcePublish},
		{name: "unpublish", method: http.MethodPost, path: "/v1/admin/resources/42/unpublish?version=1", call: handler.PostAdminMicrosoftResourceUnpublish},
		{name: "delete", method: http.MethodDelete, path: "/v1/admin/resources/42?version=1", call: handler.DeleteAdminMicrosoftResource},
		{name: "recover", method: http.MethodPost, path: "/v1/admin/resources/42/recover?version=1", call: handler.PostAdminMicrosoftResourceRecover},
		{name: "disable ids", method: http.MethodPost, path: "/v1/admin/resources/disable", call: handler.PostAdminMicrosoftResourcesDisable},
		{name: "publish bulk", method: http.MethodPost, path: "/v1/admin/resources/publish", call: handler.PostAdminMicrosoftResourcesPublish},
		{name: "unpublish bulk", method: http.MethodPost, path: "/v1/admin/resources/unpublish", call: handler.PostAdminMicrosoftResourcesUnpublish},
		{name: "delete bulk", method: http.MethodPost, path: "/v1/admin/resources/delete", call: handler.PostAdminMicrosoftResourcesDelete},
		{name: "validation bulk", method: http.MethodPost, path: "/v1/admin/resources/validations", call: handler.PostAdminMicrosoftResourceValidations},
	}
	for _, tt := range tests {
		for _, key := range []string{"", strings.Repeat("x", 129)} {
			caseName := "missing"
			if key != "" {
				caseName = "too long"
			}
			t.Run(tt.name+"/"+caseName, func(t *testing.T) {
				response := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(response)
				c.Request = httptest.NewRequest(tt.method, tt.path, strings.NewReader(`{}`))
				c.Request.Header.Set("Idempotency-Key", key)
				c.Params = gin.Params{{Key: "resourceId", Value: "42"}}
				tt.call(c)
				require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
				require.Contains(t, response.Body.String(), "Invalid Idempotency-Key")
			})
		}
	}
}

func TestAdminMicrosoftImportRejectsInvalidIdempotencyKeyBeforeMultipartParsing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewCoreHandler(&CoreModule{
		ImportUseCase: &coreapp.ImportUseCase{},
		AdminCommands: &coreapp.AdminResourceCommandService{},
	})
	for _, key := range []string{"", strings.Repeat("x", 129)} {
		response := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(response)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/admin/resources/imports", nil)
		c.Request.Header.Set("Idempotency-Key", key)
		handler.PostAdminMicrosoftResourceImport(c)
		require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), "Invalid Idempotency-Key")
	}
}

type adminHandlerImportRepo struct {
	*mockImportRepo
	idsByKey     map[string]uint
	fingerprints map[string]string
}

func newAdminHandlerImportRepo() *adminHandlerImportRepo {
	return &adminHandlerImportRepo{
		mockImportRepo: newMockImportRepo(newMockResourceRepo()),
		idsByKey:       make(map[string]uint),
		fingerprints:   make(map[string]string),
	}
}

func adminHandlerImportKey(operatorUserID uint, idempotencyKey string) string {
	return strconv.FormatUint(uint64(operatorUserID), 10) + ":" + idempotencyKey
}

func (r *adminHandlerImportRepo) FindAdminByIdempotency(_ context.Context, operatorUserID uint, idempotencyKey string) (*domain.ResourceImport, string, error) {
	key := adminHandlerImportKey(operatorUserID, idempotencyKey)
	id := r.idsByKey[key]
	if id == 0 {
		return nil, "", nil
	}
	item := *r.imports[id]
	return &item, r.fingerprints[key], nil
}

func (r *adminHandlerImportRepo) CreateAdminWithLog(_ context.Context, item *domain.ResourceImport, metadata coreapp.AdminResourceImportMetadata, _ *governancedomain.OperationLog) (*domain.ResourceImport, bool, error) {
	key := adminHandlerImportKey(metadata.OperatorUserID, metadata.IdempotencyKey)
	if id := r.idsByKey[key]; id != 0 {
		existing := *r.imports[id]
		return &existing, false, nil
	}
	r.seq++
	now := time.Now().UTC()
	stored := *item
	stored.ID = r.seq
	stored.OperatorUserID = metadata.OperatorUserID
	stored.LongLived = metadata.LongLived
	stored.ErrorStrategy = metadata.ErrorStrategy
	stored.DispatchStatus = "queued"
	stored.MaxAttempts = 3
	stored.RequestID = metadata.RequestID
	stored.CreatedAt = now
	stored.UpdatedAt = now
	r.imports[stored.ID] = &stored
	r.idsByKey[key] = stored.ID
	r.fingerprints[key] = metadata.RequestFingerprint
	result := stored
	return &result, true, nil
}

func (*adminHandlerImportRepo) ClaimAdminImportDispatchable(context.Context, int, time.Time, time.Time) ([]coreapp.AdminResourceImportDispatchItem, error) {
	return nil, nil
}

func (*adminHandlerImportRepo) MarkAdminImportRunning(context.Context, uint, string) (string, bool, error) {
	return "", false, nil
}

func (*adminHandlerImportRepo) MarkAdminImportDispatchFailed(context.Context, uint, string, string) error {
	return nil
}

func (*adminHandlerImportRepo) MarkAdminImportRetryableFailure(context.Context, uint, string, string) (bool, error) {
	return false, nil
}

func (*adminHandlerImportRepo) MarkAdminImportFailed(context.Context, uint, string, string, string) error {
	return nil
}

func TestAdminMicrosoftImportAcceptedResponseKeepsDurableRequestAndAccurateReuse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	imports := newAdminHandlerImportRepo()
	importUseCase := coreapp.NewImportUseCase(nil, imports, nil, newMockFileStore(), &mockImportQueue{})
	commandRepo := &adminHandlerCommandRepo{}
	commands := newAdminHandlerCommandService(commandRepo, &adminHandlerOperationLogs{}, adminHandlerAllocationGuard{})
	handler := NewCoreHandler(&CoreModule{ImportUseCase: importUseCase, AdminCommands: commands})

	post := func(requestID string) map[string]any {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		file, err := writer.CreateFormFile("file", "microsoft-resources.txt")
		require.NoError(t, err)
		_, err = file.Write([]byte("safe@outlook.com----password"))
		require.NoError(t, err)
		require.NoError(t, writer.WriteField("ownerId", "7"))
		require.NoError(t, writer.WriteField("longLived", "true"))
		require.NoError(t, writer.WriteField("errorStrategy", "skip"))
		require.NoError(t, writer.Close())

		response := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(response)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/admin/resources/imports", &body)
		c.Request.Header.Set("Content-Type", writer.FormDataContentType())
		c.Request.Header.Set("Idempotency-Key", "durable-import-key")
		c.Set("request_id", requestID)
		middleware.SetCurrentUser(c, 9, iamdomain.RoleAdmin, "admin@test.local", "session")
		handler.PostAdminMicrosoftResourceImport(c)
		require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
		var payload map[string]any
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
		return payload
	}

	first := post("durable-import-request")
	require.Equal(t, "import:1", first["taskId"])
	require.Equal(t, "durable-import-request", first["requestId"])
	require.Equal(t, "processing", first["status"])
	require.Equal(t, float64(0), first["accepted"])
	require.Equal(t, false, first["reused"])
	require.NotNil(t, first["task"])

	replayed := post("replay-http-request")
	require.Equal(t, "import:1", replayed["taskId"])
	require.Equal(t, "durable-import-request", replayed["requestId"])
	require.Equal(t, true, replayed["reused"])

	statusResponse := httptest.NewRecorder()
	statusContext, _ := gin.CreateTestContext(statusResponse)
	statusContext.Request = httptest.NewRequest(http.MethodGet, "/v1/admin/resources/imports/1", nil)
	statusContext.Params = gin.Params{{Key: "importId", Value: "1"}}
	handler.GetAdminMicrosoftResourceImport(statusContext)
	require.Equal(t, http.StatusOK, statusResponse.Code, statusResponse.Body.String())
	var status map[string]any
	require.NoError(t, json.Unmarshal(statusResponse.Body.Bytes(), &status))
	require.Equal(t, "durable-import-request", status["requestId"])
	require.Equal(t, false, status["reused"])
}

func TestAdminMicrosoftJSONCommandsEnforceBodyAndSelectionBounds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewCoreHandler(&CoreModule{})
	perform := func(method, path, body string, params gin.Params, call func(*gin.Context)) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(response)
		c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Request.Header.Set("Idempotency-Key", "input-boundary-test")
		c.Params = params
		middleware.SetCurrentUser(c, 9, iamdomain.RoleAdmin, "admin@test.local", "session")
		call(c)
		return response
	}

	t.Run("json body", func(t *testing.T) {
		body := `{"version":1,"emailAddress":"` + strings.Repeat("secret-canary-", int(maxAdminResourceJSONBytes)/len("secret-canary-")+2) + `"}`
		response := perform(
			http.MethodPatch,
			"/v1/admin/resources/42",
			body,
			gin.Params{{Key: "resourceId", Value: "42"}},
			handler.PatchAdminMicrosoftResource,
		)
		require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		require.NotContains(t, response.Body.String(), "secret-canary")
	})

	t.Run("positive resource ids", func(t *testing.T) {
		response := perform(
			http.MethodPost,
			"/v1/admin/resources/disable",
			`{"selection":{"mode":"ids","resourceIds":[42,0]}}`,
			nil,
			handler.PostAdminMicrosoftResourcesDisable,
		)
		require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	})

	t.Run("unknown state field", func(t *testing.T) {
		response := perform(
			http.MethodPatch,
			"/v1/admin/resources/42",
			`{"version":1,"emailAddress":"safe@example.com","status":"normal"}`,
			gin.Params{{Key: "resourceId", Value: "42"}},
			handler.PatchAdminMicrosoftResource,
		)
		require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	})

	t.Run("trailing json", func(t *testing.T) {
		response := perform(
			http.MethodPost,
			"/v1/admin/resources/disable",
			`{"selection":{"mode":"ids","resourceIds":[42]}} {}`,
			nil,
			handler.PostAdminMicrosoftResourcesDisable,
		)
		require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	})

	t.Run("resource id count", func(t *testing.T) {
		response := perform(
			http.MethodPost,
			"/v1/admin/resources/publish",
			`{"selection":{"mode":"ids","resourceIds":[`+strings.Repeat("1,", 1000)+`1]}}`,
			nil,
			handler.PostAdminMicrosoftResourcesPublish,
		)
		require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	})

	for _, test := range []struct {
		name string
		body string
	}{
		{name: "ids rejects filter", body: `{"selection":{"mode":"ids","resourceIds":[42],"filter":{"type":"microsoft"}}}`},
		{name: "filter rejects ids", body: `{"selection":{"mode":"filter","resourceIds":[42],"filter":{"type":"microsoft"}}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := perform(
				http.MethodPost,
				"/v1/admin/resources/publish",
				test.body,
				nil,
				handler.PostAdminMicrosoftResourcesPublish,
			)
			require.Equal(t, http.StatusUnprocessableEntity, response.Code, response.Body.String())
		})
	}
}

func TestAdminMicrosoftPatchRequiresFieldLevelPermissions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		body       string
		permission string
	}{
		{name: "for sale", body: `{"version":1,"forSale":true}`, permission: "core:resource/operate"},
		{name: "credentials", body: `{"version":1,"credentials":{"password":"secret","clientId":"client","refreshToken":"refresh"}}`, permission: "core:resource/operate"},
		{name: "binding", body: `{"version":1,"bindingAddress":"auxiliary@example.net"}`, permission: "mailtransport:binding/write"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := &adminHandlerPermissionChecker{allowed: map[string]bool{}}
			handler := NewCoreHandler(&CoreModule{}, checker)
			response := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(response)
			c.Request = httptest.NewRequest(http.MethodPatch, "/v1/admin/resources/42", strings.NewReader(tt.body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request.Header.Set("Idempotency-Key", "permission-test-key")
			c.Params = gin.Params{{Key: "resourceId", Value: "42"}}
			middleware.SetCurrentUser(c, 9, iamdomain.RoleAdmin, "admin@test.local", "session")

			handler.PatchAdminMicrosoftResource(c)

			require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
			require.Equal(t, []string{tt.permission}, checker.calls)
		})
	}
}

func TestAdminMicrosoftStateHandlersMapVersionAndAllocationConflicts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &adminHandlerCommandRepo{
		roots: map[uint]domain.EmailResource{42: {ID: 42, Type: domain.ResourceTypeMicrosoft, OwnerUserID: 7, Version: 2}},
		resources: map[uint]domain.MicrosoftResource{42: {
			ID: 42, EmailAddress: "state-handler@outlook.com", Password: "password", Status: domain.MicrosoftStatusNormal,
		}},
	}
	logs := &adminHandlerOperationLogs{}
	guard := adminHandlerAllocationGuard{}
	service := newAdminHandlerCommandService(repo, logs, guard)
	handler := NewCoreHandler(&CoreModule{AdminCommands: service})

	versionResponse := httptest.NewRecorder()
	versionContext, _ := gin.CreateTestContext(versionResponse)
	versionContext.Request = httptest.NewRequest(http.MethodPost, "/v1/admin/resources/42/disable?version=1", nil)
	versionContext.Request.Header.Set("Idempotency-Key", "version-conflict-key")
	versionContext.Params = gin.Params{{Key: "resourceId", Value: "42"}}
	middleware.SetCurrentUser(versionContext, 9, iamdomain.RoleAdmin, "admin@test.local", "session")
	handler.PostAdminMicrosoftResourceDisable(versionContext)
	require.Equal(t, http.StatusConflict, versionResponse.Code, versionResponse.Body.String())
	require.Contains(t, versionResponse.Body.String(), "Resource changed")

	guard.err = domain.ErrResourceHasAllocation
	service.SetPorts(
		adminHandlerOwnerPort{owner: coreapp.AdminOwnerSummary{ID: 7, Role: "supplier", Enabled: true}},
		adminHandlerBindingPort{},
		adminHandlerBindingAdminPort{},
		guard,
	)
	allocationResponse := httptest.NewRecorder()
	allocationContext, _ := gin.CreateTestContext(allocationResponse)
	allocationContext.Request = httptest.NewRequest(http.MethodDelete, "/v1/admin/resources/42?version=2", nil)
	allocationContext.Request.Header.Set("Idempotency-Key", "allocation-conflict-key")
	allocationContext.Params = gin.Params{{Key: "resourceId", Value: "42"}}
	middleware.SetCurrentUser(allocationContext, 9, iamdomain.RoleAdmin, "admin@test.local", "session")
	handler.DeleteAdminMicrosoftResource(allocationContext)
	require.Equal(t, http.StatusConflict, allocationResponse.Code, allocationResponse.Body.String())
	require.Contains(t, allocationResponse.Body.String(), "active allocation")
}

func TestAdminMicrosoftCredentialMutationResponseAndReceiptAreSafe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now().UTC()
	repo := &adminHandlerCommandRepo{
		roots: map[uint]domain.EmailResource{42: {ID: 42, Type: domain.ResourceTypeMicrosoft, OwnerUserID: 7, Version: 1}},
		resources: map[uint]domain.MicrosoftResource{42: {
			ID: 42, EmailAddress: "safe-mutation@outlook.com", Password: "before", Status: domain.MicrosoftStatusNormal,
			CredentialRevision: 1, CredentialUpdatedAt: now,
		}},
	}
	logs := &adminHandlerOperationLogs{}
	service := newAdminHandlerCommandService(repo, logs, adminHandlerAllocationGuard{})
	readRepo := adminHandlerReadRepo{record: coreapp.AdminMicrosoftRecord{
		ID: 42, OwnerUserID: 7, Version: 2, EmailAddress: "safe-mutation@outlook.com", EmailDomain: "outlook.com",
		Status: domain.MicrosoftStatusPending, PasswordConfigured: true, ClientIDConfigured: true, RefreshTokenConfigured: true,
		CredentialRevision: 2, CredentialUpdatedAt: now, CreatedAt: now, UpdatedAt: now,
	}}
	query := coreapp.NewAdminResourceQuery(readRepo)
	query.SetPorts(
		adminHandlerOwnerPort{owner: coreapp.AdminOwnerSummary{ID: 7, Email: "owner@test.local", Role: "supplier", Enabled: true}},
		adminHandlerBindingPort{binding: coreapp.AdminBindingSummary{ResourceID: 42}},
		nil,
		nil,
	)
	checker := &adminHandlerPermissionChecker{allowed: map[string]bool{"core:resource/operate": true}}
	handler := NewCoreHandler(&CoreModule{AdminCommands: service, AdminResourceQuery: query}, checker)
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v1/admin/resources/42", strings.NewReader(`{
        "version": 1,
        "credentials": {
            "password": "handler-secret-password",
            "clientId": "handler-secret-client",
            "refreshToken": "handler-secret-refresh"
        }
    }`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Idempotency-Key", "safe-mutation-key")
	c.Params = gin.Params{{Key: "resourceId", Value: "42"}}
	middleware.SetCurrentUser(c, 9, iamdomain.RoleAdmin, "admin@test.local", "session")

	handler.PatchAdminMicrosoftResource(c)

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	body := response.Body.String()
	for _, forbidden := range []string{
		"handler-secret-password", "handler-secret-client", "handler-secret-refresh",
		`"password":`, `"clientId":`, `"refreshToken":`, `"accessToken":`, `"claimToken":`, `"dispatchToken":`,
	} {
		require.NotContains(t, body, forbidden)
	}
	require.Contains(t, body, `"passwordConfigured":true`)
	require.Contains(t, body, `"validationTask"`)
	require.Len(t, logs.items, 1)
	for _, secret := range []string{"handler-secret-password", "handler-secret-client", "handler-secret-refresh"} {
		require.NotContains(t, logs.items[0].SafeSummary, secret)
	}
	var receiptResult string
	for _, receipt := range repo.receipts {
		receiptResult = string(receipt.result)
	}
	for _, secret := range []string{"handler-secret-password", "handler-secret-client", "handler-secret-refresh"} {
		require.NotContains(t, receiptResult, secret)
	}
}

func TestAdminMicrosoftValidateAcceptedResponsePublishesFlatDurableMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now().UTC()
	repo := &adminHandlerCommandRepo{
		roots: map[uint]domain.EmailResource{42: {ID: 42, Type: domain.ResourceTypeMicrosoft, OwnerUserID: 7, Version: 1}},
		resources: map[uint]domain.MicrosoftResource{42: {
			ID: 42, EmailAddress: "validate-handler@outlook.com", Password: "password", Status: domain.MicrosoftStatusNormal,
			CreatedAt: now, UpdatedAt: now,
		}},
	}
	service := newAdminHandlerCommandService(repo, &adminHandlerOperationLogs{}, adminHandlerAllocationGuard{})
	handler := NewCoreHandler(&CoreModule{AdminCommands: service})
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/admin/resources/42/validate", nil)
	c.Request.Header.Set("Idempotency-Key", "validate-accepted-key")
	c.Params = gin.Params{{Key: "resourceId", Value: "42"}}
	c.Set("request_id", "durable-validation-request")
	middleware.SetCurrentUser(c, 9, iamdomain.RoleAdmin, "admin@test.local", "session")

	handler.PostAdminMicrosoftResourceValidate(c)

	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	var payload map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	require.Equal(t, "validation:100", payload["taskId"])
	require.Equal(t, "durable-validation-request", payload["requestId"])
	require.Equal(t, "queued", payload["status"])
	require.Equal(t, float64(1), payload["accepted"])
	require.Equal(t, false, payload["reused"])
	task, ok := payload["task"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, payload["taskId"], task["taskId"])
	require.Equal(t, payload["status"], task["status"])
}

func TestAdminMicrosoftBulkHandlersKeepSyncIDsAndAsyncFilterContracts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now().UTC()
	repo := &adminHandlerCommandRepo{
		roots: map[uint]domain.EmailResource{42: {ID: 42, Type: domain.ResourceTypeMicrosoft, OwnerUserID: 7, Version: 1}},
		resources: map[uint]domain.MicrosoftResource{42: {
			ID: 42, EmailAddress: "bulk-handler@outlook.com", Password: "password", Status: domain.MicrosoftStatusNormal,
			CreatedAt: now, UpdatedAt: now,
		}},
	}
	logs := &adminHandlerOperationLogs{}
	service := newAdminHandlerCommandService(repo, logs, adminHandlerAllocationGuard{})
	bulk := coreapp.NewAdminResourceBulkService(adminHandlerBulkRepo{}, adminHandlerBulkQueue{}, service)
	handler := NewCoreHandler(&CoreModule{AdminCommands: service, AdminBulk: bulk})

	idsResponse := httptest.NewRecorder()
	idsContext, _ := gin.CreateTestContext(idsResponse)
	idsContext.Request = httptest.NewRequest(http.MethodPost, "/v1/admin/resources/publish", strings.NewReader(`{
        "selection": {"mode":"ids","resourceIds":[42]}
    }`))
	idsContext.Request.Header.Set("Content-Type", "application/json")
	idsContext.Request.Header.Set("Idempotency-Key", "sync-ids-key")
	middleware.SetCurrentUser(idsContext, 9, iamdomain.RoleAdmin, "admin@test.local", "session")
	handler.PostAdminMicrosoftResourcesPublish(idsContext)
	require.Equal(t, http.StatusOK, idsResponse.Code, idsResponse.Body.String())
	require.Contains(t, idsResponse.Body.String(), `"affected":1`)

	filterResponse := httptest.NewRecorder()
	filterContext, _ := gin.CreateTestContext(filterResponse)
	filterContext.Request = httptest.NewRequest(http.MethodPost, "/v1/admin/resources/publish", strings.NewReader(`{
        "selection": {"mode":"filter","filter":{"type":"microsoft","status":"normal"}}
    }`))
	filterContext.Request.Header.Set("Content-Type", "application/json")
	filterContext.Request.Header.Set("Idempotency-Key", "async-filter-key")
	filterContext.Set("request_id", "durable-bulk-request")
	middleware.SetCurrentUser(filterContext, 9, iamdomain.RoleAdmin, "admin@test.local", "session")
	handler.PostAdminMicrosoftResourcesPublish(filterContext)
	require.Equal(t, http.StatusAccepted, filterResponse.Code, filterResponse.Body.String())
	var filterPayload map[string]any
	require.NoError(t, json.Unmarshal(filterResponse.Body.Bytes(), &filterPayload))
	require.Equal(t, "bulk:700", filterPayload["taskId"])
	require.Equal(t, "durable-bulk-request", filterPayload["requestId"])
	require.Equal(t, "queued", filterPayload["status"])
	require.Equal(t, float64(0), filterPayload["accepted"], "filter matches are unknown until durable expansion")
	require.Equal(t, false, filterPayload["reused"])
	require.NotNil(t, filterPayload["task"])
}

func TestAdminMicrosoftAdminRoutesEnforceCSRFAheadOfCasbin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	checker := &adminHandlerPermissionChecker{allowed: map[string]bool{}}
	fetcher := middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
		return 9, iamdomain.RoleAdmin, "admin@test.local", true
	})
	router := gin.New()
	router.Use(middleware.RequestID())
	RegisterCoreRoutes(router.Group("/v1"), &CoreModule{}, fetcher, checker)

	withoutCSRF := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/admin/resources/42/disable?version=1", nil)
	request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "session"})
	request.Header.Set("Idempotency-Key", "route-csrf-key")
	router.ServeHTTP(withoutCSRF, request)
	require.Equal(t, http.StatusForbidden, withoutCSRF.Code, withoutCSRF.Body.String())
	require.Empty(t, checker.calls, "CSRF must reject the write before the permission checker or handler runs")

	withCSRF := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/v1/admin/resources/42/disable?version=1", nil)
	request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "session"})
	request.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf-value"})
	request.Header.Set(middleware.CSRFHeaderName, "csrf-value")
	request.Header.Set("Idempotency-Key", "route-casbin-key")
	router.ServeHTTP(withCSRF, request)
	require.Equal(t, http.StatusForbidden, withCSRF.Code, withCSRF.Body.String())
	require.Equal(t, []string{"core:resource/operate"}, checker.calls)
}

func TestAdminMicrosoftCoreRouteSecurityMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		method     string
		path       string
		resource   string
		action     string
		csrfNeeded bool
	}{
		{name: "Q01 list", method: http.MethodGet, path: "/v1/admin/resources?type=microsoft", resource: "core:resource", action: "read"},
		{name: "C01 import", method: http.MethodPost, path: "/v1/admin/resources/imports", resource: "core:resource", action: "write", csrfNeeded: true},
		{name: "Q04 import status", method: http.MethodGet, path: "/v1/admin/resources/imports/1", resource: "core:resource", action: "read"},
		{name: "C14 validate matching", method: http.MethodPost, path: "/v1/admin/resources/validations", resource: "core:resource", action: "operate", csrfNeeded: true},
		{name: "C15 disable ids", method: http.MethodPost, path: "/v1/admin/resources/disable", resource: "core:resource", action: "operate", csrfNeeded: true},
		{name: "C16 publish batch", method: http.MethodPost, path: "/v1/admin/resources/publish", resource: "core:resource", action: "operate", csrfNeeded: true},
		{name: "C17 unpublish batch", method: http.MethodPost, path: "/v1/admin/resources/unpublish", resource: "core:resource", action: "operate", csrfNeeded: true},
		{name: "C18 delete batch", method: http.MethodPost, path: "/v1/admin/resources/delete", resource: "core:resource", action: "operate", csrfNeeded: true},
		{name: "Q02 detail", method: http.MethodGet, path: "/v1/admin/resources/42", resource: "core:resource", action: "read"},
		{name: "Q06 aliases", method: http.MethodGet, path: "/v1/admin/resources/42/aliases?kind=explicit", resource: "core:resource", action: "read"},
		{name: "C02 edit", method: http.MethodPatch, path: "/v1/admin/resources/42", resource: "core:resource", action: "write", csrfNeeded: true},
		{name: "C03 credentials", method: http.MethodPut, path: "/v1/admin/resources/42/credentials", resource: "core:resource", action: "operate", csrfNeeded: true},
		{name: "C04 validate", method: http.MethodPost, path: "/v1/admin/resources/42/validate", resource: "core:resource", action: "operate", csrfNeeded: true},
		{name: "C05 enable", method: http.MethodPost, path: "/v1/admin/resources/42/enable?version=1", resource: "core:resource", action: "operate", csrfNeeded: true},
		{name: "C06 disable", method: http.MethodPost, path: "/v1/admin/resources/42/disable?version=1", resource: "core:resource", action: "operate", csrfNeeded: true},
		{name: "C07 publish", method: http.MethodPost, path: "/v1/admin/resources/42/publish?version=1", resource: "core:resource", action: "operate", csrfNeeded: true},
		{name: "C08 unpublish", method: http.MethodPost, path: "/v1/admin/resources/42/unpublish?version=1", resource: "core:resource", action: "operate", csrfNeeded: true},
		{name: "C09 delete", method: http.MethodDelete, path: "/v1/admin/resources/42?version=1", resource: "core:resource", action: "operate", csrfNeeded: true},
		{name: "C10 recover", method: http.MethodPost, path: "/v1/admin/resources/42/recover?version=1", resource: "core:resource", action: "operate", csrfNeeded: true},
	}

	newRouter := func(checker *adminHandlerPermissionChecker) *gin.Engine {
		router := gin.New()
		router.Use(middleware.RequestID())
		RegisterCoreRoutes(
			router.Group("/v1"),
			&CoreModule{},
			middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
				return 9, iamdomain.RoleAdmin, "admin@test.local", true
			}),
			checker,
		)
		return router
	}
	addSession := func(request *http.Request) {
		request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "valid-session"})
	}
	addCSRF := func(request *http.Request) {
		request.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf-token"})
		request.Header.Set(middleware.CSRFHeaderName, "csrf-token")
	}

	for _, test := range tests {
		t.Run(test.name+"/session", func(t *testing.T) {
			checker := &adminHandlerPermissionChecker{allowed: map[string]bool{test.resource + "/" + test.action: true}}
			request := httptest.NewRequest(test.method, test.path, nil)
			if test.csrfNeeded {
				addCSRF(request)
			}
			response := httptest.NewRecorder()
			newRouter(checker).ServeHTTP(response, request)
			require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
			require.Empty(t, checker.calls)
			require.Contains(t, response.Body.String(), "requestId")
		})

		if test.csrfNeeded {
			t.Run(test.name+"/csrf", func(t *testing.T) {
				checker := &adminHandlerPermissionChecker{allowed: map[string]bool{test.resource + "/" + test.action: true}}
				request := httptest.NewRequest(test.method, test.path, nil)
				addSession(request)
				response := httptest.NewRecorder()
				newRouter(checker).ServeHTTP(response, request)
				require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
				require.Empty(t, checker.calls, "CSRF must reject before Casbin")
				require.Contains(t, response.Body.String(), "requestId")
			})
		}

		t.Run(test.name+"/casbin", func(t *testing.T) {
			checker := &adminHandlerPermissionChecker{allowed: map[string]bool{}}
			request := httptest.NewRequest(test.method, test.path, nil)
			addSession(request)
			if test.csrfNeeded {
				addCSRF(request)
			}
			response := httptest.NewRecorder()
			newRouter(checker).ServeHTTP(response, request)
			require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
			require.Equal(t, []string{test.resource + "/" + test.action}, checker.calls)
			require.Contains(t, response.Body.String(), "requestId")
		})
	}
}

func newAdminHandlerCommandService(
	repo *adminHandlerCommandRepo,
	logs *adminHandlerOperationLogs,
	guard adminHandlerAllocationGuard,
) *coreapp.AdminResourceCommandService {
	validationRepo := &adminHandlerValidationRepo{}
	validation := coreapp.NewResourceValidationUseCase(nil, validationRepo, adminHandlerValidationQueue{}, nil)
	service := coreapp.NewAdminResourceCommandService(repo, validation, logs)
	service.SetPorts(
		adminHandlerOwnerPort{owner: coreapp.AdminOwnerSummary{ID: 7, Email: "owner@test.local", Role: "supplier", Enabled: true}},
		adminHandlerBindingPort{},
		adminHandlerBindingAdminPort{},
		guard,
	)
	return service
}
