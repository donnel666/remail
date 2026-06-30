package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/donnel666/remail/api/middleware"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/iam/app"
	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/iam/infra"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- In-memory mock stores ---

type mockUserRepo struct {
	mu            sync.Mutex
	users         map[uint]*domain.User
	byID          map[string]uint
	invites       map[string]*domain.Invite
	policies      map[uint][]domain.PermissionPolicy
	operationLogs *mockOperationLogPort
	reloads       int
	reloadErr     error
	seq           uint
}

func newMockUserRepo() *mockUserRepo {
	return &mockUserRepo{
		users:         make(map[uint]*domain.User),
		byID:          make(map[string]uint),
		invites:       make(map[string]*domain.Invite),
		policies:      make(map[uint][]domain.PermissionPolicy),
		operationLogs: &mockOperationLogPort{},
	}
}

type mockOperationLogPort struct {
	mu   sync.Mutex
	logs []*governancedomain.OperationLog
}

func (p *mockOperationLogPort) Create(_ context.Context, log *governancedomain.OperationLog) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := *log
	p.logs = append(p.logs, &cp)
	return nil
}

func (r *mockUserRepo) Create(_ context.Context, user *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existingID, ok := r.byID[user.Email]; ok {
		if _, exists := r.users[existingID]; exists {
			return domain.ErrEmailAlreadyExists
		}
	}
	r.seq++
	user.ID = r.seq
	r.users[user.ID] = user
	r.byID[user.Email] = user.ID
	return nil
}

func (r *mockUserRepo) CreateWithInvite(ctx context.Context, user *domain.User, _ string) error {
	return r.Create(ctx, user)
}

func (r *mockUserRepo) FindByEmail(_ context.Context, email string) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id, ok := r.byID[email]; ok {
		if u, exists := r.users[id]; exists {
			cp := *u
			return &cp, nil
		}
	}
	return nil, nil
}

func (r *mockUserRepo) FindByID(_ context.Context, id uint) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if u, ok := r.users[id]; ok {
		cp := *u
		return &cp, nil
	}
	return nil, nil
}

func (r *mockUserRepo) Update(_ context.Context, user *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.users[user.ID]; ok {
		cp := *user
		r.users[user.ID] = &cp
	}
	return nil
}

func (r *mockUserRepo) UpdateWithOperationLog(ctx context.Context, user *domain.User, log *governancedomain.OperationLog) error {
	if err := r.Update(ctx, user); err != nil {
		return err
	}
	return r.operationLogs.Create(ctx, log)
}

func (r *mockUserRepo) CreateFirstUser(ctx context.Context, user *domain.User) error {
	count, _ := r.Count(ctx)
	if count > 0 {
		return domain.ErrActivationAlreadyDone
	}
	return r.Create(ctx, user)
}

func (r *mockUserRepo) List(_ context.Context, offset, limit int) ([]domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []domain.User
	for _, u := range r.users {
		result = append(result, *u)
	}
	if offset >= len(result) {
		return nil, nil
	}
	end := offset + limit
	if end > len(result) {
		end = len(result)
	}
	return result[offset:end], nil
}

func (r *mockUserRepo) Count(_ context.Context) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return int64(len(r.users)), nil
}

func (r *mockUserRepo) FindByIDs(_ context.Context, ids []uint) ([]domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []domain.User
	for _, id := range ids {
		if u, ok := r.users[id]; ok {
			result = append(result, *u)
		}
	}
	return result, nil
}

func (r *mockUserRepo) ListInvites(_ context.Context, offset, limit int) ([]domain.Invite, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []domain.Invite
	for _, invite := range r.invites {
		result = append(result, *invite)
	}
	if offset >= len(result) {
		return nil, nil
	}
	end := offset + limit
	if end > len(result) {
		end = len(result)
	}
	return result[offset:end], nil
}

func (r *mockUserRepo) CountInvites(_ context.Context) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return int64(len(r.invites)), nil
}

