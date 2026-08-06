package smsbower

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/donnel666/remail/internal/money"
	"github.com/donnel666/remail/internal/upstream"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Service) DispatchDueOrders(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 200
	}
	now := s.now()
	var provisionIDs []uint
	if err := s.dbFor(ctx).Model(&orderModel{}).
		Where("status = ? OR (status IN ? AND next_poll_at IS NOT NULL AND next_poll_at <= ?)",
			StatusPending, []string{StatusProvisioning, StatusFailed, StatusUnknown}, now).
		Order("id ASC").Limit(limit).Pluck("id", &provisionIDs).Error; err != nil {
		return 0, fmt.Errorf("list SMSBower provision orders: %w", err)
	}
	queued := 0
	for _, id := range provisionIDs {
		if err := s.scheduleProvision(ctx, id); err != nil {
			return queued, err
		}
		queued++
	}
	remaining := max(limit-len(provisionIDs), 0)
	if remaining == 0 {
		return queued, nil
	}
	var pollIDs []uint
	if err := s.dbFor(ctx).Model(&orderModel{}).
		Where("status IN ? AND next_poll_at IS NOT NULL AND next_poll_at <= ?",
			[]string{StatusActive, StatusCompleting, StatusCancelling, StatusCompleted, StatusCancelled}, now).
		Order("id ASC").Limit(remaining).Pluck("id", &pollIDs).Error; err != nil {
		return queued, fmt.Errorf("list SMSBower poll orders: %w", err)
	}
	for _, id := range pollIDs {
		if err := s.schedulePoll(ctx, id); err != nil {
			return queued, err
		}
		queued++
	}
	return queued, nil
}

func (s *Service) Provision(ctx context.Context, orderID uint) error {
	order, claimed, err := s.claimProvision(ctx, orderID)
	if err != nil || order == nil {
		return err
	}
	switch order.Status {
	case StatusActive:
		if err := s.ensureTradeActivation(ctx, *order); err != nil {
			return err
		}
		return s.schedulePoll(context.WithoutCancel(ctx), order.ID)
	case StatusFailed:
		if s.trade == nil {
			return errors.New("smsbower: trade callback unavailable")
		}
		if err := s.trade.FailGmailOrder(ctx, order.OrderNo, order.LastSafeError); err != nil {
			return err
		}
		return s.clearNextPoll(ctx, order.ID, StatusFailed)
	case StatusUnknown:
		return s.settleUnknown(ctx, *order)
	case StatusCompleted, StatusCancelled, StatusCancelling:
		return nil
	}
	if !claimed {
		return s.failProvision(ctx, *order, StatusUnknown, "采购请求状态不确定，系统已停止自动重试并退款。", true)
	}
	var config configModel
	if err := s.dbFor(ctx).First(&config, "id = 1").Error; err != nil {
		return s.failProvision(ctx, *order, StatusFailed, "SMSBower 配置不可用，Gmail 采购失败并退款。", false)
	}
	apiKey := strings.TrimSpace(config.APIKey)
	maxPrice, err := money.Parse(order.MaxPriceSnapshot)
	if apiKey == "" || err != nil {
		return s.failProvision(ctx, *order, StatusFailed, "SMSBower 配置不可用，Gmail 采购失败并退款。", false)
	}
	activation, err := s.client.Activate(ctx, apiKey, order.ServiceCode, maxPrice)
	if err != nil {
		explicit := errors.Is(err, ErrBadKey) || errors.Is(err, ErrNoMail) ||
			errors.Is(err, ErrInsufficientBalance) || errors.Is(err, ErrPriceChanged)
		if explicit {
			return s.failProvision(ctx, *order, StatusFailed, safeRemoteError(err)+"，Gmail 采购失败并退款。", false)
		}
		return s.failProvision(ctx, *order, StatusUnknown, "采购结果不确定，系统未重复采购并已退款，请管理员人工核对上游。", true)
	}
	now := s.now()
	expiresAt := now.Add(lifetime)
	updates := map[string]any{
		"remote_mail_id": activation.MailID, "email": activation.Email,
		"status": StatusActive, "started_at": now, "expires_at": expiresAt,
		"next_poll_at": now, "last_safe_error": "", "version": gorm.Expr("version + 1"),
	}
	result := s.dbFor(ctx).Model(&orderModel{}).
		Where("id = ? AND status = ?", order.ID, StatusProvisioning).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("activate SMSBower order: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		var current orderModel
		recovered := false
		if err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, order.ID).Error; err != nil {
				return err
			}
			if current.RemoteMailID != nil && *current.RemoteMailID == activation.MailID && current.Status == StatusCancelling {
				recovered = true
				return nil
			}
			if current.Status != StatusUnknown || current.RemoteMailID != nil {
				return nil
			}
			result := tx.Model(&orderModel{}).Where("id = ? AND status = ? AND remote_mail_id IS NULL", current.ID, StatusUnknown).
				Updates(map[string]any{
					"remote_mail_id": activation.MailID, "email": activation.Email,
					"status": StatusCancelling, "pending_remote_action": ActionCancel,
					"started_at": now, "expires_at": expiresAt, "completed_at": nil,
					"next_poll_at": now, "last_safe_error": "", "version": gorm.Expr("version + 1"),
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errors.New("smsbower: cancelled activation recovery state conflict")
			}
			recovered = true
			return nil
		}); err != nil {
			return fmt.Errorf("recover cancelled SMSBower activation: %w", err)
		}
		if !recovered {
			return errors.New("smsbower: provision order state conflict")
		}
		return s.schedulePoll(context.WithoutCancel(ctx), order.ID)
	}
	order.RemoteMailID = &activation.MailID
	order.Email = activation.Email
	order.Status = StatusActive
	order.StartedAt = &now
	order.ExpiresAt = &expiresAt
	if err := s.ensureTradeActivation(ctx, *order); err != nil {
		_ = s.schedulePoll(context.WithoutCancel(ctx), order.ID)
		return err
	}
	return s.schedulePoll(context.WithoutCancel(ctx), order.ID)
}

