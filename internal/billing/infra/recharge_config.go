package infra

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/billing/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

type RechargeConfigProvider struct{}

func (RechargeConfigProvider) Current() (billingapp.RechargeConfig, error) {
	settings := runtimeconfig.Snapshot()
	tiers, err := rechargeTiers(
		settings.String("topup_amount_presets", "[10000,20000,50000,100000,200000,500000]"),
		settings.String("topup_amount_bonus", "{}"),
	)
	if err != nil {
		return billingapp.RechargeConfig{}, domain.ErrRechargeConfigUnavailable
	}
	enabled, _ := strconv.ParseBool(strings.TrimSpace(settings.String("epay_enabled", "false")))
	epusdtEnabled, _ := strconv.ParseBool(strings.TrimSpace(settings.String("epusdt_enabled", "false")))
	config := billingapp.RechargeConfig{
		Enabled:                    enabled,
		Version:                    strings.TrimSpace(settings.String("epay_version", "v1")),
		GatewayURL:                 strings.TrimSpace(settings.String("epay_gateway_url", "")),
		MerchantID:                 strings.TrimSpace(settings.String("epay_merchant_id", "")),
		MerchantKey:                settings.String("epay_merchant_key", ""),
		PrivateKey:                 settings.String("epay_private_key", ""),
		PlatformPublicKey:          settings.String("epay_platform_public_key", ""),
		NotifyURL:                  strings.TrimSpace(settings.String("epay_notify_url", "")),
		ReturnURL:                  strings.TrimSpace(settings.String("epay_return_url", "")),
		PointsPerYuan:              strings.TrimSpace(settings.String("points_per_yuan", "1000")),
		MinPoints:                  strings.TrimSpace(settings.String("min_topup_amount", "10000")),
		FeeRate:                    strings.TrimSpace(settings.String("topup_fee_rate", "0")),
		FeeCapPoints:               strings.TrimSpace(settings.String("topup_fee_cap", "0")),
		Tiers:                      tiers,
		MaxPendingOrders:           settings.Int("max_pending_recharge_orders", 10, 1),
		RequestTimeout:             settings.Duration("async_check_request_timeout_seconds", 5*time.Second, time.Second, 1),
		EpusdtEnabled:              epusdtEnabled,
		EpusdtGatewayURL:           strings.TrimSpace(settings.String("epusdt_gateway_url", "")),
		EpusdtPID:                  strings.TrimSpace(settings.String("epusdt_pid", "")),
		EpusdtCurrency:             "USDT",
		EpusdtPointsPerUSDT:        strings.TrimSpace(settings.String("epusdt_points_per_usdt", "0")),
		EpusdtMinimumPaymentAmount: strings.TrimSpace(settings.String("epusdt_minimum_payment_amount", "10.00")),
		EpusdtAPIKey:               settings.String("epusdt_api_key", ""),
		EpusdtAPISecret:            settings.String("epusdt_api_secret", ""),
		EpusdtToken:                strings.ToUpper(strings.TrimSpace(settings.String("epusdt_token", "USDT"))),
		EpusdtNetwork:              strings.ToLower(strings.TrimSpace(settings.String("epusdt_network", "tron"))),
		EpusdtNotifyURL:            strings.TrimSpace(settings.String("epusdt_notify_url", "")),
		EpusdtReturnURL:            strings.TrimSpace(settings.String("epusdt_return_url", "")),
		EpusdtAllowedHosts:         strings.TrimSpace(settings.String("epusdt_allowed_hosts", "")),
	}
	if config.Enabled && strings.EqualFold(config.Version, "v2") {
		if _, err := parseEPayPrivateKey(config.PrivateKey); err != nil {
			return billingapp.RechargeConfig{}, domain.ErrRechargeConfigUnavailable
		}
		if _, err := parseEPayPublicKey(config.PlatformPublicKey); err != nil {
			return billingapp.RechargeConfig{}, domain.ErrRechargeConfigUnavailable
		}
	}
	return config, nil
}

func rechargeTiers(rawPresets, rawBonuses string) ([]billingapp.RechargeTier, error) {
	var presets []json.Number
	if err := json.Unmarshal([]byte(rawPresets), &presets); err != nil {
		return nil, err
	}
	var bonuses map[string]json.Number
	if err := json.Unmarshal([]byte(rawBonuses), &bonuses); err != nil {
		return nil, err
	}
	tiers := make([]billingapp.RechargeTier, 0, len(presets))
	seen := make(map[string]struct{}, len(presets))
	for _, value := range presets {
		amount, err := domain.NormalizePositiveMoney(string(value))
		if err != nil {
			return nil, err
		}
		if _, ok := seen[amount]; ok {
			return nil, domain.ErrRechargeConfigUnavailable
		}
		seen[amount] = struct{}{}
		bonus := "0.00"
		for key, value := range bonuses {
			normalizedKey, err := domain.NormalizePositiveMoney(key)
			if err != nil || normalizedKey != amount {
				continue
			}
			bonus, err = domain.NormalizeNonNegativeMoney(string(value))
			if err != nil {
				return nil, err
			}
			break
		}
		tiers = append(tiers, billingapp.RechargeTier{Points: amount, BonusPoints: bonus})
	}
	return tiers, nil
}
