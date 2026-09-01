package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/botdisplay"
	iamapp "github.com/donnel666/remail/internal/iam/app"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	iaminfra "github.com/donnel666/remail/internal/iam/infra"
	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type botBindingTestHasher struct{}

func (botBindingTestHasher) Hash(password string) (string, error) { return password, nil }
func (botBindingTestHasher) Verify(password, hash string) bool    { return password == hash }

type botBindingKeyAuth struct{}

func (botBindingKeyAuth) AuthenticateBotSystemKey(context.Context, string) (*settingsdomain.SystemKey, error) {
	return &settingsdomain.SystemKey{
		ID: 7, Purpose: settingsdomain.SystemKeyPurposeBot,
		Platform: "qq", SubjectNamespace: "qq:main", AllowedGroupIDs: []string{"123456"},
	}, nil
}

func botBindingRouter(t *testing.T) (*gin.Engine, *iamapp.BotBindingUseCase, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&iaminfra.UserGroupModel{}, &iaminfra.UserModel{}, &iaminfra.ThirdPartyIdentityModel{}))
	require.NoError(t, db.Create(&iaminfra.UserGroupModel{
		ID: 1, Code: "default", Name: "Default", Enabled: true,
		APIConcurrencyLimit: 1, PriceDiscountRatio: "1.000000", TopupThreshold: "0.000000",
	}).Error)
	require.NoError(t, db.Create(&iaminfra.UserModel{
		ID: 1, Email: "user@example.com", PasswordHash: "correct-password",
		Status: string(iamdomain.UserStatusActive), Role: string(iamdomain.RoleUser), UserGroupID: 1,
	}).Error)
	repo := iaminfra.NewUserRepo(db)
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	bindings := iamapp.NewBotBindingUseCase(repo, botBindingTestHasher{})
	mod := &IAMModule{
		BotBindingUseCase: bindings,
		AbuseLimiter:      iaminfra.NewAbuseLimiter(rdb),
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	bot := router.Group("/v1/bot")
	bot.Use(middleware.BotSystemKeyRequired(botBindingKeyAuth{}))
	bot.Use(middleware.BotChannelRequired())
	RegisterBotBindingRoutes(bot, mod, rdb)
	return router, bindings, db
}

func botBindingHTTPRequest(method, path, subject, body string) *http.Request {
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(middleware.SystemKeyHeaderName, "sk_test")
	request.Header.Set(middleware.BotChannelHeaderName, "qq")
	request.Header.Set(middleware.BotSubjectHeaderName, subject)
	request.Header.Set(middleware.BotSceneHeaderName, middleware.BotScenePrivate)
	return request
}

