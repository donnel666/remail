package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type botDiagnosisRepoStub struct {
	userID          uint
	email           string
	projectMismatch bool
}

func (r *botDiagnosisRepoStub) LookupCodeDiagnosis(_ context.Context, userID uint, email string) (mailmatchapp.CodeDiagnosisLookup, error) {
	r.userID, r.email = userID, email
	receivedAt := time.Now().UTC().Add(-2 * time.Minute)
	var deliveryStoredAt *time.Time
	if !r.projectMismatch {
		deliveryStoredAt = &receivedAt
	}
	return mailmatchapp.CodeDiagnosisLookup{Orders: []mailmatchapp.CodeDiagnosisOrderFact{{
		OrderNo: "SECRET-ORDER-NO", ProjectID: 10, ProjectName: "GitHub",
		ServiceMode: "code", Status: "completed", DeliveryStoredAt: deliveryStoredAt,
		ProjectMismatch: r.projectMismatch,
	}}}, nil
}

type botDiagnosisKeyAuth struct{}

func (botDiagnosisKeyAuth) AuthenticateBotSystemKey(context.Context, string) (*settingsdomain.SystemKey, error) {
	return &settingsdomain.SystemKey{
		ID: 7, Purpose: settingsdomain.SystemKeyPurposeBot,
		Platform: "qq", SubjectNamespace: "qq:main", AllowedGroupIDs: []string{"123456"},
	}, nil
}

func botDiagnosisRouter(module *Module, resolver BotUserIDResolver) *gin.Engine {
	router := gin.New()
	router.Use(middleware.RequestID())
	bot := router.Group("/v1/bot")
	bot.Use(middleware.BotSystemKeyRequired(botDiagnosisKeyAuth{}))
	bot.Use(middleware.BotChannelRequired())
	bot.Use(middleware.BotIdentityRequired())
	RegisterBotRoutes(bot, module, resolver)
	return router
}

func botDiagnosisRequest(scene, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/v1/bot/diagnoses/code", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(middleware.SystemKeyHeaderName, "sk_test")
	request.Header.Set(middleware.BotChannelHeaderName, "qq")
	request.Header.Set(middleware.BotSubjectHeaderName, "123456789")
	request.Header.Set(middleware.BotSceneHeaderName, scene)
	if scene == middleware.BotSceneGroup {
		request.Header.Set(middleware.BotGroupHeaderName, "123456")
	}
	return request
}

func activeBotUser(userID uint) BotUserResolution {
	return BotUserResolution{UserID: userID, Bound: true, Available: true}
}

func TestBotCodeDiagnosisResponseCannotExposeRawFacts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &botDiagnosisRepoStub{}
	module := &Module{BotDiagnosis: mailmatchapp.NewBotDiagnosisService(repo)}
	router := botDiagnosisRouter(module, func(*gin.Context) (BotUserResolution, bool) { return activeBotUser(2), true })
	req := botDiagnosisRequest(middleware.BotScenePrivate, `{"email":"private@example.com"}`)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, req)

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "验证码邮件已经到达")
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.ElementsMatch(t, []string{"message", "diagnosisCode", "projectId", "projectName"}, mapKeys(body))
	require.Equal(t, "pickup_not_requested", body["diagnosisCode"])
	require.Equal(t, float64(10), body["projectId"])
	require.Equal(t, "GitHub", body["projectName"])
	require.Equal(t, uint(2), repo.userID)
	require.Equal(t, "private@example.com", repo.email)
	for _, secret := range []string{"private@example.com", "SECRET-ORDER-NO", "verificationCode", "token", "cache", "requestId"} {
		require.NotContains(t, response.Body.String(), secret)
	}
}

func TestBotCodeDiagnosisProjectMismatchExposesOnlyOwnedProjectAndSafeFacts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &botDiagnosisRepoStub{projectMismatch: true}
	module := &Module{BotDiagnosis: mailmatchapp.NewBotDiagnosisService(repo)}
	router := botDiagnosisRouter(module, func(*gin.Context) (BotUserResolution, bool) { return activeBotUser(2), true })
	response := httptest.NewRecorder()

	router.ServeHTTP(response, botDiagnosisRequest(middleware.BotScenePrivate, `{"email":"private@example.com"}`))

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.ElementsMatch(t, []string{
		"message", "diagnosisCode", "result", "mailReceived", "projectMismatch", "projectId", "projectName",
	}, mapKeys(body))
	require.Equal(t, "project_mismatch", body["diagnosisCode"])
	require.Equal(t, "project_mismatch", body["result"])
	require.Equal(t, true, body["mailReceived"])
	require.Equal(t, true, body["projectMismatch"])
	require.Equal(t, float64(10), body["projectId"])
	require.Equal(t, "GitHub", body["projectName"])
	require.Contains(t, body["message"], "项目买错")
	for _, forbidden := range []string{
		"private@example.com", "SECRET-ORDER-NO", "Other Project", "subject", "sender", "body", "code",
	} {
		require.NotContains(t, response.Body.String(), forbidden)
	}
}

