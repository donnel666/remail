package api

import (
	"encoding/json"
	"time"

	"github.com/donnel666/remail/internal/iam/app"
	"github.com/donnel666/remail/internal/iam/domain"
)

// --- Request DTOs ---

// ActivationRequest is the request body for POST /v1/activation.
type ActivationRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=6"`
	Nickname string `json:"nickname" binding:"omitempty,max=100"`
}

// LoginRequest is the request body for POST /v1/login.
type LoginRequest struct {
	Email          string `json:"email" binding:"required,email"`
	Password       string `json:"password" binding:"required"`
	TurnstileToken string `json:"turnstileToken" binding:"required,max=2048"`
}

// RegisterRequest is the request body for POST /v1/users.
type RegisterRequest struct {
	Email      string `json:"email" binding:"required,email"`
	Password   string `json:"password" binding:"required,min=6"`
	Nickname   string `json:"nickname" binding:"omitempty,max=100"`
	Code       string `json:"code" binding:"required"`
	InviteCode string `json:"inviteCode" binding:"omitempty,max=64"`
}

// EmailCodeRequest is the request body for POST /v1/email/code.
type EmailCodeRequest struct {
	Email          string `json:"email" binding:"required,email"`
	TurnstileToken string `json:"turnstileToken" binding:"required,max=2048"`
}

// ChangePasswordRequest is the request body for PATCH /v1/password.
type ChangePasswordRequest struct {
	OldPassword string `json:"oldPassword" binding:"required"`
	NewPassword string `json:"newPassword" binding:"required,min=6"`
}

type PasswordResetCodeRequest struct {
	Email          string `json:"email" binding:"required,email"`
	TurnstileToken string `json:"turnstileToken" binding:"required,max=2048"`
}

type PasswordResetRequest struct {
	Email       string `json:"email" binding:"required,email"`
	Code        string `json:"code" binding:"required"`
	NewPassword string `json:"newPassword" binding:"required,min=6"`
}

// AdminUpdateUserRequest is the request body for PATCH /v1/admin/users/:userId.
type AdminUpdateUserRequest struct {
	Email       *string `json:"email,omitempty" binding:"omitempty,email"`
	Nickname    *string `json:"nickname,omitempty" binding:"omitempty,max=100"`
	Password    *string `json:"password,omitempty" binding:"omitempty,min=6"`
	Enabled     *bool   `json:"enabled,omitempty"`
	Role        *string `json:"role,omitempty"`
	UserGroupID *uint   `json:"userGroupId,omitempty"`
}

// AdminCreateUserRequest is the request body for POST /v1/admin/users.
type AdminCreateUserRequest struct {
	Email       string `json:"email" binding:"required,email"`
	Nickname    string `json:"nickname" binding:"omitempty,max=100"`
	Password    string `json:"password" binding:"required,min=6"`
	Role        string `json:"role" binding:"required"`
	UserGroupID uint   `json:"userGroupId" binding:"required"`
}

type PermissionPolicyRequest struct {
	Resource string `json:"resource" binding:"required"`
	Action   string `json:"action" binding:"required"`
	Effect   string `json:"effect" binding:"required,oneof=allow deny"`
}

type AdminUpdateUserPermissionsRequest struct {
	Policies []PermissionPolicyRequest `json:"policies" binding:"required"`
}

type AdminCreateInviteRequest struct {
	Code     string     `json:"code" binding:"omitempty,max=64"`
	Enabled  *bool      `json:"enabled,omitempty"`
	MaxUse   int        `json:"maxUse" binding:"required,min=1"`
	ExpireAt *time.Time `json:"expireAt,omitempty"`
}

type AdminUpdateInviteRequest struct {
	Enabled *bool `json:"enabled,omitempty"`
	MaxUse  *int  `json:"maxUse,omitempty" binding:"omitempty,min=1"`
	// ExpireAt is tri-state: key absent = leave unchanged, null = clear, value =
	// set. RawMessage preserves the absent-vs-null distinction that *time.Time
	// loses.
	ExpireAt json.RawMessage `json:"expireAt,omitempty"`
}

type SupplierApplicationRequest struct {
	Reason string `json:"reason" binding:"required,max=1000"`
}

