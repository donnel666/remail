package kitesim

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/platform"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Service) QueueOperationReconcile(ctx context.Context, operationID uint64, meta MutationMeta) (*OperationItem, error) {
	if operationID == 0 {
		return nil, ErrOperationMissing
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	var operation operationModel
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&operation, operationID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrOperationMissing
			}
			return err
		}
		if OperationStatus(operation.Status) == OperationRunning && operation.StartedAt != nil && !operation.StartedAt.After(now.Add(-operationTaskTimeout-operationSettlementGrace)) {
			operation.Status = string(OperationUncertain)
			operation.FinishedAt = &now
			operation.SecretPayload = ""
			operation.LastSafeError = "Kitesim 任务执行超时，已停止自动重放并等待只读对账。"
		}
		if OperationStatus(operation.Status) != OperationUncertain && OperationStatus(operation.Status) != OperationRequiresAction {
			return ErrOperationState
		}
		operation.ReconcileRequestedAt = &now
		if err := tx.Model(&operationModel{}).Where("id = ?", operation.ID).Updates(map[string]any{
			"status": operation.Status, "finished_at": operation.FinishedAt,
			"secret_payload": operation.SecretPayload, "last_safe_error": operation.LastSafeError,
			"reconcile_requested_at": now,
		}).Error; err != nil {
			return fmt.Errorf("queue Kitesim operation reconciliation: %w", err)
		}
		return s.createAudit(
			platform.WithGormTx(ctx, tx), meta, "kitesim.operation.reconcile",
			"kitesim_operation", strconv.FormatUint(operation.ID, 10),
			"queued read-only Kitesim operation reconciliation",
		)
	})
	if err != nil {
		return nil, err
	}
	if queue, ok := s.queue.(OperationReconcileQueue); ok && queue != nil {
		_, _ = queue.EnqueueOperationReconcile(ctx, operation.ID)
	}
	return s.operationViewByID(ctx, operation.ID)
}

func (s *Service) ResolveOperation(ctx context.Context, operationID uint64, outcome OperationStatus, note string, meta MutationMeta) (*OperationItem, error) {
	note = strings.TrimSpace(note)
	if operationID == 0 || outcome != OperationSucceeded && outcome != OperationFailed || note == "" || len(note) > 500 {
		return nil, ErrInvalidInput
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	var operation operationModel
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&operation, operationID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrOperationMissing
			}
			return err
		}
		status := OperationStatus(operation.Status)
		if status == OperationRunning && operation.StartedAt != nil && !operation.StartedAt.After(now.Add(-operationTaskTimeout-operationSettlementGrace)) {
			status = OperationUncertain
		}
		if status == OperationSucceeded || status == OperationFailed {
			if status == outcome {
				return nil
			}
			return ErrOperationState
		}
		if status != OperationUncertain && status != OperationRequiresAction {
			return ErrOperationState
		}
		if operation.ReconcileAttempts == 0 {
			return ErrOperationState
		}
		updates := map[string]any{
			"status": outcome, "secret_payload": nil, "finished_at": now,
			"resolution_source": "manual", "resolution_note": note,
			"resolved_by_user_id": meta.OperatorUserID, "resolved_at": now,
		}
		if outcome == OperationSucceeded {
			updates["completed_count"] = gorm.Expr("requested_count")
			updates["last_safe_error"] = ""
		} else {
			updates["last_safe_error"] = note
		}
		if err := tx.Model(&operationModel{}).Where("id = ?", operation.ID).Updates(updates).Error; err != nil {
			return fmt.Errorf("resolve Kitesim operation: %w", err)
		}
		operation.Status = string(outcome)
		return s.createAudit(
			platform.WithGormTx(ctx, tx), meta, "kitesim.operation.resolve",
			"kitesim_operation", strconv.FormatUint(operation.ID, 10),
			fmt.Sprintf("manually resolved Kitesim operation as %s", outcome),
		)
	})
	if err != nil {
		return nil, err
	}
	if OperationStatus(operation.Status) == outcome {
		s.queueOperationRefresh(operation)
	}
	return s.operationViewByID(ctx, operation.ID)
}

