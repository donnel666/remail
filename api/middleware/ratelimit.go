package middleware

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

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
	return func(c *gin.Context) {
		userID, ok := GetCurrentUserID(c)
		if !ok {
			// No user means this ran ahead of authentication, which would itself
			// reject the request; there is nothing to count either way.
			c.Next()
			return
		}
		if rdb == nil {
			abortRateLimitUnavailable(c, nil)
			return
		}

		key := "ratelimit:" + scope + ":" + strconv.FormatUint(uint64(userID), 10)
		retryAfter, err := rateLimitScript.Run(c.Request.Context(), rdb, []string{key}, limit, windowSeconds).Int()
		if err != nil {
			abortRateLimitUnavailable(c, err)
			return
		}
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"message":   "Too many requests.",
				"requestId": GetRequestID(c),
			})
			return
		}

		c.Next()
	}
}

func abortRateLimitUnavailable(c *gin.Context, err error) {
	slog.Error("rate limiter unavailable", "request_id", GetRequestID(c), "error", err)
	c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
		"message":   "Service is temporarily unavailable.",
		"requestId": GetRequestID(c),
	})
}
