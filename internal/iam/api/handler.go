package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/iam/app"
	"github.com/donnel666/remail/internal/iam/domain"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/gin-gonic/gin"
)

// IAMHandler holds the IAM HTTP handlers.
type IAMHandler struct {
	module        *IAMModule
	sessionMaxAge int
	sessionSecure bool
}

// NewIAMHandler creates a new IAM handler.
func NewIAMHandler(module *IAMModule, sessionMaxAge int, sessionSecure bool) *IAMHandler {
	return &IAMHandler{
		module:        module,
		sessionMaxAge: sessionMaxAge,
		sessionSecure: sessionSecure,
	}
}

// --- Activation ---

// GET /v1/activation
func (h *IAMHandler) GetActivation(c *gin.Context) {
	status, err := h.module.ActivationUseCase.Check(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, ActivationResponse{Needed: status.Needed})
}

// POST /v1/activation
func (h *IAMHandler) PostActivation(c *gin.Context) {
	var req ActivationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	user, err := h.module.ActivationUseCase.Activate(c.Request.Context(), req.Email, req.Password, req.Nickname)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusCreated, gin.H{"user": h.userResponseWithPermissions(c.Request.Context(), user)})
}

const (
	turnstileActionLogin         = "login"
	turnstileActionRegister      = "register_email_code"
	turnstileActionPasswordReset = "password_reset_code"
)

// GET /v1/turnstile/config
func (h *IAMHandler) GetTurnstileConfig(c *gin.Context) {
	if strings.TrimSpace(h.module.TurnstileSiteKey) == "" {
		writeError(c, domain.ErrTurnstileUnavailable)
		return
	}
	c.JSON(http.StatusOK, TurnstileConfigResponse{SiteKey: h.module.TurnstileSiteKey})
}

// POST /v1/email/code
func (h *IAMHandler) PostEmailCode(c *gin.Context) {
	var req EmailCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	if !h.verifyTurnstile(c, req.TurnstileToken, turnstileActionRegister) {
		return
	}

	created, err := h.module.EmailCodeUseCase.Request(c.Request.Context(), req.Email)
	if err != nil {
		writeError(c, err)
		return
	}
	if created && h.module.AbuseLimiter != nil {
		if err := h.module.AbuseLimiter.ClearEmailCodeFailures(c.Request.Context(), req.Email); err != nil {
			slog.Warn("clear email code abuse limit", "request_id", middleware.GetRequestID(c), "error", err.Error())
		}
	}

	c.Status(http.StatusNoContent)
}

// --- Registration ---

// POST /v1/users
func (h *IAMHandler) PostRegister(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	clientIP := c.ClientIP()
	if h.module.AbuseLimiter != nil {
		retryAfter, err := h.module.AbuseLimiter.TakeRegistration(c.Request.Context(), req.Email, clientIP)
		if err != nil {
			writeError(c, err)
			return
		}
		if retryAfter > 0 {
			writeTooManyRequests(c, retryAfter)
			return
		}
	}

	user, err := h.module.RegistrationUseCase.Register(
		c.Request.Context(), req.Email, req.Password, req.Nickname, req.Code, req.InviteCode,
	)
	if err != nil {
		if !errors.Is(err, domain.ErrVerificationCodeIncorrect) && h.module.AbuseLimiter != nil {
			if limitErr := h.module.AbuseLimiter.CancelRegistration(c.Request.Context(), req.Email, clientIP); limitErr != nil {
				writeError(c, limitErr)
				return
			}
		}
		writeError(c, err)
		return
	}
	if h.module.AbuseLimiter != nil {
		if err := h.module.AbuseLimiter.CompleteRegistration(c.Request.Context(), req.Email, clientIP); err != nil {
			slog.Warn("clear registration abuse limit", "request_id", middleware.GetRequestID(c), "error", err.Error())
		}
	}

	c.JSON(http.StatusCreated, gin.H{"user": toUserResponse(user)})
}

// POST /v1/password/reset/request
func (h *IAMHandler) PostPasswordResetRequest(c *gin.Context) {
	var req PasswordResetCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	if !h.verifyTurnstile(c, req.TurnstileToken, turnstileActionPasswordReset) {
		return
	}

	created, err := h.module.PasswordResetUseCase.Request(c.Request.Context(), req.Email)
	if err != nil {
		writeError(c, err)
		return
	}
	if created && h.module.AbuseLimiter != nil {
		if err := h.module.AbuseLimiter.ClearEmailCodeFailures(c.Request.Context(), req.Email); err != nil {
			slog.Warn("clear password reset email abuse limit", "request_id", middleware.GetRequestID(c), "error", err.Error())
		}
	}
	c.Status(http.StatusNoContent)
}

// POST /v1/password/reset
func (h *IAMHandler) PostPasswordReset(c *gin.Context) {
	var req PasswordResetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	clientIP := c.ClientIP()
	if h.module.AbuseLimiter != nil {
		retryAfter, err := h.module.AbuseLimiter.TakePasswordReset(c.Request.Context(), req.Email, clientIP)
		if err != nil {
			writeError(c, err)
			return
		}
		if retryAfter > 0 {
			writeTooManyRequests(c, retryAfter)
			return
		}
	}

	if err := h.module.PasswordResetUseCase.Reset(c.Request.Context(), req.Email, req.Code, req.NewPassword); err != nil {
		if !errors.Is(err, domain.ErrVerificationCodeIncorrect) && h.module.AbuseLimiter != nil {
			if limitErr := h.module.AbuseLimiter.CancelPasswordReset(c.Request.Context(), req.Email, clientIP); limitErr != nil {
				writeError(c, limitErr)
				return
			}
		}
		writeError(c, err)
		return
	}
	if h.module.AbuseLimiter != nil {
		if err := h.module.AbuseLimiter.CompletePasswordReset(c.Request.Context(), req.Email, clientIP); err != nil {
			slog.Warn("clear password reset abuse limit", "request_id", middleware.GetRequestID(c), "error", err.Error())
		}
	}
	c.Status(http.StatusNoContent)
}