func TestBotCodeDiagnosisRejectsUnknownBodyFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	module := &Module{BotDiagnosis: mailmatchapp.NewBotDiagnosisService(&botDiagnosisRepoStub{})}
	router := botDiagnosisRouter(module, func(*gin.Context) (BotUserResolution, bool) { return activeBotUser(2), true })
	req := botDiagnosisRequest(middleware.BotScenePrivate, `{"email":"private@example.com","orderNo":"SECRET"}`)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, req)

	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.ElementsMatch(t, []string{"message"}, mapKeys(body))
	require.NotContains(t, response.Body.String(), "SECRET")
}

func TestBotCodeDiagnosisPreservesResolverFailureResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	module := &Module{BotDiagnosis: mailmatchapp.NewBotDiagnosisService(&botDiagnosisRepoStub{})}
	router := botDiagnosisRouter(module, func(c *gin.Context) (BotUserResolution, bool) {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"message": "Service is temporarily unavailable."})
		return BotUserResolution{}, false
	})
	request := botDiagnosisRequest(middleware.BotScenePrivate, `{"email":"private@example.com"}`)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.JSONEq(t, `{"message":"Service is temporarily unavailable."}`, response.Body.String())
}

func TestBotCodeDiagnosisGroupUsesBoundUserAndEmailWithoutExposingRawOrder(t *testing.T) {
	repo := &botDiagnosisRepoStub{}
	module := &Module{BotDiagnosis: mailmatchapp.NewBotDiagnosisService(repo)}
	router := botDiagnosisRouter(module, func(*gin.Context) (BotUserResolution, bool) { return activeBotUser(2), true })
	response := httptest.NewRecorder()

	router.ServeHTTP(response, botDiagnosisRequest(middleware.BotSceneGroup, `{"email":"private@example.com"}`))

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `"projectId":10`)
	require.Contains(t, response.Body.String(), `"projectName":"GitHub"`)
	require.Contains(t, response.Body.String(), "验证码邮件已经到达")
	require.Equal(t, uint(2), repo.userID)
	require.Equal(t, "private@example.com", repo.email)
	require.NotContains(t, response.Body.String(), "private@example.com")
	require.NotContains(t, response.Body.String(), "SECRET-ORDER-NO")
}

func TestBotCodeDiagnosisGroupReportsMissingBindingWithoutOrderData(t *testing.T) {
	module := &Module{BotDiagnosis: mailmatchapp.NewBotDiagnosisService(&botDiagnosisRepoStub{})}
	router := botDiagnosisRouter(module, func(*gin.Context) (BotUserResolution, bool) { return BotUserResolution{}, true })
	response := httptest.NewRecorder()

	router.ServeHTTP(response, botDiagnosisRequest(middleware.BotSceneGroup, `{"email":"private@example.com"}`))

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "尚未绑定")
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.ElementsMatch(t, []string{"message", "bindingRequired"}, mapKeys(body))
	require.Equal(t, true, body["bindingRequired"])
	require.NotContains(t, response.Body.String(), "projectId")
}

func TestBotCodeDiagnosisReportsUnavailableBoundAccount(t *testing.T) {
	module := &Module{BotDiagnosis: mailmatchapp.NewBotDiagnosisService(&botDiagnosisRepoStub{})}
	router := botDiagnosisRouter(module, func(*gin.Context) (BotUserResolution, bool) {
		return BotUserResolution{Bound: true}, true
	})
	response := httptest.NewRecorder()

	router.ServeHTTP(response, botDiagnosisRequest(middleware.BotScenePrivate, `{"email":"private@example.com"}`))

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.JSONEq(t, `{"message":"当前绑定的 ReMail 账号不可用，请重新绑定或联系客服。","accountUnavailable":true}`, response.Body.String())
}

func TestBotCodeDiagnosisGroupRequiresEmail(t *testing.T) {
	module := &Module{BotDiagnosis: mailmatchapp.NewBotDiagnosisService(&botDiagnosisRepoStub{})}
	router := botDiagnosisRouter(module, func(*gin.Context) (BotUserResolution, bool) { return activeBotUser(2), true })
	response := httptest.NewRecorder()

	router.ServeHTTP(response, botDiagnosisRequest(middleware.BotSceneGroup, `{}`))

	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
}

func mapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
