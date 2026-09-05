package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sort"
	"strings"
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

func TestGeneratedOpenAPICompatibilityAliases(t *testing.T) {
	if Qq != "qq" || Telegram != "telegram" || Private != "private" || Public != "public" {
		t.Fatalf("unexpected compatibility aliases: %q %q %q %q", Qq, Telegram, Private, Public)
	}
	if ConnectBotWebSocketParamsXBotChannelQq != Qq || ConnectBotWebSocketParamsXBotChannelTelegram != Telegram ||
		GetProjectsParamsAccessTypePrivate != Private || GetProjectsParamsAccessTypePublic != Public {
		t.Fatal("long-form OpenAPI compatibility aliases changed")
	}
}

func TestBotProjectOpenAPIModelsExcludeInternalFields(t *testing.T) {
	forbidden := map[string]bool{
		"owner": true, "applicantUserId": true, "reviewReason": true,
		"codeSupplierPrice": true, "purchaseSupplierPrice": true,
		"mainWeight": true, "dotWeight": true, "plusWeight": true,
		"mailRules": true, "accesses": true, "microsoftSuffixBlacklist": true,
	}
	for _, model := range []any{BotProjectItem{}, BotProjectProduct{}, BotProjectDetailResponse{}} {
		typeOf := reflect.TypeOf(model)
		for index := 0; index < typeOf.NumField(); index++ {
			name := typeOf.Field(index).Tag.Get("json")
			if comma := strings.IndexByte(name, ','); comma >= 0 {
				name = name[:comma]
			}
			if forbidden[name] {
				t.Fatalf("%s exposes internal field %q", typeOf.Name(), name)
			}
		}
	}
}