func (s *Service) claimProvision(ctx context.Context, orderID uint) (*orderModel, bool, error) {
	var order orderModel
	claimed := false
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&order, orderID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if order.Status != StatusPending {
			return nil
		}
		recoverAt := s.now().Add(provisionLease)
		if err := tx.Model(&orderModel{}).Where("id = ? AND status = ?", order.ID, StatusPending).
			Updates(map[string]any{"status": StatusProvisioning, "next_poll_at": recoverAt, "version": gorm.Expr("version + 1")}).Error; err != nil {
			return err
		}
		order.Status = StatusProvisioning
		order.NextPollAt = &recoverAt
		claimed = true
		return nil
	})
	if order.ID == 0 && err == nil {
		return nil, false, nil
	}
	return &order, claimed, err
}

func (s *Service) failProvision(ctx context.Context, order orderModel, status, safeMessage string, uncertain bool) error {
	now := s.now()
	result := s.dbFor(ctx).Model(&orderModel{}).Where("id = ? AND status = ?", order.ID, StatusProvisioning).Updates(map[string]any{
		"status": status, "last_safe_error": safeMessage, "completed_at": now, "next_poll_at": now,
		"version": gorm.Expr("version + 1"),
	})
	if result.Error != nil {
		return fmt.Errorf("fail SMSBower provision: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.New("smsbower: provision failure state conflict")
	}
	order.Status = status
	order.LastSafeError = safeMessage
	order.CompletedAt = &now
	order.NextPollAt = &now
	if uncertain {
		return s.settleUnknown(ctx, order)
	}
	if s.trade == nil {
		return errors.New("smsbower: trade callback unavailable")
	}
	if err := s.trade.FailGmailOrder(ctx, order.OrderNo, safeMessage); err != nil {
		return err
	}
	return s.clearNextPoll(ctx, order.ID, StatusFailed)
}

func (s *Service) settleUnknown(ctx context.Context, order orderModel) error {
	var callbackErr error
	if s.trade == nil {
		callbackErr = errors.New("smsbower: trade callback unavailable")
	} else {
		callbackErr = s.trade.FailGmailOrder(ctx, order.OrderNo, order.LastSafeError)
	}
	alertErr := s.notify(ctx, Alert{
		ID: "smsbower-unknown-" + stableDigest(order.OrderNo), Subject: "SMSBower Gmail 上游状态待人工核对",
		Body: fmt.Sprintf("订单 %s 的 SMSBower Gmail 上游状态不确定。系统不会重复采购，用户款项已进入退款流程；请在上游后台核对激活状态和实际扣费。", order.OrderNo),
	})
	if callbackErr == nil && alertErr == nil {
		return s.clearNextPoll(ctx, order.ID, StatusUnknown)
	}
	return errors.Join(callbackErr, alertErr)
}

func (s *Service) ensureTradeActivation(ctx context.Context, order orderModel) error {
	if s.trade == nil || order.StartedAt == nil || order.ExpiresAt == nil || order.Email == "" {
		return errors.New("smsbower: activation callback unavailable")
	}
	return s.trade.ActivateUpstreamOrder(ctx, upstream.Activation{
		OrderNo: order.OrderNo, Email: order.Email,
		StartedAt: order.StartedAt.UTC(), ExpiresAt: order.ExpiresAt.UTC(),
	})
}

func (s *Service) CancelOrder(ctx context.Context, orderNo string) (bool, error) {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return false, nil
	}
	now := s.now()
	var order orderModel
	schedule := false
	uncertain := false
	uncertainReason := "SMSBower 采购进行中收到取消请求，远端结果待人工核对。"
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_no = ?", orderNo).Take(&order).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		switch order.Status {
		case StatusPending:
			return tx.Model(&orderModel{}).Where("id = ?", order.ID).Updates(map[string]any{
				"status": StatusCancelled, "completed_at": now, "next_poll_at": nil,
				"last_safe_error": "SMSBower 订单已在远端采购前取消。", "version": gorm.Expr("version + 1"),
			}).Error
		case StatusProvisioning:
			uncertain = true
			return tx.Model(&orderModel{}).Where("id = ?", order.ID).Updates(map[string]any{
				"status": StatusUnknown, "completed_at": now, "next_poll_at": now,
				"last_safe_error": uncertainReason, "version": gorm.Expr("version + 1"),
			}).Error
		case StatusActive, StatusCompleting:
			if err := tx.Model(&orderModel{}).Where("id = ?", order.ID).Updates(map[string]any{
				"status": StatusCancelling, "pending_remote_action": ActionCancel,
				"next_poll_at": now, "last_safe_error": "", "version": gorm.Expr("version + 1"),
			}).Error; err != nil {
				return err
			}
			schedule = true
		case StatusCancelling:
			if err := tx.Model(&orderModel{}).Where("id = ?", order.ID).Update("next_poll_at", now).Error; err != nil {
				return err
			}
			schedule = true
		}
		return nil
	})
	if err != nil {
		return order.ID != 0, fmt.Errorf("cancel SMSBower order: %w", err)
	}
	if order.ID == 0 {
		return false, nil
	}
	if uncertain {
		order.Status = StatusUnknown
		order.LastSafeError = uncertainReason
		order.CompletedAt = &now
		order.NextPollAt = &now
		return true, s.settleUnknown(ctx, order)
	}
	if schedule {
		return true, s.schedulePoll(context.WithoutCancel(ctx), order.ID)
	}
	return true, nil
}