// --- Login / Logout ---

// POST /v1/login
func (h *IAMHandler) PostLogin(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	if !h.verifyTurnstile(c, req.TurnstileToken, turnstileActionLogin) {
		return
	}

	csrfToken, err := newCSRFToken()
	if err != nil {
		writeError(c, err)
		return
	}

	clientIP := c.ClientIP()
	if h.module.AbuseLimiter != nil {
		retryAfter, err := h.module.AbuseLimiter.TakeLogin(c.Request.Context(), req.Email, clientIP)
		if err != nil {
			writeError(c, err)
			return
		}
		if retryAfter > 0 {
			writeTooManyRequests(c, retryAfter)
			return
		}
	}

	result, err := h.module.LoginUseCase.Login(c.Request.Context(), req.Email, req.Password, h.sessionMaxAge)
	if err != nil {
		if !errors.Is(err, domain.ErrAccountOrPasswordIncorrect) && h.module.AbuseLimiter != nil {
			if limitErr := h.module.AbuseLimiter.CancelLogin(c.Request.Context(), req.Email, clientIP); limitErr != nil {
				writeError(c, limitErr)
				return
			}
		}
		writeError(c, err)
		return
	}
	if h.module.AbuseLimiter != nil {
		if err := h.module.AbuseLimiter.CompleteLogin(c.Request.Context(), req.Email, clientIP); err != nil {
			slog.Warn("clear login abuse limit", "request_id", middleware.GetRequestID(c), "error", err.Error())
		}
	}

	setAuthCookies(c, result.Session.ID, csrfToken, h.sessionMaxAge, h.sessionSecure)

	c.JSON(http.StatusOK, LoginResponse{User: h.userResponseWithPermissions(c.Request.Context(), result.User)})
}

func (h *IAMHandler) verifyTurnstile(c *gin.Context, token, action string) bool {
	if h.module.AbuseLimiter != nil {
		retryAfter, err := h.module.AbuseLimiter.HitTurnstile(c.Request.Context(), c.ClientIP())
		if err != nil {
			writeError(c, err)
			return false
		}
		if retryAfter > 0 {
			writeTooManyRequests(c, retryAfter)
			return false
		}
	}
	if h.module.TurnstileVerifier == nil {
		writeError(c, domain.ErrTurnstileUnavailable)
		return false
	}
	if err := h.module.TurnstileVerifier.Verify(c.Request.Context(), token, c.ClientIP(), action); err != nil {
		writeError(c, err)
		return false
	}
	return true
}

func writeTooManyRequests(c *gin.Context, retryAfter int) {
	c.Header("Retry-After", strconv.Itoa(retryAfter))
	c.JSON(http.StatusTooManyRequests, gin.H{
		"message":   "Too many requests.",
		"requestId": middleware.GetRequestID(c),
	})
}

// DELETE /v1/sessions/current
func (h *IAMHandler) DeleteSession(c *gin.Context) {
	sid, ok := middleware.GetCurrentSessionID(c)
	if !ok {
		c.Status(http.StatusNoContent)
		return
	}

	if err := h.module.SessionUseCase.Logout(c.Request.Context(), sid); err != nil {
		writeError(c, err)
		return
	}

	clearAuthCookies(c, h.sessionSecure)
	c.Status(http.StatusNoContent)
}

// --- Current User ---

// GET /v1/me
func (h *IAMHandler) GetMe(c *gin.Context) {
	sid, ok := middleware.GetCurrentSessionID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"message":   domain.ErrAuthenticationRequired.Error(),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	user, err := h.module.SessionUseCase.GetCurrent(c.Request.Context(), sid)
	if err != nil {
		writeError(c, err)
		return
	}
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"message":   domain.ErrAuthenticationRequired.Error(),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{"user": h.userResponseWithPermissions(c.Request.Context(), user)})
}

// GET /v1/me/invite
func (h *IAMHandler) GetMeInvite(c *gin.Context) {
	userID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"message":   domain.ErrAuthenticationRequired.Error(),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	invite, err := h.module.InviteUseCase.GetReferralInvite(c.Request.Context(), userID)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, CurrentInviteResponse{InviteCode: invite.Code})
}

// POST /v1/me/invite
func (h *IAMHandler) PostMeInvite(c *gin.Context) {
	userID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"message":   domain.ErrAuthenticationRequired.Error(),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	invite, err := h.module.InviteUseCase.CurrentReferralInvite(c.Request.Context(), userID)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, CurrentInviteResponse{InviteCode: invite.Code})
}

// --- Password Change ---

