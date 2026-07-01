package api

import (
	"context"

	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
)

// sessionFetcher resolves a session by looking up Redis and then verifying
// the user's current state from the DB. This ensures that disabled users,
// tokenVersion bumps, and roleLevel changes are caught on every request
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

func (f *sessionFetcher) FetchSession(ctx context.Context, sessionID string) (uint, domain.RoleLevel, string, bool) {
	sess, err := f.sessionStore.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return 0, 0, "", false
	}

	// Re-verify against the DB on every request:
	// - If the user was disabled, reject (INV-I2).
	// - If TokenVersion was bumped (password change / force logout), reject (INV-I3).
	// - Use the current roleLevel from DB, not the cached snapshot from Redis,
	//   so role changes take effect immediately (docs/8-iam.md:123).
	user, err := f.userRepo.FindByID(ctx, sess.UserID)
	if err != nil || user == nil || !user.Enabled || user.TokenVersion != sess.TokenVersion {
		return 0, 0, "", false
	}

	// Use current user data from DB, not the session snapshot
	return sess.UserID, user.RoleLevel, user.Email, true
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
	rg.POST("/sessions", h.PostLogin)
	rg.POST("/password/reset/request", h.PostPasswordResetRequest)
	rg.POST("/password/reset", h.PostPasswordReset)

	// Authenticated routes
	auth := rg.Group("")
	auth.Use(middleware.LoadSession(fetcher))
	auth.Use(middleware.AuthRequired())
	auth.Use(middleware.CSRFRequired())
	{
		auth.GET("/me", h.GetMe)
		auth.DELETE("/sessions/current", h.DeleteSession)
		auth.PATCH("/password", h.PatchPassword)
	}

	// Admin routes require both the role baseline and Casbin command permission.
	admin := rg.Group("/admin")
	admin.Use(middleware.LoadSession(fetcher))
	admin.Use(middleware.AuthRequired())
	admin.Use(middleware.CSRFRequired())
	admin.Use(middleware.AdminRequired(domain.RoleAdmin))
	{
		admin.GET("/users", middleware.PermissionRequired(mod.PermissionChecker, "iam:user", "read"), h.GetAdminUsers)
		admin.PATCH("/users/:userId", middleware.PermissionRequired(mod.PermissionChecker, "iam:user", "write"), h.PatchAdminUser)
		admin.POST("/users/:userId/sessions/revoke", middleware.PermissionRequired(mod.PermissionChecker, "iam:user", "operate"), h.PostAdminRevokeSessions)
		admin.GET("/permissions", middleware.PermissionRequired(mod.PermissionChecker, "iam:permission", "read"), h.GetAdminPermissions)
		admin.GET("/users/:userId/permissions", middleware.PermissionRequired(mod.PermissionChecker, "iam:permission", "read"), h.GetAdminUserPermissions)
		admin.PUT("/users/:userId/permissions", middleware.PermissionRequired(mod.PermissionChecker, "iam:permission", "write"), h.PutAdminUserPermissions)
		admin.GET("/invites", middleware.PermissionRequired(mod.PermissionChecker, "iam:invite", "read"), h.GetAdminInvites)
		admin.POST("/invites", middleware.PermissionRequired(mod.PermissionChecker, "iam:invite", "write"), h.PostAdminInvite)
		admin.PATCH("/invites/:code", middleware.PermissionRequired(mod.PermissionChecker, "iam:invite", "operate"), h.PatchAdminInvite)
	}
}