func (r *mockUserRepo) CreateInviteWithOperationLog(ctx context.Context, invite *domain.Invite, _ uint, log *governancedomain.OperationLog) error {
	r.mu.Lock()
	if _, exists := r.invites[invite.Code]; exists {
		r.mu.Unlock()
		return domain.ErrInviteAlreadyExists
	}
	cp := *invite
	r.invites[invite.Code] = &cp
	r.mu.Unlock()
	return r.operationLogs.Create(ctx, log)
}

func (r *mockUserRepo) UpdateInviteWithOperationLog(ctx context.Context, invite *domain.Invite, log *governancedomain.OperationLog) error {
	r.mu.Lock()
	cp := *invite
	r.invites[invite.Code] = &cp
	r.mu.Unlock()
	return r.operationLogs.Create(ctx, log)
}

func (r *mockUserRepo) FindInviteByCode(_ context.Context, code string) (*domain.Invite, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	invite, ok := r.invites[code]
	if !ok {
		return nil, nil
	}
	cp := *invite
	return &cp, nil
}

func (r *mockUserRepo) ListUserPermissionPolicies(_ context.Context, userID uint) ([]domain.PermissionPolicy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	policies := append([]domain.PermissionPolicy(nil), r.policies[userID]...)
	return policies, nil
}

func (r *mockUserRepo) ReplaceUserPermissionPolicies(_ context.Context, userID uint, policies []domain.PermissionPolicy) error {
	r.mu.Lock()
	r.policies[userID] = append([]domain.PermissionPolicy(nil), policies...)
	r.mu.Unlock()
	return nil
}

func (r *mockUserRepo) Reload(_ context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reloads++
	return r.reloadErr
}

func (r *mockUserRepo) lastLog() *governancedomain.OperationLog {
	r.operationLogs.mu.Lock()
	defer r.operationLogs.mu.Unlock()
	if len(r.operationLogs.logs) == 0 {
		return nil
	}
	cp := *r.operationLogs.logs[len(r.operationLogs.logs)-1]
	return &cp
}

func (r *mockUserRepo) logCount() int {
	r.operationLogs.mu.Lock()
	defer r.operationLogs.mu.Unlock()
	return len(r.operationLogs.logs)
}

func (r *mockUserRepo) reloadCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reloads
}

type mockSessionStore struct {
	mu                sync.Mutex
	sessions          map[string]*domain.Session
	byUser            map[uint][]string
	deleteByUserIDErr error
}

func newMockSessionStore() *mockSessionStore {
	return &mockSessionStore{
		sessions: make(map[string]*domain.Session),
		byUser:   make(map[uint][]string),
	}
}

func (s *mockSessionStore) Create(_ context.Context, session *domain.Session, _ int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *session
	s.sessions[session.ID] = &cp
	s.byUser[session.UserID] = append(s.byUser[session.UserID], session.ID)
	return nil
}

func (s *mockSessionStore) Get(_ context.Context, sessionID string) (*domain.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[sessionID]; ok {
		cp := *sess
		return &cp, nil
	}
	return nil, nil
}

func (s *mockSessionStore) Delete(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[sessionID]; ok {
		delete(s.sessions, sessionID)
		if sessions, ok := s.byUser[sess.UserID]; ok {
			filtered := make([]string, 0, len(sessions)-1)
			for _, sid := range sessions {
				if sid != sessionID {
					filtered = append(filtered, sid)
				}
			}
			s.byUser[sess.UserID] = filtered
		}
	}
	return nil
}

func (s *mockSessionStore) DeleteByUserID(_ context.Context, userID uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteByUserIDErr != nil {
		return s.deleteByUserIDErr
	}
	if sessions, ok := s.byUser[userID]; ok {
		for _, sid := range sessions {
			delete(s.sessions, sid)
		}
		delete(s.byUser, userID)
	}
	return nil
}

type mockCaptchaStore struct {
	mu       sync.Mutex
	captchas map[string]string
}

func newMockCaptchaStore() *mockCaptchaStore {
	return &mockCaptchaStore{
		captchas: make(map[string]string),
	}
}