// PATCH /v1/password
func (h *IAMHandler) PatchPassword(c *gin.Context) {
	// PATCH /v1/password — Supplementary design (P1-I1).
	// The original IAM API table (docs/8-iam.md:143) did not
	// include a direct change-password path. Added per P1-I1
	// acceptance criteria requiring password change capability.
	userID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"message":   domain.ErrAuthenticationRequired.Error(),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	var req ChangePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	if err := h.module.ChangePasswordUseCase.Change(c.Request.Context(), userID, req.OldPassword, req.NewPassword); err != nil {
		writeError(c, err)
		return
	}

	clearAuthCookies(c, h.sessionSecure)
	c.Status(http.StatusNoContent)
}

// --- Supplier Applications ---

// POST /v1/suppliers/applications
func (h *IAMHandler) PostSupplierApplication(c *gin.Context) {
	userID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"message":   domain.ErrAuthenticationRequired.Error(),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	var req SupplierApplicationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	application, err := h.module.SupplierApplicationUseCase.Submit(c.Request.Context(), userID, req.Reason)
	if err != nil {
		writeError(c, err)
		return
	}

	resp := toSupplierApplicationResponse(application)
	c.JSON(http.StatusCreated, gin.H{"application": resp})
}

// GET /v1/suppliers/applications/current
func (h *IAMHandler) GetCurrentSupplierApplication(c *gin.Context) {
	userID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"message":   domain.ErrAuthenticationRequired.Error(),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	application, err := h.module.SupplierApplicationUseCase.Current(c.Request.Context(), userID)
	if err != nil {
		writeError(c, err)
		return
	}
	if application == nil {
		c.JSON(http.StatusOK, SupplierApplicationCurrentResponse{Application: nil})
		return
	}
	resp := toSupplierApplicationResponse(application)
	c.JSON(http.StatusOK, SupplierApplicationCurrentResponse{Application: &resp})
}

// --- Admin ---

// GET /v1/admin/users
func (h *IAMHandler) GetAdminUsers(c *gin.Context) {
	if rawLimit := strings.TrimSpace(c.Query("limit")); rawLimit != "" {
		parsedLimit, err := strconv.Atoi(rawLimit)
		if err == nil && parsedLimit > 100 {
			c.JSON(http.StatusBadRequest, gin.H{
				"message":   "Invalid query parameters.",
				"requestId": middleware.GetRequestID(c),
			})
			return
		}
	}
	offset, limit, ok := middleware.ParsePagination(c, middleware.PaginationOptions{
		DefaultLimit: 20,
		MaxLimit:     100,
	})
	if !ok {
		return
	}
	ids, ok := parseUintQueryList(c, "ids")
	if !ok {
		return
	}
	if len(ids) > 1000 || utf8.RuneCountInString(strings.TrimSpace(c.Query("search"))) > 120 {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid query parameters.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	filter := domain.UserListFilter{IDs: ids, Search: c.Query("search")}
	if !applyAdminUserQueryFilters(c, &filter) {
		return
	}

	result, err := h.module.AdminUseCase.ListUsers(c.Request.Context(), filter, offset, limit)
	if err != nil {
		writeError(c, err)
		return
	}

	users := make([]UserResponse, len(result.Users))
	for i, u := range result.Users {
		users[i] = toUserResponse(&u)
	}

	c.JSON(http.StatusOK, AdminUserListResponse{
		Users:  users,
		Total:  result.Total,
		Offset: result.Offset,
		Limit:  result.Limit,
		Facets: toAdminUserFacetsResponse(result.Facets),
	})
}

// applyAdminUserQueryFilters parses the optional role/enabled/userGroupId/
// createdFrom/createdTo query params. Writes a 400 and returns false on any
// malformed value.
func applyAdminUserQueryFilters(c *gin.Context, filter *domain.UserListFilter) bool {
	invalid := func() bool {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid query parameters.",
			"requestId": middleware.GetRequestID(c),
		})
		return false
	}
	if raw := strings.TrimSpace(c.Query("role")); raw != "" {
		role := domain.Role(raw)
		if !role.IsValid() {
			return invalid()
		}
		filter.Role = &role
	}
	if raw := strings.TrimSpace(c.Query("enabled")); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return invalid()
		}
		filter.Enabled = &enabled
	}
	if raw := strings.TrimSpace(c.Query("userGroupId")); raw != "" {
		groupID, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || groupID == 0 {
			return invalid()
		}
		id := uint(groupID)
		filter.UserGroupID = &id
	}
	if raw := strings.TrimSpace(c.Query("createdFrom")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return invalid()
		}
		filter.CreatedFrom = &parsed
	}
	if raw := strings.TrimSpace(c.Query("createdTo")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return invalid()
		}
		filter.CreatedTo = &parsed
	}
	return true
}

func (h *IAMHandler) GetAdminPermissions(c *gin.Context) {
	c.JSON(http.StatusOK, toPermissionCatalogResponse(h.module.AdminUseCase.ListPermissions(c.Request.Context())))
}

