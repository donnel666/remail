package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/iam/domain"
)

// AdminUseCase handles admin user management.
type AdminUseCase struct {
	repo        UserRepository
	sessions    SessionStore
	invites     InviteRepository
	permissions PermissionRepository
	hasher      Hasher
	logs        governanceapp.OperationLogPort
}

// NewAdminUseCase creates a new AdminUseCase.
func NewAdminUseCase(repo UserRepository, sessions SessionStore, invites InviteRepository, permissions PermissionRepository, hasher Hasher, logs governanceapp.OperationLogPort) *AdminUseCase {
	return &AdminUseCase{repo: repo, sessions: sessions, invites: invites, permissions: permissions, hasher: hasher, logs: logs}
}

// UserListResult contains paginated user results.
type UserListResult struct {
	Users  []domain.User      `json:"users"`
	Total  int64              `json:"total"`
	Offset int                `json:"offset"`
	Limit  int                `json:"limit"`
	Facets *domain.UserFacets `json:"facets,omitempty"`
}

// InviteListItem is an invite paired with its resolved owner (nil when the
// invite has no owner or the owner row is gone).
type InviteListItem struct {
	Invite domain.Invite
	Owner  *domain.UserSummary
}

type InviteListResult struct {
	Items  []InviteListItem     `json:"items"`
	Total  int64                `json:"total"`
	Offset int                  `json:"offset"`
	Limit  int                  `json:"limit"`
	Facets *domain.InviteFacets `json:"facets,omitempty"`
}

// InviteUseItem is a redemption fact paired with the redeeming user's summary.
type InviteUseItem struct {
	Use  domain.InviteUse
	User *domain.UserSummary
}

type CreateInviteRequest struct {
	Code     string
	Enabled  bool
	MaxUse   int
	ExpireAt *time.Time
}

// BatchCreateInviteRequest asks for count admin invites sharing maxUse/expiry,
// each with a generated unique code (optionally prefixed).
type BatchCreateInviteRequest struct {
	Count    int
	MaxUse   int
	Enabled  bool
	ExpireAt *time.Time
	Prefix   string
}

// InviteBulkSelection selects the targets of a bulk invite action, either by
// explicit codes or by a browse-list filter.
type InviteBulkSelection struct {
	Mode   string
	Codes  []string
	Filter domain.InviteListFilter
}

type UpdateInviteRequest struct {
	Enabled *bool
	MaxUse  *int
	// ExpireAt is tri-state: nil = leave unchanged, non-nil pointing at nil =
	// clear, non-nil pointing at a value = set.
	ExpireAt **time.Time
}

type CreateUserGroupRequest struct {
	Code        string
	Name        string
	Description string
	Enabled     bool
}

type UpdateUserGroupRequest struct {
	Name        *string
	Description *string
	Enabled     *bool
}

var permissionCatalog = []domain.PermissionCatalogItem{
	{Resource: "iam:user", Actions: []string{"read", "write", "operate"}},
	{Resource: "iam:user_group", Actions: []string{"read", "write"}},
	{Resource: "iam:permission", Actions: []string{"read", "write", "sensitive"}},
	{Resource: "iam:invite", Actions: []string{"read", "write", "operate"}},
	{Resource: "iam:supplier_application", Actions: []string{"read", "operate"}},
	{Resource: "system:settings", Actions: []string{"read", "write", "sensitive"}},
	{Resource: "core:resource", Actions: []string{"read", "write", "operate"}},
	{Resource: "core:project", Actions: []string{"read", "write", "operate"}},
	{Resource: "trade:order", Actions: []string{"read", "write", "operate"}},
	{Resource: "billing:wallet", Actions: []string{"read", "write", "operate", "sensitive"}},
	{Resource: "billing:card", Actions: []string{"read", "write", "operate", "sensitive"}},
	{Resource: "proxy:proxy", Actions: []string{"read", "write", "operate"}},
	{Resource: "alloc:allocation", Actions: []string{"read", "operate"}},
	{Resource: "mailmatch:message", Actions: []string{"read", "operate"}},
	{Resource: "mailtransport:binding", Actions: []string{"read", "write"}},
	{Resource: "governance:task", Actions: []string{"read"}},
	{Resource: "governance:log", Actions: []string{"read", "operate"}},
}