func (c *mockCaptchaStore) Create(_ context.Context, captchaID, answer string, _ int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.captchas[captchaID] = answer
	return nil
}

func (c *mockCaptchaStore) Get(_ context.Context, captchaID string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if answer, ok := c.captchas[captchaID]; ok {
		return answer, nil
	}
	return "", nil
}

func (c *mockCaptchaStore) Delete(_ context.Context, captchaID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.captchas, captchaID)
	return nil
}

type allowPermissionChecker struct{}

func (c allowPermissionChecker) Check(_ context.Context, _ uint, _ domain.RoleLevel, _, _ string) (bool, error) {
	return true, nil
}

func (c allowPermissionChecker) Reload(_ context.Context) error {
	return nil
}

type mockEmailCodeStore struct {
	mu        sync.Mutex
	codes     map[string]string
	deleteErr error
}

func newMockEmailCodeStore() *mockEmailCodeStore {
	return &mockEmailCodeStore{codes: make(map[string]string)}
}

func (s *mockEmailCodeStore) CreateIfAbsent(_ context.Context, key, code string, _ int) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.codes[key]; ok {
		return existing, true, nil
	}
	s.codes[key] = code
	return code, false, nil
}

func (s *mockEmailCodeStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.codes[key], nil
}

func (s *mockEmailCodeStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.codes, key)
	return nil
}

func (s *mockEmailCodeStore) firstCode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, code := range s.codes {
		return code
	}
	return ""
}

type mockEmailCodeSender struct{}

func (s mockEmailCodeSender) SendEmailCode(_ context.Context, _, _ string) error {
	return nil
}

// --- Test setup ---

func newTestHandler() *IAMHandler {
	userRepo := newMockUserRepo()
	sessionStore := newMockSessionStore()
	captchaStore := newMockCaptchaStore()
	emailCodeStore := newMockEmailCodeStore()
	hasher := infra.NewHasher()
	emailCodeUseCase := app.NewEmailCodeUseCase(emailCodeStore, mockEmailCodeSender{})

	mod := &IAMModule{
		ActivationUseCase:     app.NewActivationUseCase(userRepo, hasher),
		RegistrationUseCase:   app.NewRegistrationUseCase(userRepo, hasher, captchaStore),
		LoginUseCase:          app.NewLoginUseCase(userRepo, hasher, sessionStore, captchaStore),
		SessionUseCase:        app.NewSessionUseCase(sessionStore, userRepo),
		ChangePasswordUseCase: app.NewChangePasswordUseCase(userRepo, hasher, sessionStore),
		PasswordResetUseCase:  app.NewPasswordResetUseCase(userRepo, hasher, sessionStore, emailCodeStore, emailCodeUseCase),
		AdminUseCase:          app.NewAdminUseCase(userRepo, sessionStore, userRepo, userRepo, userRepo.operationLogs),
		CaptchaUseCase:        app.NewCaptchaUseCase(captchaStore),
		EmailCodeUseCase:      emailCodeUseCase,
		PermissionChecker:     allowPermissionChecker{},
		UserRepo:              userRepo,
		SessionStore:          sessionStore,
		CaptchaStore:          captchaStore,
		EmailCodeStore:        emailCodeStore,
	}

	return NewIAMHandler(mod, 3600, false)
}

func setupTestRouterWithHandler(h *IAMHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.RequestID())
	v1 := r.Group("/v1")
	RegisterIAMRoutes(v1, h.module, 3600, false)
	return r
}

// Helper: pre-seed a captcha with a known answer and return its ID.
func seedCaptcha(t *testing.T, h *IAMHandler, answer string) string {
	t.Helper()
	ctx := context.Background()
	id := "test-captcha-" + answer
	require.NoError(t, h.module.CaptchaStore.Create(ctx, id, answer, 300))
	return id
}

func testRepo(h *IAMHandler) *mockUserRepo {
	return h.module.UserRepo.(*mockUserRepo)
}