func (h *IAMHandler) GetAdminUserGroups(c *gin.Context) {
	groups, err := h.module.AdminUseCase.ListUserGroups(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	resp := make([]UserGroupResponse, len(groups))
	for i := range groups {
		resp[i] = toUserGroupResponse(groups[i])
	}
	c.JSON(http.StatusOK, AdminUserGroupListResponse{Groups: resp})
}

func (h *IAMHandler) PostAdminUserGroup(c *gin.Context) {
	var req AdminCreateUserGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	group, err := h.module.AdminUseCase.CreateUserGroup(c.Request.Context(), app.CreateUserGroupRequest{
		Code:        req.Code,
		Name:        req.Name,
		Description: req.Description,
		Enabled:     enabled,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"group": toUserGroupResponse(*group)})
}

func (h *IAMHandler) PatchAdminUserGroup(c *gin.Context) {
	groupIDStr := c.Param("groupId")
	groupID, err := strconv.ParseUint(groupIDStr, 10, 64)
	if err != nil || groupID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid user group ID.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	var req AdminUpdateUserGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	group, err := h.module.AdminUseCase.UpdateUserGroup(c.Request.Context(), uint(groupID), app.UpdateUserGroupRequest{
		Name:        req.Name,
		Description: req.Description,
		Enabled:     req.Enabled,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"group": toUserGroupResponse(*group)})
}

func (h *IAMHandler) GetAdminUserPermissions(c *gin.Context) {
	targetUserID, ok := parseUserIDParam(c)
	if !ok {
		return
	}
	policies, err := h.module.AdminUseCase.GetUserPermissions(c.Request.Context(), targetUserID)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toPermissionPolicyResponse(policies))
}

func (h *IAMHandler) PutAdminUserPermissions(c *gin.Context) {
	targetUserID, ok := parseUserIDParam(c)
	if !ok {
		return
	}
	var req AdminUpdateUserPermissionsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	policies := make([]domain.PermissionPolicy, len(req.Policies))
	for i, policy := range req.Policies {
		policies[i] = domain.PermissionPolicy{Resource: policy.Resource, Action: policy.Action, Effect: policy.Effect}
	}
	operatorID, _ := middleware.GetCurrentUserID(c)
	allowSensitive, ok := h.currentUserHasPermission(c, "iam:permission", "sensitive")
	if !ok {
		return
	}
	if err := h.module.AdminUseCase.SaveUserPermissions(c.Request.Context(), operatorID, middleware.GetRequestID(c), c.FullPath(), targetUserID, policies, allowSensitive); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *IAMHandler) GetAdminInvites(c *gin.Context) {
	offset, limit, ok := parsePagination(c)
	if !ok {
		return
	}
	filter, ok := parseInviteListFilter(c)
	if !ok {
		return
	}
	result, err := h.module.AdminUseCase.ListInvites(c.Request.Context(), filter, offset, limit)
	if err != nil {
		writeError(c, err)
		return
	}
	invites := make([]InviteResponse, len(result.Items))
	for i := range result.Items {
		invites[i] = toInviteListItemResponse(result.Items[i])
	}
	c.JSON(http.StatusOK, InviteListResponse{
		Invites: invites,
		Total:   result.Total,
		Offset:  result.Offset,
		Limit:   result.Limit,
		Facets:  toInviteFacetsResponse(result.Facets),
	})
}

// parseInviteListFilter parses the invite browse-list query params. kind
// defaults to admin (preserving existing callers); kind=all clears the filter.
// Writes a 400 and returns false on any malformed value.
func parseInviteListFilter(c *gin.Context) (domain.InviteListFilter, bool) {
	invalid := func() bool {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid query parameters.",
			"requestId": middleware.GetRequestID(c),
		})
		return false
	}
	filter := domain.InviteListFilter{Search: c.Query("search"), Kind: domain.InviteKindAdmin}
	switch kind := strings.TrimSpace(c.DefaultQuery("kind", "admin")); kind {
	case "admin":
		filter.Kind = domain.InviteKindAdmin
	case "referral":
		filter.Kind = domain.InviteKindReferral
	case "all":
		filter.Kind = ""
	default:
		return domain.InviteListFilter{}, invalid()
	}
	if raw := strings.TrimSpace(c.Query("ownerRole")); raw != "" {
		role := domain.Role(raw)
		if !role.IsValid() {
			return domain.InviteListFilter{}, invalid()
		}
		filter.OwnerRole = &role
	}
	if raw := strings.TrimSpace(c.Query("ownerGroupId")); raw != "" {
		groupID, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || groupID == 0 {
			return domain.InviteListFilter{}, invalid()
		}
		id := uint(groupID)
		filter.OwnerGroupID = &id
	}
	if raw := strings.TrimSpace(c.Query("enabled")); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return domain.InviteListFilter{}, invalid()
		}
		filter.Enabled = &enabled
	}
	return filter, true
}

func (h *IAMHandler) PostAdminInvite(c *gin.Context) {
	var req AdminCreateInviteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	operatorID, _ := middleware.GetCurrentUserID(c)
	invite, err := h.module.AdminUseCase.CreateInvite(c.Request.Context(), operatorID, middleware.GetRequestID(c), c.FullPath(), app.CreateInviteRequest{
		Code:     req.Code,
		Enabled:  enabled,
		MaxUse:   req.MaxUse,
		ExpireAt: req.ExpireAt,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"invite": toInviteResponse(invite)})
}

func (h *IAMHandler) PatchAdminInvite(c *gin.Context) {
	code := c.Param("code")
	var req AdminUpdateInviteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	operatorID, _ := middleware.GetCurrentUserID(c)
	expireAt, ok := parseOptionalExpireAt(c, req.ExpireAt)
	if !ok {
		return
	}
	invite, err := h.module.AdminUseCase.UpdateInvite(c.Request.Context(), operatorID, middleware.GetRequestID(c), c.FullPath(), code, app.UpdateInviteRequest{
		Enabled:  req.Enabled,
		MaxUse:   req.MaxUse,
		ExpireAt: expireAt,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"invite": toInviteResponse(invite)})
}

