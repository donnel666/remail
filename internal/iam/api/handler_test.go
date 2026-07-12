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
	"time"

	"github.com/donnel666/remail/api/middleware"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/iam/app"
	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/iam/infra"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- In-memory mock stores ---

type mockUserRepo struct {
	mu                   sync.Mutex
	users                map[uint]*domain.User
	byID                 map[string]uint
	invites              map[string]*domain.Invite
	userGroups           map[uint]*domain.UserGroup
	supplierApplications map[uint]*domain.SupplierApplication
	policies             map[uint][]domain.PermissionPolicy
	operationLogs        *mockOperationLogPort
	reloads              int
	reloadErr            error
	seq                  uint
}

func newMockUserRepo() *mockUserRepo {
	return &mockUserRepo{
		users:   make(map[uint]*domain.User),
		byID:    make(map[string]uint),
		invites: make(map[string]*domain.Invite),
		userGroups: map[uint]*domain.UserGroup{
			1: {ID: 1, Code: "normal", Name: "普通用户", Description: "默认权益分组", Enabled: true},
		},
		supplierApplications: make(map[uint]*domain.SupplierApplication),
		policies:             make(map[uint][]domain.PermissionPolicy),
		operationLogs:        &mockOperationLogPort{},
	}
}

func (r *mockUserRepo) ListUserGroups(_ context.Context) ([]domain.UserGroup, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	groups := make([]domain.UserGroup, 0, len(r.userGroups))
	for _, group := range r.userGroups {
		groups = append(groups, *group)
	}
	return groups, nil
}

func (r *mockUserRepo) FindUserGroupByID(_ context.Context, id uint) (*domain.UserGroup, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	group, ok := r.userGroups[id]
	if !ok {
		return nil, nil
	}
	cp := *group
	return &cp, nil
}

func (r *mockUserRepo) CreateUserGroup(_ context.Context, group *domain.UserGroup) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	group.ID = r.seq
	group.CreatedAt = time.Now()
	group.UpdatedAt = group.CreatedAt
	cp := *group
	r.userGroups[group.ID] = &cp
	return nil
}

func (r *mockUserRepo) UpdateUserGroup(_ context.Context, group *domain.UserGroup) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.userGroups[group.ID]; !ok {
		return domain.ErrInvalidUserGroup
	}
	group.UpdatedAt = time.Now()
	cp := *group
	r.userGroups[group.ID] = &cp
	return nil
}

func (r *mockUserRepo) CreateSupplierApplicationReviewing(_ context.Context, application *domain.SupplierApplication) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.supplierApplications {
		if existing.ApplicantUserID == application.ApplicantUserID && existing.Status == domain.SupplierApplicationReviewing {
			return domain.ErrSupplierApplicationAlreadyReviewing
		}
	}
	r.seq++
	application.ID = r.seq
	application.CreatedAt = time.Now()
	application.UpdatedAt = application.CreatedAt
	cp := *application
	r.supplierApplications[application.ID] = &cp
	return nil
}

func (r *mockUserRepo) FindLatestSupplierApplicationByApplicantUserID(_ context.Context, applicantUserID uint) (*domain.SupplierApplication, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var latest *domain.SupplierApplication
	for _, application := range r.supplierApplications {
		if application.ApplicantUserID != applicantUserID {
			continue
		}
		if latest == nil || application.ID > latest.ID {
			cp := *application
			latest = &cp
		}
	}
	return latest, nil
}

func (r *mockUserRepo) FindSupplierApplicationByID(_ context.Context, id uint) (*domain.SupplierApplication, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if application, ok := r.supplierApplications[id]; ok {
		cp := *application
		return &cp, nil
	}
	return nil, nil
}