// ListUsers returns a paginated list of all users.
func (uc *AdminUseCase) ListUsers(ctx context.Context, filter domain.UserListFilter, offset, limit int) (*UserListResult, error) {
	filter.IDs = uniqueUserIDs(filter.IDs)
	maxLimit := 100
	if len(filter.IDs) > 0 {
		maxLimit = 1000
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if offset < 0 {
		offset = 0
	}
	filter.Search = strings.TrimSpace(filter.Search)
	if len([]rune(filter.Search)) > 120 {
		filter.Search = string([]rune(filter.Search)[:120])
	}

	total, err := uc.repo.CountByFilter(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("admin list users count: %w", err)
	}

	users, err := uc.repo.ListByFilter(ctx, filter, offset, limit)
	if err != nil {
		return nil, fmt.Errorf("admin list users: %w", err)
	}

	result := &UserListResult{
		Users:  users,
		Total:  total,
		Offset: offset,
		Limit:  limit,
	}

	// Facets power the role tabs and status/group filter counts on the browse
	// list. Skip them for `ids` batch lookups (owner-search selection).
	if len(filter.IDs) == 0 {
		groups, err := uc.repo.ListUserGroups(ctx)
		if err != nil {
			return nil, fmt.Errorf("admin list users facet groups: %w", err)
		}
		facets, err := uc.repo.FacetsByFilter(ctx, filter, groups)
		if err != nil {
			return nil, fmt.Errorf("admin list users facets: %w", err)
		}
		result.Facets = facets
	}

	return result, nil
}

func uniqueUserIDs(ids []uint) []uint {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[uint]struct{}, len(ids))
	result := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

func (uc *AdminUseCase) ListPermissions(_ context.Context) []domain.PermissionCatalogItem {
	return permissionCatalog
}

func (uc *AdminUseCase) ListUserGroups(ctx context.Context) ([]domain.UserGroup, error) {
	return uc.repo.ListUserGroups(ctx)
}

func (uc *AdminUseCase) CreateUserGroup(ctx context.Context, req CreateUserGroupRequest) (*domain.UserGroup, error) {
	group := &domain.UserGroup{
		Code:        strings.TrimSpace(req.Code),
		Name:        strings.TrimSpace(req.Name),
		Description: strings.TrimSpace(req.Description),
		Enabled:     req.Enabled,
	}
	if group.Code == "" || group.Name == "" {
		return nil, domain.ErrInvalidUserGroup
	}
	if err := uc.repo.CreateUserGroup(ctx, group); err != nil {
		return nil, err
	}
	return group, nil
}

func (uc *AdminUseCase) UpdateUserGroup(ctx context.Context, groupID uint, req UpdateUserGroupRequest) (*domain.UserGroup, error) {
	group, err := uc.repo.FindUserGroupByID(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if group == nil {
		return nil, domain.ErrInvalidUserGroup
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			return nil, domain.ErrInvalidUserGroup
		}
		group.Name = name
	}
	if req.Description != nil {
		group.Description = strings.TrimSpace(*req.Description)
	}
	if req.Enabled != nil {
		group.Enabled = *req.Enabled
	}
	if err := uc.repo.UpdateUserGroup(ctx, group); err != nil {
		return nil, err
	}
	return group, nil
}

func (uc *AdminUseCase) GetUserPermissions(ctx context.Context, targetUserID uint) ([]domain.PermissionPolicy, error) {
	user, err := uc.repo.FindByID(ctx, targetUserID)
	if err != nil {
		return nil, fmt.Errorf("admin permissions find user: %w", err)
	}
	if user == nil {
		return nil, domain.ErrUserNotFound
	}
	return uc.permissions.ListUserPermissionPolicies(ctx, targetUserID)
}

func (uc *AdminUseCase) SaveUserPermissions(ctx context.Context, operatorUserID uint, requestID, path string, targetUserID uint, policies []domain.PermissionPolicy, allowSensitive bool) error {
	for _, policy := range policies {
		if !validPermissionPolicy(policy) {
			_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.permissions.update", targetUserID, "failure", "User permission update failed."))
			return domain.ErrInvalidPermissionPolicy
		}
	}

	previous, err := uc.permissions.ReplaceUserPermissionPoliciesGuarded(ctx, targetUserID, policies, allowSensitive)
	if err != nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.permissions.update", targetUserID, "failure", "User permission update failed."))
		return fmt.Errorf("admin permissions guarded replace: %w", err)
	}
	if err := uc.permissions.Reload(ctx); err != nil {
		uc.restoreUserPermissions(ctx, targetUserID, previous)
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.permissions.update", targetUserID, "failure", "User permission update failed."))
		return fmt.Errorf("admin permissions reload: %w", err)
	}

	if err := uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.permissions.update", targetUserID, "success", "User permissions updated.")); err != nil {
		uc.restoreUserPermissions(ctx, targetUserID, previous)
		return fmt.Errorf("admin permissions operation log: %w", err)
	}
	return nil
}

