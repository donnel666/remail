package infra

import (
	"context"
	"fmt"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/alloc/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const microsoftEnterpriseProjectUnmatchedCondition = `NOT EXISTS (
 SELECT 1 FROM microsoft_enterprise_project_matches history
 WHERE history.resource_id = ms.id AND history.project_id = ?
)`

const microsoftEnterpriseUnusedCondition = `NOT EXISTS (
 SELECT 1 FROM microsoft_enterprise_allocations history
 WHERE history.resource_id = ms.id AND history.project_id = ?
)`

const microsoftEnterpriseNotUnderMaintenanceCondition = `ms.token_refresh_status NOT IN ('pending', 'processing')
 AND NOT EXISTS (
 SELECT 1 FROM mailmatch_resource_fetch_states maintenance_fetch
 WHERE maintenance_fetch.email_resource_id = ms.id
 AND maintenance_fetch.operation_kind = 'enterprise_history'
 AND maintenance_fetch.status IN ('pending', 'processing')
 )`

func microsoftAllocationTable(kind domain.AllocationType) string {
	if kind == domain.AllocationTypeMicrosoftEnterprise {
		return "microsoft_enterprise_allocations"
	}
	return "microsoft_allocations"
}

func microsoftEnterpriseCandidateQuery(db *gorm.DB, projectID, buyerID uint, scope domain.SupplyScope) *gorm.DB {
	query := db.Table("microsoft_enterprise_resources ms").
		Where("ms.status = 'normal'").
		Where(microsoftEnterpriseNotUnderMaintenanceCondition).
		Where(microsoftEnterpriseProjectUnmatchedCondition, projectID).
		Where(microsoftEnterpriseUnusedCondition, projectID)
	if scope == domain.SupplyScopeOwned {
		return query.Where(`ms.for_sale = FALSE AND EXISTS (
 SELECT 1 FROM email_resources er WHERE er.id = ms.id AND er.type = 'microsoft_enterprise' AND er.owner_user_id = ?
)`, buyerID)
	}
	return query.Where(`ms.for_sale = TRUE AND EXISTS (
 SELECT 1 FROM email_resources er JOIN users owner ON owner.id = er.owner_user_id
 WHERE er.id = ms.id AND er.type = 'microsoft_enterprise' AND owner.status = 'active'
 AND owner.role IN ('supplier','admin','super_admin')
)`)
}

func (r *Repo) listMicrosoftEnterpriseCandidates(ctx context.Context, projectID, buyerID uint, scope domain.SupplyScope, buckets []uint16, limit int, suffix string) ([]allocapp.MicrosoftCandidate, error) {
	query := microsoftEnterpriseCandidateQuery(r.dbFor(ctx), projectID, buyerID, scope).
		Select("ms.id AS resource_id, ms.email_address, ms.quality_score").
		Where("ms.alloc_bucket IN ?", buckets).
		Order("ms.last_allocated_at ASC, ms.quality_score DESC, ms.id ASC").Limit(limit)
	if suffix != "" {
		query = query.Where("ms.email_domain = ?", suffix)
	}
	var rows []allocapp.MicrosoftCandidate
	if err := query.Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list enterprise allocation candidates: %w", err)
	}
	return rows, nil
}

func (r *Repo) lockMicrosoftEnterpriseCandidate(ctx context.Context, resourceID, projectID, buyerID uint, scope domain.SupplyScope, suffix string) (*allocapp.MicrosoftCandidate, error) {
	query := microsoftEnterpriseCandidateQuery(r.dbFor(ctx), projectID, buyerID, scope).
		Select("ms.id AS resource_id, ms.email_address, ms.quality_score").Where("ms.id = ?", resourceID)
	if suffix != "" {
		query = query.Where("ms.email_domain = ?", suffix)
	}
	if r.dbFor(ctx).Name() == "mysql" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
	}
	var row allocapp.MicrosoftCandidate
	if err := query.Limit(1).Scan(&row).Error; err != nil {
		return nil, fmt.Errorf("lock enterprise allocation candidate: %w", err)
	}
	if row.ResourceID == 0 {
		return nil, nil
	}
	return &row, nil
}

func (r *Repo) microsoftEnterpriseSuffixInventory(ctx context.Context, projectID uint, scope string, args []any) (map[string]int64, error) {
	var rows []struct {
		Suffix string
		Count  int64
	}
	err := r.dbFor(ctx).Table("microsoft_enterprise_resources ms").
		Joins("JOIN email_resources er ON er.id=ms.id AND er.type='microsoft_enterprise'").
		Joins("JOIN users u ON u.id=er.owner_user_id").
		Select("ms.email_domain AS suffix, COUNT(*) AS count").
		Where("ms.status='normal'").Where(microsoftEnterpriseNotUnderMaintenanceCondition).
		Where(scope, args...).Where(microsoftEnterpriseUnusedCondition, projectID).
		Group("ms.email_domain").Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("enterprise suffix inventory: %w", err)
	}
	result := make(map[string]int64, len(rows))
	for _, row := range rows {
		result[row.Suffix] = row.Count
	}
	return result, nil
}