func (r *mockUserRepo) ListSupplierApplications(_ context.Context, status string, offset, limit int) ([]domain.SupplierApplication, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []domain.SupplierApplication
	for _, application := range r.supplierApplications {
		if status == "" || string(application.Status) == status {
			result = append(result, *application)
		}
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

func (r *mockUserRepo) CountSupplierApplications(_ context.Context, status string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var count int64
	for _, application := range r.supplierApplications {
		if status == "" || string(application.Status) == status {
			count++
		}
	}
	return count, nil
}

func (r *mockUserRepo) ApproveSupplierApplicationWithUserAndLog(ctx context.Context, application *domain.SupplierApplication, user *domain.User, log *governancedomain.OperationLog) error {
	r.mu.Lock()
	if stored, ok := r.supplierApplications[application.ID]; ok {
		cp := *application
		cp.UpdatedAt = time.Now()
		*stored = cp
	}
	if storedUser, ok := r.users[user.ID]; ok {
		cp := *user
		*storedUser = cp
	}
	r.mu.Unlock()
	return r.operationLogs.Create(ctx, log)
}

func (r *mockUserRepo) RejectSupplierApplicationWithLog(ctx context.Context, application *domain.SupplierApplication, log *governancedomain.OperationLog) error {
	r.mu.Lock()
	if stored, ok := r.supplierApplications[application.ID]; ok {
		cp := *application
		cp.UpdatedAt = time.Now()
		*stored = cp
	}
	r.mu.Unlock()
	return r.operationLogs.Create(ctx, log)
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
	return r.ListByFilter(context.Background(), domain.UserListFilter{}, offset, limit)
}

func (r *mockUserRepo) ListByFilter(_ context.Context, filter domain.UserListFilter, offset, limit int) ([]domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []domain.User
	for _, u := range r.users {
		if !mockUserMatchesFilter(u, filter) {
			continue
		}
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
	return r.CountByFilter(context.Background(), domain.UserListFilter{})
}

func (r *mockUserRepo) CountByFilter(_ context.Context, filter domain.UserListFilter) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var count int64
	for _, user := range r.users {
		if mockUserMatchesFilter(user, filter) {
			count++
		}
	}
	return count, nil
}

func mockUserMatchesFilter(user *domain.User, filter domain.UserListFilter) bool {
	if len(filter.IDs) > 0 {
		found := false
		for _, id := range filter.IDs {
			if user.ID == id {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	search := strings.ToLower(strings.TrimSpace(filter.Search))
	if search == "" {
		return true
	}
	return strings.Contains(strings.ToLower(user.Email), search) ||
		strings.Contains(strings.ToLower(user.Nickname), search) ||
		strings.Contains(fmt.Sprintf("%d", user.ID), search)
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

func (r *mockUserRepo) FindReferralInviteByOwner(_ context.Context, userID uint) (*domain.Invite, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, invite := range r.invites {
		if invite.Kind == domain.InviteKindReferral && invite.CreatedByUserID != nil && *invite.CreatedByUserID == userID {
			cp := *invite
			return &cp, nil
		}
	}
	return nil, nil
}

func (r *mockUserRepo) GetOrCreateReferralInvite(_ context.Context, userID uint, code string, maxUse int) (*domain.Invite, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.users[userID]; !ok {
		return nil, domain.ErrUserNotFound
	}
	for _, invite := range r.invites {
		if invite.Kind == domain.InviteKindReferral && invite.CreatedByUserID != nil && *invite.CreatedByUserID == userID {
			cp := *invite
			return &cp, nil
		}
	}
	if _, exists := r.invites[code]; exists {
		return nil, domain.ErrInviteAlreadyExists
	}
	now := time.Now()
	invite := &domain.Invite{
		Code:            code,
		Kind:            domain.InviteKindReferral,
		Enabled:         true,
		MaxUse:          maxUse,
		CreatedByUserID: &userID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	r.invites[code] = invite
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

func (c *mockCaptchaStore) GetDel(_ context.Context, captchaID string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	answer := c.captchas[captchaID]
	delete(c.captchas, captchaID)
	return answer, nil
}

func (c *mockCaptchaStore) Delete(_ context.Context, captchaID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.captchas, captchaID)
	return nil
}

type allowPermissionChecker struct{}

func (c allowPermissionChecker) Check(_ context.Context, _ uint, _ domain.Role, _, _ string) (bool, error) {
	return true, nil
}

func (c allowPermissionChecker) Reload(_ context.Context) error {
	return nil
}

type recordingPermissionChecker struct {
	allowed  bool
	resource string
	action   string
}

func (c *recordingPermissionChecker) Check(_ context.Context, _ uint, _ domain.Role, resource, action string) (bool, error) {
	c.resource = resource
	c.action = action
	return c.allowed, nil
}

func (*recordingPermissionChecker) Reload(context.Context) error { return nil }

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

func (s *mockEmailCodeStore) codeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.codes)
}

type mockMailDelivery struct{}

func (s mockMailDelivery) Send(_ context.Context, _ maildomain.OutboundMessage) error {
	return nil
}

// --- Test setup ---

func newTestHandler() *IAMHandler {
	userRepo := newMockUserRepo()
	sessionStore := newMockSessionStore()
	captchaStore := newMockCaptchaStore()
	emailCodeStore := newMockEmailCodeStore()
	hasher := infra.NewHasher()
	emailCodeUseCase := app.NewEmailCodeUseCase(emailCodeStore, mockMailDelivery{}, captchaStore)

	mod := &IAMModule{
		ActivationUseCase:          app.NewActivationUseCase(userRepo, hasher),
		RegistrationUseCase:        app.NewRegistrationUseCase(userRepo, hasher, emailCodeStore),
		LoginUseCase:               app.NewLoginUseCase(userRepo, hasher, sessionStore, captchaStore),
		SessionUseCase:             app.NewSessionUseCase(sessionStore, userRepo),
		ChangePasswordUseCase:      app.NewChangePasswordUseCase(userRepo, hasher, sessionStore),
		PasswordResetUseCase:       app.NewPasswordResetUseCase(userRepo, hasher, sessionStore, emailCodeStore, emailCodeUseCase),
		AdminUseCase:               app.NewAdminUseCase(userRepo, sessionStore, userRepo, userRepo, userRepo.operationLogs),
		InviteUseCase:              app.NewInviteUseCase(userRepo),
		SupplierApplicationUseCase: app.NewSupplierApplicationUseCase(userRepo, userRepo),
		CaptchaUseCase:             app.NewCaptchaUseCase(captchaStore),
		EmailCodeUseCase:           emailCodeUseCase,
		PermissionChecker:          allowPermissionChecker{},
		UserRepo:                   userRepo,
		SessionStore:               sessionStore,
		CaptchaStore:               captchaStore,
		EmailCodeStore:             emailCodeStore,
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

func requestEmailCode(t *testing.T, h *IAMHandler, r *gin.Engine, email, captchaAnswer string) string {
	t.Helper()
	captchaID := seedCaptcha(t, h, captchaAnswer)
	body := fmt.Sprintf(
		`{"email":%q,"captchaId":%q,"captchaAnswer":%q}`,
		email,
		captchaID,
		captchaAnswer,
	)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/email/code", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)

	code := h.module.EmailCodeStore.(*mockEmailCodeStore).firstCode()
	require.NotEmpty(t, code)
	return code
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
		Role:         admin.Role,
		Email:        admin.Email,
		TokenVersion: admin.TokenVersion,
	}, 3600))
	return admin
}

func addAuthenticatedRequest(req *http.Request, sessionID string) {
	const csrfToken = "test-csrf-token"
	req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: sessionID})
	req.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: csrfToken})
	req.Header.Set(middleware.CSRFHeaderName, csrfToken)
}