func (uc *AdminUseCase) ListInvites(ctx context.Context, filter domain.InviteListFilter, offset, limit int) (*InviteListResult, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	filter.Search = clampSearch(filter.Search)

	total, err := uc.invites.CountInvitesByFilter(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("admin list invites count: %w", err)
	}
	invites, err := uc.invites.ListInvitesByFilter(ctx, filter, offset, limit)
	if err != nil {
		return nil, fmt.Errorf("admin list invites: %w", err)
	}
	owners, err := uc.lookupOwners(ctx, inviteOwnerIDs(invites))
	if err != nil {
		return nil, err
	}
	items := make([]InviteListItem, len(invites))
	for i := range invites {
		item := InviteListItem{Invite: invites[i]}
		if invites[i].CreatedByUserID != nil {
			if owner, ok := owners[*invites[i].CreatedByUserID]; ok {
				o := owner
				item.Owner = &o
			}
		}
		items[i] = item
	}
	facets, err := uc.invites.InviteFacetsByFilter(ctx, filter.Kind)
	if err != nil {
		return nil, fmt.Errorf("admin list invites facets: %w", err)
	}
	return &InviteListResult{Items: items, Total: total, Offset: offset, Limit: limit, Facets: facets}, nil
}

// BatchCreateInvites creates count admin invites with generated unique codes.
func (uc *AdminUseCase) BatchCreateInvites(ctx context.Context, operatorUserID uint, requestID, path string, req BatchCreateInviteRequest) ([]domain.Invite, error) {
	if req.Count < 1 || req.Count > 100 || req.MaxUse < 1 || (req.ExpireAt != nil && req.ExpireAt.Before(time.Now())) {
		_ = uc.logs.Create(ctx, inviteOperationLog(operatorUserID, requestID, path, "iam.invite.batch_create", "batch", "failure", "Invite batch create failed."))
		return nil, domain.ErrInviteInvalid
	}
	codes, err := generateInviteCodes(req.Prefix, req.Count)
	if err != nil {
		return nil, err
	}
	invites := make([]*domain.Invite, len(codes))
	for i, code := range codes {
		invites[i] = &domain.Invite{
			Code:     code,
			Kind:     domain.InviteKindAdmin,
			Enabled:  req.Enabled,
			MaxUse:   req.MaxUse,
			ExpireAt: req.ExpireAt,
		}
	}
	log := inviteOperationLog(operatorUserID, requestID, path, "iam.invite.batch_create", fmt.Sprintf("batch:%d", req.Count), "success", "Invites batch created.")
	if err := uc.invites.CreateInvitesBatch(ctx, invites, operatorUserID, log); err != nil {
		_ = uc.logs.Create(ctx, inviteOperationLog(operatorUserID, requestID, path, "iam.invite.batch_create", "batch", "failure", "Invite batch create failed."))
		return nil, err
	}
	out := make([]domain.Invite, len(invites))
	for i := range invites {
		out[i] = *invites[i]
	}
	return out, nil
}

// BulkSetInviteEnabled enables or disables the selected invites. It is
// idempotent: rows already in the target state are counted as skipped.
func (uc *AdminUseCase) BulkSetInviteEnabled(ctx context.Context, operatorUserID uint, requestID, path string, sel InviteBulkSelection, enabled bool) (*BulkResult, error) {
	opType := "iam.invite.bulk.enable"
	summary := "Invites enabled."
	if !enabled {
		opType = "iam.invite.bulk.disable"
		summary = "Invites disabled."
	}
	codes, requested, err := uc.resolveInviteBulkTargets(ctx, sel)
	if err != nil {
		_ = uc.logs.Create(ctx, inviteOperationLog(operatorUserID, requestID, path, opType, "bulk", "failure", "Invite bulk action failed."))
		return nil, fmt.Errorf("admin bulk set invite enabled: %w", err)
	}
	affected, err := uc.invites.BatchSetInviteEnabled(ctx, codes, enabled)
	if err != nil {
		_ = uc.logs.Create(ctx, inviteOperationLog(operatorUserID, requestID, path, opType, "bulk", "failure", "Invite bulk action failed."))
		return nil, fmt.Errorf("admin bulk set invite enabled: %w", err)
	}
	_ = uc.logs.Create(ctx, inviteOperationLog(operatorUserID, requestID, path, opType, "bulk", "success", summary))
	return bulkResult(requested, int(affected)), nil
}

