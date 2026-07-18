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
	updatePasswordErr    error
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

func (r *mockUserRepo) RecordLogin(_ context.Context, userID uint, expectedPasswordHash string) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	user := r.users[userID]
	if user == nil || !user.Enabled || user.PasswordHash != expectedPasswordHash {
		return nil, nil
	}
	now := time.Now()
	user.LastLoginAt = &now
	cp := *user
	return &cp, nil
}

func (r *mockUserRepo) UpdatePassword(_ context.Context, userID uint, expectedPasswordHash, passwordHash string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updatePasswordErr != nil {
		return false, r.updatePasswordErr
	}
	user := r.users[userID]
	if user == nil || !user.Enabled || user.PasswordHash != expectedPasswordHash {
		return false, nil
	}
	user.PasswordHash = passwordHash
	user.TokenVersion++
	return true, nil
}

func (r *mockUserRepo) UpdateWithOperationLog(ctx context.Context, user *domain.User, log *governancedomain.OperationLog) error {
	if err := r.Update(ctx, user); err != nil {
		return err
	}
	return r.operationLogs.Create(ctx, log)
}

func (r *mockUserRepo) UpdateNonSuperAdminAccessWithOperationLog(ctx context.Context, userID uint, enabled *bool, role *domain.Role, userGroupID *uint, incrementTokenVersion bool, log *governancedomain.OperationLog) (*domain.User, error) {
	r.mu.Lock()
	current, ok := r.users[userID]
	if !ok {
		r.mu.Unlock()
		return nil, domain.ErrUserNotFound
	}
	if current.Role == domain.RoleSuperAdmin {
		r.mu.Unlock()
		return nil, domain.ErrPermissionDenied
	}
	cp := *current
	if enabled != nil {
		cp.Enabled = *enabled
	}
	if role != nil {
		cp.Role = *role
	}
	if userGroupID != nil {
		cp.UserGroupID = *userGroupID
		if group, exists := r.userGroups[*userGroupID]; exists {
			cp.UserGroup = *group
		}
	}
	if incrementTokenVersion {
		cp.TokenVersion++
	}
	r.users[userID] = &cp
	r.mu.Unlock()
	if err := r.operationLogs.Create(ctx, log); err != nil {
		return nil, err
	}
	return &cp, nil
}

func (r *mockUserRepo) UpdateNonSuperAdminProfileWithOperationLog(ctx context.Context, userID uint, email, nickname, passwordHash *string, enabled *bool, role *domain.Role, userGroupID *uint, incrementTokenVersion bool, log *governancedomain.OperationLog) (*domain.User, error) {
	r.mu.Lock()
	current, ok := r.users[userID]
	if !ok {
		r.mu.Unlock()
		return nil, domain.ErrUserNotFound
	}
	if current.Role == domain.RoleSuperAdmin {
		r.mu.Unlock()
		return nil, domain.ErrPermissionDenied
	}
	cp := *current
	if email != nil {
		for other := range r.users {
			if other != userID && r.users[other].Email == *email {
				r.mu.Unlock()
				return nil, domain.ErrEmailAlreadyExists
			}
		}
		cp.Email = *email
	}
	if nickname != nil {
		cp.Nickname = *nickname
	}
	if passwordHash != nil {
		cp.PasswordHash = *passwordHash
	}
	if enabled != nil {
		cp.Enabled = *enabled
	}
	if role != nil {
		cp.Role = *role
	}
	if userGroupID != nil {
		cp.UserGroupID = *userGroupID
		if group, exists := r.userGroups[*userGroupID]; exists {
			cp.UserGroup = *group
		}
	}
	if incrementTokenVersion {
		cp.TokenVersion++
	}
	r.users[userID] = &cp
	r.mu.Unlock()
	if err := r.operationLogs.Create(ctx, log); err != nil {
		return nil, err
	}
	return &cp, nil
}

func (r *mockUserRepo) DeleteNonSuperAdminWithOperationLog(ctx context.Context, userID uint, log *governancedomain.OperationLog) error {
	r.mu.Lock()
	current, ok := r.users[userID]
	if !ok {
		r.mu.Unlock()
		return domain.ErrUserNotFound
	}
	if current.Role == domain.RoleSuperAdmin {
		r.mu.Unlock()
		return domain.ErrPermissionDenied
	}
	delete(r.users, userID)
	delete(r.byID, current.Email)
	r.mu.Unlock()
	return r.operationLogs.Create(ctx, log)
}

func (r *mockUserRepo) ResolveBulkUserIDs(_ context.Context, ids []uint, filter domain.UserListFilter) ([]uint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	idSet := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		idSet[id] = struct{}{}
	}
	var out []uint
	for _, u := range r.users {
		if u.Role == domain.RoleSuperAdmin {
			continue
		}
		if len(ids) > 0 {
			if _, ok := idSet[u.ID]; !ok {
				continue
			}
		} else if !mockUserMatchesFilter(u, filter) {
			continue
		}
		out = append(out, u.ID)
	}
	return out, nil
}

func (r *mockUserRepo) BatchSetEnabledNonSuperAdmin(_ context.Context, ids []uint, enabled bool) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var affected int64
	for _, id := range ids {
		u, ok := r.users[id]
		if !ok || u.Role == domain.RoleSuperAdmin || u.Enabled == enabled {
			continue
		}
		u.Enabled = enabled
		if !enabled {
			u.TokenVersion++
		}
		affected++
	}
	return affected, nil
}

