package infra

import (
	"context"
	"errors"
	"fmt"
	"time"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/billing/domain"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SupplierSettlementModel struct {
	OrderNo        string     `gorm:"primaryKey;column:order_no"`
	SupplierUserID uint       `gorm:"column:supplier_user_id"`
	Amount         string     `gorm:"type:decimal(18,6);not null"`
	Status         string     `gorm:"not null"`
	AvailableAt    time.Time  `gorm:"not null;column:available_at"`
	CreditedAt     *time.Time `gorm:"column:credited_at"`
	CancelledAt    *time.Time `gorm:"column:cancelled_at"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (SupplierSettlementModel) TableName() string { return "supplier_settlements" }

func (r *BillingRepo) LockSupplierWallets(ctx context.Context, userIDs ...uint) error {
	tx, ok := platform.GormTxFromContext(ctx)
	if !ok {
		return fmt.Errorf("supplier wallet locks require a parent transaction")
	}
	_, err := r.lockWalletsInTx(ctx, tx, userIDs...)
	return err
}

func (r *BillingRepo) FreezeSupplierSettlement(ctx context.Context, req billingapp.SupplierSettlementRequest) error {
	amount, err := domain.NormalizePositiveMoney(req.Amount)
	if err != nil {
		return err
	}
	return r.withTx(ctx, func(ctx context.Context, tx *gorm.DB) error {
		wallet, err := r.lockWalletInTx(ctx, tx, req.SupplierUserID)
		if err != nil {
			return err
		}
		var existing SupplierSettlementModel
		found := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_no = ?", req.OrderNo).Limit(1).Find(&existing)
		if found.Error != nil {
			return found.Error
		}
		if found.RowsAffected != 0 {
			if existing.SupplierUserID != req.SupplierUserID || normalizeMoneyString(existing.Amount) != amount || existing.Status == "cancelled" {
				return domain.ErrSupplierSettlementConflict
			}
			return nil
		}
		settlement := SupplierSettlementModel{OrderNo: req.OrderNo, SupplierUserID: req.SupplierUserID, Amount: amount, Status: "frozen", AvailableAt: req.AvailableAt.UTC()}
		if err := tx.Create(&settlement).Error; err != nil {
			return fmt.Errorf("create supplier settlement: %w", err)
		}
		return r.supplierSettlementEntry(ctx, tx, wallet, settlement, domain.BalanceBucketSupplierFrozen, domain.TransactionDirectionIn, domain.TransactionTypeCredit, "freeze", "supplier_settlement")
	})
}

// Money changes lock wallets before settlement rows; deadline changes only lock the settlement.
func (r *BillingRepo) withSupplierSettlement(ctx context.Context, orderNo string, fn func(context.Context, *gorm.DB, *WalletModel, *SupplierSettlementModel) error) error {
	return r.withTx(ctx, func(ctx context.Context, tx *gorm.DB) error {
		var row SupplierSettlementModel
		if err := tx.First(&row, "order_no = ?", orderNo).Error; err != nil {
			return err
		}
		wallet, err := r.lockWalletInTx(ctx, tx, row.SupplierUserID)
		if err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "order_no = ?", orderNo).Error; err != nil {
			return err
		}
		return fn(ctx, tx, wallet, &row)
	})
}

func (r *BillingRepo) RescheduleSupplierSettlement(ctx context.Context, orderNo string, due time.Time) error {
	return r.withTx(ctx, func(_ context.Context, tx *gorm.DB) error {
		var row SupplierSettlementModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "order_no = ?", orderNo).Error; err != nil {
			return err
		}
		if row.Status == "credited" && !due.After(row.AvailableAt) {
			return nil
		}
		if row.Status != "frozen" {
			return domain.ErrSupplierSettlementConflict
		}
		return tx.Model(&row).Update("available_at", due.UTC()).Error
	})
}

func (r *BillingRepo) CreditSupplierSettlement(ctx context.Context, orderNo string, now time.Time) (bool, error) {
	credited := false
	err := r.withSupplierSettlement(ctx, orderNo, func(ctx context.Context, tx *gorm.DB, wallet *WalletModel, row *SupplierSettlementModel) error {
		if row.Status == "credited" {
			return nil
		}
		if row.Status != "frozen" || now.Before(row.AvailableAt) {
			return domain.ErrSupplierSettlementConflict
		}
		if err := r.supplierSettlementEntry(ctx, tx, wallet, *row, domain.BalanceBucketSupplierFrozen, domain.TransactionDirectionOut, domain.TransactionTypeTransfer, "release", "supplier_settlement"); err != nil {
			return err
		}
		if err := r.supplierSettlementEntry(ctx, tx, wallet, *row, domain.BalanceBucketSupplierAvailable, domain.TransactionDirectionIn, domain.TransactionTypeCredit, "credit", "supplier_settlement"); err != nil {
			return err
		}
		if err := tx.Model(row).Updates(map[string]any{"status": "credited", "credited_at": now.UTC()}).Error; err != nil {
			return err
		}
		credited = true
		return nil
	})
	return credited && err == nil, err
}

func (r *BillingRepo) CancelSupplierSettlement(ctx context.Context, orderNo string) error {
	return r.withSupplierSettlement(ctx, orderNo, func(ctx context.Context, tx *gorm.DB, wallet *WalletModel, row *SupplierSettlementModel) error {
		if row.Status == "cancelled" {
			return nil
		}
		bucket := domain.BalanceBucketSupplierFrozen
		if row.Status == "credited" {
			bucket = domain.BalanceBucketSupplierAvailable
		} else if row.Status != "frozen" {
			return domain.ErrSupplierSettlementConflict
		}
		if err := r.supplierSettlementEntry(ctx, tx, wallet, *row, bucket, domain.TransactionDirectionOut, domain.TransactionTypeManualAdjustment, "cancel", "supplier_settlement_cancel"); err != nil {
			if errors.Is(err, domain.ErrInsufficientBalance) {
				return domain.ErrSupplierSettlementBalance
			}
			return err
		}
		return tx.Model(row).Updates(map[string]any{"status": "cancelled", "cancelled_at": time.Now().UTC()}).Error
	})
}

func (r *BillingRepo) supplierSettlementEntry(ctx context.Context, tx *gorm.DB, wallet *WalletModel, row SupplierSettlementModel, bucket domain.BalanceBucket, direction domain.TransactionDirection, kind domain.TransactionType, action, bizType string) error {
	_, err := r.createLedgerEntryInTx(ctx, tx, wallet, ledgerEntryRequest{
		UserID: row.SupplierUserID, Bucket: bucket, Direction: direction, TransactionType: kind,
		Amount: row.Amount, BizType: bizType, BizID: row.OrderNo, IdempotencyKey: "supplier:" + row.OrderNo + ":" + action,
	})
	return err
}

var _ billingapp.SupplierSettlementRepository = (*BillingRepo)(nil)