// parseOptionalExpireAt decodes a PATCH expireAt into the tri-state carrier:
// absent (no bytes) -> nil (leave unchanged); null -> non-nil *(*time.Time)=nil
// (clear); a value -> the parsed time (set). Writes 400 and returns false on a
// malformed value.
func parseOptionalExpireAt(c *gin.Context, raw json.RawMessage) (**time.Time, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	var t *time.Time
	if err := json.Unmarshal(raw, &t); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"requestId": middleware.GetRequestID(c),
		})
		return nil, false
	}
	return &t, true
}

// POST /v1/admin/invites/batch
func (h *IAMHandler) PostAdminInvitesBatch(c *gin.Context) {
	var req AdminBatchCreateInviteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	operatorID, _ := middleware.GetCurrentUserID(c)
	invites, err := h.module.AdminUseCase.BatchCreateInvites(c.Request.Context(), operatorID, middleware.GetRequestID(c), c.FullPath(), app.BatchCreateInviteRequest{
		Count:    req.Count,
		MaxUse:   req.MaxUse,
		Enabled:  enabled,
		ExpireAt: req.ExpireAt,
		Prefix:   req.Prefix,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	items := make([]InviteResponse, len(invites))
	for i := range invites {
		items[i] = toInviteResponse(&invites[i])
	}
	c.JSON(http.StatusCreated, AdminBatchCreateInviteResponse{Items: items, Created: len(items)})
}

// POST /v1/admin/invites/enable
func (h *IAMHandler) PostAdminInvitesEnable(c *gin.Context) {
	sel, ok := h.bindInviteBulkSelection(c)
	if !ok {
		return
	}
	operatorID, _ := middleware.GetCurrentUserID(c)
	result, err := h.module.AdminUseCase.BulkSetInviteEnabled(c.Request.Context(), operatorID, middleware.GetRequestID(c), c.FullPath(), sel, true)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAdminBulkResponse(result))
}

// POST /v1/admin/invites/disable
func (h *IAMHandler) PostAdminInvitesDisable(c *gin.Context) {
	sel, ok := h.bindInviteBulkSelection(c)
	if !ok {
		return
	}
	operatorID, _ := middleware.GetCurrentUserID(c)
	result, err := h.module.AdminUseCase.BulkSetInviteEnabled(c.Request.Context(), operatorID, middleware.GetRequestID(c), c.FullPath(), sel, false)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAdminBulkResponse(result))
}

// GET /v1/admin/invites/:code/uses
func (h *IAMHandler) GetAdminInviteUses(c *gin.Context) {
	items, err := h.module.AdminUseCase.ListInviteUses(c.Request.Context(), c.Param("code"))
	if err != nil {
		writeError(c, err)
		return
	}
	uses := make([]InviteUseResponse, len(items))
	for i := range items {
		uses[i] = toInviteUseResponse(items[i])
	}
	c.JSON(http.StatusOK, InviteUsesResponse{Uses: uses})
}

// bindInviteBulkSelection binds the bulk command body and converts it to an app
// selection. Writes a 400 and returns false on a malformed body/role/kind. In
// filter mode kind defaults to admin (kind=all clears it), mirroring the list.
func (h *IAMHandler) bindInviteBulkSelection(c *gin.Context) (app.InviteBulkSelection, bool) {
	var req InviteBulkCommandRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return app.InviteBulkSelection{}, false
	}
	badBody := func() (app.InviteBulkSelection, bool) {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"requestId": middleware.GetRequestID(c),
		})
		return app.InviteBulkSelection{}, false
	}
	sel := app.InviteBulkSelection{Mode: req.Selection.Mode}
	if req.Selection.Mode == "ids" {
		sel.Codes = req.Selection.Codes
		return sel, true
	}
	f := req.Selection.Filter
	if f == nil {
		return badBody()
	}
	filter := domain.InviteListFilter{Search: strings.TrimSpace(f.Search), Kind: domain.InviteKindAdmin, Enabled: f.Enabled}
	switch f.Kind {
	case "", "admin":
		filter.Kind = domain.InviteKindAdmin
	case "referral":
		filter.Kind = domain.InviteKindReferral
	case "all":
		filter.Kind = ""
	default:
		return badBody()
	}
	if raw := strings.TrimSpace(f.OwnerRole); raw != "" {
		role := domain.Role(raw)
		if !role.IsValid() {
			return badBody()
		}
		filter.OwnerRole = &role
	}
	if f.OwnerGroupID != 0 {
		groupID := f.OwnerGroupID
		filter.OwnerGroupID = &groupID
	}
	sel.Filter = filter
	return sel, true
}

func toAdminBulkResponse(result *app.BulkResult) AdminBulkResponse {
	return AdminBulkResponse{Requested: result.Requested, Affected: result.Affected, Skipped: result.Skipped}
}

func (h *IAMHandler) GetAdminSupplierApplications(c *gin.Context) {
	offset, limit, ok := parsePagination(c)
	if !ok {
		return
	}
	status := c.DefaultQuery("status", "all")
	result, err := h.module.SupplierApplicationUseCase.List(c.Request.Context(), status, offset, limit)
	if err != nil {
		writeError(c, err)
		return
	}
	applications := make([]SupplierApplicationResponse, len(result.Applications))
	for i := range result.Applications {
		applications[i] = toSupplierApplicationResponse(&result.Applications[i])
	}
	c.JSON(http.StatusOK, SupplierApplicationListResponse{
		Applications: applications,
		Total:        result.Total,
		Offset:       result.Offset,
		Limit:        result.Limit,
	})
}