func (r *mockUserRepo) BatchBumpTokenVersionNonSuperAdmin(_ context.Context, ids []uint) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var affected int64
	for _, id := range ids {
		u, ok := r.users[id]
		if !ok || u.Role == domain.RoleSuperAdmin {
			continue
		}
		u.TokenVersion++
		affected++
	}
	return affected, nil
}

func (r *mockUserRepo) BatchDeleteNonSuperAdmin(_ context.Context, ids []uint) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var affected int64
	for _, id := range ids {
		u, ok := r.users[id]
		if !ok || u.Role == domain.RoleSuperAdmin {
			continue
		}
		delete(r.users, id)
		delete(r.byID, u.Email)
		affected++
	}
	return affected, nil
}

func (r *mockUserRepo) FacetsByFilter(_ context.Context, filter domain.UserListFilter, groups []domain.UserGroup) (*domain.UserFacets, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	role := map[string]int64{}
	status := domain.StatusFacet{}
	groupCounts := map[uint]int64{}
	for _, u := range r.users {
		roleFilter := filter
		roleFilter.Role = nil
		if mockUserMatchesFilter(u, roleFilter) {
			role[u.Role.String()]++
			role["all"]++
		}
		statusFilter := filter
		statusFilter.Enabled = nil
		if mockUserMatchesFilter(u, statusFilter) {
			if u.Enabled {
				status.Enabled++
			} else {
				status.Disabled++
			}
		}
		groupFilter := filter
		groupFilter.UserGroupID = nil
		if mockUserMatchesFilter(u, groupFilter) {
			groupCounts[u.UserGroupID]++
		}
	}
	status.All = status.Enabled + status.Disabled
	groupFacets := make([]domain.GroupFacet, len(groups))
	for i, g := range groups {
		groupFacets[i] = domain.GroupFacet{ID: g.ID, Code: g.Code, Name: g.Name, Count: groupCounts[g.ID]}
	}
	return &domain.UserFacets{Role: role, Status: status, Group: groupFacets}, nil
}

func (r *mockUserRepo) FindInviterID(_ context.Context, _ uint) (*uint, error) {
	return nil, nil
}

func (r *mockUserRepo) ListInviteeIDs(_ context.Context, _ uint) ([]uint, error) {
	return nil, nil
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

func (r *mockUserRepo) LookupUserSummaries(_ context.Context, ids []uint) (map[uint]domain.UserSummary, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[uint]domain.UserSummary, len(ids))
	for _, id := range ids {
		u, ok := r.users[id]
		if !ok {
			continue
		}
		groupName := ""
		if g, ok := r.userGroups[u.UserGroupID]; ok {
			groupName = g.Name
		}
		out[id] = domain.UserSummary{ID: u.ID, Email: u.Email, Nickname: u.Nickname, Role: u.Role.String(), GroupID: u.UserGroupID, GroupName: groupName}
	}
	return out, nil
}

func (r *mockUserRepo) matchInviteLocked(invite *domain.Invite, f domain.InviteListFilter) bool {
	if f.Kind != "" && invite.Kind != f.Kind {
		return false
	}
	if f.Enabled != nil && invite.Enabled != *f.Enabled {
		return false
	}
	var owner *domain.User
	if invite.CreatedByUserID != nil {
		owner = r.users[*invite.CreatedByUserID]
	}
	if f.OwnerRole != nil && (owner == nil || owner.Role != *f.OwnerRole) {
		return false
	}
	if f.OwnerGroupID != nil && (owner == nil || owner.UserGroupID != *f.OwnerGroupID) {
		return false
	}
	if s := strings.TrimSpace(f.Search); s != "" {
		if strings.Contains(invite.Code, s) {
			return true
		}
		if owner == nil {
			return false
		}
		return strings.Contains(owner.Email, s) || strings.Contains(owner.Nickname, s) || strings.Contains(fmt.Sprintf("%d", owner.ID), s)
	}
	return true
}

func (r *mockUserRepo) ListInvitesByFilter(_ context.Context, filter domain.InviteListFilter, offset, limit int) ([]domain.Invite, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []domain.Invite
	for _, invite := range r.invites {
		if r.matchInviteLocked(invite, filter) {
			result = append(result, *invite)
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

func (r *mockUserRepo) CountInvitesByFilter(_ context.Context, filter domain.InviteListFilter) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var count int64
	for _, invite := range r.invites {
		if r.matchInviteLocked(invite, filter) {
			count++
		}
	}
	return count, nil
}

func (r *mockUserRepo) InviteFacetsByFilter(_ context.Context, kind domain.InviteKind) (*domain.InviteFacets, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	facets := &domain.InviteFacets{}
	groups := map[uint]*domain.GroupFacet{}
	for _, invite := range r.invites {
		if kind != "" && invite.Kind != kind {
			continue
		}
		facets.Role.All++
		if invite.Enabled {
			facets.Enabled.Enabled++
		} else {
			facets.Enabled.Disabled++
		}
		if invite.CreatedByUserID == nil {
			continue
		}
		owner := r.users[*invite.CreatedByUserID]
		if owner == nil {
			continue
		}
		switch owner.Role {
		case domain.RoleUser:
			facets.Role.User++
		case domain.RoleSupplier:
			facets.Role.Supplier++
		case domain.RoleAdmin:
			facets.Role.Admin++
		case domain.RoleSuperAdmin:
			facets.Role.SuperAdmin++
		}
		if g, ok := groups[owner.UserGroupID]; ok {
			g.Count++
		} else {
			name := ""
			if grp, ok := r.userGroups[owner.UserGroupID]; ok {
				name = grp.Name
			}
			groups[owner.UserGroupID] = &domain.GroupFacet{ID: owner.UserGroupID, Name: name, Count: 1}
		}
	}
	facets.Enabled.All = facets.Enabled.Enabled + facets.Enabled.Disabled
	for _, g := range groups {
		facets.Group = append(facets.Group, *g)
	}
	return facets, nil
}

func (r *mockUserRepo) ResolveInviteCodesByFilter(_ context.Context, filter domain.InviteListFilter) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var codes []string
	for _, invite := range r.invites {
		if r.matchInviteLocked(invite, filter) {
			codes = append(codes, invite.Code)
		}
	}
	return codes, nil
}

func (r *mockUserRepo) BatchSetInviteEnabled(_ context.Context, codes []string, enabled bool) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var affected int64
	for _, code := range codes {
		if invite, ok := r.invites[code]; ok && invite.Enabled != enabled {
			invite.Enabled = enabled
			affected++
		}
	}
	return affected, nil
}

