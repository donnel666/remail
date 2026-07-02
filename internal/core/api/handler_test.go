package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
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

func (r *mockResourceRepo) CreateMicrosoft(_ context.Context, resource *coredomain.EmailResource, ms *coredomain.MicrosoftResource) error {
	r.seq++
	resource.ID = r.seq
	resource.CreatedAt = time.Now()
	resource.UpdatedAt = time.Now()
	ms.ID = resource.ID
	ms.CreatedAt = resource.CreatedAt
	r.resources[resource.ID] = resource
	r.microsoft[resource.ID] = ms
	return nil
}

func (r *mockResourceRepo) CreateDomain(_ context.Context, resource *coredomain.EmailResource, dr *coredomain.MailDomainResource) error {
	r.seq++
	resource.ID = r.seq
	resource.CreatedAt = time.Now()
	resource.UpdatedAt = time.Now()
	dr.ID = resource.ID
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
		if _, ok := wanted[ms.EmailAddress]; ok {
			result[ms.EmailAddress] = struct{}{}
		}
	}
	return result, nil
}

func (r *mockResourceRepo) List(_ context.Context, ownerUserID uint, resourceType string, _, _ int) ([]coredomain.EmailResource, error) {
	var result []coredomain.EmailResource
	for _, res := range r.resources {
		if res.OwnerUserID == ownerUserID && resourceMatchesType(res.Type, resourceType) {
			result = append(result, *res)
		}
	}
	return result, nil
}

func (r *mockResourceRepo) ListAll(_ context.Context, resourceType string, _, _ int) ([]coredomain.EmailResource, error) {
	var result []coredomain.EmailResource
	for _, res := range r.resources {
		if resourceMatchesType(res.Type, resourceType) {
			result = append(result, *res)
		}
	}
	return result, nil
}

func (r *mockResourceRepo) Count(_ context.Context, ownerUserID uint, resourceType string) (int64, error) {
	var count int64
	for _, res := range r.resources {
		if res.OwnerUserID == ownerUserID && resourceMatchesType(res.Type, resourceType) {
			count++
		}
	}
	return count, nil
}

func (r *mockResourceRepo) CountAll(_ context.Context, resourceType string) (int64, error) {
	var count int64
	for _, res := range r.resources {
		if resourceMatchesType(res.Type, resourceType) {
			count++
		}
	}
	return count, nil
}

func resourceMatchesType(actual coredomain.ResourceType, filter string) bool {
	return filter == "" || filter == "all" || string(actual) == filter
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
	if ms.ForSale {
		return false, nil
	}
	ms.ForSale = true
	return true, nil
}

func (r *mockResourceRepo) PublishMicrosoftBatchWithLog(_ context.Context, ownerUserID uint, resourceIDs []uint, _ governancedomain.OperationLog) (int, error) {
	published := 0
	for _, id := range resourceIDs {
		root, ok := r.resources[id]
		if !ok || root.OwnerUserID != ownerUserID || root.Type != coredomain.ResourceTypeMicrosoft {
			return 0, coredomain.ErrForbiddenResource
		}
		ms, ok := r.microsoft[id]
		if !ok {
			return 0, coredomain.ErrResourceNotFound
		}
		if !ms.ForSale {
			ms.ForSale = true
			published++
		}
	}
	return published, nil
}

func (r *mockResourceRepo) UpdateDomainWithLog(_ context.Context, _ *coredomain.MailDomainResource, _ *governancedomain.OperationLog) error {
	return nil
}

func (r *mockResourceRepo) ListMicrosoftStatus(_ context.Context, ids []uint) ([]coreapp.MicrosoftStatusResult, error) {
	var result []coreapp.MicrosoftStatusResult
	for _, id := range ids {
		if ms, ok := r.microsoft[id]; ok {
			result = append(result, coreapp.MicrosoftStatusResult{
				ID:            ms.ID,
				EmailAddress:  ms.EmailAddress,
				ForSale:       ms.ForSale,
				LongLived:     ms.LongLived,
				Status:        string(ms.Status),
				LastSafeError: ms.LastSafeError,
			})
		}
	}
	return result, nil
}

