package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type botSystemKeyAuthenticatorFunc func(context.Context, string) (*settingsdomain.SystemKey, error)

func (f botSystemKeyAuthenticatorFunc) AuthenticateBotSystemKey(ctx context.Context, plain string) (*settingsdomain.SystemKey, error) {
	return f(ctx, plain)
}

func TestBotMiddlewarePinsKeyScopeAndEventSubject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	auth := botSystemKeyAuthenticatorFunc(func(_ context.Context, plain string) (*settingsdomain.SystemKey, error) {
		require.Equal(t, "sk_bot", plain)
		return &settingsdomain.SystemKey{
			ID: 9, Purpose: settingsdomain.SystemKeyPurposeBot, Platform: "aiocqhttp", SubjectNamespace: "qq:main",
			AllowedGroupIDs: []string{"10001", "10002"},
		}, nil
	})
	router.GET("/bot", BotSystemKeyRequired(auth), BotIdentityRequired(), func(c *gin.Context) {
		identity, ok := GetCurrentBotIdentity(c)
		require.True(t, ok)
		require.Equal(t, []string{"10001", "10002"}, identity.AllowedGroupIDs)
		keyID, ok := GetCurrentSystemKeyID(c)
		require.True(t, ok)
		c.JSON(http.StatusOK, gin.H{
			"keyId": keyID, "platform": identity.Platform, "namespace": identity.SubjectNamespace,
			"subject": identity.Subject, "scene": identity.Scene, "groupId": identity.GroupID,
		})
	})

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/bot", nil)
	request.Header.Set(SystemKeyHeaderName, "sk_bot")
	request.Header.Set(BotSubjectHeaderName, "123456789")
	request.Header.Set(BotSceneHeaderName, "PRIVATE")
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"keyId":9,"platform":"aiocqhttp","namespace":"qq:main","subject":"123456789","scene":"private","groupId":""}`, response.Body.String())

	invalid := httptest.NewRequest(http.MethodGet, "/bot", nil)
	invalid.Header.Set(SystemKeyHeaderName, "sk_bot")
	invalid.Header.Set(BotSubjectHeaderName, "openid-alice")
	invalid.Header.Set(BotSceneHeaderName, BotScenePrivate)
	invalidResponse := httptest.NewRecorder()
	router.ServeHTTP(invalidResponse, invalid)
	require.Equal(t, http.StatusUnauthorized, invalidResponse.Code)
}

func TestBotIdentityRequiresTrustedPrivateOrAllowedGroupContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	auth := botSystemKeyAuthenticatorFunc(func(context.Context, string) (*settingsdomain.SystemKey, error) {
		return &settingsdomain.SystemKey{
			ID: 9, Purpose: settingsdomain.SystemKeyPurposeBot, Platform: "telegram", SubjectNamespace: "telegram:main",
			AllowedGroupIDs: []string{"group-allowed"},
		}, nil
	})
	router := gin.New()
	router.POST("/identity", BotSystemKeyRequired(auth), BotIdentityRequired(), func(c *gin.Context) {
		identity, ok := GetCurrentBotIdentity(c)
		require.True(t, ok)
		c.JSON(http.StatusOK, gin.H{"scene": identity.Scene, "groupId": identity.GroupID})
	})
	router.POST("/private", BotSystemKeyRequired(auth), BotIdentityRequired(), BotPrivateRequired(), func(c *gin.Context) { c.Status(http.StatusNoContent) })

	allowed := httptest.NewRequest(http.MethodPost, "/identity", nil)
	allowed.Header.Set(SystemKeyHeaderName, "sk_bot")
	allowed.Header.Set(BotSubjectHeaderName, "123")
	allowed.Header.Set(BotSceneHeaderName, BotSceneGroup)
	allowed.Header.Set(BotGroupHeaderName, "group-allowed")
	allowedResponse := httptest.NewRecorder()
	router.ServeHTTP(allowedResponse, allowed)
	require.Equal(t, http.StatusOK, allowedResponse.Code)
	require.JSONEq(t, `{"scene":"group","groupId":"group-allowed"}`, allowedResponse.Body.String())

	tests := []struct {
		name, path, subject, scene, groupID string
	}{
		{name: "private missing subject", path: "/identity", scene: BotScenePrivate},
		{name: "private rejects group", path: "/identity", subject: "123", scene: BotScenePrivate, groupID: "group-allowed"},
		{name: "group missing subject", path: "/identity", scene: BotSceneGroup, groupID: "group-allowed"},
		{name: "group missing group", path: "/identity", subject: "123", scene: BotSceneGroup},
		{name: "group outside whitelist", path: "/identity", subject: "123", scene: BotSceneGroup, groupID: "group-secret"},
		{name: "group cannot use private route", path: "/private", subject: "123", scene: BotSceneGroup, groupID: "group-allowed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, nil)
			request.Header.Set(SystemKeyHeaderName, "sk_bot")
			request.Header.Set(BotSubjectHeaderName, test.subject)
			request.Header.Set(BotSceneHeaderName, test.scene)
			request.Header.Set(BotGroupHeaderName, test.groupID)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, http.StatusUnauthorized, response.Code)
			require.Contains(t, response.Body.String(), "Authentication is required.")
			require.NotContains(t, response.Body.String(), "group-allowed")
			require.NotContains(t, response.Body.String(), "group-secret")
		})
	}
}

func TestBotSystemKeyRejectsNonBotMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for name, key := range map[string]*settingsdomain.SystemKey{
		"wrong purpose": {ID: 3, Purpose: settingsdomain.SystemKeyPurposeSMTPSubmission},
		"missing group scope": {
			ID: 9, Purpose: settingsdomain.SystemKeyPurposeBot,
			Platform: "qq_official", SubjectNamespace: "qq:main",
		},
	} {
		t.Run(name, func(t *testing.T) {
			auth := botSystemKeyAuthenticatorFunc(func(context.Context, string) (*settingsdomain.SystemKey, error) { return key, nil })
			router := gin.New()
			router.GET("/bot", BotSystemKeyRequired(auth), func(c *gin.Context) { c.Status(http.StatusNoContent) })
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/bot", nil))
			require.Equal(t, http.StatusUnauthorized, response.Code)
		})
	}
}
