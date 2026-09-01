package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/donnel666/remail/api/middleware"
	billingapi "github.com/donnel666/remail/internal/billing/api"
	billingdomain "github.com/donnel666/remail/internal/billing/domain"
	"github.com/donnel666/remail/internal/botdisplay"
	iamapi "github.com/donnel666/remail/internal/iam/api"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

type botProfileResponse struct {
	Bound            bool   `json:"bound"`
	Available        bool   `json:"available,omitempty"`
	Message          string `json:"message,omitempty"`
	Balance          string `json:"balance,omitempty"`
	TotalRecharged   string `json:"totalRecharged,omitempty"`
	GroupName        string `json:"groupName,omitempty"`
	Role             string `json:"role,omitempty"`
	RoleDisplay      string `json:"roleDisplay,omitempty"`
	NextGroupName    string `json:"nextGroupName,omitempty"`
	UpgradeRemaining string `json:"upgradeRemaining,omitempty"`
	HighestGroup     bool   `json:"highestGroup,omitempty"`
}

func getBotProfile(c *gin.Context, iamMod *iamapi.IAMModule, billingMod *billingapi.BillingModule) {
	identity, ok := middleware.GetCurrentBotIdentity(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "当前会话未获授权。"})
		return
	}
	if iamMod == nil || iamMod.BotBindingUseCase == nil {
		writeBotProfileUnavailable(c, errors.New("bot profile dependencies unavailable"))
		return
	}
	resolution, err := iamMod.BotBindingUseCase.ResolveBinding(
		c.Request.Context(), identity.Platform, identity.SubjectNamespace, identity.Subject,
	)
	if err != nil {
		writeBotProfileUnavailable(c, err)
		return
	}
	if !resolution.Bound {
		c.JSON(http.StatusOK, botProfileResponse{Bound: false, Message: botdisplay.BindingRequiredMessage})
		return
	}
	if !resolution.Available {
		c.JSON(http.StatusOK, botProfileResponse{Bound: true, Message: botdisplay.BindingUnavailableMessage})
		return
	}
	profile := resolution.User
	if iamMod.Users == nil || billingMod == nil || billingMod.WalletUseCase == nil {
		writeBotProfileUnavailable(c, errors.New("bot profile dependencies unavailable"))
		return
	}
	wallet, err := billingMod.WalletUseCase.GetWallet(c.Request.Context(), profile.UserID)
	if err != nil || wallet == nil {
		writeBotProfileUnavailable(c, err)
		return
	}
	groups, err := iamMod.Users.ListUserGroups(c.Request.Context())
	if err != nil {
		writeBotProfileUnavailable(c, err)
		return
	}
	balance, err := billingdomain.NormalizeNonNegativeMoney(wallet.Wallet.ConsumerBalance)
	if err != nil {
		writeBotProfileUnavailable(c, err)
		return
	}
	totalRecharged, err := billingdomain.NormalizeNonNegativeMoney(wallet.TotalRecharged)
	if err != nil {
		writeBotProfileUnavailable(c, err)
		return
	}
	nextGroup, remaining, highest, err := botProfileUpgrade(profile.UserGroup, groups, totalRecharged)
	if err != nil {
		writeBotProfileUnavailable(c, err)
		return
	}
	c.JSON(http.StatusOK, botProfileResponse{
		Bound: true, Available: true, Balance: balance, TotalRecharged: totalRecharged,
		GroupName: botProfileGroupName(profile.UserGroup), Role: profile.Role.String(),
		RoleDisplay: botProfileRoleDisplay(profile.Role), NextGroupName: nextGroup,
		UpgradeRemaining: remaining, HighestGroup: highest,
	})
}

func botProfileUpgrade(current iamdomain.UserGroup, groups []iamdomain.UserGroup, total string) (string, string, bool, error) {
	currentThreshold, err := billingdomain.ParseMoney(current.TopupThreshold)
	if err != nil || currentThreshold.IsNegative() {
		return "", "", false, billingdomain.ErrInvalidAmount
	}
	totalRecharged, err := billingdomain.ParseMoney(total)
	if err != nil || totalRecharged.IsNegative() {
		return "", "", false, billingdomain.ErrInvalidAmount
	}
	hasHigher := false
	var next *iamdomain.UserGroup
	var nextThreshold decimal.Decimal
	for i := range groups {
		threshold, parseErr := billingdomain.ParseMoney(groups[i].TopupThreshold)
		if parseErr != nil || !groups[i].Enabled || !threshold.GreaterThan(currentThreshold) {
			continue
		}
		hasHigher = true
		if !groups[i].AutoUpgradeEnabled {
			continue
		}
		if next == nil || threshold.LessThan(nextThreshold) ||
			(threshold.Equal(nextThreshold) && groups[i].ID > next.ID) {
			next = &groups[i]
			nextThreshold = threshold
		}
	}
	if next == nil {
		return "", "", !hasHigher, nil
	}
	remaining := nextThreshold.Sub(totalRecharged)
	if remaining.IsNegative() {
		remaining = decimal.Zero
	}
	return botProfileGroupName(*next), billingdomain.MoneyString(remaining), false, nil
}

func botProfileGroupName(group iamdomain.UserGroup) string {
	if name := strings.TrimSpace(group.Name); name != "" {
		return name
	}
	return strings.TrimSpace(group.Code)
}

func botProfileRoleDisplay(role iamdomain.Role) string {
	switch role {
	case iamdomain.RoleSupplier:
		return "供应商"
	case iamdomain.RoleAdmin:
		return "管理员"
	case iamdomain.RoleSuperAdmin:
		return "超级管理员"
	default:
		return "普通用户"
	}
}

func writeBotProfileUnavailable(c *gin.Context, err error) {
	slog.Error("bot profile failed", "request_id", middleware.GetRequestID(c), "error", err)
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"message": "服务暂时不可用，请稍后重试。", "requestId": middleware.GetRequestID(c),
	})
}