type AdminRejectSupplierApplicationRequest struct {
	ReviewReason string `json:"reviewReason" binding:"required,max=500"`
}

// AdminUserBulkFilterRequest mirrors the browse-list filter for selection-based
// bulk user actions.
type AdminUserBulkFilterRequest struct {
	Search      string     `json:"search"`
	Role        string     `json:"role" binding:"omitempty,oneof=user supplier admin super_admin"`
	Enabled     *bool      `json:"enabled"`
	UserGroupID uint       `json:"userGroupId"`
	CreatedFrom *time.Time `json:"createdFrom"`
	CreatedTo   *time.Time `json:"createdTo"`
}

// AdminUserBulkSelectionRequest selects bulk targets by ids or by filter.
type AdminUserBulkSelectionRequest struct {
	Mode    string                      `json:"mode" binding:"required,oneof=ids filter"`
	UserIDs []uint                      `json:"userIds" binding:"omitempty,dive,gt=0"`
	Filter  *AdminUserBulkFilterRequest `json:"filter"`
}

// AdminUserBulkCommandRequest is the body for the selection-based bulk user endpoints.
type AdminUserBulkCommandRequest struct {
	Selection AdminUserBulkSelectionRequest `json:"selection" binding:"required"`
}

// AdminUserBulkResponse reports how many rows a bulk action requested, changed, and skipped.
type AdminUserBulkResponse struct {
	Requested int `json:"requested"`
	Affected  int `json:"affected"`
	Skipped   int `json:"skipped"`
}

// --- Response DTOs ---

// ActivationResponse is the response for GET /v1/activation.
type ActivationResponse struct {
	Needed bool `json:"needed"`
}

// UserResponse is the public representation of a user.
// Never exposes passwordHash or tokenVersion.
type UserResponse struct {
	ID          uint              `json:"id"`
	Email       string            `json:"email"`
	Nickname    string            `json:"nickname"`
	Role        string            `json:"role"`
	UserGroup   UserGroupResponse `json:"userGroup"`
	Permissions []string          `json:"permissions,omitempty"`
	Enabled     bool              `json:"enabled"`
	CreatedAt   time.Time         `json:"createdAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`
	LastLoginAt *time.Time        `json:"lastLoginAt,omitempty"`
}

type UserGroupResponse struct {
	ID          uint   `json:"id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

type CurrentInviteResponse struct {
	InviteCode string `json:"inviteCode"`
}

// LoginResponse is the response for POST /v1/login.
type LoginResponse struct {
	User UserResponse `json:"user"`
}

// TurnstileConfigResponse exposes only the public widget site key.
type TurnstileConfigResponse struct {
	SiteKey string `json:"siteKey"`
}

// AdminUserListResponse is the response for GET /v1/admin/users.
type AdminUserListResponse struct {
	Users  []UserResponse           `json:"users"`
	Total  int64                    `json:"total"`
	Offset int                      `json:"offset"`
	Limit  int                      `json:"limit"`
	Facets *AdminUserFacetsResponse `json:"facets,omitempty"`
}

// AdminUserFacetsResponse holds the browse-list aggregate counts.
type AdminUserFacetsResponse struct {
	Role   map[string]int64      `json:"role"`
	Status AdminUserStatusFacet  `json:"status"`
	Group  []AdminUserGroupFacet `json:"group"`
}

type AdminUserStatusFacet struct {
	All      int64 `json:"all"`
	Enabled  int64 `json:"enabled"`
	Disabled int64 `json:"disabled"`
}

type AdminUserGroupFacet struct {
	ID    uint   `json:"id"`
	Code  string `json:"code"`
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// AdminUserInvitationMember is a safe inviter/invitee view.
type AdminUserInvitationMember struct {
	ID       uint      `json:"id"`
	Email    string    `json:"email"`
	Nickname string    `json:"nickname"`
	Role     string    `json:"role"`
	Enabled  bool      `json:"enabled"`
	JoinedAt time.Time `json:"joinedAt"`
}

// AdminUserInvitationsResponse is the response for GET
// /v1/admin/users/:userId/invitations.
type AdminUserInvitationsResponse struct {
	Inviter  *AdminUserInvitationMember  `json:"inviter"`
	Invitees []AdminUserInvitationMember `json:"invitees"`
}

type AdminUserGroupListResponse struct {
	Groups []UserGroupResponse `json:"groups"`
}

type AdminCreateUserGroupRequest struct {
	Code        string `json:"code" binding:"required,max=64"`
	Name        string `json:"name" binding:"required,max=100"`
	Description string `json:"description" binding:"omitempty,max=500"`
	Enabled     *bool  `json:"enabled,omitempty"`
}

type AdminUpdateUserGroupRequest struct {
	Name        *string `json:"name,omitempty" binding:"omitempty,max=100"`
	Description *string `json:"description,omitempty" binding:"omitempty,max=500"`
	Enabled     *bool   `json:"enabled,omitempty"`
}

type PermissionCatalogResponse struct {
	Permissions []PermissionCatalogItemResponse `json:"permissions"`
}

type PermissionCatalogItemResponse struct {
	Resource string   `json:"resource"`
	Actions  []string `json:"actions"`
}

type UserPermissionPoliciesResponse struct {
	Policies []PermissionPolicyResponse `json:"policies"`
}

type PermissionPolicyResponse struct {
	Resource string `json:"resource"`
	Action   string `json:"action"`
	Effect   string `json:"effect"`
}

type InviteResponse struct {
	Code           string     `json:"code"`
	Kind           string     `json:"kind"`
	Enabled        bool       `json:"enabled"`
	MaxUse         int        `json:"maxUse"`
	Used           int        `json:"used"`
	ExpireAt       *time.Time `json:"expireAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	OwnerUserID    *uint      `json:"ownerUserId"`
	OwnerEmail     *string    `json:"ownerEmail"`
	OwnerNickname  *string    `json:"ownerNickname"`
	OwnerRole      *string    `json:"ownerRole"`
	OwnerGroupID   *uint      `json:"ownerGroupId"`
	OwnerGroupName *string    `json:"ownerGroupName"`
}