func seedUser(t *testing.T, h *IAMHandler, email string) *domain.User {
	t.Helper()
	hash, err := infra.NewHasher().Hash("User123!")
	require.NoError(t, err)
	user := &domain.User{
		Email:        email,
		PasswordHash: hash,
		Enabled:      true,
		Role:         domain.RoleUser,
	}
	require.NoError(t, testRepo(h).Create(context.Background(), user))
	return user
}

func seedUserSession(t *testing.T, h *IAMHandler, email, sessionID string) *domain.User {
	t.Helper()
	user := seedUser(t, h, email)
	require.NoError(t, h.module.SessionStore.Create(context.Background(), &domain.Session{
		ID:           sessionID,
		UserID:       user.ID,
		Role:         user.Role,
		Email:        user.Email,
		TokenVersion: user.TokenVersion,
	}, 3600))
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

	captchaID := seedCaptcha(t, h, "1234")
	body := `{"email":"user@test.com","captchaId":"` + captchaID + `","captchaAnswer":"1234"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/email/code", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Empty(t, w.Body.String())
}

// --- Registration Tests ---

func TestPostRegister_RequiresEmailCode(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)

	body := `{"email":"user@test.com","password":"User123!"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPostRegister_WithEmailCodeCreatesUserAndConsumesCode(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)

	code := requestEmailCode(t, h, r, "User@Test.COM", "1234")
	body := fmt.Sprintf(
		`{"email":%q,"password":"User123!","nickname":" User ","code":%q}`,
		"USER@Test.COM",
		code,
	)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	require.Contains(t, w.Body.String(), `"email":"user@test.com"`)
	require.Equal(t, 0, h.module.EmailCodeStore.(*mockEmailCodeStore).codeCount())

	user, err := testRepo(h).FindByEmail(context.Background(), "user@test.com")
	require.NoError(t, err)
	require.NotNil(t, user)
	require.Equal(t, "User", user.Nickname)
}

func TestPostRegister_WrongEmailCodeReturnsVerificationError(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)

	_ = requestEmailCode(t, h, r, "user@test.com", "1234")
	body := `{"email":"user@test.com","password":"User123!","code":"000000"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.Contains(t, w.Body.String(), "Verification code is incorrect or expired")
}

func TestPostRegister_ExistingEmailWithWrongEmailCodeReturnsVerificationError(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	seedUser(t, h, "user@test.com")

	body := `{"email":"user@test.com","password":"User123!","code":"000000"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.Contains(t, w.Body.String(), "Verification code is incorrect or expired")
	require.NotContains(t, w.Body.String(), "Email already exists")
}

func TestPostRegister_ExistingEmailWithValidEmailCodeReturnsConflict(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	seedUser(t, h, "user@test.com")

	code := requestEmailCode(t, h, r, "user@test.com", "1234")
	body := fmt.Sprintf(`{"email":"user@test.com","password":"User123!","code":%q}`, code)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusConflict, w.Code)
	require.Contains(t, w.Body.String(), "Email already exists")
}