func (s *Service) Poll(ctx context.Context, orderID uint) error {
	order, claimed, err := s.claimPoll(ctx, orderID)
	if err != nil || order == nil {
		return err
	}
	if !claimed {
		return nil
	}
	switch order.Status {
	case StatusCompleted:
		if s.trade == nil {
			return errors.New("smsbower: trade callback unavailable")
		}
		if err := s.trade.CompleteGmailOrder(ctx, order.OrderNo, completionReason(*order)); err != nil {
			return err
		}
		return s.clearNextPoll(ctx, order.ID, StatusCompleted)
	case StatusCancelled:
		if s.trade == nil {
			return errors.New("smsbower: trade callback unavailable")
		}
		if err := s.trade.FailGmailOrder(ctx, order.OrderNo, "SMSBower 接码生命周期已结束且未收到验证码，订单已退款。"); err != nil {
			return err
		}
		return s.clearNextPoll(ctx, order.ID, StatusCancelled)
	case StatusUnknown:
		return s.settleUnknown(ctx, *order)
	case StatusFailed, StatusPending, StatusProvisioning:
		return nil
	}
	if order.Status == StatusActive {
		if err := s.ensureTradeActivation(ctx, *order); err != nil {
			return err
		}
	}
	if order.PendingRemoteAction != "" {
		return s.applyRemoteAction(ctx, *order)
	}
	if order.Status != StatusActive {
		return nil
	}
	var config configModel
	if err := s.dbFor(ctx).First(&config, "id = 1").Error; err != nil || order.RemoteMailID == nil || *order.RemoteMailID == 0 || strings.TrimSpace(config.APIKey) == "" {
		return s.deferPoll(ctx, order.ID, "SMSBower 激活信息不可用", ErrRemote)
	}
	code, err := s.client.Code(ctx, strings.TrimSpace(config.APIKey), *order.RemoteMailID)
	if errors.Is(err, ErrCodeWaiting) {
		return s.deferPoll(ctx, order.ID, "", nil)
	}
	if errors.Is(err, ErrActivationStatus) {
		updated, prepareErr := s.prepareTerminalAction(ctx, order.ID)
		if prepareErr != nil {
			return prepareErr
		}
		return s.finishRemoteAction(ctx, *updated, nil)
	}
	if errors.Is(err, ErrActivationMissing) {
		return s.markActivationMissing(ctx, *order)
	}
	if err != nil {
		return s.deferPoll(ctx, order.ID, safeRemoteError(err), err)
	}
	updated, err := s.recordCode(ctx, order.ID, code)
	if err != nil {
		return err
	}
	return s.applyRemoteAction(ctx, *updated)
}

