package api

import (
	"context"

	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
)

// sessionFetcher resolves a session by looking up Redis and then verifying
// the user's current state from the DB. This ensures that disabled users,
// tokenVersion bumps, and role changes are caught on every request
// (docs/8-iam.md:122 — INV-I3: disable/force-logout must invalidate sessions).
type sessionFetcher struct {
	sessionStore interface {
		Get(ctx context.Context, sessionID string) (*domain.Session, error)
	}
	userRepo interface {
		FindByID(ctx context.Context, id uint) (*domain.User, error)
	}
}

// NewSessionFetcher creates a SessionFetcher for auth middleware.
func NewSessionFetcher(
	store interface {
		Get(ctx context.Context, sessionID string) (*domain.Session, error)
	},
	repo interface {
		FindByID(ctx context.Context, id uint) (*domain.User, error)
	},
) middleware.SessionFetcher {
	return &sessionFetcher{sessionStore: store, userRepo: repo}
}

func (f *sessionFetcher) FetchSession(ctx context.Context, sessionID string) (uint, domain.Role, string, bool) {
	sess, err := f.sessionStore.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return 0, "", "", false
	}

	// Re-verify against the DB on every request:
	// - If the user was disabled, reject (INV-I2).
	// - If TokenVersion was bumped (password change / force logout), reject (INV-I3).
	// - Use the current role from DB, not the cached snapshot from Redis,
	//   so role changes take effect immediately (docs/8-iam.md:123).
	user, err := f.userRepo.FindByID(ctx, sess.UserID)
	if err != nil || user == nil || !user.Enabled || user.TokenVersion != sess.TokenVersion {
		return 0, "", "", false
	}

	// Use current user data from DB, not the session snapshot
	return sess.UserID, user.Role, user.Email, true
}