func (h *IAMHandler) PostAdminSupplierApplicationApprove(c *gin.Context) {
	applicationID, ok := parseSupplierApplicationIDParam(c)
	if !ok {
		return
	}
	operatorID, _ := middleware.GetCurrentUserID(c)
	application, err := h.module.SupplierApplicationUseCase.Approve(
		c.Request.Context(),
		operatorID,
		middleware.GetRequestID(c),
		c.FullPath(),
		applicationID,
	)
	if err != nil {
		writeError(c, err)
		return
	}
	resp := toSupplierApplicationResponse(application)
	c.JSON(http.StatusOK, gin.H{"application": resp})
}

func (h *IAMHandler) PostAdminSupplierApplicationReject(c *gin.Context) {
	applicationID, ok := parseSupplierApplicationIDParam(c)
	if !ok {
		return
	}

	var req AdminRejectSupplierApplicationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	operatorID, _ := middleware.GetCurrentUserID(c)
	application, err := h.module.SupplierApplicationUseCase.Reject(
		c.Request.Context(),
		operatorID,
		middleware.GetRequestID(c),
		c.FullPath(),
		applicationID,
		req.ReviewReason,
	)
	if err != nil {
		writeError(c, err)
		return
	}
	resp := toSupplierApplicationResponse(application)
	c.JSON(http.StatusOK, gin.H{"application": resp})
}

// PATCH /v1/admin/users/:userId
func (h *IAMHandler) PatchAdminUser(c *gin.Context) {
	userIDStr := c.Param("userId")
	targetUserID, err := strconv.ParseUint(userIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid user ID.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	var req AdminUpdateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	var role *domain.Role
	if req.Role != nil {
		parsedRole := domain.Role(strings.TrimSpace(*req.Role))
		role = &parsedRole
	}

	updateReq := &app.UpdateUserRequest{
		Email:       req.Email,
		Nickname:    req.Nickname,
		Password:    req.Password,
		Enabled:     req.Enabled,
		Role:        role,
		UserGroupID: req.UserGroupID,
	}

	operatorID, _ := middleware.GetCurrentUserID(c)
	allowSensitive, ok := h.currentUserHasPermission(c, "iam:permission", "sensitive")
	if !ok {
		return
	}
	user, err := h.module.AdminUseCase.UpdateUser(c.Request.Context(), operatorID, middleware.GetRequestID(c), c.FullPath(), uint(targetUserID), updateReq, allowSensitive)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"user": toUserResponse(user)})
}

// POST /v1/admin/users
func (h *IAMHandler) PostAdminUser(c *gin.Context) {
	var req AdminCreateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	operatorID, _ := middleware.GetCurrentUserID(c)
	allowSensitive, ok := h.currentUserHasPermission(c, "iam:permission", "sensitive")
	if !ok {
		return
	}
	user, err := h.module.AdminUseCase.CreateUser(c.Request.Context(), operatorID, middleware.GetRequestID(c), c.FullPath(), app.CreateUserRequest{
		Email:       req.Email,
		Nickname:    req.Nickname,
		Password:    req.Password,
		Role:        domain.Role(strings.TrimSpace(req.Role)),
		UserGroupID: req.UserGroupID,
	}, allowSensitive)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusCreated, gin.H{"user": toUserResponse(user)})
}

// DELETE /v1/admin/users/:userId
func (h *IAMHandler) DeleteAdminUser(c *gin.Context) {
	targetUserID, ok := parseUserIDParam(c)
	if !ok {
		return
	}
	operatorID, _ := middleware.GetCurrentUserID(c)
	if err := h.module.AdminUseCase.DeleteUser(c.Request.Context(), operatorID, middleware.GetRequestID(c), c.FullPath(), targetUserID); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// GET /v1/admin/users/:userId/invitations
func (h *IAMHandler) GetAdminUserInvitations(c *gin.Context) {
	targetUserID, ok := parseUserIDParam(c)
	if !ok {
		return
	}
	overview, err := h.module.AdminUseCase.Invitations(c.Request.Context(), targetUserID)
	if err != nil {
		writeError(c, err)
		return
	}
	resp := AdminUserInvitationsResponse{Invitees: make([]AdminUserInvitationMember, 0, len(overview.Invitees))}
	if overview.Inviter != nil {
		member := toAdminUserInvitationMember(*overview.Inviter)
		resp.Inviter = &member
	}
	for _, invitee := range overview.Invitees {
		resp.Invitees = append(resp.Invitees, toAdminUserInvitationMember(invitee))
	}
	c.JSON(http.StatusOK, resp)
}

// POST /v1/admin/users/:userId/sessions/revoke
func (h *IAMHandler) PostAdminRevokeSessions(c *gin.Context) {
	userIDStr := c.Param("userId")
	targetUserID, err := strconv.ParseUint(userIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid user ID.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	operatorID, _ := middleware.GetCurrentUserID(c)
	if err := h.module.AdminUseCase.ForceLogout(c.Request.Context(), operatorID, middleware.GetRequestID(c), c.FullPath(), uint(targetUserID)); err != nil {
		writeError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

// --- Admin bulk user actions (selection-based) ---

// POST /v1/admin/users/enable
func (h *IAMHandler) PostAdminUsersEnable(c *gin.Context) {
	sel, ok := h.bindAdminUserBulkSelection(c)
	if !ok {
		return
	}
	operatorID, _ := middleware.GetCurrentUserID(c)
	result, err := h.module.AdminUseCase.BulkSetEnabled(c.Request.Context(), operatorID, middleware.GetRequestID(c), c.FullPath(), sel, true)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAdminUserBulkResponse(result))
}

// POST /v1/admin/users/disable
func (h *IAMHandler) PostAdminUsersDisable(c *gin.Context) {
	sel, ok := h.bindAdminUserBulkSelection(c)
	if !ok {
		return
	}
	operatorID, _ := middleware.GetCurrentUserID(c)
	result, err := h.module.AdminUseCase.BulkSetEnabled(c.Request.Context(), operatorID, middleware.GetRequestID(c), c.FullPath(), sel, false)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAdminUserBulkResponse(result))
}

// POST /v1/admin/users/delete
func (h *IAMHandler) PostAdminUsersDelete(c *gin.Context) {
	sel, ok := h.bindAdminUserBulkSelection(c)
	if !ok {
		return
	}
	operatorID, _ := middleware.GetCurrentUserID(c)
	result, err := h.module.AdminUseCase.BulkDeleteUsers(c.Request.Context(), operatorID, middleware.GetRequestID(c), c.FullPath(), sel)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAdminUserBulkResponse(result))
}

// POST /v1/admin/users/sessions/revoke
func (h *IAMHandler) PostAdminUsersRevokeSessions(c *gin.Context) {
	sel, ok := h.bindAdminUserBulkSelection(c)
	if !ok {
		return
	}
	operatorID, _ := middleware.GetCurrentUserID(c)
	result, err := h.module.AdminUseCase.BulkRevokeSessions(c.Request.Context(), operatorID, middleware.GetRequestID(c), c.FullPath(), sel)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAdminUserBulkResponse(result))
}