func (s *Service) claimPoll(ctx context.Context, orderID uint) (*orderModel, bool, error) {
	var order orderModel
	claimed := false
	now := s.now()
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&order, orderID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if order.NextPollAt == nil || order.NextPollAt.UTC().After(now) {
			return nil
		}
		switch order.Status {
		case StatusActive, StatusCompleting, StatusCompleted, StatusCancelling, StatusCancelled, StatusUnknown:
		default:
			return nil
		}
		leaseUntil := now.Add(pollLease)
		if err := tx.Model(&orderModel{}).Where("id = ?", order.ID).
			Updates(map[string]any{"next_poll_at": leaseUntil, "version": gorm.Expr("version + 1")}).Error; err != nil {
			return err
		}
		order.NextPollAt = &leaseUntil
		claimed = true
		return nil
	})
	if order.ID == 0 && err == nil {
		return nil, false, nil
	}
	return &order, claimed, err
}

func (s *Service) prepareTerminalAction(ctx context.Context, orderID uint) (*orderModel, error) {
	var order orderModel
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&order, orderID).Error; err != nil {
			return err
		}
		if order.Status != StatusActive || order.PendingRemoteAction != "" {
			return nil
		}
		action, status := ActionComplete, StatusCompleting
		if order.ReceivedCount == 0 {
			action, status = ActionCancel, StatusCancelling
		}
		if err := tx.Model(&orderModel{}).Where("id = ?", order.ID).Updates(map[string]any{
			"status": status, "pending_remote_action": action, "next_poll_at": s.now(), "version": gorm.Expr("version + 1"),
		}).Error; err != nil {
			return err
		}
		order.Status, order.PendingRemoteAction = status, action
		return nil
	})
	return &order, err
}

func (s *Service) recordCode(ctx context.Context, orderID uint, value string) (*orderModel, error) {
	var order orderModel
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&order, orderID).Error; err != nil {
			return err
		}
		if order.Status != StatusActive || order.PendingRemoteAction != "" || order.ReceivedCount >= MaxCodes {
			return errors.New("smsbower: code order state conflict")
		}
		codes, err := decodeCodes(order.CodesJSON)
		if err != nil || len(codes) != int(order.ReceivedCount) {
			return errors.New("smsbower: code order count mismatch")
		}
		now := s.now()
		count := int(order.ReceivedCount) + 1
		codes = append(codes, Code{Seq: count, Code: value, ReceivedAt: now})
		payload, err := json.Marshal(codes)
		if err != nil {
			return err
		}
		action, status := nextCodeAction(count)
		if err := tx.Model(&orderModel{}).Where("id = ?", order.ID).Updates(map[string]any{
			"codes_json": string(payload), "received_count": count, "pending_remote_action": action,
			"status": status, "next_poll_at": now, "last_safe_error": "", "version": gorm.Expr("version + 1"),
		}).Error; err != nil {
			return err
		}
		order.CodesJSON, order.ReceivedCount = string(payload), uint8(count)
		order.PendingRemoteAction, order.Status = action, status
		return nil
	})
	return &order, err
}

func nextCodeAction(count int) (action, status string) {
	if count >= MaxCodes {
		return ActionComplete, StatusCompleting
	}
	return ActionWaitNext, StatusActive
}

func (s *Service) applyRemoteAction(ctx context.Context, order orderModel) error {
	var config configModel
	if err := s.dbFor(ctx).First(&config, "id = 1").Error; err != nil || order.RemoteMailID == nil || *order.RemoteMailID == 0 || strings.TrimSpace(config.APIKey) == "" {
		return s.deferPoll(ctx, order.ID, "SMSBower 激活信息不可用", ErrRemote)
	}
	status := 0
	switch order.PendingRemoteAction {
	case ActionWaitNext:
		status = 5
	case ActionComplete:
		status = 3
	case ActionCancel:
		status = 2
	default:
		return errors.New("smsbower: invalid pending remote action")
	}
	remoteErr := s.client.SetStatus(ctx, strings.TrimSpace(config.APIKey), *order.RemoteMailID, status)
	if remoteErr != nil && !remoteActionFinal(remoteErr) {
		return s.deferPoll(ctx, order.ID, safeRemoteError(remoteErr), remoteErr)
	}
	return s.finishRemoteAction(ctx, order, remoteErr)
}