type InviteListResponse struct {
	Invites []InviteResponse     `json:"invites"`
	Total   int64                `json:"total"`
	Offset  int                  `json:"offset"`
	Limit   int                  `json:"limit"`
	Facets  InviteFacetsResponse `json:"facets"`
}

// InviteFacetsResponse holds the invite browse-list aggregate counts.
type InviteFacetsResponse struct {
	Role    InviteRoleFacetResponse    `json:"role"`
	Group   []InviteGroupFacetResponse `json:"group"`
	Enabled InviteEnabledFacetResponse `json:"enabled"`
}

type InviteRoleFacetResponse struct {
	All        int64 `json:"all"`
	User       int64 `json:"user"`
	Supplier   int64 `json:"supplier"`
	Admin      int64 `json:"admin"`
	SuperAdmin int64 `json:"super_admin"`
}

type InviteEnabledFacetResponse struct {
	All      int64 `json:"all"`
	Enabled  int64 `json:"enabled"`
	Disabled int64 `json:"disabled"`
}

type InviteGroupFacetResponse struct {
	ID    uint   `json:"id"`
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// AdminBatchCreateInviteRequest is the body for POST /v1/admin/invites/batch.
type AdminBatchCreateInviteRequest struct {
	Count    int        `json:"count" binding:"required,min=1,max=100"`
	MaxUse   int        `json:"maxUse" binding:"required,min=1"`
	Enabled  *bool      `json:"enabled,omitempty"`
	ExpireAt *time.Time `json:"expireAt,omitempty"`
	Prefix   string     `json:"prefix" binding:"omitempty,max=32"`
}

// AdminBatchCreateInviteResponse is the 201 body for POST /v1/admin/invites/batch.
type AdminBatchCreateInviteResponse struct {
	Items   []InviteResponse `json:"items"`
	Created int              `json:"created"`
}

// InviteBulkFilterRequest mirrors the browse-list filter for selection-based
// bulk invite actions.
type InviteBulkFilterRequest struct {
	Search       string `json:"search"`
	Kind         string `json:"kind" binding:"omitempty,oneof=admin referral all"`
	OwnerRole    string `json:"ownerRole" binding:"omitempty,oneof=user supplier admin super_admin"`
	OwnerGroupID uint   `json:"ownerGroupId"`
	Enabled      *bool  `json:"enabled"`
}

// InviteBulkSelectionRequest selects bulk targets by codes or by filter.
type InviteBulkSelectionRequest struct {
	Mode   string                   `json:"mode" binding:"required,oneof=ids filter"`
	Codes  []string                 `json:"codes" binding:"omitempty,dive,required,max=64"`
	Filter *InviteBulkFilterRequest `json:"filter"`
}

// InviteBulkCommandRequest is the body for the selection-based bulk invite endpoints.
type InviteBulkCommandRequest struct {
	Selection InviteBulkSelectionRequest `json:"selection" binding:"required"`
}

// AdminBulkResponse reports how many rows a bulk action requested, changed, and skipped.
type AdminBulkResponse struct {
	Requested int `json:"requested"`
	Affected  int `json:"affected"`
	Skipped   int `json:"skipped"`
}

// InviteUseResponse is one invite redemption with the redeeming user resolved.
type InviteUseResponse struct {
	ID            uint64    `json:"id"`
	InviteCode    string    `json:"inviteCode"`
	UserID        uint      `json:"userId"`
	UserEmail     *string   `json:"userEmail"`
	UserNickname  *string   `json:"userNickname"`
	UserRole      *string   `json:"userRole"`
	UserGroupName *string   `json:"userGroupName"`
	UsedAt        time.Time `json:"usedAt"`
}

type InviteUsesResponse struct {
	Uses []InviteUseResponse `json:"uses"`
}

type SupplierApplicationResponse struct {
	ID              uint       `json:"id"`
	ApplicantUserID uint       `json:"applicantUserId"`
	Reason          string     `json:"reason"`
	Status          string     `json:"status"`
	ReviewReason    string     `json:"reviewReason"`
	ReviewedBy      *uint      `json:"reviewedBy,omitempty"`
	ReviewedAt      *time.Time `json:"reviewedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type SupplierApplicationCurrentResponse struct {
	Application *SupplierApplicationResponse `json:"application"`
}

type SupplierApplicationListResponse struct {
	Applications []SupplierApplicationResponse `json:"applications"`
	Total        int64                         `json:"total"`
	Offset       int                           `json:"offset"`
	Limit        int                           `json:"limit"`
}

// --- Helpers ---

// toUserResponse converts a domain User to a safe API response.
func toUserResponse(u *domain.User) UserResponse {
	return UserResponse{
		ID:          u.ID,
		Email:       u.Email,
		Nickname:    u.Nickname,
		Role:        u.Role.String(),
		UserGroup:   toUserGroupResponse(u.UserGroup),
		Enabled:     u.IsActive(),
		CreatedAt:   u.CreatedAt,
		UpdatedAt:   u.UpdatedAt,
		LastLoginAt: u.LastLoginAt,
	}
}

func toUserGroupResponse(group domain.UserGroup) UserGroupResponse {
	if group.ID == 0 {
		return UserGroupResponse{ID: 1, Code: "normal", Name: "普通用户", Description: "默认权益分组", Enabled: true}
	}
	return UserGroupResponse{
		ID:          group.ID,
		Code:        group.Code,
		Name:        group.Name,
		Description: group.Description,
		Enabled:     group.Enabled,
	}
}

func toAdminUserFacetsResponse(facets *domain.UserFacets) *AdminUserFacetsResponse {
	if facets == nil {
		return nil
	}
	groups := make([]AdminUserGroupFacet, len(facets.Group))
	for i, g := range facets.Group {
		groups[i] = AdminUserGroupFacet{ID: g.ID, Code: g.Code, Name: g.Name, Count: g.Count}
	}
	return &AdminUserFacetsResponse{
		Role: facets.Role,
		Status: AdminUserStatusFacet{
			All:      facets.Status.All,
			Enabled:  facets.Status.Enabled,
			Disabled: facets.Status.Disabled,
		},
		Group: groups,
	}
}

func toAdminUserInvitationMember(user domain.User) AdminUserInvitationMember {
	return AdminUserInvitationMember{
		ID:       user.ID,
		Email:    user.Email,
		Nickname: user.Nickname,
		Role:     user.Role.String(),
		Enabled:  user.IsActive(),
		JoinedAt: user.CreatedAt,
	}
}

func toPermissionCatalogResponse(items []domain.PermissionCatalogItem) PermissionCatalogResponse {
	out := make([]PermissionCatalogItemResponse, len(items))
	for i, item := range items {
		out[i] = PermissionCatalogItemResponse{Resource: item.Resource, Actions: item.Actions}
	}
	return PermissionCatalogResponse{Permissions: out}
}

func toPermissionPolicyResponse(policies []domain.PermissionPolicy) UserPermissionPoliciesResponse {
	out := make([]PermissionPolicyResponse, len(policies))
	for i, policy := range policies {
		out[i] = PermissionPolicyResponse{Resource: policy.Resource, Action: policy.Action, Effect: policy.Effect}
	}
	return UserPermissionPoliciesResponse{Policies: out}
}

func toInviteResponse(invite *domain.Invite) InviteResponse {
	return InviteResponse{
		Code:      invite.Code,
		Kind:      string(invite.Kind),
		Enabled:   invite.Enabled,
		MaxUse:    invite.MaxUse,
		Used:      invite.Used,
		ExpireAt:  invite.ExpireAt,
		CreatedAt: invite.CreatedAt,
		UpdatedAt: invite.UpdatedAt,
	}
}

func toInviteListItemResponse(item app.InviteListItem) InviteResponse {
	resp := toInviteResponse(&item.Invite)
	if item.Owner != nil {
		applyOwnerFields(&resp, *item.Owner)
	}
	return resp
}

// applyOwnerFields copies the resolved owner summary into an invite response.
func applyOwnerFields(resp *InviteResponse, owner domain.UserSummary) {
	id := owner.ID
	resp.OwnerUserID = &id
	email := owner.Email
	resp.OwnerEmail = &email
	nickname := owner.Nickname
	resp.OwnerNickname = &nickname
	role := owner.Role
	resp.OwnerRole = &role
	if owner.GroupID != 0 {
		groupID := owner.GroupID
		resp.OwnerGroupID = &groupID
		groupName := owner.GroupName
		resp.OwnerGroupName = &groupName
	}
}

func toInviteFacetsResponse(facets *domain.InviteFacets) InviteFacetsResponse {
	if facets == nil {
		return InviteFacetsResponse{Group: []InviteGroupFacetResponse{}}
	}
	groups := make([]InviteGroupFacetResponse, len(facets.Group))
	for i, g := range facets.Group {
		groups[i] = InviteGroupFacetResponse{ID: g.ID, Name: g.Name, Count: g.Count}
	}
	return InviteFacetsResponse{
		Role: InviteRoleFacetResponse{
			All:        facets.Role.All,
			User:       facets.Role.User,
			Supplier:   facets.Role.Supplier,
			Admin:      facets.Role.Admin,
			SuperAdmin: facets.Role.SuperAdmin,
		},
		Group: groups,
		Enabled: InviteEnabledFacetResponse{
			All:      facets.Enabled.All,
			Enabled:  facets.Enabled.Enabled,
			Disabled: facets.Enabled.Disabled,
		},
	}
}

func toInviteUseResponse(item app.InviteUseItem) InviteUseResponse {
	resp := InviteUseResponse{
		ID:         item.Use.ID,
		InviteCode: item.Use.InviteCode,
		UserID:     item.Use.UserID,
		UsedAt:     item.Use.UsedAt,
	}
	if item.User != nil {
		email := item.User.Email
		resp.UserEmail = &email
		nickname := item.User.Nickname
		resp.UserNickname = &nickname
		role := item.User.Role
		resp.UserRole = &role
		groupName := item.User.GroupName
		resp.UserGroupName = &groupName
	}
	return resp
}

func toSupplierApplicationResponse(application *domain.SupplierApplication) SupplierApplicationResponse {
	return SupplierApplicationResponse{
		ID:              application.ID,
		ApplicantUserID: application.ApplicantUserID,
		Reason:          application.Reason,
		Status:          string(application.Status),
		ReviewReason:    application.ReviewReason,
		ReviewedBy:      application.ReviewedBy,
		ReviewedAt:      application.ReviewedAt,
		CreatedAt:       application.CreatedAt,
		UpdatedAt:       application.UpdatedAt,
	}
}