// bindAdminUserBulkSelection binds the bulk command body and converts it to an
// app selection. Writes a 400 and returns false on a malformed body or role.
// The optional Idempotency-Key header is tolerated (declared in the spec) but
// not enforced here.
func (h *IAMHandler) bindAdminUserBulkSelection(c *gin.Context) (app.UserBulkSelection, bool) {
	var req AdminUserBulkCommandRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return app.UserBulkSelection{}, false
	}
	sel := app.UserBulkSelection{Mode: req.Selection.Mode}
	if req.Selection.Mode == "ids" {
		sel.UserIDs = req.Selection.UserIDs
		return sel, true
	}
	filter := domain.UserListFilter{}
	f := req.Selection.Filter
	if f == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"requestId": middleware.GetRequestID(c),
		})
		return app.UserBulkSelection{}, false
	}
	if raw := strings.TrimSpace(f.Role); raw != "" {
		role := domain.Role(raw)
		if !role.IsValid() {
			c.JSON(http.StatusBadRequest, gin.H{
				"message":   "Invalid request body.",
				"requestId": middleware.GetRequestID(c),
			})
			return app.UserBulkSelection{}, false
		}
		filter.Role = &role
	}
	filter.Search = strings.TrimSpace(f.Search)
	filter.Enabled = f.Enabled
	if f.UserGroupID != 0 {
		groupID := f.UserGroupID
		filter.UserGroupID = &groupID
	}
	filter.CreatedFrom = f.CreatedFrom
	filter.CreatedTo = f.CreatedTo
	sel.Filter = filter
	return sel, true
}

func toAdminUserBulkResponse(result *app.BulkResult) AdminUserBulkResponse {
	return AdminUserBulkResponse{Requested: result.Requested, Affected: result.Affected, Skipped: result.Skipped}
}

// --- Helpers ---

func parseUserIDParam(c *gin.Context) (uint, bool) {
	userIDStr := c.Param("userId")
	targetUserID, err := strconv.ParseUint(userIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid user ID.",
			"requestId": middleware.GetRequestID(c),
		})
		return 0, false
	}
	return uint(targetUserID), true
}

func parseSupplierApplicationIDParam(c *gin.Context) (uint, bool) {
	idStr := c.Param("applicationId")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid supplier application ID.",
			"requestId": middleware.GetRequestID(c),
		})
		return 0, false
	}
	return uint(id), true
}

func parsePagination(c *gin.Context) (int, int, bool) {
	return middleware.ParsePagination(c, middleware.PaginationOptions{
		DefaultLimit: 20,
		MaxLimit:     1000,
	})
}

func (h *IAMHandler) currentUserHasPermission(c *gin.Context, resource, action string) (bool, bool) {
	userID, userOK := middleware.GetCurrentUserID(c)
	role, roleOK := middleware.GetCurrentRole(c)
	if !userOK || !roleOK {
		c.JSON(http.StatusUnauthorized, gin.H{
			"message":   "Authentication is required.",
			"requestId": middleware.GetRequestID(c),
		})
		return false, false
	}
	if h == nil || h.module == nil || h.module.PermissionChecker == nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"message":   "An unexpected error occurred.",
			"requestId": middleware.GetRequestID(c),
		})
		return false, false
	}
	allowed, err := h.module.PermissionChecker.Check(c.Request.Context(), userID, role, resource, action)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"message":   "An unexpected error occurred.",
			"requestId": middleware.GetRequestID(c),
		})
		return false, false
	}
	return allowed, true
}

