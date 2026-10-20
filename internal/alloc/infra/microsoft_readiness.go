package infra

import (
	"context"
	"fmt"
	"strings"

	"github.com/donnel666/remail/internal/alloc/domain"
)

func (r *Repo) MicrosoftAllocationReady(ctx context.Context, orderNo string, allocationID uint, enterprise bool) (bool, error) {
	if strings.TrimSpace(orderNo) == "" {
		return false, domain.ErrInvalidAllocationRequest
	}
	var row struct {
		ID    uint
		Ready bool
	}
	if enterprise {
		query := r.dbFor(ctx).Table("microsoft_enterprise_allocations allocation").
			Select("allocation.id, (allocation.status='allocated' AND resource.status='normal' AND allocation.email=resource.email_address) AS ready").
			Joins("JOIN microsoft_enterprise_resources resource ON resource.id=allocation.resource_id").
			Joins("JOIN email_resources root ON root.id=resource.id AND root.type='microsoft_enterprise'").
			Where("allocation.order_no=?", orderNo)
		if allocationID != 0 {
			query = query.Where("allocation.id=?", allocationID)
		}
		if err := query.Limit(1).Scan(&row).Error; err != nil {
			return false, err
		}
		return (row.ID == 0 && allocationID == 0) || row.Ready, nil
	}
	query := r.dbFor(ctx).Table("microsoft_allocations allocation").Select(`allocation.id,(allocation.status='allocated' AND EXISTS(
 SELECT 1 FROM microsoft_resources resource JOIN email_resources root ON root.id=resource.id AND root.type='microsoft'
 WHERE resource.id=allocation.resource_id AND resource.status='normal' AND (
 (allocation.mailbox='main' AND resource.email_address=allocation.email)
 OR (allocation.mailbox='alias' AND EXISTS(SELECT 1 FROM explicit_aliases alias WHERE alias.id=allocation.explicit_alias_id AND alias.resource_id=resource.id AND alias.email=allocation.email AND alias.status='normal'))
 OR (allocation.mailbox='dot' AND EXISTS(SELECT 1 FROM dot_aliases alias WHERE alias.id=allocation.dot_alias_id AND alias.resource_id=resource.id AND alias.email=allocation.email AND alias.status='normal'))
 OR (allocation.mailbox='plus' AND EXISTS(SELECT 1 FROM plus_aliases alias WHERE alias.id=allocation.plus_alias_id AND alias.resource_id=resource.id AND alias.email=allocation.email AND alias.status='normal'))
 ))) AS ready`).Where("allocation.order_no=?", strings.TrimSpace(orderNo))
	if allocationID != 0 {
		query = query.Where("allocation.id=?", allocationID)
	}
	if err := query.Limit(1).Scan(&row).Error; err != nil {
		return false, fmt.Errorf("check Microsoft allocation readiness: %w", err)
	}
	return (row.ID == 0 && allocationID == 0) || row.Ready, nil
}