func TestGeneratedBotFactContractsFailClosed(t *testing.T) {
	if !BotDiagnosisResultProjectMismatch.Valid() || BotDiagnosisResponseResult("cause_not_confirmed").Valid() {
		t.Fatal("bot diagnosis result contract exposes an unproven internal result")
	}
	if !BotDiagnosisResponseMailReceived(true).Valid() || BotDiagnosisResponseMailReceived(false).Valid() ||
		!BotDiagnosisResponseProjectMismatch(true).Valid() || BotDiagnosisResponseProjectMismatch(false).Valid() {
		t.Fatal("bot diagnosis proof flags must only allow true when present")
	}
	for _, fieldName := range []string{"TotalAvailable", "PublicAvailable"} {
		field, ok := reflect.TypeOf(ProjectProductSummary{}).FieldByName(fieldName)
		if !ok || field.Type.Kind() != reflect.Pointer {
			t.Fatalf("ProjectProductSummary.%s must be nullable for unknown inventory", fieldName)
		}
	}
	observedAt, ok := reflect.TypeOf(ProjectInventoryTotalResponse{}).FieldByName("ObservedAt")
	if !ok || observedAt.Type.Kind() != reflect.Pointer {
		t.Fatal("ready project inventory must support an optional observation time")
	}
	if !GetBotRechargeConfigParamsXBotChannelQq.Valid() || !GetBotRechargeConfigParamsXBotScenePrivate.Valid() {
		t.Fatal("bot recharge config path is missing trusted identity parameters")
	}
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
		"GET /v1/bot/profile",
		"GET /v1/bot/projects",
		"GET /v1/bot/projects/:projectId",
		"GET /v1/bot/projects/:projectId/inventory",
		"GET /v1/bot/recharges/config",
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

func TestEveryBotBusinessRouteRequiresSubjectAndGroupContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	redisServer := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	router := gin.New()
	auth := botRouterAuthenticator(func(context.Context, string) (*settingsdomain.SystemKey, error) {
		return &settingsdomain.SystemKey{
			ID: 9, Purpose: settingsdomain.SystemKeyPurposeBot,
			Platform: "qq", SubjectNamespace: "qq:main", AllowedGroupIDs: []string{"10001"},
		}, nil
	})
	registerBotRoutes(router.Group("/v1"), router, auth, nil, nil, nil, nil, nil, rdb)
	routes := []struct{ method, path string }{
		{http.MethodGet, "/v1/bot/context"},
		{http.MethodGet, "/v1/bot/profile"},
		{http.MethodPost, "/v1/bot/bindings"},
		{http.MethodGet, "/v1/bot/binding"},
		{http.MethodDelete, "/v1/bot/binding"},
		{http.MethodGet, "/v1/bot/projects"},
		{http.MethodGet, "/v1/bot/projects/1"},
		{http.MethodGet, "/v1/bot/projects/1/inventory"},
		{http.MethodGet, "/v1/bot/recharges/config"},
		{http.MethodGet, "/v1/bot/rankings/orders"},
		{http.MethodGet, "/v1/bot/rankings/rewards/latest"},
		{http.MethodPost, "/v1/bot/diagnoses/code"},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			missingSubject := httptest.NewRequest(route.method, route.path, nil)
			missingSubject.Header.Set(middleware.SystemKeyHeaderName, "sk_test")
			missingSubject.Header.Set(middleware.BotChannelHeaderName, "qq")
			missingSubject.Header.Set(middleware.BotSceneHeaderName, middleware.BotScenePrivate)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, missingSubject)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("missing subject = %d %s", response.Code, response.Body.String())
			}

			missingGroup := httptest.NewRequest(route.method, route.path, nil)
			missingGroup.Header.Set(middleware.SystemKeyHeaderName, "sk_test")
			missingGroup.Header.Set(middleware.BotChannelHeaderName, "qq")
			missingGroup.Header.Set(middleware.BotSubjectHeaderName, "123456789")
			missingGroup.Header.Set(middleware.BotSceneHeaderName, middleware.BotSceneGroup)
			response = httptest.NewRecorder()
			router.ServeHTTP(response, missingGroup)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("missing group = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestBotUserResolverTreatsGroupAsPublicWithoutReadingBinding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	auth := botRouterAuthenticator(func(context.Context, string) (*settingsdomain.SystemKey, error) {
		return &settingsdomain.SystemKey{
			ID: 9, Purpose: settingsdomain.SystemKeyPurposeBot,
			Platform: "qq", SubjectNamespace: "qq:main", AllowedGroupIDs: []string{"10001"},
		}, nil
	})
	router.GET("/resolve", middleware.BotSystemKeyRequired(auth), middleware.BotIdentityRequired(), func(c *gin.Context) {
		viewer, ok := botUserResolver(nil)(c)
		c.JSON(http.StatusOK, gin.H{"userId": viewer.UserID, "ok": ok})
	})

	request := httptest.NewRequest(http.MethodGet, "/resolve", nil)
	request.Header.Set(middleware.SystemKeyHeaderName, "sk_test")
	request.Header.Set(middleware.BotChannelHeaderName, "qq")
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
			Platform: "qq", SubjectNamespace: "qq:main", AllowedGroupIDs: []string{"10001"},
		}, nil
	})
	registerBotRoutes(router.Group("/v1"), router, auth, nil, nil, nil, nil, nil, rdb)
	wrongChannel := httptest.NewRequest(http.MethodGet, "/v1/bot/context", nil)
	wrongChannel.Header.Set(middleware.SystemKeyHeaderName, "sk_test")
	wrongChannel.Header.Set(middleware.BotChannelHeaderName, "telegram")
	wrongChannel.Header.Set(middleware.BotSubjectHeaderName, "123456789")
	wrongChannel.Header.Set(middleware.BotSceneHeaderName, middleware.BotScenePrivate)
	wrongChannelResponse := httptest.NewRecorder()
	router.ServeHTTP(wrongChannelResponse, wrongChannel)
	if wrongChannelResponse.Code != http.StatusUnauthorized {
		t.Fatalf("wrong channel response = %d %s", wrongChannelResponse.Code, wrongChannelResponse.Body.String())
	}

	for attempt := 1; attempt <= botSubjectReadsPerMinute+1; attempt++ {
		request := httptest.NewRequest(http.MethodGet, "/v1/bot/context", nil)
		request.Header.Set(middleware.SystemKeyHeaderName, "sk_test")
		request.Header.Set(middleware.BotChannelHeaderName, "qq")
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