// resolveInviteBulkTargets returns the target codes and the requested count. In
// ids mode requested is the raw code count; in filter mode it is the number of
// resolved codes. Empty ids mode is a no-op (never a match-all).
func (uc *AdminUseCase) resolveInviteBulkTargets(ctx context.Context, sel InviteBulkSelection) ([]string, int, error) {
	if sel.Mode == "ids" {
		return sel.Codes, len(sel.Codes), nil
	}
	codes, err := uc.invites.ResolveInviteCodesByFilter(ctx, sel.Filter)
	if err != nil {
		return nil, 0, err
	}
	return codes, len(codes), nil
}

// ListInviteUses returns one invite's redemption history, newest first, with the
// redeeming users resolved. Capped at inviteUseHistoryLimit.
func (uc *AdminUseCase) ListInviteUses(ctx context.Context, code string) ([]InviteUseItem, error) {
	code = strings.TrimSpace(code)
	invite, err := uc.invites.FindInviteByCode(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("admin invite uses find: %w", err)
	}
	if invite == nil {
		return nil, domain.ErrInviteNotFound
	}
	uses, err := uc.invites.ListInviteUses(ctx, code, inviteUseHistoryLimit)
	if err != nil {
		return nil, fmt.Errorf("admin invite uses: %w", err)
	}
	ids := make([]uint, 0, len(uses))
	for i := range uses {
		ids = append(ids, uses[i].UserID)
	}
	users, err := uc.lookupOwners(ctx, ids)
	if err != nil {
		return nil, err
	}
	items := make([]InviteUseItem, len(uses))
	for i := range uses {
		item := InviteUseItem{Use: uses[i]}
		if u, ok := users[uses[i].UserID]; ok {
			s := u
			item.User = &s
		}
		items[i] = item
	}
	return items, nil
}

const inviteUseHistoryLimit = 500

func (uc *AdminUseCase) lookupOwners(ctx context.Context, ids []uint) (map[uint]domain.UserSummary, error) {
	if len(ids) == 0 {
		return map[uint]domain.UserSummary{}, nil
	}
	owners, err := uc.repo.LookupUserSummaries(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("admin resolve invite owners: %w", err)
	}
	return owners, nil
}

func inviteOwnerIDs(invites []domain.Invite) []uint {
	seen := make(map[uint]struct{}, len(invites))
	ids := make([]uint, 0, len(invites))
	for i := range invites {
		id := invites[i].CreatedByUserID
		if id == nil || *id == 0 {
			continue
		}
		if _, ok := seen[*id]; ok {
			continue
		}
		seen[*id] = struct{}{}
		ids = append(ids, *id)
	}
	return ids
}

func clampSearch(search string) string {
	search = strings.TrimSpace(search)
	if len([]rune(search)) > 120 {
		search = string([]rune(search)[:120])
	}
	return search
}

func (uc *AdminUseCase) CreateInvite(ctx context.Context, operatorUserID uint, requestID, path string, req CreateInviteRequest) (*domain.Invite, error) {
	code := strings.TrimSpace(req.Code)
	if req.MaxUse <= 0 || (req.ExpireAt != nil && req.ExpireAt.Before(time.Now())) {
		_ = uc.logs.Create(ctx, inviteOperationLog(operatorUserID, requestID, path, "iam.invite.create", code, "failure", "Invite create failed."))
		return nil, domain.ErrInviteInvalid
	}
	if code == "" {
		codes, err := generateInviteCodes("", 1)
		if err != nil {
			return nil, err
		}
		code = codes[0]
	}
	invite := &domain.Invite{
		Code:     code,
		Kind:     domain.InviteKindAdmin,
		Enabled:  req.Enabled,
		MaxUse:   req.MaxUse,
		Used:     0,
		ExpireAt: req.ExpireAt,
	}
	if err := uc.invites.CreateInviteWithOperationLog(ctx, invite, operatorUserID, inviteOperationLog(operatorUserID, requestID, path, "iam.invite.create", code, "success", "Invite created.")); err != nil {
		_ = uc.logs.Create(ctx, inviteOperationLog(operatorUserID, requestID, path, "iam.invite.create", code, "failure", "Invite create failed."))
		return nil, err
	}
	return invite, nil
}

