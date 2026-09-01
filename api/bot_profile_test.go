package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/donnel666/remail/api/middleware"
	billingapi "github.com/donnel666/remail/internal/billing/api"
	billingapp "github.com/donnel666/remail/internal/billing/app"
	billingdomain "github.com/donnel666/remail/internal/billing/domain"
	iamapi "github.com/donnel666/remail/internal/iam/api"
	iamapp "github.com/donnel666/remail/internal/iam/app"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	iaminfra "github.com/donnel666/remail/internal/iam/infra"
	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type botProfileHasher struct{}

func (botProfileHasher) Hash(password string) (string, error) { return password, nil }
func (botProfileHasher) Verify(password, hash string) bool    { return password == hash }

type botProfileWalletRepo struct {
	billingapp.WalletRepository
	summary *billingdomain.WalletSummary
	userID  uint
}

func (r *botProfileWalletRepo) GetOrCreateWalletSummary(_ context.Context, userID uint) (*billingdomain.WalletSummary, error) {
	r.userID = userID
	return r.summary, nil
}

type botProfileKeyAuth struct{}

func (botProfileKeyAuth) AuthenticateBotSystemKey(context.Context, string) (*settingsdomain.SystemKey, error) {
	return &settingsdomain.SystemKey{
		ID: 7, Purpose: settingsdomain.SystemKeyPurposeBot,
		Platform: "qq", SubjectNamespace: "qq:main", AllowedGroupIDs: []string{"123456"},
	}, nil
}

func TestBotProfileReturnsOnlyCurrentBoundUsersSafeSummary(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&iaminfra.UserGroupModel{}, &iaminfra.UserModel{}, &iaminfra.ThirdPartyIdentityModel{}))
	for _, group := range []iaminfra.UserGroupModel{
		{ID: 1, Code: "vip1", Name: "VIP 1", Enabled: true, TopupThreshold: "100.000000", AutoUpgradeEnabled: true},
		{ID: 2, Code: "manual", Name: "人工分组", Enabled: true, TopupThreshold: "300.000000"},
		{ID: 3, Code: "vip2", Name: "VIP 2", Enabled: true, TopupThreshold: "500.000000", AutoUpgradeEnabled: true},
	} {
		require.NoError(t, db.Create(&group).Error)
	}
	require.NoError(t, db.Create(&iaminfra.UserModel{
		ID: 9, Email: "secret@example.com", PasswordHash: "password",
		Status: string(iamdomain.UserStatusActive), Role: string(iamdomain.RoleUser), UserGroupID: 1,
	}).Error)
	repo := iaminfra.NewUserRepo(db)
	bindings := iamapp.NewBotBindingUseCase(repo, botProfileHasher{})
	_, err = bindings.Bind(context.Background(), "qq", "qq:main", "123456789", "secret@example.com", "password")
	require.NoError(t, err)
	iamMod := &iamapi.IAMModule{BotBindingUseCase: bindings, Users: repo}
	walletRepo := &botProfileWalletRepo{summary: &billingdomain.WalletSummary{
		Wallet: billingdomain.Wallet{UserID: 9, ConsumerBalance: "12.5"}, TotalRecharged: "200",
	}}
	billingMod := &billingapi.BillingModule{WalletUseCase: billingapp.NewWalletUseCase(walletRepo)}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.GET("/profile",
		middleware.BotSystemKeyRequired(botProfileKeyAuth{}), middleware.BotChannelRequired(), middleware.BotIdentityRequired(),
		func(c *gin.Context) { getBotProfile(c, iamMod, billingMod) },
	)

	request := httptest.NewRequest(http.MethodGet, "/profile", nil)
	request.Header.Set(middleware.SystemKeyHeaderName, "sk_test")
	request.Header.Set(middleware.BotChannelHeaderName, "qq")
	request.Header.Set(middleware.BotSubjectHeaderName, "123456789")
	request.Header.Set(middleware.BotSceneHeaderName, middleware.BotSceneGroup)
	request.Header.Set(middleware.BotGroupHeaderName, "123456")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, true, body["bound"])
	require.Equal(t, true, body["available"])
	require.Equal(t, "12.50", body["balance"])
	require.Equal(t, "200.00", body["totalRecharged"])
	require.Equal(t, "VIP 1", body["groupName"])
	require.Equal(t, "user", body["role"])
	require.Equal(t, "普通用户", body["roleDisplay"])
	require.Equal(t, "VIP 2", body["nextGroupName"])
	require.Equal(t, "300.00", body["upgradeRemaining"])
	for _, forbidden := range []string{"secret@example.com", "123456789", "password", "userId"} {
		require.NotContains(t, response.Body.String(), forbidden)
	}
	require.Equal(t, uint(9), walletRepo.userID)

	walletUseCase := billingMod.WalletUseCase
	billingMod.WalletUseCase = nil
	unavailableDependency := httptest.NewRecorder()
	router.ServeHTTP(unavailableDependency, request.Clone(context.Background()))
	require.Equal(t, http.StatusServiceUnavailable, unavailableDependency.Code)
	var unavailableDependencyBody map[string]any
	require.NoError(t, json.Unmarshal(unavailableDependency.Body.Bytes(), &unavailableDependencyBody))
	require.NotEmpty(t, unavailableDependencyBody["requestId"])
	billingMod.WalletUseCase = walletUseCase

	require.NoError(t, db.Model(&iaminfra.UserModel{}).Where("id = ?", 9).Update("status", iamdomain.UserStatusDisabled).Error)
	unavailableResponse := httptest.NewRecorder()
	router.ServeHTTP(unavailableResponse, request.Clone(context.Background()))
	require.Equal(t, http.StatusOK, unavailableResponse.Code)
	require.JSONEq(t, `{"bound":true,"message":"当前绑定的 ReMail 账号不可用，请重新绑定或联系客服。"}`, unavailableResponse.Body.String())

	unbound := httptest.NewRequest(http.MethodGet, "/profile", nil)
	unbound.Header = request.Header.Clone()
	unbound.Header.Set(middleware.BotSubjectHeaderName, "987654321")
	unboundResponse := httptest.NewRecorder()
	router.ServeHTTP(unboundResponse, unbound)
	require.Equal(t, http.StatusOK, unboundResponse.Code)
	require.JSONEq(t, `{"bound":false,"message":"当前账号尚未绑定 ReMail。\n请先私聊机器人发送 /绑定 <ReMail邮箱> <密码> 完成绑定。"}`, unboundResponse.Body.String())
}

func TestBotProfileUpgradeMatchesWorkbenchMembershipRules(t *testing.T) {
	current := iamdomain.UserGroup{TopupThreshold: "100"}
	groups := []iamdomain.UserGroup{
		{ID: 2, Name: "同档旧分组", Enabled: true, AutoUpgradeEnabled: true, TopupThreshold: "500"},
		{ID: 3, Name: "同档新分组", Enabled: true, AutoUpgradeEnabled: true, TopupThreshold: "500"},
	}
	name, remaining, highest, err := botProfileUpgrade(current, groups, "499.5")
	require.NoError(t, err)
	require.Equal(t, "同档新分组", name)
	require.Equal(t, "0.50", remaining)
	require.False(t, highest)

	name, remaining, highest, err = botProfileUpgrade(current, nil, "499.5")
	require.NoError(t, err)
	require.Empty(t, name)
	require.Empty(t, remaining)
	require.True(t, highest)
}