func seedAdminSession(t *testing.T, h *IAMHandler, sessionID string) *domain.User {
	t.Helper()
	admin, err := h.module.ActivationUseCase.Activate(context.Background(), "admin@test.com", "Admin123!", "")
	require.NoError(t, err)
	require.NoError(t, h.module.SessionStore.Create(context.Background(), &domain.Session{
		ID:           sessionID,
		UserID:       admin.ID,
		RoleLevel:    admin.RoleLevel,
		Email:        admin.Email,
		TokenVersion: admin.TokenVersion,
	}, 3600))
	return admin
}

func seedUser(t *testing.T, h *IAMHandler, email string) *domain.User {
	t.Helper()
	hash, err := infra.NewHasher().Hash("User123!")
	require.NoError(t, err)
	user := &domain.User{
		Email:        email,
		PasswordHash: hash,
		Enabled:      true,
		RoleLevel:    domain.RoleUser,
	}
	require.NoError(t, testRepo(h).Create(context.Background(), user))
	return user
}

// --- Activation Tests ---

func TestGetActivation_Needed(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/v1/activation", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"needed":true}`, w.Body.String())
}

func TestPostActivation_CreatesSuperAdmin(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)

	body := `{"email":"admin@test.com","password":"Admin123!"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/activation", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Contains(t, w.Body.String(), `"role":"super_admin"`)
	assert.Contains(t, w.Body.String(), `"email":"admin@test.com"`)
}

func TestPostActivation_AlreadyDone(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)

	// First activation succeeds
	body1 := `{"email":"admin@test.com","password":"Admin123!"}`
	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest("POST", "/v1/activation", strings.NewReader(body1))
	req1.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusCreated, w1.Code)

	// Second activation fails with 409
	body2 := `{"email":"another@test.com","password":"Admin123!"}`
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest("POST", "/v1/activation", strings.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusConflict, w2.Code)
	assert.Contains(t, w2.Body.String(), "already been completed")
}

// --- Captcha Tests ---

func TestPostCaptcha_ReturnsImage(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/captchas", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"captchaId"`)
	assert.Contains(t, w.Body.String(), `"image"`)
}

func TestPostEmailCode_ReturnsNoContent(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)

	body := `{"email":"user@test.com"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/email/code", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Empty(t, w.Body.String())
}

// --- Registration Tests ---

func TestPostRegister_RequiresCaptcha(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)

	// Without captcha: should fail validation
	body := `{"email":"user@test.com","password":"User123!","captchaId":"","captchaAnswer":""}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	// Missing captcha should return 400 (required binding validation)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- Login Tests ---

func TestPostLogin_WithoutCaptcha(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)

	// Login without captcha should fail binding validation (400)
	body := `{"email":"admin@test.com","password":"Admin123!"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPostLogin_WrongPassword(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)

	// First activate
	body := `{"email":"admin@test.com","password":"Admin123!"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/activation", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusCreated, w.Code)

	// Pre-seed a captcha
	captchaID := seedCaptcha(t, h, "4321")

	// Login with wrong password (correct captcha)
	loginBody := `{"email":"admin@test.com","password":"wrong","captchaId":"` + captchaID + `","captchaAnswer":"4321"}`
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest("POST", "/v1/sessions", strings.NewReader(loginBody))
	req2.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusUnprocessableEntity, w2.Code)
	assert.Contains(t, w2.Body.String(), "Account or password is incorrect")
}

