package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/api/middleware"
	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

type botRouterAuthenticator func(context.Context, string) (*settingsdomain.SystemKey, error)

func (f botRouterAuthenticator) AuthenticateBotSystemKey(ctx context.Context, key string) (*settingsdomain.SystemKey, error) {
	return f(ctx, key)
}

func TestBotRoutesRequireBotSystemKeyAndMatchContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	redisServer := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	registerBotRoutes(router.Group("/v1"), router, botRouterAuthenticator(func(context.Context, string) (*settingsdomain.SystemKey, error) {
		return nil, settingsdomain.ErrInvalidSystemKey
	}), nil, nil, nil, nil, nil, rdb)

	got := make([]string, 0, 12)
	for _, route := range router.Routes() {
		got = append(got, route.Method+" "+route.Path)
	}
	sort.Strings(got)
	want := []string{
		"DELETE /v1/bot/binding",
		"GET /v1/bot/binding",
		"GET /v1/bot/context",
		"GET /v1/bot/projects",
		"GET /v1/bot/projects/:projectId",
		"GET /v1/bot/projects/:projectId/inventory",
		"GET /v1/bot/rankings/orders",
		"GET /v1/bot/rankings/rewards/latest",
		"GET /v1/bot/ws",
		"POST /v1/bot/bindings",
		"POST /v1/bot/diagnoses/code",
	}
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("bot routes = %v, want %v", got, want)
	}

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/bot/rankings/orders", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated bot route status = %d, want 401", response.Code)
	}
}

func TestBotUserResolverTreatsGroupAsPublicWithoutReadingBinding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	auth := botRouterAuthenticator(func(context.Context, string) (*settingsdomain.SystemKey, error) {
		return &settingsdomain.SystemKey{
			ID: 9, Purpose: settingsdomain.SystemKeyPurposeBot,
			Platform: "aiocqhttp", SubjectNamespace: "qq:main", AllowedGroupIDs: []string{"10001"},
		}, nil
	})
	router.GET("/resolve", middleware.BotSystemKeyRequired(auth), middleware.BotIdentityRequired(), func(c *gin.Context) {
		viewer, ok := botUserResolver(nil)(c)
		c.JSON(http.StatusOK, gin.H{"userId": viewer.UserID, "ok": ok})
	})

	request := httptest.NewRequest(http.MethodGet, "/resolve", nil)
	request.Header.Set(middleware.SystemKeyHeaderName, "sk_test")
	request.Header.Set(middleware.BotSubjectHeaderName, "123456789")
	request.Header.Set(middleware.BotSceneHeaderName, middleware.BotSceneGroup)
	request.Header.Set(middleware.BotGroupHeaderName, "10001")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Body.String() != `{"ok":true,"userId":0}` {
		t.Fatalf("group resolver response = %d %s", response.Code, response.Body.String())
	}
}

func TestBotContextReturnsOnlyAuthorizationAndUsesSubjectLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	redisServer := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	router := gin.New()
	router.Use(middleware.RequestID())
	auth := botRouterAuthenticator(func(context.Context, string) (*settingsdomain.SystemKey, error) {
		return &settingsdomain.SystemKey{
			ID: 9, Purpose: settingsdomain.SystemKeyPurposeBot,
			Platform: "aiocqhttp", SubjectNamespace: "qq:main", AllowedGroupIDs: []string{"10001"},
		}, nil
	})
	registerBotRoutes(router.Group("/v1"), router, auth, nil, nil, nil, nil, nil, rdb)

	for attempt := 1; attempt <= botSubjectReadsPerMinute+1; attempt++ {
		request := httptest.NewRequest(http.MethodGet, "/v1/bot/context", nil)
		request.Header.Set(middleware.SystemKeyHeaderName, "sk_test")
		request.Header.Set(middleware.BotSubjectHeaderName, "123456789")
		request.Header.Set(middleware.BotSceneHeaderName, middleware.BotScenePrivate)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if attempt <= botSubjectReadsPerMinute {
			if response.Code != http.StatusOK || response.Body.String() != `{"authorized":true}` {
				t.Fatalf("context attempt %d = %d %s", attempt, response.Code, response.Body.String())
			}
			continue
		}
		if response.Code != http.StatusTooManyRequests {
			t.Fatalf("context limit response = %d %s", response.Code, response.Body.String())
		}
	}
}