func (uc *AdminUseCase) UpdateInvite(ctx context.Context, operatorUserID uint, requestID, path, code string, req UpdateInviteRequest) (*domain.Invite, error) {
	invite, err := uc.invites.FindInviteByCode(ctx, strings.TrimSpace(code))
	if err != nil {
		_ = uc.logs.Create(ctx, inviteOperationLog(operatorUserID, requestID, path, "iam.invite.update", code, "failure", "Invite update failed."))
		return nil, fmt.Errorf("admin find invite: %w", err)
	}
	if invite == nil {
		_ = uc.logs.Create(ctx, inviteOperationLog(operatorUserID, requestID, path, "iam.invite.update", code, "failure", "Invite update failed."))
		return nil, domain.ErrInviteNotFound
	}
	if req.Enabled != nil {
		invite.Enabled = *req.Enabled
	}
	if req.MaxUse != nil {
		if *req.MaxUse <= 0 || *req.MaxUse < invite.Used {
			_ = uc.logs.Create(ctx, inviteOperationLog(operatorUserID, requestID, path, "iam.invite.update", code, "failure", "Invite update failed."))
			return nil, domain.ErrInviteInvalid
		}
		invite.MaxUse = *req.MaxUse
	}
	// expireAt tri-state (standard PATCH): only touch the field when the caller
	// provided it, so a toggle that omits expireAt can't wipe an existing expiry.
	if req.ExpireAt != nil {
		invite.ExpireAt = *req.ExpireAt
	}
	if err := uc.invites.UpdateInviteWithOperationLog(ctx, invite, inviteOperationLog(operatorUserID, requestID, path, "iam.invite.update", code, "success", "Invite updated.")); err != nil {
		_ = uc.logs.Create(ctx, inviteOperationLog(operatorUserID, requestID, path, "iam.invite.update", code, "failure", "Invite update failed."))
		return nil, err
	}
	return invite, nil
}

// UpdateUserRequest contains the fields that can be updated by an admin.
type UpdateUserRequest struct {
	Email       *string      `json:"email,omitempty"`
	Nickname    *string      `json:"nickname,omitempty"`
	Password    *string      `json:"password,omitempty"`
	Enabled     *bool        `json:"enabled,omitempty"`
	Role        *domain.Role `json:"role,omitempty"`
	UserGroupID *uint        `json:"userGroupId,omitempty"`
}

// CreateUserRequest contains the fields for an admin-created user.
type CreateUserRequest struct {
	Email       string
	Nickname    string
	Password    string
	Role        domain.Role
	UserGroupID uint
}

// InvitationMember is a safe view of an inviter/invitee.
type InvitationMember struct {
	User domain.User
}

// InvitationOverview is the inviter + invitees of a user.
type InvitationOverview struct {
	Inviter  *domain.User
	Invitees []domain.User
}

// CreateUser creates a managed user with an admin-set password (no email
// verification). Only iam:permission/sensitive holders may create super_admins;
// the handler passes allowSensitive.
func (uc *AdminUseCase) CreateUser(ctx context.Context, operatorUserID uint, requestID, path string, req CreateUserRequest, allowSensitive bool) (*domain.User, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !req.Role.IsValid() {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.create", 0, "failure", "User create failed."))
		return nil, domain.ErrInvalidRole
	}
	if req.Role == domain.RoleSuperAdmin && !allowSensitive {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.create", 0, "failure", "User create failed."))
		return nil, domain.ErrPermissionDenied
	}
	group, err := uc.repo.FindUserGroupByID(ctx, req.UserGroupID)
	if err != nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.create", 0, "failure", "User create failed."))
		return nil, fmt.Errorf("admin create find user group: %w", err)
	}
	if group == nil || !group.Enabled {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.create", 0, "failure", "User create failed."))
		return nil, domain.ErrInvalidUserGroup
	}
	hash, err := uc.hasher.Hash(req.Password)
	if err != nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.create", 0, "failure", "User create failed."))
		return nil, fmt.Errorf("admin create hash password: %w", err)
	}
	nickname := strings.TrimSpace(req.Nickname)
	if nickname == "" {
		nickname = email
	}
	user := &domain.User{
		Email:        email,
		PasswordHash: hash,
		Nickname:     nickname,
		Status:       domain.UserStatusActive,
		Role:         req.Role,
		UserGroupID:  req.UserGroupID,
		TokenVersion: 1,
	}
	if err := uc.repo.Create(ctx, user); err != nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.create", 0, "failure", "User create failed."))
		return nil, err
	}
	_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.create", user.ID, "success", "User created."))
	return user, nil
}

