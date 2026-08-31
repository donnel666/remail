package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func rateLimitRouter(rdb redis.UniversalClient, userID uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/redeem",
		func(c *gin.Context) {
			if userID != 0 {
				SetCurrentUser(c, userID, iamdomain.RoleUser, "", "")
			}
			c.Next()
		},
		RateLimitPerUser(rdb, "open_card_redeem", 1, 60),
		func(c *gin.Context) { c.Status(http.StatusOK) },
	)
	return router
}

func postRedeem(router *gin.Engine) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/redeem", nil))
	return response
}

func TestRateLimitPerUserBlocksSecondCallInWindow(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	router := rateLimitRouter(rdb, 7)

	require.Equal(t, http.StatusOK, postRedeem(router).Code)
	require.Contains(t, server.Keys(), "ratelimit:open_card_redeem:7")

	blocked := postRedeem(router)
	require.Equal(t, http.StatusTooManyRequests, blocked.Code)
	require.Equal(t, "60", blocked.Header().Get("Retry-After"))

	// The window is fixed, so the budget returns only after it lapses.
	server.FastForward(60 * 1e9)
	require.Equal(t, http.StatusOK, postRedeem(router).Code)
}

func TestRateLimitPerUserCountsPerAccount(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	require.Equal(t, http.StatusOK, postRedeem(rateLimitRouter(rdb, 7)).Code)
	require.Equal(t, http.StatusTooManyRequests, postRedeem(rateLimitRouter(rdb, 7)).Code)
	// A different account has its own budget; extra API keys for the same
	// account would share one, since the key is the user.
	require.Equal(t, http.StatusOK, postRedeem(rateLimitRouter(rdb, 8)).Code)
}

func TestRateLimitPerUserFailsClosedWithoutRedis(t *testing.T) {
	require.Equal(t, http.StatusServiceUnavailable, postRedeem(rateLimitRouter(nil, 7)).Code)

	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	server.Close()

	require.Equal(t, http.StatusServiceUnavailable, postRedeem(rateLimitRouter(rdb, 7)).Code)
}

func systemKeyRateLimitRouter(rdb redis.UniversalClient, keyID uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/code",
		func(c *gin.Context) {
			c.Set(contextKeySystemKeyID, keyID)
			c.Next()
		},
		RateLimitPerSystemKey(rdb, "icloud_forwarding_email_read", 1, 60),
		func(c *gin.Context) { c.Status(http.StatusOK) },
	)
	return router
}

func getCode(router *gin.Engine) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/code", nil))
	return response
}

func TestRateLimitPerSystemKeyCountsEachApplicationSeparately(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	require.Equal(t, http.StatusOK, getCode(systemKeyRateLimitRouter(rdb, 7)).Code)
	blocked := getCode(systemKeyRateLimitRouter(rdb, 7))
	require.Equal(t, http.StatusTooManyRequests, blocked.Code)
	require.Equal(t, "60", blocked.Header().Get("Retry-After"))
	require.Equal(t, http.StatusOK, getCode(systemKeyRateLimitRouter(rdb, 8)).Code)
}

func botSubjectRateLimitRouter(rdb redis.UniversalClient, keyID uint, namespace, subject string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/redeem",
		func(c *gin.Context) {
			c.Set(contextKeySystemKeyID, keyID)
			c.Set(contextKeyBotIdentity, BotIdentity{
				BotIntegration: BotIntegration{SystemKeyID: keyID, Platform: "qq_official", SubjectNamespace: namespace},
				Subject:        subject, Scene: BotScenePrivate,
			})
			c.Next()
		},
		RateLimitPerBotSubject(rdb, "binding", 1, 60),
		func(c *gin.Context) { c.Status(http.StatusOK) },
	)
	return router
}

func TestRateLimitPerBotSubjectSurvivesKeyRotationAndHidesSubject(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	require.Equal(t, http.StatusOK, postRedeem(botSubjectRateLimitRouter(rdb, 7, "qq:main", "secret-openid")).Code)
	require.Equal(t, http.StatusTooManyRequests, postRedeem(botSubjectRateLimitRouter(rdb, 8, "qq:main", "secret-openid")).Code)
	require.Equal(t, http.StatusOK, postRedeem(botSubjectRateLimitRouter(rdb, 8, "qq:main", "another-openid")).Code)
	for _, key := range server.Keys() {
		require.NotContains(t, key, "secret-openid")
		require.NotContains(t, key, "another-openid")
	}
}

func TestRateLimitPerClientIPStopsWorkBeforeAuthentication(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	gin.SetMode(gin.TestMode)
	authCalls := 0
	router := gin.New()
	router.GET("/bot",
		RateLimitPerClientIP(rdb, "bot_auth_test", 1, 60),
		func(c *gin.Context) { authCalls++; c.Status(http.StatusUnauthorized) },
	)

	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/bot", nil))
	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/bot", nil))
	third := httptest.NewRecorder()
	router.ServeHTTP(third, WithTrustedBotDispatch(httptest.NewRequest(http.MethodGet, "/bot", nil)))

	require.Equal(t, http.StatusUnauthorized, first.Code)
	require.Equal(t, http.StatusTooManyRequests, second.Code)
	require.Equal(t, http.StatusUnauthorized, third.Code)
	require.Equal(t, 2, authCalls)
}
