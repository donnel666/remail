package infra

import (
	"context"
	"fmt"

	"github.com/donnel666/remail/internal/alloc/domain"
	"gorm.io/gorm"
)

// Explicit Microsoft/Daum aliases belong to platform user 1; snapshot new allocations only.
func (r *Repo) snapshotAllocationSupplier(ctx context.Context, orderNo string, resourceID uint, explicitAlias bool) error {
	owner := any(gorm.Expr("(SELECT owner_user_id FROM email_resources WHERE id = ?)", resourceID))
	if explicitAlias {
		owner = uint(1)
	}
	result := r.dbFor(ctx).Model(&OrderGuardModel{}).Where("order_no = ? AND owner_user_id IS NULL", orderNo).
		Update("owner_user_id", owner)
	if result.Error != nil {
		return fmt.Errorf("snapshot allocation supplier: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return domain.ErrAllocationConflict
	}
	return nil
}

func (r *Repo) AllocationSupplierUserID(ctx context.Context, orderNo string) (uint, error) {
	var guard OrderGuardModel
	if err := r.dbFor(ctx).Where("order_no = ?", orderNo).Take(&guard).Error; err != nil {
		return 0, fmt.Errorf("load allocation supplier: %w", err)
	}
	if guard.OwnerUserID == nil || *guard.OwnerUserID == 0 {
		return 0, domain.ErrInvalidAllocationRequest
	}
	return *guard.OwnerUserID, nil
}