func parseUintQueryList(c *gin.Context, name string) ([]uint, bool) {
	values := c.QueryArray(name)
	if len(values) == 0 {
		return nil, true
	}
	var result []uint
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			value, err := strconv.ParseUint(part, 10, 64)
			if err != nil || value == 0 {
				c.JSON(http.StatusBadRequest, gin.H{
					"message":   "Invalid query parameters.",
					"requestId": middleware.GetRequestID(c),
				})
				return nil, false
			}
			result = append(result, uint(value))
		}
	}
	return result, true
}

func (h *IAMHandler) userResponseWithPermissions(ctx context.Context, user *domain.User) UserResponse {
	resp := toUserResponse(user)
	if user == nil || h == nil || h.module == nil || h.module.PermissionChecker == nil || h.module.AdminUseCase == nil {
		return resp
	}
	catalog := h.module.AdminUseCase.ListPermissions(ctx)
	permissions := make([]string, 0)
	for _, item := range catalog {
		for _, action := range item.Actions {
			allowed, err := h.module.PermissionChecker.Check(ctx, user.ID, user.Role, item.Resource, action)
			if err != nil || !allowed {
				continue
			}
			permissions = append(permissions, item.Resource+":"+action)
		}
	}
	resp.Permissions = permissions
	return resp
}

// writeError maps domain errors to HTTP responses.
func writeError(c *gin.Context, err error) {
	rid := middleware.GetRequestID(c)

	switch {
	case errors.Is(err, domain.ErrEmailAlreadyExists):
		c.JSON(http.StatusConflict, gin.H{
			"message":   "Email already exists.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrRegistrationEmailLocalInvalid):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Email local part must contain only letters and digits.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrRegistrationEmailDomainBlocked):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Registration with this email domain is not allowed.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrAccountOrPasswordIncorrect):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Account or password is incorrect.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrTurnstileInvalid):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Human verification failed.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrTurnstileUnavailable):
		slog.Warn("turnstile unavailable", "request_id", rid, "error", err.Error())
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"message":   "Human verification is temporarily unavailable.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrEmailCodeThrottled):
		retryAfter := app.EmailCodeResendGapSeconds
		var throttled *domain.EmailCodeThrottledError
		if errors.As(err, &throttled) && throttled.RetryAfterSeconds > 0 {
			retryAfter = throttled.RetryAfterSeconds
		}
		c.Header("Retry-After", strconv.Itoa(retryAfter))
		c.JSON(http.StatusTooManyRequests, gin.H{
			"message":   "Please wait before requesting another verification code.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrAuthenticationRequired):
		c.JSON(http.StatusUnauthorized, gin.H{
			"message":   "Authentication is required.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrPermissionDenied):
		c.JSON(http.StatusForbidden, gin.H{
			"message":   "Permission denied.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrActivationAlreadyDone):
		c.JSON(http.StatusConflict, gin.H{
			"message":   "Activation has already been completed.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrUserDisabled):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Account has been disabled.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrInvalidPassword):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Account or password is incorrect.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrUserNotFound):
		c.JSON(http.StatusNotFound, gin.H{
			"message":   "Resource not found.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrInvalidRole):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Invalid role.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrInvalidUserGroup):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Invalid user group.",
			"requestId": rid,
		})
	case errors.Is(err, maildomain.ErrOutboundIdempotencyConflict):
		c.JSON(http.StatusConflict, gin.H{
			"message":   "Idempotency key conflicts with another request.",
			"requestId": rid,
		})
	case errors.Is(err, maildomain.ErrDeliveryUnavailable):
		slog.Warn("mail delivery unavailable", "request_id", rid, "error", err.Error())
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"message":   "Mail service is temporarily unavailable.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrVerificationCodeIncorrect):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Verification code is incorrect or expired.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrInviteAlreadyExists):
		c.JSON(http.StatusConflict, gin.H{
			"message":   "Invite already exists.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrInviteNotFound):
		c.JSON(http.StatusNotFound, gin.H{
			"message":   "Resource not found.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrInviteInvalid):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Invitation code is invalid or expired.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrInvalidPermissionPolicy):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Invalid permission policy.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrSupplierApplicationAlreadyReviewing):
		c.JSON(http.StatusConflict, gin.H{
			"message":   "Supplier application is already under review.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrSupplierApplicationNotFound):
		c.JSON(http.StatusNotFound, gin.H{
			"message":   "Supplier application not found.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrInvalidSupplierApplication):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Invalid supplier application.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrInvalidSupplierApplicationStatus):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Invalid supplier application status.",
			"requestId": rid,
		})
	default:
		slog.Error("iam request failed", "request_id", rid, "error", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{
			"message":   "An unexpected error occurred.",
			"requestId": rid,
		})
	}
}

func setAuthCookies(c *gin.Context, sessionID, csrfToken string, maxAge int, secure bool) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(middleware.SessionCookieName, sessionID, maxAge, "/", "", secure, true)
	c.SetCookie(middleware.CSRFCookieName, csrfToken, maxAge, "/", "", secure, false)
}

func clearAuthCookies(c *gin.Context, secure bool) {
	middleware.ClearAuthCookies(c, secure)
}

func newCSRFToken() (string, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("generate csrf token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(token[:]), nil
}

// validationErrors deliberately keeps parser and validator internals out of
// public responses while preserving the existing fields.body contract.
func validationErrors(error) map[string]string {
	return map[string]string{"body": "Invalid request body."}
}