// RegisterIAMRoutes registers all IAM routes on the given router group.
func RegisterIAMRoutes(rg *gin.RouterGroup, mod *IAMModule, sessionMaxAge int, sessionSecure bool) {
	h := NewIAMHandler(mod, sessionMaxAge, sessionSecure)
	fetcher := NewSessionFetcher(mod.SessionStore, mod.UserRepo)

	// Public routes (no authentication required)
	rg.GET("/activation", h.GetActivation)
	rg.POST("/activation", h.PostActivation)
	rg.POST("/captchas", h.PostCaptcha)
	rg.POST("/email/code", h.PostEmailCode)
	rg.POST("/users", h.PostRegister)
	rg.POST("/login", h.PostLogin)
	rg.POST("/password/reset/request", h.PostPasswordResetRequest)
	rg.POST("/password/reset", h.PostPasswordReset)

	// Authenticated routes
	auth := rg.Group("")
	auth.Use(middleware.LoadSession(fetcher))
	auth.Use(middleware.AuthRequired())
	auth.Use(middleware.CSRFRequired())
	{
		auth.GET("/me", h.GetMe)
		auth.GET("/me/invite", h.GetMeInvite)
		auth.POST("/me/invite", h.PostMeInvite)
		auth.DELETE("/sessions/current", h.DeleteSession)
		auth.PATCH("/password", h.PatchPassword)
		auth.POST("/suppliers/applications", h.PostSupplierApplication)
		auth.GET("/suppliers/applications/current", h.GetCurrentSupplierApplication)
	}

	// Admin routes are authorized by Casbin RBAC permissions.
	admin := rg.Group("/admin")
	admin.Use(middleware.LoadSession(fetcher))
	admin.Use(middleware.AuthRequired())
	admin.Use(middleware.CSRFRequired())
	{
		admin.GET("/users", middleware.PermissionRequired(mod.PermissionChecker, "iam:user", "read"), h.GetAdminUsers)
		admin.POST("/users", middleware.PermissionRequired(mod.PermissionChecker, "iam:user", "write"), h.PostAdminUser)
		admin.PATCH("/users/:userId", middleware.PermissionRequired(mod.PermissionChecker, "iam:user", "write"), h.PatchAdminUser)
		admin.DELETE("/users/:userId", middleware.PermissionRequired(mod.PermissionChecker, "iam:user", "operate"), h.DeleteAdminUser)
		admin.GET("/users/:userId/invitations", middleware.PermissionRequired(mod.PermissionChecker, "iam:user", "read"), h.GetAdminUserInvitations)
		admin.POST("/users/:userId/sessions/revoke", middleware.PermissionRequired(mod.PermissionChecker, "iam:user", "operate"), h.PostAdminRevokeSessions)
		// Selection-based bulk actions. Static /users/<verb> paths coexist with the
		// /users/:userId param routes above (gin supports static+param siblings).
		admin.POST("/users/enable", middleware.PermissionRequired(mod.PermissionChecker, "iam:user", "operate"), h.PostAdminUsersEnable)
		admin.POST("/users/disable", middleware.PermissionRequired(mod.PermissionChecker, "iam:user", "operate"), h.PostAdminUsersDisable)
		admin.POST("/users/delete", middleware.PermissionRequired(mod.PermissionChecker, "iam:user", "operate"), h.PostAdminUsersDelete)
		admin.POST("/users/sessions/revoke", middleware.PermissionRequired(mod.PermissionChecker, "iam:user", "operate"), h.PostAdminUsersRevokeSessions)
		admin.GET("/users/groups", middleware.PermissionRequired(mod.PermissionChecker, "iam:user_group", "read"), h.GetAdminUserGroups)
		admin.POST("/users/groups", middleware.PermissionRequired(mod.PermissionChecker, "iam:user_group", "write"), h.PostAdminUserGroup)
		admin.PATCH("/users/groups/:groupId", middleware.PermissionRequired(mod.PermissionChecker, "iam:user_group", "write"), h.PatchAdminUserGroup)
		admin.GET("/permissions", middleware.PermissionRequired(mod.PermissionChecker, "iam:permission", "read"), h.GetAdminPermissions)
		admin.GET("/users/:userId/permissions", middleware.PermissionRequired(mod.PermissionChecker, "iam:permission", "read"), h.GetAdminUserPermissions)
		admin.PUT("/users/:userId/permissions", middleware.PermissionRequired(mod.PermissionChecker, "iam:permission", "write"), h.PutAdminUserPermissions)
		admin.GET("/invites", middleware.PermissionRequired(mod.PermissionChecker, "iam:invite", "read"), h.GetAdminInvites)
		admin.POST("/invites", middleware.PermissionRequired(mod.PermissionChecker, "iam:invite", "write"), h.PostAdminInvite)
		admin.POST("/invites/batch", middleware.PermissionRequired(mod.PermissionChecker, "iam:invite", "write"), h.PostAdminInvitesBatch)
		admin.POST("/invites/enable", middleware.PermissionRequired(mod.PermissionChecker, "iam:invite", "write"), h.PostAdminInvitesEnable)
		admin.POST("/invites/disable", middleware.PermissionRequired(mod.PermissionChecker, "iam:invite", "write"), h.PostAdminInvitesDisable)
		admin.GET("/invites/:code/uses", middleware.PermissionRequired(mod.PermissionChecker, "iam:invite", "read"), h.GetAdminInviteUses)
		admin.PATCH("/invites/:code", middleware.PermissionRequired(mod.PermissionChecker, "iam:invite", "operate"), h.PatchAdminInvite)
		admin.GET("/suppliers/applications", middleware.PermissionRequired(mod.PermissionChecker, "iam:supplier_application", "read"), h.GetAdminSupplierApplications)
		admin.POST("/suppliers/applications/:applicationId/approve", middleware.PermissionRequired(mod.PermissionChecker, "iam:supplier_application", "operate"), h.PostAdminSupplierApplicationApprove)
		admin.POST("/suppliers/applications/:applicationId/reject", middleware.PermissionRequired(mod.PermissionChecker, "iam:supplier_application", "operate"), h.PostAdminSupplierApplicationReject)
	}
}