func (s *Service) finishRemoteAction(ctx context.Context, order orderModel, remoteErr error) error {
	now := s.now()
	uncertainCancel := order.PendingRemoteAction == ActionCancel && errors.Is(remoteErr, ErrActivationMissing)
	cancelReason := "SMSBower 接码生命周期已结束且未收到验证码，订单已退款。"
	if uncertainCancel {
		cancelReason = "SMSBower 无法查询该激活记录，订单已进入退款流程，请管理员核对上游是否产生费用。"
	}
	updates := map[string]any{
		"pending_remote_action": "", "last_safe_error": "", "version": gorm.Expr("version + 1"),
	}
	switch order.PendingRemoteAction {
	case ActionWaitNext:
		updates["status"] = StatusActive
		updates["next_poll_at"] = now.Add(pollInterval)
	case ActionComplete:
		updates["status"] = StatusCompleted
		updates["completed_at"] = now
		updates["next_poll_at"] = now
	case ActionCancel:
		updates["status"] = StatusCancelled
		if uncertainCancel {
			updates["status"] = StatusUnknown
			updates["last_safe_error"] = cancelReason
		}
		updates["completed_at"] = now
		updates["next_poll_at"] = now
	}
	result := s.dbFor(ctx).Model(&orderModel{}).
		Where("id = ? AND pending_remote_action = ?", order.ID, order.PendingRemoteAction).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("finish SMSBower remote action: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.New("smsbower: remote action state conflict")
	}
	switch order.PendingRemoteAction {
	case ActionComplete:
		if s.trade == nil {
			return errors.New("smsbower: trade callback unavailable")
		}
		if err := s.trade.CompleteGmailOrder(ctx, order.OrderNo, completionReason(order)); err != nil {
			return err
		}
		return s.clearNextPoll(ctx, order.ID, StatusCompleted)
	case ActionCancel:
		if uncertainCancel {
			order.Status = StatusUnknown
			order.LastSafeError = cancelReason
			order.CompletedAt = &now
			order.NextPollAt = &now
			return s.settleUnknown(ctx, order)
		}
		if s.trade == nil {
			return errors.New("smsbower: trade callback unavailable")
		}
		if err := s.trade.FailGmailOrder(ctx, order.OrderNo, cancelReason); err != nil {
			return err
		}
		return s.clearNextPoll(ctx, order.ID, StatusCancelled)
	default:
		return nil
	}
}

func (s *Service) markActivationMissing(ctx context.Context, order orderModel) error {
	now := s.now()
	reason := "SMSBower 无法查询该激活记录，订单已进入退款流程，请管理员核对上游。"
	result := s.dbFor(ctx).Model(&orderModel{}).
		Where("id = ? AND status = ? AND pending_remote_action = ''", order.ID, StatusActive).
		Updates(map[string]any{
			"status": StatusUnknown, "completed_at": now, "next_poll_at": now,
			"last_safe_error": reason, "version": gorm.Expr("version + 1"),
		})
	if result.Error != nil {
		return fmt.Errorf("mark missing SMSBower activation: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.New("smsbower: missing activation state conflict")
	}
	order.Status = StatusUnknown
	order.CompletedAt = &now
	order.NextPollAt = &now
	order.LastSafeError = reason
	return s.settleUnknown(ctx, order)
}

func completionReason(order orderModel) string {
	if order.ReceivedCount >= MaxCodes {
		return "Gmail 已接收 3 个验证码，接码会话完成。"
	}
	return fmt.Sprintf("SMSBower 接码生命周期已结束，共接收 %d 个验证码。", order.ReceivedCount)
}

func (s *Service) deferPoll(ctx context.Context, orderID uint, safeMessage string, cause error) error {
	updates := map[string]any{"next_poll_at": s.now().Add(pollInterval)}
	if safeMessage != "" {
		updates["last_safe_error"] = safeMessage
	}
	if err := s.dbFor(ctx).Model(&orderModel{}).Where("id = ?", orderID).Updates(updates).Error; err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func remoteActionFinal(err error) bool {
	return errors.Is(err, ErrActivationMissing) || errors.Is(err, ErrActivationStatus)
}

func (s *Service) clearNextPoll(ctx context.Context, orderID uint, status string) error {
	return s.dbFor(ctx).Model(&orderModel{}).Where("id = ? AND status = ?", orderID, status).Update("next_poll_at", nil).Error
}