// UpdateUser updates a user's profile, access role and entitlement group.
// If the user is disabled or the password changes, increments tokenVersion and
// clears sessions (INV-I3).
func (uc *AdminUseCase) UpdateUser(ctx context.Context, operatorUserID uint, requestID, path string, targetUserID uint, req *UpdateUserRequest, allowSensitive bool) (*domain.User, error) {
	user, err := uc.repo.FindByID(ctx, targetUserID)
	if err != nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.update", targetUserID, "failure", "User access update failed."))
		return nil, fmt.Errorf("admin update find user: %w", err)
	}
	if user == nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.update", targetUserID, "failure", "User access update failed."))
		return nil, domain.ErrUserNotFound
	}
	if user.Role == domain.RoleSuperAdmin || (!allowSensitive && req.Role != nil && *req.Role == domain.RoleSuperAdmin) {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.update", targetUserID, "failure", "User access update failed."))
		return nil, domain.ErrPermissionDenied
	}

	var (
		emailUpdate     *string
		nicknameUpdate  *string
		passwordUpdate  *string
		enabledUpdate   *bool
		roleUpdate      *domain.Role
		userGroupUpdate *uint
		tokenBump       = false
	)

	if req.Email != nil {
		email := strings.ToLower(strings.TrimSpace(*req.Email))
		if email != user.Email {
			emailUpdate = &email
		}
	}
	if req.Nickname != nil {
		nickname := strings.TrimSpace(*req.Nickname)
		if nickname != user.Nickname {
			nicknameUpdate = &nickname
		}
	}
	if req.Password != nil {
		hash, err := uc.hasher.Hash(*req.Password)
		if err != nil {
			return nil, fmt.Errorf("admin update hash password: %w", err)
		}
		passwordUpdate = &hash
		tokenBump = true
	}
	if req.Enabled != nil && user.IsActive() != *req.Enabled {
		enabled := *req.Enabled
		enabledUpdate = &enabled
		if !enabled {
			tokenBump = true
		}
	}
	if req.Role != nil {
		role := *req.Role
		if !role.IsValid() {
			_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.update", targetUserID, "failure", "User access update failed."))
			return nil, domain.ErrInvalidRole
		}
		if user.Role != role {
			roleUpdate = &role
		}
	}
	if req.UserGroupID != nil {
		group, err := uc.repo.FindUserGroupByID(ctx, *req.UserGroupID)
		if err != nil {
			_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.update", targetUserID, "failure", "User access update failed."))
			return nil, fmt.Errorf("admin update find user group: %w", err)
		}
		if group == nil || !group.Enabled {
			_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.update", targetUserID, "failure", "User access update failed."))
			return nil, domain.ErrInvalidUserGroup
		}
		if user.UserGroupID != group.ID {
			groupID := group.ID
			userGroupUpdate = &groupID
		}
	}

	if emailUpdate == nil && nicknameUpdate == nil && passwordUpdate == nil && enabledUpdate == nil && roleUpdate == nil && userGroupUpdate == nil {
		if err := uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.update", targetUserID, "success", "User access settings unchanged.")); err != nil {
			return nil, fmt.Errorf("admin update unchanged log: %w", err)
		}
		return user, nil
	}

	updated, err := uc.repo.UpdateNonSuperAdminProfileWithOperationLog(
		ctx,
		targetUserID,
		emailUpdate,
		nicknameUpdate,
		passwordUpdate,
		enabledUpdate,
		roleUpdate,
		userGroupUpdate,
		tokenBump,
		adminOperationLog(operatorUserID, requestID, path, "iam.user.update", targetUserID, "success", "User access settings updated."),
	)
	if err != nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.update", targetUserID, "failure", "User access update failed."))
		return nil, fmt.Errorf("admin update user: %w", err)
	}

	if tokenBump {
		_ = uc.sessions.DeleteByUserID(ctx, targetUserID)
	}

	return updated, nil
}

// DeleteUser logically deletes a non-super-admin user and clears their sessions.
func (uc *AdminUseCase) DeleteUser(ctx context.Context, operatorUserID uint, requestID, path string, targetUserID uint) error {
	if err := uc.repo.DeleteNonSuperAdminWithOperationLog(
		ctx,
		targetUserID,
		adminOperationLog(operatorUserID, requestID, path, "iam.user.delete", targetUserID, "success", "User deleted."),
	); err != nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.delete", targetUserID, "failure", "User delete failed."))
		return fmt.Errorf("admin delete user: %w", err)
	}
	_ = uc.sessions.DeleteByUserID(ctx, targetUserID)
	return nil
}

