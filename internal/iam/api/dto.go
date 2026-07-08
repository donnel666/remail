package api

import (
	"time"

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
// Captcha is required to prevent brute-force attacks.
type LoginRequest struct {
	Email         string `json:"email" binding:"required,email"`
	Password      string `json:"password" binding:"required"`
	CaptchaID     string `json:"captchaId" binding:"required"`
	CaptchaAnswer string `json:"captchaAnswer" binding:"required"`
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
	Email         string `json:"email" binding:"required,email"`
	CaptchaID     string `json:"captchaId" binding:"required"`
	CaptchaAnswer string `json:"captchaAnswer" binding:"required"`
}

// ChangePasswordRequest is the request body for PATCH /v1/password.
type ChangePasswordRequest struct {
	OldPassword string `json:"oldPassword" binding:"required"`
	NewPassword string `json:"newPassword" binding:"required,min=6"`
}

type PasswordResetCodeRequest struct {
	Email         string `json:"email" binding:"required,email"`
	CaptchaID     string `json:"captchaId" binding:"required"`
	CaptchaAnswer string `json:"captchaAnswer" binding:"required"`
}

type PasswordResetRequest struct {
	Email       string `json:"email" binding:"required,email"`
	Code        string `json:"code" binding:"required"`
	NewPassword string `json:"newPassword" binding:"required,min=6"`
}

// AdminUpdateUserRequest is the request body for PATCH /v1/admin/users/:userId.
type AdminUpdateUserRequest struct {
	Enabled     *bool   `json:"enabled,omitempty"`
	Role        *string `json:"role,omitempty"`
	UserGroupID *uint   `json:"userGroupId,omitempty"`
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
	Code     string     `json:"code" binding:"required,max=64"`
	Enabled  *bool      `json:"enabled,omitempty"`
	MaxUse   int        `json:"maxUse" binding:"required,min=1"`
	ExpireAt *time.Time `json:"expireAt,omitempty"`
}

type AdminUpdateInviteRequest struct {
	Enabled  *bool      `json:"enabled,omitempty"`
	MaxUse   *int       `json:"maxUse,omitempty" binding:"omitempty,min=1"`
	ExpireAt *time.Time `json:"expireAt,omitempty"`
}

type SupplierApplicationRequest struct {
	Reason string `json:"reason" binding:"required,max=1000"`
}

type AdminRejectSupplierApplicationRequest struct {
	ReviewReason string `json:"reviewReason" binding:"required,max=500"`
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

// CaptchaResponse is the response for POST /v1/captchas.
type CaptchaResponse struct {
	CaptchaID string `json:"captchaId"`
	Image     string `json:"image"`
}

// AdminUserListResponse is the response for GET /v1/admin/users.
type AdminUserListResponse struct {
	Users  []UserResponse `json:"users"`
	Total  int64          `json:"total"`
	Offset int            `json:"offset"`
	Limit  int            `json:"limit"`
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
	Code      string     `json:"code"`
	Enabled   bool       `json:"enabled"`
	MaxUse    int        `json:"maxUse"`
	Used      int        `json:"used"`
	ExpireAt  *time.Time `json:"expireAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

type InviteListResponse struct {
	Invites []InviteResponse `json:"invites"`
	Total   int64            `json:"total"`
	Offset  int              `json:"offset"`
	Limit   int              `json:"limit"`
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
		Enabled:     u.Enabled,
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
		Enabled:   invite.Enabled,
		MaxUse:    invite.MaxUse,
		Used:      invite.Used,
		ExpireAt:  invite.ExpireAt,
		CreatedAt: invite.CreatedAt,
		UpdatedAt: invite.UpdatedAt,
	}
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
