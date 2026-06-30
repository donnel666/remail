package middleware

import (
	"context"
	"net/http"

	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
)

// context keys for storing authenticated user info
const (
	contextKeyUserID    = "iam_user_id"
	contextKeyRoleLevel = "iam_role_level"
	contextKeyEmail     = "iam_email"
	contextKeySessionID = "iam_session_id"
)

// SessionFetcher looks up a session and returns authenticated user details.
// Implementations should do a Redis lookup and return the session data.
type SessionFetcher interface {
	// FetchSession retrieves session data. Returns ok=false if invalid/expired.
	FetchSession(ctx context.Context, sessionID string) (userID uint, roleLevel domain.RoleLevel, email string, ok bool)
}

// SessionFetcherFunc is a function adapter for SessionFetcher.
type SessionFetcherFunc func(ctx context.Context, sessionID string) (uint, domain.RoleLevel, string, bool)

// FetchSession implements SessionFetcher.
func (f SessionFetcherFunc) FetchSession(ctx context.Context, sessionID string) (uint, domain.RoleLevel, string, bool) {
	return f(ctx, sessionID)
}

// PermissionChecker checks fine-grained permissions for authenticated users.
type PermissionChecker interface {
	Check(ctx context.Context, userID uint, roleLevel domain.RoleLevel, resource, action string) (bool, error)
}

// LoadSession returns a middleware that reads the "sid" cookie and populates
// the gin context with authenticated user info. If the cookie is missing or
// the session is invalid, the middleware simply continues without setting
// auth context (protected routes will use AuthRequired to reject).
func LoadSession(fetcher SessionFetcher) gin.HandlerFunc {
	return func(c *gin.Context) {
		sid, err := c.Cookie("sid")
		if err != nil || sid == "" {
			c.Next()
			return
		}

		userID, roleLevel, email, ok := fetcher.FetchSession(c.Request.Context(), sid)
		if !ok {
			c.Next()
			return
		}

		SetCurrentUser(c, userID, roleLevel, email, sid)
		c.Next()
	}
}

// AuthRequired returns a middleware that requires a valid session.
// Must be used after LoadSession (or equivalent) has run.
func AuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		_, ok := GetCurrentUserID(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"message":   "Authentication is required.",
				"requestId": GetRequestID(c),
			})
			return
		}
		c.Next()
	}
}

// AdminRequired returns a middleware that requires roleLevel >= minLevel.
// Must be used after LoadSession + AuthRequired.
func AdminRequired(minLevel domain.RoleLevel) gin.HandlerFunc {
	return func(c *gin.Context) {
		roleLevel, ok := GetCurrentRoleLevel(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"message":   "Authentication is required.",
				"requestId": GetRequestID(c),
			})
			return
		}

		if !roleLevel.IsAtLeast(minLevel) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"message":   "Permission denied.",
				"requestId": GetRequestID(c),
			})
			return
		}
		c.Next()
	}
}

// PermissionRequired requires a Casbin-backed permission.
// Must be used after LoadSession + AuthRequired.
func PermissionRequired(checker PermissionChecker, resource, action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := GetCurrentUserID(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"message":   "Authentication is required.",
				"requestId": GetRequestID(c),
			})
			return
		}

		roleLevel, ok := GetCurrentRoleLevel(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"message":   "Authentication is required.",
				"requestId": GetRequestID(c),
			})
			return
		}

		allowed, err := checker.Check(c.Request.Context(), userID, roleLevel, resource, action)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"message":   "An unexpected error occurred.",
				"requestId": GetRequestID(c),
			})
			return
		}
		if !allowed {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"message":   "Permission denied.",
				"requestId": GetRequestID(c),
			})
			return
		}

		c.Next()
	}
}

// SetCurrentUser stores authenticated user info in the gin context.
func SetCurrentUser(c *gin.Context, userID uint, roleLevel domain.RoleLevel, email, sessionID string) {
	c.Set(contextKeyUserID, userID)
	c.Set(contextKeyRoleLevel, int(roleLevel))
	c.Set(contextKeyEmail, email)
	c.Set(contextKeySessionID, sessionID)
}

// GetCurrentUserID returns the authenticated user's ID from the gin context.
func GetCurrentUserID(c *gin.Context) (uint, bool) {
	v, exists := c.Get(contextKeyUserID)
	if !exists {
		return 0, false
	}
	id, ok := v.(uint)
	return id, ok
}

// GetCurrentRoleLevel returns the authenticated user's role level.
func GetCurrentRoleLevel(c *gin.Context) (domain.RoleLevel, bool) {
	v, exists := c.Get(contextKeyRoleLevel)
	if !exists {
		return 0, false
	}
	level, ok := v.(int)
	if !ok {
		return 0, false
	}
	return domain.RoleLevel(level), true
}

// GetCurrentEmail returns the authenticated user's email.
func GetCurrentEmail(c *gin.Context) (string, bool) {
	v, exists := c.Get(contextKeyEmail)
	if !exists {
		return "", false
	}
	email, ok := v.(string)
	return email, ok
}

// GetCurrentSessionID returns the current session ID from the gin context.
func GetCurrentSessionID(c *gin.Context) (string, bool) {
	v, exists := c.Get(contextKeySessionID)
	if !exists {
		return "", false
	}
	sid, ok := v.(string)
	return sid, ok
}