func (r *mockResourceRepo) ListDomainStatus(_ context.Context, ids []uint) ([]coreapp.DomainStatusResult, error) {
	var result []coreapp.DomainStatusResult
	for _, id := range ids {
		if dr, ok := r.domains[id]; ok {
			result = append(result, coreapp.DomainStatusResult{
				ID:      dr.ID,
				Domain:  dr.Domain,
				Purpose: string(dr.Purpose),
				Status:  string(dr.Status),
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

func (r *mockGeneratedMailboxRepo) List(_ context.Context, resourceID uint, _, _ int) ([]coredomain.GeneratedMailbox, error) {
	var result []coredomain.GeneratedMailbox
	for _, mb := range r.mailboxes {
		if mb.ResourceID == resourceID {
			result = append(result, *mb)
		}
	}
	return result, nil
}

func (r *mockGeneratedMailboxRepo) Count(_ context.Context, resourceID uint) (int64, error) {
	var count int64
	for _, mb := range r.mailboxes {
		if mb.ResourceID == resourceID {
			count++
		}
	}
	return count, nil
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

func (r *mockImportRepo) CreateMicrosoftResourcesAndMarkSucceeded(ctx context.Context, id uint, resources []coredomain.EmailResource, ms []coredomain.MicrosoftResource) error {
	item := r.imports[id]
	if item == nil {
		return coredomain.ErrResourceNotFound
	}
	if item.Status != coredomain.ResourceImportProcessing {
		return nil
	}
	if err := r.resources.createMicrosoftBatch(ctx, resources, ms); err != nil {
		return err
	}
	item.Status = coredomain.ResourceImportImported
	item.ImportedCount = len(ms)
	item.UpdatedAt = time.Now()
	return nil
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

func (s *mockFileStore) ReadPrivate(_ context.Context, objectKey string) (*governancedomain.PrivateFile, error) {
	file := s.files[objectKey]
	return &file, nil
}

type mockImportQueue struct {
	tasks []coreapp.MicrosoftImportTask
}

func (q *mockImportQueue) EnqueueMicrosoftImport(_ context.Context, task coreapp.MicrosoftImportTask) error {
	q.tasks = append(q.tasks, task)
	return nil
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
	mailServerRepo := newMockMailServerRepo()
	mailboxRepo := newMockGeneratedMailboxRepo()
	fileStore := newMockFileStore()

	mod := &CoreModule{
		ImportUseCase:   coreapp.NewImportUseCase(resourceRepo, importRepo, txtParser, fileStore, importQueue),
		ResourceUseCase: coreapp.NewResourceUseCase(resourceRepo),
		DomainUseCase:   coreapp.NewDomainUseCase(resourceRepo, mailServerRepo, mailboxRepo),
		ServerUseCase:   coreapp.NewServerUseCase(mailServerRepo),
		MailboxUseCase:  coreapp.NewDomainMailboxUseCase(mailboxRepo, resourceRepo),
	}
	return mod, resourceRepo, mailServerRepo, mailboxRepo, importQueue, importRepo, fileStore
}

type mockTXTParser struct{}

func (p *mockTXTParser) ParseMicrosoftImport(content string) ([]coredomain.MicrosoftImportLine, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, coredomain.ErrInvalidImportFormat
	}
	lines := strings.Split(content, "\n")
	var result []coredomain.MicrosoftImportLine
	for i, line := range lines {
		lineNumber := i + 1
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "----")
		if len(parts) != 2 && len(parts) != 3 && len(parts) != 4 && len(parts) != 5 {
			return nil, coredomain.ErrInvalidImportFormat
		}
		email := strings.TrimSpace(parts[0])
		password := strings.TrimSpace(parts[1])
		if email == "" || password == "" {
			return nil, coredomain.ErrInvalidImportFormat
		}
		item := coredomain.MicrosoftImportLine{
			LineNumber: lineNumber,
			Email:      email,
			Password:   password,
		}
		switch len(parts) {
		case 3:
			item.AuxiliaryAddress = strings.TrimSpace(parts[2])
			if item.AuxiliaryAddress == "" {
				return nil, coredomain.ErrInvalidImportFormat
			}
		case 4:
			item.ClientID = strings.TrimSpace(parts[2])
			item.RefreshToken = strings.TrimSpace(parts[3])
			if item.ClientID == "" || item.RefreshToken == "" {
				return nil, coredomain.ErrInvalidImportFormat
			}
		case 5:
			item.ClientID = strings.TrimSpace(parts[2])
			item.RefreshToken = strings.TrimSpace(parts[3])
			item.AuxiliaryAddress = strings.TrimSpace(parts[4])
			if item.ClientID == "" || item.RefreshToken == "" || item.AuxiliaryAddress == "" {
				return nil, coredomain.ErrInvalidImportFormat
			}
		}
		result = append(result, item)
	}
	if len(result) == 0 {
		return nil, coredomain.ErrInvalidImportFormat
	}
	return result, nil
}

func setAuthContext(c *gin.Context, userID uint, roleLevel int) {
	middleware.SetCurrentUser(c, userID, iamdomain.RoleLevel(roleLevel), "test@example.com", "test-session-id")
}

func multipartImportBody(t *testing.T, fileName string, content string) (*bytes.Buffer, string) {
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
		{"POST", "/v1/resources/imports", `{"content":"a@b----c"}`},
		{"POST", "/v1/resources/publish", `{"resourceIds":[1]}`},
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
			body:   `{"resourceIds":[1]}`,
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
		{
			name:   "create domain",
			method: "POST",
			path:   "/v1/domains",
			body:   `{"domain":"example.com","mailServerId":1,"purpose":"sale"}`,
			call:   (*CoreHandler).PostDomain,
		},
		{
			name:   "domain mailboxes",
			method: "GET",
			path:   "/v1/domains/1/mailboxes",
			params: []gin.Param{{Key: "domainId", Value: "1"}},
			call:   (*CoreHandler).GetDomainMailboxes,
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
			setAuthContext(c, 1, 10) // roleLevel=user (10), below supplier (20)

			tt.call(h, c)

			if w.Code != http.StatusForbidden {
				t.Errorf("expected 403, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestCoreHandler_ImportSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, _, _, importQueue, _, _ := setupCoreTestModuleWithImportMocks()
	h := NewCoreHandler(mod)

	body, contentType := multipartImportBody(t, "resources.txt", "user@example.com----pass123\nuser2@test.com----pass456----aux@example.net")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/resources/imports", body)
	c.Request.Header.Set("Content-Type", contentType)
	setAuthContext(c, 1, 10) // regular users can import private Microsoft resources

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
	require.NoError(t, mod.ImportUseCase.ProcessMicrosoftImport(context.Background(), importQueue.tasks[0]))
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

func TestCoreHandler_ImportInvalidFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, _, _, _, importQueue, importRepo, _ := setupCoreTestModuleWithImportMocks()
	h := NewCoreHandler(mod)

	body, contentType := multipartImportBody(t, "resources.txt", "invalid")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/resources/imports", body)
	c.Request.Header.Set("Content-Type", contentType)
	setAuthContext(c, 1, 10)

	h.PostResourceImport(c)

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	require.Len(t, importQueue.tasks, 1)
	require.NoError(t, mod.ImportUseCase.ProcessMicrosoftImport(context.Background(), importQueue.tasks[0]))
	require.Equal(t, coredomain.ResourceImportFailed, importRepo.imports[importQueue.tasks[0].ImportID].Status)
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
	setAuthContext(c, 1, 10)

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
	setAuthContext(c, 2, 10)

	h.GetResourceDetail(c)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for non-owner, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCoreHandler_ValidateStubReturns501(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, _, _, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/resources/1/validate", nil)
	c.Params = []gin.Param{{Key: "resourceId", Value: "1"}}
	setAuthContext(c, 1, 10)

	h.PostResourceValidate(c)

	if w.Code != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCoreHandler_ResourceListIncludesStatusFields(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mod, resourceRepo, mailServerRepo, _ := setupCoreTestModule()
	h := NewCoreHandler(mod)

	msRoot := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	ms := &coredomain.MicrosoftResource{
		EmailAddress:  "ms@example.com",
		Password:      "secret",
		Status:        coredomain.MicrosoftStatusNormal,
		ForSale:       true,
		LongLived:     true,
		LastSafeError: "safe diagnostic",
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
		Status:       coredomain.DomainStatusDNSNormal,
	}
	if err := resourceRepo.CreateDomain(context.Background(), domainRoot, dr); err != nil {
		t.Fatalf("create domain resource: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/v1/resources", nil)
	setAuthContext(c, 1, 20)

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
			if item.LastSafeError != "safe diagnostic" {
				t.Errorf("expected lastSafeError safe diagnostic, got %q", item.LastSafeError)
			}
		case string(coredomain.ResourceTypeDomain):
			sawDomain = true
			if item.Status != string(coredomain.DomainStatusDNSNormal) {
				t.Errorf("expected domain status dns_normal, got %q", item.Status)
			}
			if item.Domain != "example.com" {
				t.Errorf("expected domain example.com, got %q", item.Domain)
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
	setAuthContext(c, 1, 10)

	h.GetResources(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp ResourceListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, 1, len(resp.Items))
	require.Equal(t, int64(1), resp.Total)
	require.Equal(t, "owner@example.com", resp.Items[0].Email)
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
	setAuthContext(c, 1, 20)

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
	c.Request = httptest.NewRequest("POST", "/v1/resources/publish", strings.NewReader(`{"resourceIds":[1,2]}`))
	c.Request.Header.Set("Content-Type", "application/json")
	setAuthContext(c, 1, 20)

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
	setAuthContext(c, 2, 20)

	h.PostResourcePublish(c)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for non-owner, got %d: %s", w.Code, w.Body.String())
	}
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
		Status:       coredomain.DomainStatusDNSNormal,
	}
	if err := resourceRepo.CreateDomain(context.Background(), root, dr); err != nil {
		t.Fatalf("create domain resource: %v", err)
	}
	mailboxRepo.mailboxes[1] = &coredomain.GeneratedMailbox{
		ID:         1,
		ResourceID: dr.ID,
		Email:      "box@example.com",
		Status:     coredomain.GeneratedMailboxNormal,
		CreatedAt:  time.Now(),
	}

	ownerW := httptest.NewRecorder()
	ownerCtx, _ := gin.CreateTestContext(ownerW)
	ownerCtx.Request = httptest.NewRequest("GET", "/v1/domains/1/mailboxes", nil)
	ownerCtx.Params = []gin.Param{{Key: "domainId", Value: "1"}}
	setAuthContext(ownerCtx, 1, 20)

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
	setAuthContext(otherCtx, 2, 20)

	h.GetDomainMailboxes(otherCtx)

	if otherW.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for non-owner, got %d: %s", otherW.Code, otherW.Body.String())
	}
}
