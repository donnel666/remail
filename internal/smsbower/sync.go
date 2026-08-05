package smsbower

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/money"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type priceChange struct {
	Code      string
	Name      string
	Previous  string
	Current   string
	ChangedAt time.Time
}

func (s *Service) Sync(ctx context.Context) error {
	var config configModel
	if err := s.dbFor(ctx).First(&config, "id = 1").Error; err != nil {
		return fmt.Errorf("load SMSBower sync config: %w", err)
	}
	apiKey := strings.TrimSpace(config.APIKey)
	if apiKey == "" {
		return nil
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	balance, err := s.client.Balance(ctx, apiKey)
	if err != nil {
		return s.syncFailure(ctx, err)
	}
	services, err := s.client.Services(ctx, apiKey)
	if err != nil {
		return s.syncFailure(ctx, err)
	}
	prices, err := s.client.GmailPrices(ctx, apiKey)
	if err != nil {
		return s.syncFailure(ctx, err)
	}
	usedCodes := make(map[string]bool)
	var routedCodes []string
	if err := s.dbFor(ctx).Model(&routeModel{}).Where("enabled = ?", true).Distinct("service_code").Pluck("service_code", &routedCodes).Error; err != nil {
		return fmt.Errorf("load routed SMSBower services: %w", err)
	}
	for _, code := range routedCodes {
		usedCodes[code] = true
	}
	missingRoutedPrices := make([]string, 0)
	for code := range usedCodes {
		if _, ok := prices[code]; !ok {
			missingRoutedPrices = append(missingRoutedPrices, code)
		}
	}
	if len(missingRoutedPrices) > 0 {
		sort.Strings(missingRoutedPrices)
		return s.syncFailure(ctx, fmt.Errorf("%w: routed Gmail prices missing for %s", ErrRemote, strings.Join(missingRoutedPrices, ",")))
	}
	changes := make([]priceChange, 0)
	var state accountStateModel
	threshold := parseDecimalOrZero(config.BalanceWarningThreshold)
	err = s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&state, "id = 1").Error; err != nil {
			return err
		}
		var existing []serviceModel
		if err := tx.Find(&existing).Error; err != nil {
			return err
		}
		byCode := make(map[string]serviceModel, len(existing))
		for _, item := range existing {
			byCode[item.Code] = item
		}
		if err := tx.Model(&serviceModel{}).Where("active = ?", true).Update("active", false).Error; err != nil {
			return err
		}
		for _, remote := range services {
			rest, supportsGmail := prices[remote.Code]
			if !supportsGmail {
				continue
			}
			priceValue := money.Format(rest.Price)
			stock := uint(max(rest.Count, 0))
			old, exists := byCode[remote.Code]
			changed := exists && parseDecimalOrZero(old.GmailPrice).Cmp(rest.Price) != 0
			notificationPending := changed
			if !notificationPending && exists && old.PriceChangedAt != nil {
				notificationPending = old.LastNotifiedPrice == nil || parseDecimalOrZero(*old.LastNotifiedPrice).Cmp(rest.Price) != 0
			}
			if notificationPending && usedCodes[remote.Code] {
				previous := old.GmailPrice
				changedAt := now
				if !changed {
					if old.PreviousPrice != nil {
						previous = *old.PreviousPrice
					}
					if old.PriceChangedAt != nil {
						changedAt = old.PriceChangedAt.UTC()
					}
				}
				changes = append(changes, priceChange{
					Code: remote.Code, Name: remote.Name, Previous: previous, Current: priceValue, ChangedAt: changedAt,
				})
			}
			if !exists {
				if err := tx.Create(&serviceModel{
					Code: remote.Code, Name: remote.Name, GmailPrice: priceValue, GmailStock: stock,
					Active: true, LastSeenAt: now,
				}).Error; err != nil {
					return err
				}
				continue
			}
			updates := map[string]any{
				"name": remote.Name, "gmail_price": priceValue, "gmail_stock": stock,
				"active": true, "last_seen_at": now,
			}
			if changed {
				updates["previous_price"] = old.GmailPrice
				updates["price_changed_at"] = now
			}
			if err := tx.Model(&serviceModel{}).Where("code = ?", remote.Code).Updates(updates).Error; err != nil {
				return err
			}
		}
		updates := map[string]any{
			"balance": money.Format(balance), "health_status": "healthy", "consecutive_failures": 0,
			"last_safe_error": "", "last_synced_at": now, "last_success_at": now,
		}
		if state.FailureAlertActive {
			updates["failure_alert_active"] = false
			updates["generation"] = gorm.Expr("generation + 1")
		}
		if balance.GreaterThan(threshold) && state.BalanceAlertActive {
			updates["balance_alert_active"] = false
			updates["generation"] = gorm.Expr("generation + 1")
		}
		if err := tx.Model(&accountStateModel{}).Where("id = 1").Updates(updates).Error; err != nil {
			return err
		}
		return tx.First(&state, "id = 1").Error
	})
	if err != nil {
		return fmt.Errorf("persist SMSBower sync: %w", err)
	}
	if balance.LessThanOrEqual(threshold) && !state.BalanceAlertActive {
		alert := Alert{
			ID: fmt.Sprintf("smsbower-balance-%d", state.Generation), Subject: "SMSBower 上游余额不足",
			Body: fmt.Sprintf("SMSBower 当前余额为 %s，已低于或等于预警阈值 %s。请及时充值，以免 Gmail 上游履约停止。", money.Format(balance), money.Format(threshold)),
		}
		if err := s.notify(ctx, alert); err != nil {
			return err
		}
		if err := s.dbFor(ctx).Model(&accountStateModel{}).Where("id = 1 AND balance_alert_active = ?", false).Update("balance_alert_active", true).Error; err != nil {
			return fmt.Errorf("mark SMSBower balance alert: %w", err)
		}
	}
	if len(changes) > 0 {
		sort.Slice(changes, func(i, j int) bool { return changes[i].Code < changes[j].Code })
		lines := make([]string, len(changes))
		parts := make([]string, len(changes))
		for i := range changes {
			lines[i] = fmt.Sprintf("%s（%s）：%s → %s", changes[i].Name, changes[i].Code, changes[i].Previous, changes[i].Current)
			parts[i] = changes[i].Code + ":" + changes[i].Current + ":" + changes[i].ChangedAt.UTC().Format(time.RFC3339Nano)
		}
		alert := Alert{
			ID: "smsbower-price-" + stableDigest(strings.Join(parts, "|")), Subject: "SMSBower Gmail 上游价格变动",
			Body: "以下已映射服务的 Gmail 价格发生变化：\n" + strings.Join(lines, "\n") + "\n系统会继续按最低毛利率拦截亏损订单，请检查项目售价。",
		}
		if err := s.notify(ctx, alert); err != nil {
			return err
		}
		for _, change := range changes {
			if err := s.dbFor(ctx).Model(&serviceModel{}).
				Where("code = ? AND gmail_price = ? AND price_changed_at = ?", change.Code, change.Current, change.ChangedAt).
				Update("last_notified_price", change.Current).Error; err != nil {
				return fmt.Errorf("mark SMSBower price notification: %w", err)
			}
		}
	}
	return nil
}