// --- Login Tests ---

func TestPostLogin_WithoutCaptcha(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)

	// Login without captcha should fail binding validation (400)
	body := `{"email":"admin@test.com","password":"Admin123!"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/login", strings.NewReader(body))
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
	req2, _ := http.NewRequest("POST", "/v1/login", strings.NewReader(loginBody))
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
	req2, _ := http.NewRequest("POST", "/v1/login", strings.NewReader(loginBody))
	req2.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusUnprocessableEntity, w2.Code)
	assert.Contains(t, w2.Body.String(), "Captcha is incorrect or expired")
}

func TestPostLogin_NormalizesEmail(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)

	body := `{"email":"Admin@Test.COM","password":"Admin123!"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/activation", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	captchaID := seedCaptcha(t, h, "1234")
	loginBody := `{"email":"ADMIN@TEST.COM","password":"Admin123!","captchaId":"` + captchaID + `","captchaAnswer":"1234"}`
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest("POST", "/v1/login", strings.NewReader(loginBody))
	req2.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w2, req2)

	require.Equal(t, http.StatusOK, w2.Code)
	require.Contains(t, w2.Body.String(), `"email":"admin@test.com"`)
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
	req2, _ := http.NewRequest("POST", "/v1/login", strings.NewReader(loginBody))
	req2.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusOK, w2.Code)

	// Check auth cookies
	cookies := w2.Result().Cookies()
	foundSid := false
	foundCSRF := false
	for _, c := range cookies {
		if c.Name == middleware.SessionCookieName {
			foundSid = true
			assert.True(t, c.HttpOnly, "sid cookie must be HttpOnly")
			assert.True(t, c.MaxAge > 0, "sid cookie must have MaxAge > 0")
			assert.Equal(t, http.SameSiteLaxMode, c.SameSite)
		}
		if c.Name == middleware.CSRFCookieName {
			foundCSRF = true
			assert.False(t, c.HttpOnly, "csrf cookie must be readable by the frontend")
			assert.True(t, c.MaxAge > 0, "csrf cookie must have MaxAge > 0")
			assert.Equal(t, http.SameSiteLaxMode, c.SameSite)
		}
	}
	assert.True(t, foundSid, "sid cookie should be set")
	assert.True(t, foundCSRF, "csrf cookie should be set")
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