func TestBotBindingRoutesUseOnlyHeaderSubjectAndReturnMaskedEmail(t *testing.T) {
	router, bindings, db := botBindingRouter(t)
	unbound := httptest.NewRecorder()
	router.ServeHTTP(unbound, botBindingHTTPRequest(http.MethodGet, "/v1/bot/binding", "987654321", ""))
	require.Equal(t, http.StatusOK, unbound.Code)
	var unboundBody botBindingResponse
	require.NoError(t, json.Unmarshal(unbound.Body.Bytes(), &unboundBody))
	require.Equal(t, botdisplay.BindingRequiredMessage, unboundBody.Reason)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, botBindingHTTPRequest(http.MethodPost, "/v1/bot/bindings", "123456789", `{"email":"user@example.com","password":"correct-password"}`))

	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), `"reason":"当前账号已绑定 ReMail。"`)
	require.Contains(t, response.Body.String(), `"accountDisplay":"u***@example.com"`)
	require.NotContains(t, response.Body.String(), "requestId")
	require.NotContains(t, response.Body.String(), "correct-password")
	require.NotContains(t, response.Body.String(), `"user@example.com"`)
	bound, err := bindings.Get(context.Background(), "qq", "qq:main", "123456789")
	require.NoError(t, err)
	require.True(t, bound.Bound)
	attacker, err := bindings.Get(context.Background(), "qq", "qq:main", "987654321")
	require.NoError(t, err)
	require.False(t, attacker.Bound)

	override := httptest.NewRecorder()
	router.ServeHTTP(override, botBindingHTTPRequest(http.MethodPost, "/v1/bot/bindings", "987654321", `{"email":"user@example.com","password":"correct-password","subject":"123456789"}`))
	require.Equal(t, http.StatusBadRequest, override.Code)
	require.NotContains(t, override.Body.String(), "123456789")

	status := httptest.NewRecorder()
	router.ServeHTTP(status, botBindingHTTPRequest(http.MethodGet, "/v1/bot/binding", "123456789", ""))
	require.Equal(t, http.StatusOK, status.Code)
	require.Contains(t, status.Body.String(), `"result":"bound"`)
	require.Contains(t, status.Body.String(), `"reason":"当前账号已绑定 ReMail。"`)
	require.NotContains(t, status.Body.String(), `"user@example.com"`)

	require.NoError(t, db.Model(&iaminfra.UserModel{}).Where("id = ?", 1).
		Update("status", string(iamdomain.UserStatusDisabled)).Error)
	unavailable := httptest.NewRecorder()
	router.ServeHTTP(unavailable, botBindingHTTPRequest(http.MethodGet, "/v1/bot/binding", "123456789", ""))
	require.Equal(t, http.StatusOK, unavailable.Code)
	require.Contains(t, unavailable.Body.String(), `"result":"account_unavailable"`)
	require.Contains(t, unavailable.Body.String(), `"reason":"已绑定的 ReMail 账号不可用，请重新绑定。"`)

	group := botBindingHTTPRequest(http.MethodDelete, "/v1/bot/binding", "123456789", "")
	group.Header.Set(middleware.BotSceneHeaderName, middleware.BotSceneGroup)
	group.Header.Set(middleware.BotGroupHeaderName, "123456")
	groupResponse := httptest.NewRecorder()
	router.ServeHTTP(groupResponse, group)
	require.Equal(t, http.StatusUnauthorized, groupResponse.Code)
}

func TestBotBindingEmailFailureLimitSpansDifferentSubjects(t *testing.T) {
	router, _, _ := botBindingRouter(t)
	for i := range 10 {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, botBindingHTTPRequest(http.MethodPost, "/v1/bot/bindings", fmt.Sprintf("10000%d", i), `{"email":"USER@example.com","password":"wrong"}`))
		require.Equal(t, http.StatusUnprocessableEntity, response.Code)
		require.Contains(t, response.Body.String(), `"result":"credential_incorrect"`)
		require.Contains(t, response.Body.String(), `"reason":"ReMail 账号或密码错误。"`)
	}
	blocked := httptest.NewRecorder()
	router.ServeHTTP(blocked, botBindingHTTPRequest(http.MethodPost, "/v1/bot/bindings", "200000", `{"email":"user@example.com","password":"wrong"}`))
	require.Equal(t, http.StatusTooManyRequests, blocked.Code)
	require.NotEmpty(t, blocked.Header().Get("Retry-After"))
}

func TestBotBindingErrorReasonsUseNaturalChinese(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		err    error
		status int
		result string
		reason string
	}{
		{iamdomain.ErrThirdPartyIdentityAlreadyBound, http.StatusConflict, "binding_conflict", "当前账号或该 ReMail 账号已存在其他绑定。"},
		{iamdomain.ErrThirdPartyIdentityUnavailable, http.StatusUnauthorized, "bot_identity_required", "身份验证失败。"},
		{errors.New("unavailable"), http.StatusServiceUnavailable, "service_unavailable", "服务暂时不可用，请稍后重试。"},
	}
	for _, test := range tests {
		response := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(response)
		writeBotBindingError(context, test.err)
		require.Equal(t, test.status, response.Code)
		require.JSONEq(t, fmt.Sprintf(`{"result":%q,"reason":%q}`, test.result, test.reason), response.Body.String())
		require.NotContains(t, response.Body.String(), "The current bot account")
	}
}

func TestBotBindingRejectsOversizedCredentialBody(t *testing.T) {
	router, _, _ := botBindingRouter(t)
	response := httptest.NewRecorder()
	body := `{"email":"user@example.com","password":"` + strings.Repeat("x", botBindingRequestMaxBytes) + `"}`
	router.ServeHTTP(response, botBindingHTTPRequest(http.MethodPost, "/v1/bot/bindings", "123456789", body))

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.NotContains(t, response.Body.String(), strings.Repeat("x", 32))
}
