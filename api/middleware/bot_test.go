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
			ID: 9, Purpose: settingsdomain.SystemKeyPurposeBot, Platform: "qq", SubjectNamespace: "qq:main",
			AllowedGroupIDs: []string{"10001", "10002"},
		}, nil
	})
	router.GET("/bot", BotSystemKeyRequired(auth), BotChannelRequired(), BotIdentityRequired(), func(c *gin.Context) {
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
	request.Header.Set(BotChannelHeaderName, "qq")
	request.Header.Set(BotSubjectHeaderName, "123456789")
	request.Header.Set(BotSceneHeaderName, "PRIVATE")
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"keyId":9,"platform":"qq","namespace":"qq:main","subject":"123456789","scene":"private","groupId":""}`, response.Body.String())

	wrongChannel := httptest.NewRequest(http.MethodGet, "/bot", nil)
	wrongChannel.Header.Set(SystemKeyHeaderName, "sk_bot")
	wrongChannel.Header.Set(BotChannelHeaderName, "telegram")
	wrongChannel.Header.Set(BotSubjectHeaderName, "123456789")
	wrongChannel.Header.Set(BotSceneHeaderName, BotScenePrivate)
	wrongChannelResponse := httptest.NewRecorder()
	router.ServeHTTP(wrongChannelResponse, wrongChannel)
	require.Equal(t, http.StatusUnauthorized, wrongChannelResponse.Code)

	invalid := httptest.NewRequest(http.MethodGet, "/bot", nil)
	invalid.Header.Set(SystemKeyHeaderName, "sk_bot")
	invalid.Header.Set(BotChannelHeaderName, "qq")
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
			AllowedGroupIDs: []string{"-1001234567890"},
		}, nil
	})
	router := gin.New()
	router.POST("/identity", BotSystemKeyRequired(auth), BotChannelRequired(), BotIdentityRequired(), func(c *gin.Context) {
		identity, ok := GetCurrentBotIdentity(c)
		require.True(t, ok)
		c.JSON(http.StatusOK, gin.H{"scene": identity.Scene, "groupId": identity.GroupID})
	})
	router.POST("/private", BotSystemKeyRequired(auth), BotChannelRequired(), BotIdentityRequired(), BotPrivateRequired(), func(c *gin.Context) { c.Status(http.StatusNoContent) })

	allowed := httptest.NewRequest(http.MethodPost, "/identity", nil)
	allowed.Header.Set(SystemKeyHeaderName, "sk_bot")
	allowed.Header.Set(BotChannelHeaderName, "telegram")
	allowed.Header.Set(BotSubjectHeaderName, "123")
	allowed.Header.Set(BotSceneHeaderName, BotSceneGroup)
	allowed.Header.Set(BotGroupHeaderName, "-1001234567890")
	allowedResponse := httptest.NewRecorder()
	router.ServeHTTP(allowedResponse, allowed)
	require.Equal(t, http.StatusOK, allowedResponse.Code)
	require.JSONEq(t, `{"scene":"group","groupId":"-1001234567890"}`, allowedResponse.Body.String())

	tests := []struct {
		name, path, subject, scene, groupID string
	}{
		{name: "private missing subject", path: "/identity", scene: BotScenePrivate},
		{name: "private rejects group", path: "/identity", subject: "123", scene: BotScenePrivate, groupID: "-1001234567890"},
		{name: "private rejects nonnumeric subject", path: "/identity", subject: "alice", scene: BotScenePrivate},
		{name: "group missing subject", path: "/identity", scene: BotSceneGroup, groupID: "-1001234567890"},
		{name: "group missing group", path: "/identity", subject: "123", scene: BotSceneGroup},
		{name: "group outside whitelist", path: "/identity", subject: "123", scene: BotSceneGroup, groupID: "-1009999999999"},
		{name: "group cannot use private route", path: "/private", subject: "123", scene: BotSceneGroup, groupID: "-1001234567890"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, nil)
			request.Header.Set(SystemKeyHeaderName, "sk_bot")
			request.Header.Set(BotChannelHeaderName, "telegram")
			request.Header.Set(BotSubjectHeaderName, test.subject)
			request.Header.Set(BotSceneHeaderName, test.scene)
			request.Header.Set(BotGroupHeaderName, test.groupID)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, http.StatusUnauthorized, response.Code)
			require.Contains(t, response.Body.String(), "Authentication is required.")
			require.NotContains(t, response.Body.String(), "-1001234567890")
			require.NotContains(t, response.Body.String(), "-1009999999999")
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