func TestAdminUsers_RequiresReadPermission(t *testing.T) {
	h := newTestHandler()
	checker := &recordingPermissionChecker{}
	h.module.PermissionChecker = checker
	r := setupTestRouterWithHandler(h)
	seedAdminSession(t, h, "admin-session")

	w := httptest.NewRecorder()
	req, err := http.NewRequest("GET", "/v1/admin/users", nil)
	require.NoError(t, err)
	addAuthenticatedRequest(req, "admin-session")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Equal(t, "iam:user", checker.resource)
	require.Equal(t, "read", checker.action)
	require.Contains(t, w.Body.String(), "Permission denied.")
}

func TestAdminUsers_InvalidQuery(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)

	admin, err := h.module.ActivationUseCase.Activate(context.Background(), "admin@test.com", "Admin123!", "")
	assert.NoError(t, err)
	assert.NoError(t, h.module.SessionStore.Create(context.Background(), &domain.Session{
		ID:           "admin-session",
		UserID:       admin.ID,
		Role:         admin.Role,
		Email:        admin.Email,
		TokenVersion: admin.TokenVersion,
	}, 3600))

	paths := []string{
		"/v1/admin/users?offset=bad",
		"/v1/admin/users?limit=101",
		"/v1/admin/users?search=" + strings.Repeat("x", 121),
	}
	for _, path := range paths {
		w := httptest.NewRecorder()
		req, requestErr := http.NewRequest("GET", path, nil)
		require.NoError(t, requestErr)
		req.AddCookie(&http.Cookie{Name: "sid", Value: "admin-session"})
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code, path)
		assert.Contains(t, w.Body.String(), "Invalid query parameters.", path)
	}
}

func TestAdminUsers_SearchMatchesEmailNicknameAndID(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	seedAdminSession(t, h, "admin-session")
	alpha := seedUser(t, h, "alpha@example.com")
	alpha.Nickname = "Project Alpha"
	require.NoError(t, testRepo(h).Update(context.Background(), alpha))
	beta := seedUser(t, h, "beta@example.com")
	beta.Nickname = "Beta User"
	require.NoError(t, testRepo(h).Update(context.Background(), beta))

	search := func(query string) string {
		t.Helper()
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/v1/admin/users?limit=20&search="+query, nil)
		addAuthenticatedRequest(req, "admin-session")
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		return w.Body.String()
	}

	assert.Contains(t, search("alpha%40example.com"), "alpha@example.com")
	assert.NotContains(t, search("alpha%40example.com"), "beta@example.com")
	assert.Contains(t, search("Project"), "alpha@example.com")
	assert.NotContains(t, search("Project"), "beta@example.com")
	assert.Contains(t, search(fmt.Sprintf("%d", beta.ID)), "beta@example.com")
}