func (s *Service) syncFailure(ctx context.Context, cause error) error {
	now := s.now()
	safe := safeRemoteError(cause)
	health := "degraded"
	if errors.Is(cause, ErrBadKey) {
		health = "unavailable"
	}
	var state accountStateModel
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&accountStateModel{}).Where("id = 1").Updates(map[string]any{
			"health_status": health, "consecutive_failures": gorm.Expr("consecutive_failures + 1"),
			"last_safe_error": safe, "last_synced_at": now,
		}).Error; err != nil {
			return err
		}
		return tx.First(&state, "id = 1").Error
	})
	if err != nil {
		return fmt.Errorf("persist SMSBower sync failure: %w", err)
	}
	if (errors.Is(cause, ErrBadKey) || state.ConsecutiveFailures >= 3) && !state.FailureAlertActive {
		alert := Alert{
			ID: fmt.Sprintf("smsbower-failure-%d", state.Generation), Subject: "SMSBower 上游连接异常",
			Body: fmt.Sprintf("SMSBower 同步失败：%s。当前连续失败 %d 次，请检查 API Key 和上游服务状态。", safe, state.ConsecutiveFailures),
		}
		if notifyErr := s.notify(ctx, alert); notifyErr != nil {
			return errors.Join(cause, notifyErr)
		}
		if err := s.dbFor(ctx).Model(&accountStateModel{}).Where("id = 1 AND failure_alert_active = ?", false).Update("failure_alert_active", true).Error; err != nil {
			return errors.Join(cause, err)
		}
	}
	return cause
}

func (s *Service) notify(ctx context.Context, alert Alert) error {
	if s.notifier == nil {
		return nil
	}
	if err := s.notifier.NotifySMSBower(context.WithoutCancel(ctx), alert); err != nil {
		return fmt.Errorf("send SMSBower alert: %w", err)
	}
	return nil
}

func safeRemoteError(err error) string {
	switch {
	case errors.Is(err, ErrBadKey):
		return "API Key 无效"
	case errors.Is(err, ErrInsufficientBalance):
		return "上游余额不足"
	case errors.Is(err, ErrNoMail):
		return "上游暂无可用 Gmail"
	case errors.Is(err, ErrPriceChanged):
		return "上游价格已变化"
	case errors.Is(err, ErrCodeWaiting):
		return "等待验证码"
	case remoteActionFinal(err):
		return "上游激活已结束"
	default:
		return "上游网络或响应异常"
	}
}

func stableDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