// UserBulkSelection selects the targets of a bulk admin user action, either by
// explicit ids or by a browse-list filter.
type UserBulkSelection struct {
	Mode    string
	UserIDs []uint
	Filter  domain.UserListFilter
}

// BulkResult reports the outcome of a bulk admin user action.
type BulkResult struct {
	Requested int
	Affected  int
	Skipped   int
}

// resolveBulkTargets returns the non-super-admin target ids and the requested
// count. For ids mode requested is the raw id count; for filter mode it is the
// number of resolved targets. Empty ids mode is a no-op (never falls through to
// a match-all filter).
func (uc *AdminUseCase) resolveBulkTargets(ctx context.Context, sel UserBulkSelection) ([]uint, int, error) {
	if sel.Mode == "ids" {
		if len(sel.UserIDs) == 0 {
			return nil, 0, nil
		}
		resolved, err := uc.repo.ResolveBulkUserIDs(ctx, sel.UserIDs, domain.UserListFilter{})
		if err != nil {
			return nil, 0, err
		}
		return resolved, len(sel.UserIDs), nil
	}
	resolved, err := uc.repo.ResolveBulkUserIDs(ctx, nil, sel.Filter)
	if err != nil {
		return nil, 0, err
	}
	return resolved, len(resolved), nil
}

func (uc *AdminUseCase) clearSessions(ctx context.Context, ids []uint) {
	for _, id := range ids {
		_ = uc.sessions.DeleteByUserID(ctx, id)
	}
}

func bulkResult(requested, affected int) *BulkResult {
	return &BulkResult{Requested: requested, Affected: affected, Skipped: requested - affected}
}

// BulkSetEnabled enables or disables a batch of non-super-admin users. On
// disable it bumps tokenVersion and clears the resolved users' sessions.
func (uc *AdminUseCase) BulkSetEnabled(ctx context.Context, operatorUserID uint, requestID, path string, sel UserBulkSelection, enabled bool) (*BulkResult, error) {
	opType := "iam.user.bulk.enable"
	summary := "Users enabled."
	if !enabled {
		opType = "iam.user.bulk.disable"
		summary = "Users disabled."
	}
	ids, requested, err := uc.resolveBulkTargets(ctx, sel)
	if err != nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, opType, 0, "failure", "User bulk action failed."))
		return nil, fmt.Errorf("admin bulk set enabled: %w", err)
	}
	affected, err := uc.repo.BatchSetEnabledNonSuperAdmin(ctx, ids, enabled)
	if err != nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, opType, 0, "failure", "User bulk action failed."))
		return nil, fmt.Errorf("admin bulk set enabled: %w", err)
	}
	if !enabled {
		uc.clearSessions(ctx, ids)
	}
	_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, opType, 0, "success", summary))
	return bulkResult(requested, int(affected)), nil
}

// BulkDeleteUsers logically deletes a batch of non-super-admin users and clears the
// resolved users' sessions.
func (uc *AdminUseCase) BulkDeleteUsers(ctx context.Context, operatorUserID uint, requestID, path string, sel UserBulkSelection) (*BulkResult, error) {
	ids, requested, err := uc.resolveBulkTargets(ctx, sel)
	if err != nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.bulk.delete", 0, "failure", "User bulk delete failed."))
		return nil, fmt.Errorf("admin bulk delete users: %w", err)
	}
	affected, err := uc.repo.BatchDeleteNonSuperAdmin(ctx, ids)
	if err != nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.bulk.delete", 0, "failure", "User bulk delete failed."))
		return nil, fmt.Errorf("admin bulk delete users: %w", err)
	}
	uc.clearSessions(ctx, ids)
	_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.bulk.delete", 0, "success", "Users deleted."))
	return bulkResult(requested, int(affected)), nil
}

// BulkRevokeSessions bumps tokenVersion for a batch of non-super-admin users and
// clears the resolved users' sessions.
func (uc *AdminUseCase) BulkRevokeSessions(ctx context.Context, operatorUserID uint, requestID, path string, sel UserBulkSelection) (*BulkResult, error) {
	ids, requested, err := uc.resolveBulkTargets(ctx, sel)
	if err != nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.bulk.sessions.revoke", 0, "failure", "User bulk sessions revoke failed."))
		return nil, fmt.Errorf("admin bulk revoke sessions: %w", err)
	}
	affected, err := uc.repo.BatchBumpTokenVersionNonSuperAdmin(ctx, ids)
	if err != nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.bulk.sessions.revoke", 0, "failure", "User bulk sessions revoke failed."))
		return nil, fmt.Errorf("admin bulk revoke sessions: %w", err)
	}
	uc.clearSessions(ctx, ids)
	_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.bulk.sessions.revoke", 0, "success", "User sessions revoked."))
	return bulkResult(requested, int(affected)), nil
}

