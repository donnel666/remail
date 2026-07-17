package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
)

// context keys for storing authenticated user info
const (
	contextKeyUserID    = "iam_user_id"
	contextKeyRole      = "iam_role"
	contextKeyEmail     = "iam_email"
	contextKeySessionID = "iam_session_id"
)

// SessionFetcher looks up a session and returns authenticated user details.
// Implementations should do a Redis lookup and return the session data.
type SessionFetcher interface {
	// FetchSession retrieves session data. Returns ok=false if invalid/expired.
	FetchSession(ctx context.Context, sessionID string) (userID uint, role domain.Role, email string, ok bool)
}

// SessionFetcherFunc is a function adapter for SessionFetcher.
type SessionFetcherFunc func(ctx context.Context, sessionID string) (uint, domain.Role, string, bool)

// FetchSession implements SessionFetcher.
func (f SessionFetcherFunc) FetchSession(ctx context.Context, sessionID string) (uint, domain.Role, string, bool) {
	return f(ctx, sessionID)
}

// PermissionChecker checks fine-grained permissions for authenticated users.
type PermissionChecker interface {
	Check(ctx context.Context, userID uint, role domain.Role, resource, action string) (bool, error)
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

		userID, role, email, ok := fetcher.FetchSession(c.Request.Context(), sid)
		if !ok {
			clearRequestAuthCookies(c)
			c.Next()
			return
		}

		SetCurrentUser(c, userID, role, email, sid)
		c.Next()
	}
}

// AuthRequired returns a middleware that requires a valid session.
// Must be used after LoadSession (or equivalent) has run.
func AuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		_, ok := GetCurrentUserID(c)
		if !ok {
			clearRequestAuthCookies(c)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"message":   "Authentication is required.",
				"requestId": GetRequestID(c),
			})
			return
		}
		c.Next()
	}
}

// RoleRequired returns a middleware that requires one of the given RBAC roles.
// Prefer PermissionRequired for resource-level authorization.
func RoleRequired(allowedRoles ...domain.Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, ok := GetCurrentRole(c)
		if !ok {
			clearRequestAuthCookies(c)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"message":   "Authentication is required.",
				"requestId": GetRequestID(c),
			})
			return
		}

		allowed := false
		for _, allowedRole := range allowedRoles {
			if role == allowedRole {
				allowed = true
				break
			}
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

// PermissionRequired requires a Casbin-backed permission.
// Must be used after LoadSession + AuthRequired.
func PermissionRequired(checker PermissionChecker, resource, action string) gin.HandlerFunc {
	return permissionGuard(checker, [][2]string{{resource, action}})
}

// PermissionRequiredAny requires any one of the given resource/action pairs.
// Used when the frontend gates an action on an OR of permission keys (e.g. a
// support action allowed to either user operators or order operators).
func PermissionRequiredAny(checker PermissionChecker, pairs ...[2]string) gin.HandlerFunc {
	return permissionGuard(checker, pairs)
}

func permissionGuard(checker PermissionChecker, pairs [][2]string) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := GetCurrentUserID(c)
		if !ok {
			clearRequestAuthCookies(c)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"message":   "Authentication is required.",
				"requestId": GetRequestID(c),
			})
			return
		}

		role, ok := GetCurrentRole(c)
		if !ok {
			clearRequestAuthCookies(c)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"message":   "Authentication is required.",
				"requestId": GetRequestID(c),
			})
			return
		}

		for _, pair := range pairs {
			allowed, err := checker.Check(c.Request.Context(), userID, role, pair[0], pair[1])
			if err != nil {
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"message":   "An unexpected error occurred.",
					"requestId": GetRequestID(c),
				})
				return
			}
			if allowed {
				c.Next()
				return
			}
		}

		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"message":   "Permission denied.",
			"requestId": GetRequestID(c),
		})
	}
}

func ClearAuthCookies(c *gin.Context, secure bool) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(SessionCookieName, "", -1, "/", "", secure, true)
	c.SetCookie(CSRFCookieName, "", -1, "/", "", secure, false)
}

func clearRequestAuthCookies(c *gin.Context) {
	ClearAuthCookies(c, requestIsSecure(c))
}

func requestIsSecure(c *gin.Context) bool {
	if c.Request != nil && c.Request.TLS != nil {
		return true
	}
	return strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") ||
		strings.EqualFold(c.GetHeader("X-Forwarded-Ssl"), "on")
}

// SetCurrentUser stores authenticated user info in the gin context.
func SetCurrentUser(c *gin.Context, userID uint, role domain.Role, email, sessionID string) {
	c.Set(contextKeyUserID, userID)
	c.Set(contextKeyRole, role)
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

// GetCurrentRole returns the authenticated user's RBAC role.
func GetCurrentRole(c *gin.Context) (domain.Role, bool) {
	v, exists := c.Get(contextKeyRole)
	if !exists {
		return "", false
	}
	role, ok := v.(domain.Role)
	if ok {
		return role, true
	}
	roleString, ok := v.(string)
	if !ok {
		return "", false
	}
	return domain.Role(roleString), true
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
