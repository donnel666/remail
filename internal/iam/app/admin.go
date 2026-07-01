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
	logs        governanceapp.OperationLogPort
}

// NewAdminUseCase creates a new AdminUseCase.
func NewAdminUseCase(repo UserRepository, sessions SessionStore, invites InviteRepository, permissions PermissionRepository, logs governanceapp.OperationLogPort) *AdminUseCase {
	return &AdminUseCase{repo: repo, sessions: sessions, invites: invites, permissions: permissions, logs: logs}
}

// UserListResult contains paginated user results.
type UserListResult struct {
	Users  []domain.User `json:"users"`
	Total  int64         `json:"total"`
	Offset int           `json:"offset"`
	Limit  int           `json:"limit"`
}

type InviteListResult struct {
	Invites []domain.Invite `json:"invites"`
	Total   int64           `json:"total"`
	Offset  int             `json:"offset"`
	Limit   int             `json:"limit"`
}

type CreateInviteRequest struct {
	Code     string
	Enabled  bool
	MaxUse   int
	ExpireAt *time.Time
}

type UpdateInviteRequest struct {
	Enabled  *bool
	MaxUse   *int
	ExpireAt *time.Time
}

var permissionCatalog = []domain.PermissionCatalogItem{
	{Resource: "iam:user", Actions: []string{"read", "write", "operate"}},
	{Resource: "iam:permission", Actions: []string{"read", "write"}},
	{Resource: "iam:invite", Actions: []string{"read", "write", "operate"}},
	{Resource: "iam:supplier_application", Actions: []string{"read", "operate"}},
}

// ListUsers returns a paginated list of all users.
func (uc *AdminUseCase) ListUsers(ctx context.Context, offset, limit int) (*UserListResult, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	total, err := uc.repo.Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("admin list users count: %w", err)
	}

	users, err := uc.repo.List(ctx, offset, limit)
	if err != nil {
		return nil, fmt.Errorf("admin list users: %w", err)
	}

	return &UserListResult{
		Users:  users,
		Total:  total,
		Offset: offset,
		Limit:  limit,
	}, nil
}

func (uc *AdminUseCase) ListPermissions(_ context.Context) []domain.PermissionCatalogItem {
	return permissionCatalog
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

func (uc *AdminUseCase) SaveUserPermissions(ctx context.Context, operatorUserID uint, requestID, path string, targetUserID uint, policies []domain.PermissionPolicy) error {
	user, err := uc.repo.FindByID(ctx, targetUserID)
	if err != nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.permissions.update", targetUserID, "failure", "User permission update failed."))
		return fmt.Errorf("admin permissions find user: %w", err)
	}
	if user == nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.permissions.update", targetUserID, "failure", "User permission update failed."))
		return domain.ErrUserNotFound
	}
	for _, policy := range policies {
		if !validPermissionPolicy(policy) {
			_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.permissions.update", targetUserID, "failure", "User permission update failed."))
			return domain.ErrInvalidPermissionPolicy
		}
	}

	previous, err := uc.permissions.ListUserPermissionPolicies(ctx, targetUserID)
	if err != nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.permissions.update", targetUserID, "failure", "User permission update failed."))
		return fmt.Errorf("admin permissions snapshot: %w", err)
	}

	if err := uc.permissions.ReplaceUserPermissionPolicies(ctx, targetUserID, policies); err != nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.permissions.update", targetUserID, "failure", "User permission update failed."))
		return fmt.Errorf("admin permissions replace: %w", err)
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

func (uc *AdminUseCase) ListInvites(ctx context.Context, offset, limit int) (*InviteListResult, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	total, err := uc.invites.CountInvites(ctx)
	if err != nil {
		return nil, fmt.Errorf("admin list invites count: %w", err)
	}
	invites, err := uc.invites.ListInvites(ctx, offset, limit)
	if err != nil {
		return nil, fmt.Errorf("admin list invites: %w", err)
	}
	return &InviteListResult{Invites: invites, Total: total, Offset: offset, Limit: limit}, nil
}

