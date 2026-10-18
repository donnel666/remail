package infra

import (
	"context"
	"errors"
	"fmt"

	"github.com/donnel666/remail/internal/platform"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"github.com/donnel666/remail/internal/trade/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *Repo) ListUnavailableDaumOrders(ctx context.Context, id uint, limit int) ([]tradeapp.DaumUnavailableOrder, error) {
	return r.listUnavailableResourceOrders(ctx, "daum", id, limit)
}
func (r *Repo) ListUnavailableMicrosoftOrders(ctx context.Context, id uint, limit int) ([]tradeapp.MicrosoftUnavailableOrder, error) {
	return r.listUnavailableResourceOrders(ctx, "microsoft", id, limit)
}
func (r *Repo) AssertDaumRefundFailure(ctx context.Context, proof tradeapp.DaumUnavailableOrder) error {
	return r.assertResourceRefundFailure(ctx, "daum", proof)
}
func (r *Repo) AssertMicrosoftRefundFailure(ctx context.Context, proof tradeapp.MicrosoftUnavailableOrder) error {
	return r.assertResourceRefundFailure(ctx, "microsoft", proof)
}

func (r *Repo) listUnavailableResourceOrders(ctx context.Context, provider string, resourceID uint, limit int) ([]tradeapp.ResourceFailureProof, error) {
	if provider != "microsoft" && provider != "daum" {
		return nil, domain.ErrInvalidOrderRequest
	}
	category := "''"
	if provider == "daum" {
		category = "resource.failure_category"
	}
	query := r.dbFor(ctx).Table("orders o").Select("o.order_no, allocation.resource_id, resource.credential_revision, resource.validation_generation, "+category+" AS failure_category").
		Joins("JOIN "+provider+"_allocations allocation ON allocation.order_no=o.order_no AND allocation.status='allocated'").
		Joins("JOIN "+provider+"_resources resource ON resource.id=allocation.resource_id AND resource.status='abnormal'").
		Where("o.allocation_type=? AND o.status='active' AND o.debit_tx_id IS NOT NULL AND o.refund_tx_id IS NULL", provider).Order("o.id ASC")
	if provider == "daum" {
		query = query.Where("allocation.guard_type='daum'")
	}
	if resourceID > 0 {
		query = query.Where("resource.id=?", resourceID)
	}
	if limit <= 0 {
		limit = 200
	}
	var proofs []tradeapp.ResourceFailureProof
	if err := query.Limit(limit).Scan(&proofs).Error; err != nil {
		return nil, fmt.Errorf("list unavailable %s orders: %w", provider, err)
	}
	return proofs, nil
}

// The caller locks wallets first; hold the root/credential proof until the refund commits.
func (r *Repo) assertResourceRefundFailure(ctx context.Context, provider string, proof tradeapp.ResourceFailureProof) error {
	if proof.CredentialRevision == 0 {
		return domain.ErrInvalidOrderRequest
	}
	resource, err := r.lockRefundResource(ctx, provider, proof.OrderNo, proof.ResourceID, false)
	if err != nil {
		return err
	}
	if resource.Status != "abnormal" || resource.CredentialRevision != proof.CredentialRevision || resource.ValidationGeneration != proof.ValidationGeneration {
		return domain.ErrOrderStateConflict
	}
	return nil
}

// Completion and settlement must not outrun a deferred or failed refund.
// Callers hold wallets first when moving money, and lock the order only after this fence.
func (r *Repo) LockPurchaseRefundState(ctx context.Context, orderNo string) (bool, error) {
	return r.lockPurchaseRefundState(ctx, orderNo, false)
}

func (r *Repo) lockPurchaseRefundState(ctx context.Context, orderNo string, skipLocked bool) (bool, error) {
	if _, ok := platform.GormTxFromContext(ctx); !ok {
		return false, domain.ErrInvalidOrderRequest
	}
	order, err := r.FindOrder(ctx, orderNo)
	if err != nil {
		return false, err
	}
	// A valid late delivery can reactivate a completed purchase before the order lock.
	if (order.Status != domain.OrderStatusActive && order.Status != domain.OrderStatusCompleted) || order.ServiceMode != domain.ServiceModePurchase || order.AllocationType == nil || order.DebitTxID == nil || order.RefundTxID != nil {
		return false, nil
	}
	provider := string(*order.AllocationType)
	if provider != "microsoft" && provider != "daum" {
		return false, nil
	}
	var allocation struct{ ResourceID uint }
	query := r.dbFor(ctx).Table(provider+"_allocations").Select("resource_id").Where("order_no=? AND status='allocated'", orderNo)
	if provider == "daum" {
		query = query.Where("guard_type='daum'")
	}
	if err := query.Take(&allocation).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	resource, err := r.lockRefundResource(ctx, provider, orderNo, allocation.ResourceID, skipLocked)
	if err != nil {
		return false, err
	}
	return resource.Status == "abnormal", nil
}

type refundResourceState struct {
	Status                                   string
	CredentialRevision, ValidationGeneration uint64
}

func (r *Repo) lockRefundResource(ctx context.Context, provider, orderNo string, resourceID uint, skipLocked bool) (*refundResourceState, error) {
	tx, ok := platform.GormTxFromContext(ctx)
	if !ok || (provider != "microsoft" && provider != "daum") || orderNo == "" || resourceID == 0 {
		return nil, domain.ErrInvalidOrderRequest
	}
	lock := clause.Locking{Strength: "UPDATE"}
	if skipLocked {
		lock.Options = "SKIP LOCKED"
	}
	var root struct{ ID, OwnerUserID uint }
	err := tx.WithContext(ctx).Table("email_resources").Select("id,owner_user_id").Clauses(lock).Where("id=? AND type=?", resourceID, provider).Take(&root).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domain.ErrOrderStateConflict
	}
	if err != nil {
		return nil, err
	}
	var resource refundResourceState
	query := tx.WithContext(ctx).Table(provider+"_resources").Select("status,credential_revision,validation_generation").Clauses(lock).Where("id=?", resourceID)
	if provider == "daum" {
		query = query.Where("owner_user_id=? AND resource_type='daum'", root.OwnerUserID)
	}
	err = query.Take(&resource).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domain.ErrOrderStateConflict
	}
	if err != nil {
		return nil, err
	}
	var currentAllocation struct{ ResourceID uint }
	allocation := tx.WithContext(ctx).Table(provider+"_allocations").Select("resource_id").Clauses(lock).Where("order_no=? AND resource_id=? AND status='allocated'", orderNo, resourceID)
	if provider == "daum" {
		allocation = allocation.Where("guard_type='daum'")
	}
	if err := allocation.Take(&currentAllocation).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domain.ErrOrderStateConflict
	} else if err != nil {
		return nil, err
	}
	return &resource, nil
}