func TestPostLogin_WrongCaptcha(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)

	// First activate
	body := `{"email":"admin@test.com","password":"Admin123!"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/activation", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusCreated, w.Code)

	// Pre-seed a captcha with answer "1234", but submit "wrong"
	captchaID := seedCaptcha(t, h, "1234")
	loginBody := `{"email":"admin@test.com","password":"Admin123!","captchaId":"` + captchaID + `","captchaAnswer":"wrong"}`
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest("POST", "/v1/sessions", strings.NewReader(loginBody))
	req2.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusUnprocessableEntity, w2.Code)
	assert.Contains(t, w2.Body.String(), "Captcha is incorrect or expired")
}

func TestPostLogin_Success(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)

	// First activate
	body := `{"email":"admin@test.com","password":"Admin123!"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/activation", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusCreated, w.Code)

	// Pre-seed a known captcha
	captchaID := seedCaptcha(t, h, "1234")

	// Login with correct password and known captcha
	loginBody := `{"email":"admin@test.com","password":"Admin123!","captchaId":"` + captchaID + `","captchaAnswer":"1234"}`
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest("POST", "/v1/sessions", strings.NewReader(loginBody))
	req2.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusOK, w2.Code)

	// Check HttpOnly cookie
	cookies := w2.Result().Cookies()
	foundSid := false
	for _, c := range cookies {
		if c.Name == "sid" {
			foundSid = true
			assert.True(t, c.HttpOnly, "sid cookie must be HttpOnly")
			assert.True(t, c.MaxAge > 0, "sid cookie must have MaxAge > 0")
			break
		}
	}
	assert.True(t, foundSid, "sid cookie should be set")
}

// --- Auth Middleware Tests ---

func TestGetMe_Unauthenticated(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/v1/me", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "Authentication is required")
}

func TestAdminUsers_Unauthenticated(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/v1/admin/users", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAdminUsers_InvalidQuery(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)

	admin, err := h.module.ActivationUseCase.Activate(context.Background(), "admin@test.com", "Admin123!", "")
	assert.NoError(t, err)
	assert.NoError(t, h.module.SessionStore.Create(context.Background(), &domain.Session{
		ID:           "admin-session",
		UserID:       admin.ID,
		RoleLevel:    admin.RoleLevel,
		Email:        admin.Email,
		TokenVersion: admin.TokenVersion,
	}, 3600))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/v1/admin/users?offset=bad", nil)
	req.AddCookie(&http.Cookie{Name: "sid", Value: "admin-session"})
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "Invalid query parameters.")
}

func TestPatchAdminUserWritesOperationLog(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	admin := seedAdminSession(t, h, "admin-session")
	target := seedUser(t, h, "user@test.com")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PATCH", fmt.Sprintf("/v1/admin/users/%d", target.ID), strings.NewReader(`{"enabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-admin-update")
	req.AddCookie(&http.Cookie{Name: "sid", Value: "admin-session"})
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	updated, err := testRepo(h).FindByID(context.Background(), target.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.False(t, updated.Enabled)
	require.Equal(t, 1, updated.TokenVersion)

	log := testRepo(h).lastLog()
	require.NotNil(t, log)
	require.Equal(t, admin.ID, log.OperatorUserID)
	require.Equal(t, "iam.user.update", log.OperationType)
	require.Equal(t, "user", log.ResourceType)
	require.Equal(t, fmt.Sprintf("%d", target.ID), log.ResourceID)
	require.Equal(t, "success", log.Result)
	require.Equal(t, "User access settings updated.", log.SafeSummary)
	require.Equal(t, "req-admin-update", log.RequestID)
}

func TestPatchAdminUserNoopWritesOperationLog(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	admin := seedAdminSession(t, h, "admin-session")
	target := seedUser(t, h, "user@test.com")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PATCH", fmt.Sprintf("/v1/admin/users/%d", target.ID), strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-admin-noop")
	req.AddCookie(&http.Cookie{Name: "sid", Value: "admin-session"})
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	log := testRepo(h).lastLog()
	require.NotNil(t, log)
	require.Equal(t, admin.ID, log.OperatorUserID)
	require.Equal(t, "iam.user.update", log.OperationType)
	require.Equal(t, "success", log.Result)
	require.Equal(t, "User access settings unchanged.", log.SafeSummary)
	require.Equal(t, "req-admin-noop", log.RequestID)
}