func (s *Service) processOperationReconcile(ctx context.Context, operationID uint64) error {
	var operation operationModel
	if err := s.db.WithContext(ctx).First(&operation, operationID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("load Kitesim operation reconciliation: %w", err)
	}
	status := OperationStatus(operation.Status)
	if status != OperationUncertain && status != OperationRequiresAction {
		return nil
	}
	refs, err := decodeOperationRefs([]byte(operation.ProviderOrderNos))
	if err != nil {
		return s.finishReconcileAttempt(ctx, operation, "", operation.CompletedCount, err)
	}
	var account accountModel
	if err := s.db.WithContext(ctx).Where("id = ? AND deleted_at IS NULL", operation.AccountID).First(&account).Error; err != nil {
		return s.finishReconcileAttempt(ctx, operation, "", operation.CompletedCount, err)
	}
	var outcome OperationStatus
	completed := operation.CompletedCount
	queryErr := s.withSingleUpstreamClient(ctx, account.Account, proxydomain.ProxyPurposeAuth, func(client *Client) error {
		token, err := s.authenticateOperationClient(ctx, client, account)
		if err != nil {
			return err
		}
		switch OperationKind(operation.Kind) {
		case OperationPurchase:
			if len(refs.OrderNos) == 0 || len(refs.OrderNos) > operation.RequestedCount {
				return ErrOperationState
			}
			for _, orderNo := range refs.OrderNos {
				detail, err := client.PhoneOrderDetail(ctx, token, orderNo)
				if err != nil || detail == nil || !phoneOrderPaid(*detail) {
					return errors.Join(ErrPaymentUncertain, err)
				}
			}
			completed = len(refs.OrderNos)
			if completed == operation.RequestedCount {
				outcome = OperationSucceeded
			} else {
				outcome = OperationFailed
			}
		case OperationRecharge:
			if refs.OutTransNo == "" || refs.PayOrderID == "" {
				return ErrOperationState
			}
			paid, err := client.queryRechargePayment(ctx, token, rechargePayment{
				OriginalOrder: refs.OutTransNo, PaymentOrderID: refs.PayOrderID,
			})
			if paid {
				completed = 1
				outcome = OperationSucceeded
				return nil
			}
			if errors.Is(err, errPaymentRejected) {
				completed = 0
				outcome = OperationFailed
				return nil
			}
			return errors.Join(ErrPaymentUncertain, err)
		case OperationRenew:
			if operation.PhoneID == nil || len(refs.OrderNos) != 1 {
				return ErrOperationState
			}
			return ErrOperationState
		default:
			return ErrOperationState
		}
		return nil
	})
	return s.finishReconcileAttempt(ctx, operation, outcome, completed, queryErr)
}

func (s *Service) finishReconcileAttempt(ctx context.Context, operation operationModel, outcome OperationStatus, completed int, queryErr error) error {
	now := s.now().UTC().Truncate(time.Millisecond)
	updates := map[string]any{
		"last_reconciled_at": now, "reconcile_attempts": gorm.Expr("reconcile_attempts + 1"),
	}
	if outcome == OperationSucceeded || outcome == OperationFailed {
		updates["status"] = outcome
		updates["finished_at"] = now
		updates["resolution_source"] = "query"
		updates["resolved_at"] = now
		if outcome == OperationSucceeded {
			updates["completed_count"] = gorm.Expr("requested_count")
			updates["last_safe_error"] = ""
		} else if OperationKind(operation.Kind) == OperationPurchase && completed > 0 {
			updates["completed_count"] = completed
			updates["last_safe_error"] = fmt.Sprintf(
				"Kitesim 补号仅完成 %d/%d；只读对账已确认已保存订单，未完成部分不会自动重放。",
				completed,
				operation.RequestedCount,
			)
		} else {
			updates["completed_count"] = completed
			updates["last_safe_error"] = "Kitesim 上游明确返回支付失败。"
		}
	} else if queryErr != nil {
		updates["last_safe_error"] = "Kitesim 只读对账尚未确认最终结果，请稍后重试或人工核对。"
		if !errors.Is(queryErr, ErrOperationState) && !errors.Is(queryErr, ErrLoginFailed) {
			updates["reconcile_requested_at"] = now.Add(reconcileRetryInterval)
		}
	}
	result := s.db.WithContext(context.WithoutCancel(ctx)).Model(&operationModel{}).
		Where("id = ? AND status IN ?", operation.ID, []OperationStatus{OperationUncertain, OperationRequiresAction}).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("finish Kitesim reconciliation: %w", result.Error)
	}
	if result.RowsAffected > 0 && (outcome == OperationSucceeded || outcome == OperationFailed) {
		operation.Status = string(outcome)
		s.queueOperationRefresh(operation)
	}
	return nil
}

func (s *Service) operationViewByID(ctx context.Context, operationID uint64) (*OperationItem, error) {
	var operation operationModel
	err := s.db.WithContext(ctx).First(&operation, operationID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrOperationMissing
	}
	if err != nil {
		return nil, fmt.Errorf("load Kitesim operation: %w", err)
	}
	var account string
	if err := s.db.WithContext(ctx).Model(&accountModel{}).
		Where("id = ?", operation.AccountID).Pluck("account", &account).Error; err != nil {
		return nil, fmt.Errorf("load Kitesim operation account: %w", err)
	}
	phone := ""
	if operation.PhoneID != nil {
		if err := s.db.WithContext(ctx).Model(&phoneModel{}).
			Where("id = ?", *operation.PhoneID).Pluck("phone_number", &phone).Error; err != nil {
			return nil, fmt.Errorf("load Kitesim operation phone: %w", err)
		}
	}
	return operationView(operation, account, phone), nil
}
