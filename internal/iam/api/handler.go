package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/iam/app"
	"github.com/donnel666/remail/internal/iam/domain"
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

	c.JSON(http.StatusCreated, gin.H{"user": toUserResponse(user)})
}

// --- Captcha ---

// POST /v1/captchas
func (h *IAMHandler) PostCaptcha(c *gin.Context) {
	result, err := h.module.CaptchaUseCase.Create(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, CaptchaResponse{
		CaptchaID: result.CaptchaID,
		Image:     result.Image,
	})
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

	if err := h.module.EmailCodeUseCase.Send(c.Request.Context(), req.Email); err != nil {
		writeError(c, err)
		return
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

	user, err := h.module.RegistrationUseCase.Register(
		c.Request.Context(), req.Email, req.Password, req.Nickname, req.CaptchaID, req.CaptchaAnswer, req.InviteCode,
	)
	if err != nil {
		writeError(c, err)
		return
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
	if err := h.module.PasswordResetUseCase.Request(c.Request.Context(), req.Email); err != nil {
		writeError(c, err)
		return
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
	if err := h.module.PasswordResetUseCase.Reset(c.Request.Context(), req.Email, req.Code, req.NewPassword); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// --- Login / Logout ---

// POST /v1/sessions
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

	result, err := h.module.LoginUseCase.Login(c.Request.Context(), req.Email, req.Password, req.CaptchaID, req.CaptchaAnswer, h.sessionMaxAge)
	if err != nil {
		writeError(c, err)
		return
	}

	// Set HttpOnly (and optionally Secure) session cookie
	c.SetCookie(
		"sid",
		result.Session.ID,
		h.sessionMaxAge,
		"/",
		"",
		h.sessionSecure,
		true, // HttpOnly
	)

	c.JSON(http.StatusOK, LoginResponse{User: toUserResponse(result.User)})
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

	c.SetCookie("sid", "", -1, "/", "", h.sessionSecure, true)
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

	c.JSON(http.StatusOK, gin.H{"user": toUserResponse(user)})
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

	c.SetCookie("sid", "", -1, "/", "", h.sessionSecure, true)
	c.Status(http.StatusNoContent)
}

// --- Admin ---

// GET /v1/admin/users
func (h *IAMHandler) GetAdminUsers(c *gin.Context) {
	offset, limit, ok := parsePagination(c)
	if !ok {
		return
	}

	result, err := h.module.AdminUseCase.ListUsers(c.Request.Context(), offset, limit)
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
	})
}

func (h *IAMHandler) GetAdminPermissions(c *gin.Context) {
	c.JSON(http.StatusOK, toPermissionCatalogResponse(h.module.AdminUseCase.ListPermissions(c.Request.Context())))
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
	if err := h.module.AdminUseCase.SaveUserPermissions(c.Request.Context(), operatorID, middleware.GetRequestID(c), c.FullPath(), targetUserID, policies); err != nil {
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
	result, err := h.module.AdminUseCase.ListInvites(c.Request.Context(), offset, limit)
	if err != nil {
		writeError(c, err)
		return
	}
	invites := make([]InviteResponse, len(result.Invites))
	for i := range result.Invites {
		invites[i] = toInviteResponse(&result.Invites[i])
	}
	c.JSON(http.StatusOK, InviteListResponse{Invites: invites, Total: result.Total, Offset: result.Offset, Limit: result.Limit})
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
	invite, err := h.module.AdminUseCase.UpdateInvite(c.Request.Context(), operatorID, middleware.GetRequestID(c), c.FullPath(), code, app.UpdateInviteRequest{
		Enabled:  req.Enabled,
		MaxUse:   req.MaxUse,
		ExpireAt: req.ExpireAt,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"invite": toInviteResponse(invite)})
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

	var roleLevel *domain.RoleLevel
	if req.RoleLevel != nil {
		rl := domain.RoleLevel(*req.RoleLevel)
		roleLevel = &rl
	}

	updateReq := &app.UpdateUserRequest{Enabled: req.Enabled, RoleLevel: roleLevel}

	operatorID, _ := middleware.GetCurrentUserID(c)
	user, err := h.module.AdminUseCase.UpdateUser(c.Request.Context(), operatorID, middleware.GetRequestID(c), c.FullPath(), uint(targetUserID), updateReq)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"user": toUserResponse(user)})
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

func parsePagination(c *gin.Context) (int, int, bool) {
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid query parameters.",
			"requestId": middleware.GetRequestID(c),
		})
		return 0, 0, false
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid query parameters.",
			"requestId": middleware.GetRequestID(c),
		})
		return 0, 0, false
	}
	return offset, limit, true
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
	case errors.Is(err, domain.ErrAccountOrPasswordIncorrect):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Account or password is incorrect.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrCaptchaIncorrect):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Captcha is incorrect or expired.",
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
	case errors.Is(err, domain.ErrInvalidRoleLevel):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Invalid role level.",
			"requestId": rid,
		})
	case errors.Is(err, domain.ErrMailServiceUnavailable):
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
	default:
		c.JSON(http.StatusInternalServerError, gin.H{
			"message":   "An unexpected error occurred.",
			"requestId": rid,
		})
	}
}

// validationErrors extracts field-level validation error messages.
func validationErrors(err error) map[string]string {
	type validator interface {
		Field() string
		Tag() string
	}

	fields := make(map[string]string)
	if errs, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range errs.Unwrap() {
			if v, ok := e.(validator); ok {
				fields[v.Field()] = v.Tag() + " validation failed"
			}
		}
	} else {
		fields["body"] = err.Error()
	}
	return fields
}