// Invitations returns a user's referral inviter and direct invitees.
func (uc *AdminUseCase) Invitations(ctx context.Context, targetUserID uint) (*InvitationOverview, error) {
	user, err := uc.repo.FindByID(ctx, targetUserID)
	if err != nil {
		return nil, fmt.Errorf("admin invitations find user: %w", err)
	}
	if user == nil {
		return nil, domain.ErrUserNotFound
	}

	overview := &InvitationOverview{Invitees: []domain.User{}}

	inviterID, err := uc.repo.FindInviterID(ctx, targetUserID)
	if err != nil {
		return nil, fmt.Errorf("admin invitations inviter: %w", err)
	}
	if inviterID != nil {
		inviters, err := uc.repo.FindByIDs(ctx, []uint{*inviterID})
		if err != nil {
			return nil, fmt.Errorf("admin invitations load inviter: %w", err)
		}
		if len(inviters) > 0 {
			overview.Inviter = &inviters[0]
		}
	}

	inviteeIDs, err := uc.repo.ListInviteeIDs(ctx, targetUserID)
	if err != nil {
		return nil, fmt.Errorf("admin invitations invitees: %w", err)
	}
	if len(inviteeIDs) > 0 {
		invitees, err := uc.repo.FindByIDs(ctx, inviteeIDs)
		if err != nil {
			return nil, fmt.Errorf("admin invitations load invitees: %w", err)
		}
		// FindByIDs does not preserve order; re-sort newest first by id desc as a
		// stable proxy for join order.
		byID := make(map[uint]domain.User, len(invitees))
		for _, u := range invitees {
			byID[u.ID] = u
		}
		ordered := make([]domain.User, 0, len(inviteeIDs))
		for _, id := range inviteeIDs {
			if u, ok := byID[id]; ok {
				ordered = append(ordered, u)
			}
		}
		overview.Invitees = ordered
	}

	return overview, nil
}

// ForceLogout increments the user's tokenVersion and clears all sessions.
func (uc *AdminUseCase) ForceLogout(ctx context.Context, operatorUserID uint, requestID, path string, targetUserID uint) error {
	_, err := uc.repo.UpdateNonSuperAdminAccessWithOperationLog(
		ctx,
		targetUserID,
		nil,
		nil,
		nil,
		true,
		adminOperationLog(operatorUserID, requestID, path, "iam.user.sessions.revoke", targetUserID, "success", "User sessions revoked."),
	)
	if err != nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.sessions.revoke", targetUserID, "failure", "User sessions revoke failed."))
		return fmt.Errorf("admin force logout update: %w", err)
	}

	_ = uc.sessions.DeleteByUserID(ctx, targetUserID)

	return nil
}

func validPermissionPolicy(policy domain.PermissionPolicy) bool {
	if policy.Effect != "allow" && policy.Effect != "deny" {
		return false
	}
	for _, item := range permissionCatalog {
		if item.Resource != policy.Resource {
			continue
		}
		for _, action := range item.Actions {
			if action == policy.Action {
				return true
			}
		}
	}
	return false
}

func (uc *AdminUseCase) restoreUserPermissions(ctx context.Context, userID uint, previous []domain.PermissionPolicy) {
	if _, err := uc.permissions.ReplaceUserPermissionPoliciesGuarded(ctx, userID, previous, true); err != nil {
		return
	}
	_ = uc.permissions.Reload(ctx)
}

func adminOperationLog(operatorUserID uint, requestID, path, operationType string, targetUserID uint, result, summary string) *governancedomain.OperationLog {
	return &governancedomain.OperationLog{
		OperatorUserID: operatorUserID,
		OperationType:  operationType,
		ResourceType:   "user",
		ResourceID:     fmt.Sprintf("%d", targetUserID),
		Path:           path,
		Result:         result,
		SafeSummary:    summary,
		RequestID:      requestID,
	}
}

func inviteOperationLog(operatorUserID uint, requestID, path, operationType, code, result, summary string) *governancedomain.OperationLog {
	return &governancedomain.OperationLog{
		OperatorUserID: operatorUserID,
		OperationType:  operationType,
		ResourceType:   "invite",
		ResourceID:     code,
		Path:           path,
		Result:         result,
		SafeSummary:    summary,
		RequestID:      requestID,
	}
}