func TestAdminUsers_IDsFilter(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	seedAdminSession(t, h, "admin-session")
	alpha := seedUser(t, h, "ids-alpha@example.com")
	beta := seedUser(t, h, "ids-beta@example.com")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", fmt.Sprintf("/v1/admin/users?ids=%d", beta.ID), nil)
	addAuthenticatedRequest(req, "admin-session")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), alpha.Email)
	assert.Contains(t, w.Body.String(), beta.Email)
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
	addAuthenticatedRequest(req, "admin-session")
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
	addAuthenticatedRequest(req, "admin-session")
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
	req, _ := http.NewRequest("PATCH", fmt.Sprintf("/v1/admin/users/%d", target.ID), strings.NewReader(`{"enabled":true,"role":"user"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-admin-same-values")
	addAuthenticatedRequest(req, "admin-session")
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
		Role:         target.Role,
		Email:        target.Email,
		TokenVersion: target.TokenVersion,
	}, 3600))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", fmt.Sprintf("/v1/admin/users/%d/sessions/revoke", target.ID), nil)
	req.Header.Set("X-Request-ID", "req-revoke")
	addAuthenticatedRequest(req, "admin-session")
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
	addAuthenticatedRequest(req, "admin-session")
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
	addAuthenticatedRequest(req, "admin-session")
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
	addAuthenticatedRequest(req, "admin-session")
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
	addAuthenticatedRequest(req, "admin-session")
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
	addAuthenticatedRequest(req, "admin-session")
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

func TestChangePassword_RequiresCSRF(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	seedAdminSession(t, h, "admin-session")

	body := `{"oldPassword":"Admin123!","newPassword":"NewPass123!"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PATCH", "/v1/password", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "admin-session"})
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "Permission denied")
}

func TestPostPasswordResetIgnoresCleanupFailuresAfterPasswordUpdate(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	user := seedUser(t, h, "user@test.com")

	captchaID := seedCaptcha(t, h, "1234")
	require.NoError(t, h.module.PasswordResetUseCase.Request(context.Background(), "USER@Test.COM", captchaID, "1234"))
	code := h.module.EmailCodeStore.(*mockEmailCodeStore).firstCode()
	require.NotEmpty(t, code)

	h.module.EmailCodeStore.(*mockEmailCodeStore).deleteErr = errors.New("redis email code delete failed")
	h.module.SessionStore.(*mockSessionStore).deleteByUserIDErr = errors.New("redis session cleanup failed")

	body := fmt.Sprintf(`{"email":"%s","code":"%s","newPassword":"NewPass123!"}`, "USER@Test.COM", code)
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

func TestSupplierApplicationSubmitAndCurrent(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	seedUserSession(t, h, "user@test.com", "user-session")

	body := `{"reason":"I want to publish my Microsoft resources."}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/suppliers/applications", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	addAuthenticatedRequest(req, "user-session")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"status":"reviewing"`)

	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/v1/suppliers/applications/current", nil)
	addAuthenticatedRequest(req, "user-session")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"status":"reviewing"`)

	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/v1/suppliers/applications", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	addAuthenticatedRequest(req, "user-session")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "already under review")
}

func TestGetMeInviteReturnsStableReferralCode(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	seedUserSession(t, h, "aff-user@test.com", "user-session")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/v1/me/invite", nil)
	addAuthenticatedRequest(req, "user-session")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())

	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/v1/me/invite", nil)
	addAuthenticatedRequest(req, "user-session")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"inviteCode":"AFF`)
	first := w.Body.String()

	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/v1/me/invite", nil)
	addAuthenticatedRequest(req, "user-session")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, first, w.Body.String())

	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/v1/me/invite", nil)
	addAuthenticatedRequest(req, "user-session")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, first, w.Body.String())
}

func TestAdminApproveSupplierApplicationPromotesUser(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	admin := seedAdminSession(t, h, "admin-session")
	user := seedUser(t, h, "user@test.com")

	application, err := h.module.SupplierApplicationUseCase.Submit(context.Background(), user.ID, "I have resources.")
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", fmt.Sprintf("/v1/admin/suppliers/applications/%d/approve", application.ID), nil)
	req.Header.Set("X-Request-ID", "req-supplier-approve")
	addAuthenticatedRequest(req, "admin-session")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"status":"approved"`)

	updated, err := testRepo(h).FindByID(context.Background(), user.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, domain.RoleSupplier, updated.Role)

	log := testRepo(h).lastLog()
	require.NotNil(t, log)
	require.Equal(t, admin.ID, log.OperatorUserID)
	require.Equal(t, "iam.supplier_application.approve", log.OperationType)
	require.Equal(t, "supplier_application", log.ResourceType)
	require.Equal(t, "success", log.Result)
	require.Equal(t, "req-supplier-approve", log.RequestID)
}

// --- Validation Error Tests ---

func TestPostRegister_InvalidEmail(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)

	body := `{"email":"not-an-email","password":"Pass123!","code":"123456"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}