func (uc *AdminUseCase) CreateInvite(ctx context.Context, operatorUserID uint, requestID, path string, req CreateInviteRequest) (*domain.Invite, error) {
	code := strings.TrimSpace(req.Code)
	if code == "" || req.MaxUse <= 0 || (req.ExpireAt != nil && req.ExpireAt.Before(time.Now())) {
		_ = uc.logs.Create(ctx, inviteOperationLog(operatorUserID, requestID, path, "iam.invite.create", code, "failure", "Invite create failed."))
		return nil, domain.ErrInviteInvalid
	}
	invite := &domain.Invite{
		Code:     code,
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
	if req.ExpireAt != nil {
		invite.ExpireAt = req.ExpireAt
	}
	if err := uc.invites.UpdateInviteWithOperationLog(ctx, invite, inviteOperationLog(operatorUserID, requestID, path, "iam.invite.update", code, "success", "Invite updated.")); err != nil {
		_ = uc.logs.Create(ctx, inviteOperationLog(operatorUserID, requestID, path, "iam.invite.update", code, "failure", "Invite update failed."))
		return nil, err
	}
	return invite, nil
}

// UpdateUserRequest contains the fields that can be updated by an admin.
type UpdateUserRequest struct {
	Enabled   *bool             `json:"enabled,omitempty"`
	RoleLevel *domain.RoleLevel `json:"roleLevel,omitempty"`
}

// UpdateUser updates a user's profile (enabled, roleLevel).
// If the user is disabled, increments tokenVersion and clears sessions (INV-I3).
func (uc *AdminUseCase) UpdateUser(ctx context.Context, operatorUserID uint, requestID, path string, targetUserID uint, req *UpdateUserRequest) (*domain.User, error) {
	user, err := uc.repo.FindByID(ctx, targetUserID)
	if err != nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.update", targetUserID, "failure", "User access update failed."))
		return nil, fmt.Errorf("admin update find user: %w", err)
	}
	if user == nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.update", targetUserID, "failure", "User access update failed."))
		return nil, domain.ErrUserNotFound
	}

	changed := false
	tokenBump := false

	if req.Enabled != nil {
		if user.Enabled != *req.Enabled {
			user.Enabled = *req.Enabled
			changed = true
			if !*req.Enabled {
				tokenBump = true // Disabling user invalidates sessions
			}
		}
	}

	if req.RoleLevel != nil {
		rl := *req.RoleLevel
		if !isValidRoleLevel(rl) {
			_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.update", targetUserID, "failure", "User access update failed."))
			return nil, domain.ErrInvalidRoleLevel
		}
		if user.RoleLevel != rl {
			user.RoleLevel = rl
			changed = true
		}
	}

	if !changed {
		if err := uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.update", targetUserID, "success", "User access settings unchanged.")); err != nil {
			return nil, fmt.Errorf("admin update unchanged log: %w", err)
		}
		return user, nil
	}

	if tokenBump {
		user.TokenVersion++
	}

	if err := uc.repo.UpdateWithOperationLog(ctx, user, adminOperationLog(operatorUserID, requestID, path, "iam.user.update", targetUserID, "success", "User access settings updated.")); err != nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.update", targetUserID, "failure", "User access update failed."))
		return nil, fmt.Errorf("admin update user: %w", err)
	}

	if tokenBump {
		_ = uc.sessions.DeleteByUserID(ctx, targetUserID)
	}

	return user, nil
}

// ForceLogout increments the user's tokenVersion and clears all sessions.
func (uc *AdminUseCase) ForceLogout(ctx context.Context, operatorUserID uint, requestID, path string, targetUserID uint) error {
	user, err := uc.repo.FindByID(ctx, targetUserID)
	if err != nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.sessions.revoke", targetUserID, "failure", "User sessions revoke failed."))
		return fmt.Errorf("admin force logout find user: %w", err)
	}
	if user == nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.sessions.revoke", targetUserID, "failure", "User sessions revoke failed."))
		return domain.ErrUserNotFound
	}

	user.TokenVersion++
	if err := uc.repo.UpdateWithOperationLog(ctx, user, adminOperationLog(operatorUserID, requestID, path, "iam.user.sessions.revoke", targetUserID, "success", "User sessions revoked.")); err != nil {
		_ = uc.logs.Create(ctx, adminOperationLog(operatorUserID, requestID, path, "iam.user.sessions.revoke", targetUserID, "failure", "User sessions revoke failed."))
		return fmt.Errorf("admin force logout update: %w", err)
	}

	_ = uc.sessions.DeleteByUserID(ctx, targetUserID)

	return nil
}

// isValidRoleLevel checks if the given role level is one of the defined values.
func isValidRoleLevel(rl domain.RoleLevel) bool {
	switch rl {
	case domain.RoleUser, domain.RoleSupplier, domain.RoleAdmin, domain.RoleSuperAdmin:
		return true
	default:
		return false
	}
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
	if err := uc.permissions.ReplaceUserPermissionPolicies(ctx, userID, previous); err != nil {
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