func (r *mockUserRepo) ListInviteUses(_ context.Context, _ string, _ int) ([]domain.InviteUse, error) {
	return nil, nil
}

func (r *mockUserRepo) CreateInvitesBatch(ctx context.Context, invites []*domain.Invite, createdByUserID uint, log *governancedomain.OperationLog) error {
	r.mu.Lock()
	for _, invite := range invites {
		if _, exists := r.invites[invite.Code]; exists {
			r.mu.Unlock()
			return domain.ErrInviteAlreadyExists
		}
	}
	for _, invite := range invites {
		cp := *invite
		cp.Kind = domain.InviteKindAdmin
		cp.CreatedByUserID = &createdByUserID
		r.invites[invite.Code] = &cp
	}
	r.mu.Unlock()
	return r.operationLogs.Create(ctx, log)
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

func (r *mockUserRepo) ReplaceUserPermissionPoliciesGuarded(_ context.Context, userID uint, policies []domain.PermissionPolicy, allowSensitive bool) ([]domain.PermissionPolicy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	user, ok := r.users[userID]
	if !ok {
		return nil, domain.ErrUserNotFound
	}
	if user.Role == domain.RoleSuperAdmin {
		return nil, domain.ErrPermissionDenied
	}
	previous := append([]domain.PermissionPolicy(nil), r.policies[userID]...)
	if !allowSensitive && (mockPoliciesContainSensitive(previous) || mockPoliciesContainSensitive(policies)) {
		return nil, domain.ErrPermissionDenied
	}
	r.policies[userID] = append([]domain.PermissionPolicy(nil), policies...)
	return previous, nil
}

func mockPoliciesContainSensitive(policies []domain.PermissionPolicy) bool {
	for _, policy := range policies {
		if policy.Action == "sensitive" {
			return true
		}
	}
	return false
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

type permissionMapChecker map[string]bool

func (c permissionMapChecker) Check(_ context.Context, _ uint, _ domain.Role, resource, action string) (bool, error) {
	return c[resource+":"+action], nil
}

func (permissionMapChecker) Reload(context.Context) error { return nil }

type mockEmailCodeStore struct {
	mu         sync.Mutex
	codes      map[string]string
	claims     map[string]string
	cooldowns  map[string]bool
	claimErr   error
	commitErr  error
	restoreErr error
}

func newMockEmailCodeStore() *mockEmailCodeStore {
	return &mockEmailCodeStore{codes: make(map[string]string), claims: make(map[string]string), cooldowns: make(map[string]bool)}
}

func (s *mockEmailCodeStore) StartCooldown(_ context.Context, key string, seconds int) (bool, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cooldowns[key] {
		return false, seconds, nil
	}
	s.cooldowns[key] = true
	return true, 0, nil
}

func (s *mockEmailCodeStore) ClearCooldown(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cooldowns, key)
	return nil
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

func (s *mockEmailCodeStore) Claim(_ context.Context, key, expected, claimToken string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.codes[key] != expected {
		return false, nil
	}
	delete(s.codes, key)
	s.claims[key] = claimToken
	if s.claimErr != nil {
		return false, s.claimErr
	}
	return true, nil
}

func (s *mockEmailCodeStore) Commit(_ context.Context, key, claimToken string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claims[key] != claimToken {
		return false, nil
	}
	delete(s.claims, key)
	if s.commitErr != nil {
		return false, s.commitErr
	}
	return true, nil
}

func (s *mockEmailCodeStore) Restore(_ context.Context, key, claimToken, code string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claims[key] != claimToken {
		return false, nil
	}
	if s.restoreErr != nil {
		return false, s.restoreErr
	}
	delete(s.claims, key)
	s.codes[key] = code
	return true, nil
}

func (s *mockEmailCodeStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		AdminUseCase:               app.NewAdminUseCase(userRepo, sessionStore, userRepo, userRepo, hasher, userRepo.operationLogs),
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

func TestPostEmailCode_ThrottlesResendWithRetryAfter(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)

	send := func() *httptest.ResponseRecorder {
		captchaID := seedCaptcha(t, h, "1234")
		body := `{"email":"user@test.com","captchaId":"` + captchaID + `","captchaAnswer":"1234"}`
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/v1/email/code", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}

	require.Equal(t, http.StatusNoContent, send().Code)

	second := send()
	require.Equal(t, http.StatusTooManyRequests, second.Code)
	require.Equal(t, fmt.Sprintf("%d", app.EmailCodeResendGapSeconds), second.Header().Get("Retry-After"))
}

// A repeated password-reset request must throttle the same way for an unknown
// email as for a registered one, so the response cannot be used to probe whether
// an account exists.
func TestPasswordResetRequestThrottlesUnknownEmailIdentically(t *testing.T) {
	h := newTestHandler()

	cap1 := seedCaptcha(t, h, "1234")
	_, err := h.module.PasswordResetUseCase.Request(context.Background(), "ghost@test.com", cap1, "1234")
	require.NoError(t, err)

	cap2 := seedCaptcha(t, h, "1234")
	_, err = h.module.PasswordResetUseCase.Request(context.Background(), "ghost@test.com", cap2, "1234")
	require.ErrorIs(t, err, domain.ErrEmailCodeThrottled)
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

func TestPostRegister_ExistingEmailWithValidEmailCodeDoesNotRevealCode(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	seedUser(t, h, "user@test.com")

	code := requestEmailCode(t, h, r, "user@test.com", "1234")
	register := func(submittedCode string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"email":"user@test.com","password":"User123!","code":%q}`, submittedCode)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/v1/users", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	wrong := register("wrong")
	correct := register(code)
	require.Equal(t, http.StatusUnprocessableEntity, wrong.Code)
	require.Equal(t, wrong.Code, correct.Code)
	require.Contains(t, wrong.Body.String(), "Verification code is incorrect or expired")
	require.Contains(t, correct.Body.String(), "Verification code is incorrect or expired")
	require.NotContains(t, correct.Body.String(), "Email already exists")
	require.Equal(t, code, h.module.EmailCodeStore.(*mockEmailCodeStore).firstCode())

	body := fmt.Sprintf(`{"email":"user@test.com","newPassword":"Reset123!","code":%q}`, code)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/password/reset", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
}

func TestPostRegister_RestoreFailureReturnsServerError(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	seedUser(t, h, "user@test.com")

	code := requestEmailCode(t, h, r, "user@test.com", "1234")
	h.module.EmailCodeStore.(*mockEmailCodeStore).restoreErr = errors.New("redis restore failed")
	body := fmt.Sprintf(`{"email":"user@test.com","password":"User123!","code":%q}`, code)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.NotContains(t, w.Body.String(), "Verification code is incorrect")
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

func TestGetMeIncludesAdminNavigationAndOperationPermissions(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	seedAdminSession(t, h, "admin-session")

	w := httptest.NewRecorder()
	req, err := http.NewRequest("GET", "/v1/me", nil)
	require.NoError(t, err)
	addAuthenticatedRequest(req, "admin-session")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	for _, permission := range []string{
		"billing:wallet:read",
		"billing:wallet:operate",
		"billing:card:read",
		"trade:order:read",
		"trade:order:operate",
		"iam:permission:sensitive",
	} {
		require.Contains(t, w.Body.String(), `"`+permission+`"`)
	}
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

func TestPatchAdminUserRequiresSensitivePermissionForSuperAdminChanges(t *testing.T) {
	h := newTestHandler()
	h.module.PermissionChecker = permissionMapChecker{
		"iam:user:write":           true,
		"iam:permission:sensitive": false,
	}
	r := setupTestRouterWithHandler(h)
	seedAdminSession(t, h, "admin-session")
	target := seedUser(t, h, "sensitive-role-target@test.com")

	w := httptest.NewRecorder()
	req, err := http.NewRequest(
		"PATCH",
		fmt.Sprintf("/v1/admin/users/%d", target.ID),
		strings.NewReader(`{"role":"super_admin"}`),
	)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	addAuthenticatedRequest(req, "admin-session")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	stored, err := testRepo(h).FindByID(context.Background(), target.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RoleUser, stored.Role)
}

func TestPatchAdminUserAllowsOrdinaryChangesWithoutSensitivePermission(t *testing.T) {
	h := newTestHandler()
	h.module.PermissionChecker = permissionMapChecker{
		"iam:user:write": true,
	}
	r := setupTestRouterWithHandler(h)
	seedAdminSession(t, h, "admin-session")
	target := seedUser(t, h, "ordinary-role-target@test.com")

	w := httptest.NewRecorder()
	req, err := http.NewRequest(
		"PATCH",
		fmt.Sprintf("/v1/admin/users/%d", target.ID),
		strings.NewReader(`{"role":"admin"}`),
	)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	addAuthenticatedRequest(req, "admin-session")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	stored, err := testRepo(h).FindByID(context.Background(), target.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RoleAdmin, stored.Role)
}

func TestPatchAdminUserAllowsSuperAdminChangesWithSensitivePermission(t *testing.T) {
	h := newTestHandler()
	h.module.PermissionChecker = permissionMapChecker{
		"iam:user:write":           true,
		"iam:permission:sensitive": true,
	}
	r := setupTestRouterWithHandler(h)
	seedAdminSession(t, h, "admin-session")
	target := seedUser(t, h, "authorized-sensitive-role@test.com")

	w := httptest.NewRecorder()
	req, err := http.NewRequest(
		"PATCH",
		fmt.Sprintf("/v1/admin/users/%d", target.ID),
		strings.NewReader(`{"role":"super_admin"}`),
	)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	addAuthenticatedRequest(req, "admin-session")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	stored, err := testRepo(h).FindByID(context.Background(), target.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RoleSuperAdmin, stored.Role)
}

func TestPatchAdminUserCannotModifyExistingSuperAdminEvenWithSensitivePermission(t *testing.T) {
	h := newTestHandler()
	h.module.PermissionChecker = permissionMapChecker{
		"iam:user:write":           true,
		"iam:permission:sensitive": true,
	}
	r := setupTestRouterWithHandler(h)
	seedAdminSession(t, h, "admin-session")
	target := seedUser(t, h, "protected-super-admin@test.com")
	target.Role = domain.RoleSuperAdmin
	require.NoError(t, testRepo(h).Update(context.Background(), target))

	w := httptest.NewRecorder()
	req, err := http.NewRequest(
		"PATCH",
		fmt.Sprintf("/v1/admin/users/%d", target.ID),
		strings.NewReader(`{"role":"admin"}`),
	)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	addAuthenticatedRequest(req, "admin-session")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	stored, err := testRepo(h).FindByID(context.Background(), target.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RoleSuperAdmin, stored.Role)
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

func TestPostAdminRevokeSessionsRejectsSuperAdmin(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	seedAdminSession(t, h, "admin-session")
	target := seedUser(t, h, "protected-revoke-super-admin@test.com")
	target.Role = domain.RoleSuperAdmin
	require.NoError(t, testRepo(h).Update(context.Background(), target))

	w := httptest.NewRecorder()
	req, err := http.NewRequest("POST", fmt.Sprintf("/v1/admin/users/%d/sessions/revoke", target.ID), nil)
	require.NoError(t, err)
	addAuthenticatedRequest(req, "admin-session")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	stored, err := testRepo(h).FindByID(context.Background(), target.ID)
	require.NoError(t, err)
	require.Zero(t, stored.TokenVersion)
}

func TestPostAdminUsersBulkDisableExcludesSuperAdminAndCountsSkipped(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	admin := seedAdminSession(t, h, "admin-session")
	active := seedUser(t, h, "bulk-active@test.com")
	already := seedUser(t, h, "bulk-already@test.com")
	already.Enabled = false
	require.NoError(t, testRepo(h).Update(context.Background(), already))

	body := fmt.Sprintf(`{"selection":{"mode":"ids","userIds":[%d,%d,%d]}}`, active.ID, already.ID, admin.ID)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/admin/users/disable", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	addAuthenticatedRequest(req, "admin-session")
	r.ServeHTTP(w, req)

	// requested counts all 3 ids; only the enabled non-super-admin flips
	// (affected=1); the already-disabled user and the super_admin are skipped.
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.JSONEq(t, `{"requested":3,"affected":1,"skipped":2}`, w.Body.String())

	updated, err := testRepo(h).FindByID(context.Background(), active.ID)
	require.NoError(t, err)
	require.False(t, updated.Enabled)
	require.Equal(t, 1, updated.TokenVersion)

	protectedAdmin, err := testRepo(h).FindByID(context.Background(), admin.ID)
	require.NoError(t, err)
	require.True(t, protectedAdmin.Enabled)
	require.Zero(t, protectedAdmin.TokenVersion)

	log := testRepo(h).lastLog()
	require.NotNil(t, log)
	require.Equal(t, "iam.user.bulk.disable", log.OperationType)
	require.Equal(t, "success", log.Result)
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

func TestPutAdminUserPermissionsRequiresSensitivePermission(t *testing.T) {
	h := newTestHandler()
	h.module.PermissionChecker = permissionMapChecker{
		"iam:permission:write":     true,
		"iam:permission:sensitive": false,
	}
	r := setupTestRouterWithHandler(h)
	seedAdminSession(t, h, "admin-session")
	target := seedUser(t, h, "sensitive-policy-target@test.com")

	body := `{"policies":[{"resource":"iam:permission","action":"sensitive","effect":"allow"}]}`
	w := httptest.NewRecorder()
	req, err := http.NewRequest(
		"PUT",
		fmt.Sprintf("/v1/admin/users/%d/permissions", target.ID),
		strings.NewReader(body),
	)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	addAuthenticatedRequest(req, "admin-session")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	policies, err := testRepo(h).ListUserPermissionPolicies(context.Background(), target.ID)
	require.NoError(t, err)
	require.Empty(t, policies)
}

func TestPutAdminUserPermissionsCannotRemoveSensitivePolicyWithoutPermission(t *testing.T) {
	h := newTestHandler()
	h.module.PermissionChecker = permissionMapChecker{
		"iam:permission:write": true,
	}
	r := setupTestRouterWithHandler(h)
	seedAdminSession(t, h, "admin-session")
	target := seedUser(t, h, "sensitive-policy-removal@test.com")
	testRepo(h).policies[target.ID] = []domain.PermissionPolicy{{
		Resource: "iam:permission",
		Action:   "sensitive",
		Effect:   "allow",
	}}

	w := httptest.NewRecorder()
	req, err := http.NewRequest(
		"PUT",
		fmt.Sprintf("/v1/admin/users/%d/permissions", target.ID),
		strings.NewReader(`{"policies":[]}`),
	)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	addAuthenticatedRequest(req, "admin-session")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	policies, err := testRepo(h).ListUserPermissionPolicies(context.Background(), target.ID)
	require.NoError(t, err)
	require.Equal(t, []domain.PermissionPolicy{{
		Resource: "iam:permission",
		Action:   "sensitive",
		Effect:   "allow",
	}}, policies)
}

func TestPutAdminUserPermissionsAllowsSensitivePolicyWithPermission(t *testing.T) {
	h := newTestHandler()
	h.module.PermissionChecker = permissionMapChecker{
		"iam:permission:write":     true,
		"iam:permission:sensitive": true,
	}
	r := setupTestRouterWithHandler(h)
	seedAdminSession(t, h, "admin-session")
	target := seedUser(t, h, "authorized-sensitive-policy@test.com")

	body := `{"policies":[{"resource":"iam:permission","action":"sensitive","effect":"allow"}]}`
	w := httptest.NewRecorder()
	req, err := http.NewRequest(
		"PUT",
		fmt.Sprintf("/v1/admin/users/%d/permissions", target.ID),
		strings.NewReader(body),
	)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	addAuthenticatedRequest(req, "admin-session")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	policies, err := testRepo(h).ListUserPermissionPolicies(context.Background(), target.ID)
	require.NoError(t, err)
	require.Equal(t, []domain.PermissionPolicy{{
		Resource: "iam:permission",
		Action:   "sensitive",
		Effect:   "allow",
	}}, policies)
}

func TestPutAdminUserPermissionsRejectsExistingSuperAdmin(t *testing.T) {
	h := newTestHandler()
	h.module.PermissionChecker = permissionMapChecker{
		"iam:permission:write":     true,
		"iam:permission:sensitive": true,
	}
	r := setupTestRouterWithHandler(h)
	seedAdminSession(t, h, "admin-session")
	target := seedUser(t, h, "protected-permission-super-admin@test.com")
	target.Role = domain.RoleSuperAdmin
	require.NoError(t, testRepo(h).Update(context.Background(), target))

	w := httptest.NewRecorder()
	req, err := http.NewRequest(
		"PUT",
		fmt.Sprintf("/v1/admin/users/%d/permissions", target.ID),
		strings.NewReader(`{"policies":[]}`),
	)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	addAuthenticatedRequest(req, "admin-session")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
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

// Regression: toggling enabled sends {"enabled":...} with no expireAt key and
// must not wipe an existing expiry. null clears; a value sets.
func TestPatchAdminInviteExpireAtTriState(t *testing.T) {
	future := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	harness := func() (*IAMHandler, *gin.Engine) {
		h := newTestHandler()
		r := setupTestRouterWithHandler(h)
		seedAdminSession(t, h, "admin-session")
		testRepo(h).invites["INV-EXP"] = &domain.Invite{Code: "INV-EXP", Kind: domain.InviteKindAdmin, Enabled: true, MaxUse: 5, ExpireAt: &future}
		return h, r
	}
	patch := func(r *gin.Engine, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("PATCH", "/v1/admin/invites/INV-EXP", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		addAuthenticatedRequest(req, "admin-session")
		r.ServeHTTP(w, req)
		return w
	}

	h, r := harness()
	w := patch(r, `{"enabled":false}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	got := testRepo(h).invites["INV-EXP"]
	require.False(t, got.Enabled)
	require.NotNil(t, got.ExpireAt, "toggling enabled must not wipe the expiry")
	require.WithinDuration(t, future, *got.ExpireAt, time.Second)

	h, r = harness()
	w = patch(r, `{"expireAt":null}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Nil(t, testRepo(h).invites["INV-EXP"].ExpireAt, "explicit null clears the expiry")

	next := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	h, r = harness()
	w = patch(r, fmt.Sprintf(`{"expireAt":%q}`, next.Format(time.RFC3339)))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotNil(t, testRepo(h).invites["INV-EXP"].ExpireAt)
	require.WithinDuration(t, next, *testRepo(h).invites["INV-EXP"].ExpireAt, time.Second)
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

func TestPostPasswordResetIgnoresSessionCleanupFailureAfterPasswordUpdate(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	user := seedUser(t, h, "user@test.com")

	captchaID := seedCaptcha(t, h, "1234")
	_, err := h.module.PasswordResetUseCase.Request(context.Background(), "USER@Test.COM", captchaID, "1234")
	require.NoError(t, err)
	code := h.module.EmailCodeStore.(*mockEmailCodeStore).firstCode()
	require.NotEmpty(t, code)

	h.module.SessionStore.(*mockSessionStore).deleteByUserIDErr = errors.New("redis session cleanup failed")
	h.module.EmailCodeStore.(*mockEmailCodeStore).commitErr = errors.New("redis commit result unavailable")

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
	assert.Contains(t, w.Body.String(), `"fields":{"body":"Invalid request body."}`)
	assert.NotContains(t, w.Body.String(), "RegisterRequest")
	assert.NotContains(t, w.Body.String(), "validation failed")
}

func TestPostRegister_MalformedJSONDoesNotExposeParserError(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/users", strings.NewReader(`{"email":`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), `"fields":{"body":"Invalid request body."}`)
	require.NotContains(t, w.Body.String(), "unexpected EOF")
}

type fakeAbuseLimiter struct {
	captchaRetry      int
	loginRetry        int
	resetRetry        int
	loginTaken        int
	loginCanceled     int
	loginCompleted    int
	registerRetry     int
	registerTaken     int
	registerCanceled  int
	registerCompleted int
	resetTaken        int
	resetCanceled     int
	resetCompleted    int
	resetCleared      int
	resetClearErr     error
}

func (l *fakeAbuseLimiter) HitCaptcha(context.Context, string) (int, error) {
	return l.captchaRetry, nil
}

func (l *fakeAbuseLimiter) TakeLogin(context.Context, string, string) (int, error) {
	l.loginTaken++
	return l.loginRetry, nil
}

func (l *fakeAbuseLimiter) CancelLogin(context.Context, string, string) error {
	l.loginCanceled++
	return nil
}

func (l *fakeAbuseLimiter) CompleteLogin(context.Context, string, string) error {
	l.loginCompleted++
	return nil
}

func (l *fakeAbuseLimiter) TakeRegistration(context.Context, string, string) (int, error) {
	l.registerTaken++
	return l.registerRetry, nil
}

func (l *fakeAbuseLimiter) CancelRegistration(context.Context, string, string) error {
	l.registerCanceled++
	return nil
}

func (l *fakeAbuseLimiter) CompleteRegistration(context.Context, string, string) error {
	l.registerCompleted++
	return nil
}

func (l *fakeAbuseLimiter) TakePasswordReset(context.Context, string, string) (int, error) {
	l.resetTaken++
	return l.resetRetry, nil
}

func (l *fakeAbuseLimiter) CancelPasswordReset(context.Context, string, string) error {
	l.resetCanceled++
	return nil
}

func (l *fakeAbuseLimiter) CompletePasswordReset(context.Context, string, string) error {
	l.resetCompleted++
	return nil
}

func (l *fakeAbuseLimiter) ClearEmailCodeFailures(context.Context, string) error {
	l.resetCleared++
	return l.resetClearErr
}

func TestPostLoginAbuseLimiterCountsOnlyCredentialFailuresAndClearsOnSuccess(t *testing.T) {
	h := newTestHandler()
	limiter := &fakeAbuseLimiter{}
	h.module.AbuseLimiter = limiter
	r := setupTestRouterWithHandler(h)

	body := `{"email":"admin@test.com","password":"Admin123!"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/activation", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	login := func(email, password, captchaAnswer, submittedAnswer string) *httptest.ResponseRecorder {
		captchaID := seedCaptcha(t, h, captchaAnswer)
		body := fmt.Sprintf(`{"email":%q,"password":%q,"captchaId":%q,"captchaAnswer":%q}`, email, password, captchaID, submittedAnswer)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/v1/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}

	require.Equal(t, http.StatusUnprocessableEntity, login("admin@test.com", "Admin123!", "1234", "wrong").Code)
	require.Zero(t, limiter.loginTaken, "captcha failures must not consume the account failure budget")

	wrongPassword := login("admin@test.com", "wrong", "1234", "1234")
	require.Equal(t, http.StatusUnprocessableEntity, wrongPassword.Code)
	require.Contains(t, wrongPassword.Body.String(), "Account or password is incorrect")

	unknownAccount := login("ghost@test.com", "wrong", "1234", "1234")
	require.Equal(t, http.StatusUnprocessableEntity, unknownAccount.Code)
	require.Contains(t, unknownAccount.Body.String(), "Account or password is incorrect")
	require.Equal(t, 2, limiter.loginTaken)

	require.Equal(t, http.StatusOK, login("admin@test.com", "Admin123!", "1234", "1234").Code)
	require.Equal(t, 3, limiter.loginTaken)
	require.Equal(t, 1, limiter.loginCompleted)
}

func TestIAMAbuseLimiterReturnsRetryAfter(t *testing.T) {
	h := newTestHandler()
	limiter := &fakeAbuseLimiter{captchaRetry: 17, loginRetry: 42, registerRetry: 58, resetRetry: 73}
	h.module.AbuseLimiter = limiter
	r := setupTestRouterWithHandler(h)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/captchas", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, "17", w.Header().Get("Retry-After"))

	captchaID := seedCaptcha(t, h, "1234")
	body := fmt.Sprintf(`{"email":"user@test.com","password":"wrong","captchaId":%q,"captchaAnswer":"1234"}`, captchaID)
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/v1/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, "42", w.Header().Get("Retry-After"))

	body = `{"email":"user@test.com","password":"User123!","code":"123456"}`
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/v1/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, "58", w.Header().Get("Retry-After"))

	body = `{"email":"user@test.com","code":"123456","newPassword":"NewPass123!"}`
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/v1/password/reset", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, "73", w.Header().Get("Retry-After"))
}

func TestPostRegisterAbuseLimiterCountsCodeFailuresAndClearsOnSuccess(t *testing.T) {
	h := newTestHandler()
	limiter := &fakeAbuseLimiter{}
	h.module.AbuseLimiter = limiter
	r := setupTestRouterWithHandler(h)
	code := requestEmailCode(t, h, r, "new@test.com", "1234")

	register := func(code string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"email":"new@test.com","password":"User123!","code":%q}`, code)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/v1/users", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}

	require.Equal(t, http.StatusUnprocessableEntity, register("wrong").Code)
	require.Equal(t, 1, limiter.registerTaken)
	require.Zero(t, limiter.registerCanceled)
	require.Equal(t, http.StatusCreated, register(code).Code)
	require.Equal(t, 2, limiter.registerTaken)
	require.Equal(t, 1, limiter.registerCompleted)
}

func TestPostPasswordResetAbuseLimiterCountsInvalidCodeAndClearsOnSuccess(t *testing.T) {
	h := newTestHandler()
	limiter := &fakeAbuseLimiter{}
	h.module.AbuseLimiter = limiter
	r := setupTestRouterWithHandler(h)
	seedUser(t, h, "user@test.com")

	captchaID := seedCaptcha(t, h, "1234")
	_, err := h.module.PasswordResetUseCase.Request(context.Background(), "user@test.com", captchaID, "1234")
	require.NoError(t, err)
	code := h.module.EmailCodeStore.(*mockEmailCodeStore).firstCode()
	require.NotEmpty(t, code)

	reset := func(code string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"email":"user@test.com","code":%q,"newPassword":"NewPass123!"}`, code)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/v1/password/reset", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}

	require.Equal(t, http.StatusUnprocessableEntity, reset("wrong").Code)
	require.Equal(t, 1, limiter.resetTaken)
	require.Equal(t, http.StatusNoContent, reset(code).Code)
	require.Equal(t, 2, limiter.resetTaken)
	require.Equal(t, 1, limiter.resetCompleted)
}

func TestPasswordResetRequestClearsFailuresOnlyForNewCodeGeneration(t *testing.T) {
	h := newTestHandler()
	limiter := &fakeAbuseLimiter{}
	h.module.AbuseLimiter = limiter
	r := setupTestRouterWithHandler(h)
	seedUser(t, h, "user@test.com")

	request := func() *httptest.ResponseRecorder {
		captchaID := seedCaptcha(t, h, "1234")
		body := fmt.Sprintf(`{"email":"user@test.com","captchaId":%q,"captchaAnswer":"1234"}`, captchaID)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/v1/password/reset/request", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}

	require.Equal(t, http.StatusNoContent, request().Code)
	require.Equal(t, 1, limiter.resetCleared)

	store := h.module.EmailCodeStore.(*mockEmailCodeStore)
	store.mu.Lock()
	store.cooldowns = make(map[string]bool)
	store.mu.Unlock()
	require.Equal(t, http.StatusNoContent, request().Code)
	require.Equal(t, 1, limiter.resetCleared, "resending the same code must not reset its failure budget")
}

func TestUnknownPasswordResetRequestCreatesDummyGeneration(t *testing.T) {
	h := newTestHandler()
	limiter := &fakeAbuseLimiter{}
	h.module.AbuseLimiter = limiter
	r := setupTestRouterWithHandler(h)
	captchaID := seedCaptcha(t, h, "1234")
	body := fmt.Sprintf(`{"email":"ghost@test.com","captchaId":%q,"captchaAnswer":"1234"}`, captchaID)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/password/reset/request", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNoContent, w.Code)
	require.Equal(t, 1, limiter.resetCleared)
	require.Equal(t, 1, h.module.EmailCodeStore.(*mockEmailCodeStore).codeCount())
}

func TestPasswordResetRestoresClaimAfterDatabaseFailure(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	seedUser(t, h, "user@test.com")

	captchaID := seedCaptcha(t, h, "1234")
	_, err := h.module.PasswordResetUseCase.Request(context.Background(), "user@test.com", captchaID, "1234")
	require.NoError(t, err)
	store := h.module.EmailCodeStore.(*mockEmailCodeStore)
	code := store.firstCode()
	require.NotEmpty(t, code)

	testRepo(h).updatePasswordErr = errors.New("database unavailable")
	body := fmt.Sprintf(`{"email":"user@test.com","code":%q,"newPassword":"NewPass123!"}`, code)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/password/reset", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Equal(t, code, store.firstCode(), "database failure must restore the claimed code")

	testRepo(h).updatePasswordErr = nil
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/v1/password/reset", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
}

func TestPasswordReset_RestoreFailureReturnsServerError(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	user := seedUser(t, h, "user@test.com")

	captchaID := seedCaptcha(t, h, "1234")
	_, err := h.module.PasswordResetUseCase.Request(context.Background(), "user@test.com", captchaID, "1234")
	require.NoError(t, err)
	store := h.module.EmailCodeStore.(*mockEmailCodeStore)
	code := store.firstCode()
	user.Enabled = false
	store.restoreErr = errors.New("redis restore failed")

	body := fmt.Sprintf(`{"email":"user@test.com","code":%q,"newPassword":"NewPass123!"}`, code)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/password/reset", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.NotContains(t, w.Body.String(), "Verification code is incorrect")
}

func TestPasswordResetConcurrentCorrectCodeSucceedsOnce(t *testing.T) {
	h := newTestHandler()
	seedUser(t, h, "user@test.com")
	captchaID := seedCaptcha(t, h, "1234")
	_, err := h.module.PasswordResetUseCase.Request(context.Background(), "user@test.com", captchaID, "1234")
	require.NoError(t, err)
	code := h.module.EmailCodeStore.(*mockEmailCodeStore).firstCode()

	const requests = 20
	errs := make(chan error, requests)
	var wg sync.WaitGroup
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- h.module.PasswordResetUseCase.Reset(context.Background(), "user@test.com", code, "NewPass123!")
		}()
	}
	wg.Wait()
	close(errs)
	succeeded := 0
	for resetErr := range errs {
		if resetErr == nil {
			succeeded++
			continue
		}
		require.ErrorIs(t, resetErr, domain.ErrVerificationCodeIncorrect)
	}
	require.Equal(t, 1, succeeded)
}

func TestPasswordResetRestoresAmbiguousClaim(t *testing.T) {
	h := newTestHandler()
	seedUser(t, h, "user@test.com")
	captchaID := seedCaptcha(t, h, "1234")
	_, err := h.module.PasswordResetUseCase.Request(context.Background(), "user@test.com", captchaID, "1234")
	require.NoError(t, err)
	store := h.module.EmailCodeStore.(*mockEmailCodeStore)
	code := store.firstCode()
	store.claimErr = errors.New("redis connection lost after script")

	err = h.module.PasswordResetUseCase.Reset(context.Background(), "user@test.com", code, "NewPass123!")
	require.Error(t, err)
	require.Equal(t, code, store.firstCode(), "an ambiguous claim result must be restored when the claim actually ran")
}

func TestPasswordResetRequestDoesNotFailAfterCodeWasCreated(t *testing.T) {
	h := newTestHandler()
	limiter := &fakeAbuseLimiter{resetClearErr: errors.New("redis clear failed")}
	h.module.AbuseLimiter = limiter
	r := setupTestRouterWithHandler(h)
	seedUser(t, h, "user@test.com")
	captchaID := seedCaptcha(t, h, "1234")
	body := fmt.Sprintf(`{"email":"user@test.com","captchaId":%q,"captchaAnswer":"1234"}`, captchaID)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/password/reset/request", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNoContent, w.Code)
	require.NotEmpty(t, h.module.EmailCodeStore.(*mockEmailCodeStore).firstCode())
}
