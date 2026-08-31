package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

type trustedBotDispatchContextKey struct{}

// WithTrustedBotDispatch marks an in-process request that already passed the
// external connection's pre-authentication IP limit. It is not settable over HTTP.
func WithTrustedBotDispatch(request *http.Request) *http.Request {
	return request.WithContext(context.WithValue(request.Context(), trustedBotDispatchContextKey{}, true))
}

var rateLimitScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('EXPIRE', KEYS[1], ARGV[2])
end
if count <= tonumber(ARGV[1]) then return 0 end
local ttl = redis.call('TTL', KEYS[1])
if ttl < 1 then return 1 end
return ttl
`)

// RateLimitPerUser caps how often one account may call a single route, counting
// in a fixed window in Redis.
//
// It exists for routes where a challenge is not an option — the API-key surface
// cannot render a Turnstile widget, so the only way to bound a guessable or
// money-moving endpoint there is to limit the caller. Keyed on the user, not the
// API key, so minting extra keys buys no extra budget.
//
// Fails closed when Redis is unreachable: accepting an uncounted money-moving
// request would silently remove the protection this middleware exists for.
func RateLimitPerUser(rdb redis.UniversalClient, scope string, limit, windowSeconds int) gin.HandlerFunc {
	return rateLimitPerID(rdb, "ratelimit:"+scope+":", limit, windowSeconds, GetCurrentUserID)
}

// RateLimitPerSystemKey gives each third-party application its own budget.
func RateLimitPerSystemKey(rdb redis.UniversalClient, scope string, limit, windowSeconds int) gin.HandlerFunc {
	return rateLimitPerID(rdb, "ratelimit:system_key:"+scope+":", limit, windowSeconds, GetCurrentSystemKeyID)
}

// RateLimitPerClientIP protects authentication storage before a credential is
// known. It complements, rather than replaces, per-key and per-subject limits.
func RateLimitPerClientIP(rdb redis.UniversalClient, scope string, limit, windowSeconds int) gin.HandlerFunc {
	return func(c *gin.Context) {
		if trusted, _ := c.Request.Context().Value(trustedBotDispatchContextKey{}).(bool); trusted {
			c.Next()
			return
		}
		ip := strings.TrimSpace(c.ClientIP())
		sum := sha256.Sum256([]byte(ip))
		if takeRateLimit(c, rdb, "ratelimit:client_ip:"+scope+":"+hex.EncodeToString(sum[:]), limit, windowSeconds) {
			c.Next()
		}
	}
}

// RateLimitPerBotSubject gives one platform user a stable budget across Bot
// System Key rotations. The opaque platform subject is hashed before it enters
// Redis keys.
func RateLimitPerBotSubject(rdb redis.UniversalClient, scope string, limit, windowSeconds int) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, ok := GetCurrentBotIdentity(c)
		if !ok {
			c.Next()
			return
		}
		sum := sha256.Sum256([]byte(identity.Platform + "\x00" + identity.SubjectNamespace + "\x00" + identity.Subject))
		if takeRateLimit(c, rdb, "ratelimit:bot_subject:"+scope+":"+hex.EncodeToString(sum[:]), limit, windowSeconds) {
			c.Next()
		}
	}
}

func rateLimitPerID(
	rdb redis.UniversalClient,
	keyPrefix string,
	limit, windowSeconds int,
	getID func(*gin.Context) (uint, bool),
) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := getID(c)
		if !ok {
			// No identity means this ran ahead of authentication, which would itself
			// reject the request; there is nothing to count either way.
			c.Next()
			return
		}
		key := keyPrefix + strconv.FormatUint(uint64(id), 10)
		if takeRateLimit(c, rdb, key, limit, windowSeconds) {
			c.Next()
		}
	}
}

func takeRateLimit(c *gin.Context, rdb redis.UniversalClient, key string, limit, windowSeconds int) bool {
	if rdb == nil {
		abortRateLimitUnavailable(c, nil)
		return false
	}
	retryAfter, err := rateLimitScript.Run(c.Request.Context(), rdb, []string{key}, limit, windowSeconds).Int()
	if err != nil {
		abortRateLimitUnavailable(c, err)
		return false
	}
	if retryAfter > 0 {
		c.Header("Retry-After", strconv.Itoa(retryAfter))
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
			"message":   "Too many requests.",
			"requestId": GetRequestID(c),
		})
		return false
	}
	return true
}

func abortRateLimitUnavailable(c *gin.Context, err error) {
	slog.Error("rate limiter unavailable", "request_id", GetRequestID(c), "error", err)
	c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
		"message":   "Service is temporarily unavailable.",
		"requestId": GetRequestID(c),
	})
}