func TestPatchAdminUserSameValuesWritesUnchangedOperationLog(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	admin := seedAdminSession(t, h, "admin-session")
	target := seedUser(t, h, "user@test.com")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PATCH", fmt.Sprintf("/v1/admin/users/%d", target.ID), strings.NewReader(`{"enabled":true,"roleLevel":10}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-admin-same-values")
	req.AddCookie(&http.Cookie{Name: "sid", Value: "admin-session"})
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	updated, err := testRepo(h).FindByID(context.Background(), target.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, 0, updated.TokenVersion)

	log := testRepo(h).lastLog()
	require.NotNil(t, log)
	require.Equal(t, admin.ID, log.OperatorUserID)
	require.Equal(t, "iam.user.update", log.OperationType)
	require.Equal(t, "success", log.Result)
	require.Equal(t, "User access settings unchanged.", log.SafeSummary)
	require.Equal(t, "req-admin-same-values", log.RequestID)
}

func TestPostAdminRevokeSessionsWritesOperationLog(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	admin := seedAdminSession(t, h, "admin-session")
	target := seedUser(t, h, "user@test.com")

	require.NoError(t, h.module.SessionStore.Create(context.Background(), &domain.Session{
		ID:           "target-session",
		UserID:       target.ID,
		RoleLevel:    target.RoleLevel,
		Email:        target.Email,
		TokenVersion: target.TokenVersion,
	}, 3600))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", fmt.Sprintf("/v1/admin/users/%d/sessions/revoke", target.ID), nil)
	req.Header.Set("X-Request-ID", "req-revoke")
	req.AddCookie(&http.Cookie{Name: "sid", Value: "admin-session"})
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNoContent, w.Code)

	updated, err := testRepo(h).FindByID(context.Background(), target.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, 1, updated.TokenVersion)

	log := testRepo(h).lastLog()
	require.NotNil(t, log)
	require.Equal(t, admin.ID, log.OperatorUserID)
	require.Equal(t, "iam.user.sessions.revoke", log.OperationType)
	require.Equal(t, "success", log.Result)
	require.Equal(t, "User sessions revoked.", log.SafeSummary)
	require.Equal(t, "req-revoke", log.RequestID)
}

func TestPutAdminUserPermissionsWritesOperationLogAndReloads(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	admin := seedAdminSession(t, h, "admin-session")
	target := seedUser(t, h, "user@test.com")

	body := `{"policies":[{"resource":"iam:user","action":"read","effect":"deny"}]}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", fmt.Sprintf("/v1/admin/users/%d/permissions", target.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-permissions")
	req.AddCookie(&http.Cookie{Name: "sid", Value: "admin-session"})
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNoContent, w.Code)
	require.Equal(t, 1, testRepo(h).reloadCount())

	policies, err := testRepo(h).ListUserPermissionPolicies(context.Background(), target.ID)
	require.NoError(t, err)
	require.Equal(t, []domain.PermissionPolicy{{Resource: "iam:user", Action: "read", Effect: "deny"}}, policies)

	log := testRepo(h).lastLog()
	require.NotNil(t, log)
	require.Equal(t, admin.ID, log.OperatorUserID)
	require.Equal(t, "iam.user.permissions.update", log.OperationType)
	require.Equal(t, "success", log.Result)
	require.Equal(t, "User permissions updated.", log.SafeSummary)
	require.Equal(t, "req-permissions", log.RequestID)
}

func TestPutAdminUserPermissionsRejectsWildcardAction(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	admin := seedAdminSession(t, h, "admin-session")
	target := seedUser(t, h, "user@test.com")

	body := `{"policies":[{"resource":"iam:user","action":"*","effect":"deny"}]}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", fmt.Sprintf("/v1/admin/users/%d/permissions", target.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-permissions-wildcard")
	req.AddCookie(&http.Cookie{Name: "sid", Value: "admin-session"})
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.Contains(t, w.Body.String(), "Invalid permission policy.")
	require.Equal(t, 0, testRepo(h).reloadCount())

	log := testRepo(h).lastLog()
	require.NotNil(t, log)
	require.Equal(t, admin.ID, log.OperatorUserID)
	require.Equal(t, "iam.user.permissions.update", log.OperationType)
	require.Equal(t, "failure", log.Result)
	require.Equal(t, "User permission update failed.", log.SafeSummary)
	require.Equal(t, "req-permissions-wildcard", log.RequestID)
}

func TestPutAdminUserPermissionsRestoresPoliciesWhenReloadFails(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	admin := seedAdminSession(t, h, "admin-session")
	target := seedUser(t, h, "user@test.com")
	repo := testRepo(h)
	repo.policies[target.ID] = []domain.PermissionPolicy{{Resource: "iam:user", Action: "read", Effect: "allow"}}
	repo.reloadErr = errors.New("reload failed")

	body := `{"policies":[{"resource":"iam:user","action":"read","effect":"deny"}]}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", fmt.Sprintf("/v1/admin/users/%d/permissions", target.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-permissions-reload-fail")
	req.AddCookie(&http.Cookie{Name: "sid", Value: "admin-session"})
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.GreaterOrEqual(t, repo.reloadCount(), 2)

	policies, err := repo.ListUserPermissionPolicies(context.Background(), target.ID)
	require.NoError(t, err)
	require.Equal(t, []domain.PermissionPolicy{{Resource: "iam:user", Action: "read", Effect: "allow"}}, policies)

	log := repo.lastLog()
	require.NotNil(t, log)
	require.Equal(t, admin.ID, log.OperatorUserID)
	require.Equal(t, "iam.user.permissions.update", log.OperationType)
	require.Equal(t, "failure", log.Result)
	require.Equal(t, "User permission update failed.", log.SafeSummary)
	require.Equal(t, "req-permissions-reload-fail", log.RequestID)
}

func TestPostAdminInviteWritesSuccessAndDuplicateFailureLogs(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	admin := seedAdminSession(t, h, "admin-session")

	body := `{"code":"INVITE-1","maxUse":2}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/admin/invites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-invite-create")
	req.AddCookie(&http.Cookie{Name: "sid", Value: "admin-session"})
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	require.Equal(t, 1, testRepo(h).logCount())
	log := testRepo(h).lastLog()
	require.NotNil(t, log)
	require.Equal(t, admin.ID, log.OperatorUserID)
	require.Equal(t, "iam.invite.create", log.OperationType)
	require.Equal(t, "invite", log.ResourceType)
	require.Equal(t, "INVITE-1", log.ResourceID)
	require.Equal(t, "success", log.Result)

	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/v1/admin/invites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-invite-duplicate")
	req.AddCookie(&http.Cookie{Name: "sid", Value: "admin-session"})
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusConflict, w.Code)
	require.Equal(t, 2, testRepo(h).logCount())
	log = testRepo(h).lastLog()
	require.NotNil(t, log)
	require.Equal(t, "iam.invite.create", log.OperationType)
	require.Equal(t, "failure", log.Result)
	require.Equal(t, "Invite create failed.", log.SafeSummary)
	require.Equal(t, "req-invite-duplicate", log.RequestID)
}

func TestChangePassword_Unauthenticated(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)

	body := `{"oldPassword":"wrong","newPassword":"NewPass123!"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PATCH", "/v1/password", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestPostPasswordResetIgnoresCleanupFailuresAfterPasswordUpdate(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	user := seedUser(t, h, "user@test.com")

	require.NoError(t, h.module.PasswordResetUseCase.Request(context.Background(), user.Email))
	code := h.module.EmailCodeStore.(*mockEmailCodeStore).firstCode()
	require.NotEmpty(t, code)

	h.module.EmailCodeStore.(*mockEmailCodeStore).deleteErr = errors.New("redis email code delete failed")
	h.module.SessionStore.(*mockSessionStore).deleteByUserIDErr = errors.New("redis session cleanup failed")

	body := fmt.Sprintf(`{"email":"%s","code":"%s","newPassword":"NewPass123!"}`, user.Email, code)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/password/reset", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNoContent, w.Code)

	updated, err := testRepo(h).FindByEmail(context.Background(), user.Email)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, 1, updated.TokenVersion)
	require.True(t, infra.NewHasher().Verify("NewPass123!", updated.PasswordHash))
}

// --- Validation Error Tests ---

func TestPostRegister_InvalidEmail(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)

	body := `{"email":"not-an-email","password":"Pass123!","captchaId":"c1","captchaAnswer":"1234"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}
