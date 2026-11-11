package infra

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/platform"
)

func (r *ResourceValidationRepo) recoverStaleValidations(ctx context.Context, now time.Time, enterprise bool) error {
	if r.redis == nil {
		return nil
	}
	r.validationRecoveryMu.Lock()
	defer r.validationRecoveryMu.Unlock()
	before := now.Add(-validationTaskTimeout - time.Minute)
	for index, resourceType := range []domain.ResourceType{domain.ResourceTypeMicrosoft, domain.ResourceTypeDomain, domain.ResourceTypeMicrosoftEnterprise} {
		if (resourceType == domain.ResourceTypeMicrosoftEnterprise) != enterprise {
			continue
		}
		state := &r.validationRecovery[index]
		if now.Sub(state.lastAttempt) < 30*time.Second {
			continue
		}
		state.lastAttempt = now
		table, queue, indexName := microsoftResourceTable(resourceType), MicrosoftResourceValidationQueueName, "idx_microsoft_status"
		if resourceType == domain.ResourceTypeDomain {
			table, queue, indexName = "domain_resources", DomainResourceValidationQueueName, "idx_domain_resources_status"
		} else if enterprise {
			queue, indexName = platform.QueueBackgroundMicrosoftEnterpriseValidation, "idx_ms_enterprise_status"
		}
		selectSQL := "/*+ MAX_EXECUTION_TIME(1500) */ resource.id, resource.updated_at, root.owner_user_id, resource.validation_generation"
		if resourceType != domain.ResourceTypeDomain {
			selectSQL += ", resource.credential_revision"
		}
		source, join := table+" AS resource", "JOIN "
		if r.db.Name() == "mysql" {
			source += " FORCE INDEX (" + indexName + ")"
			join = "STRAIGHT_JOIN "
		}
		query := r.db.WithContext(ctx).Table(source).Select(selectSQL).
			Joins(join+"email_resources AS root ON root.id=resource.id AND root.type=?", resourceType).
			Where("resource.status='validating'")
		if enterprise {
			query = query.Where("resource.updated_at < ?", before).Order("resource.updated_at ASC, resource.id ASC")
			if state.afterID > 0 {
				query = query.Where("resource.updated_at > ? OR (resource.updated_at = ? AND resource.id > ?)", state.afterTime, state.afterTime, state.afterID)
			}
		} else {
			// Page the status index before filtering age; even a large fresh backlog has a bounded scan.
			query = query.Where("resource.id > ?", state.afterID).Order("resource.id ASC")
		}
		var rows []pendingValidationRow
		if err := query.Limit(100).Find(&rows).Error; err != nil {
			return fmt.Errorf("list stale validation assignments: %w", err)
		}
		if len(rows) == 0 {
			state.afterID = 0
			continue
		}
		stale := false
		for _, row := range rows {
			stale = stale || row.UpdatedAt.Before(before)
		}
		candidates := make(map[uint]struct{})
		for _, row := range rows {
			if row.UpdatedAt.Before(before) {
				candidates[row.ID] = struct{}{}
			}
		}
		var live map[uint]struct{}
		var err error
		if stale && len(candidates) > 0 {
			live, err = platform.LiveTaskResourceIDs(ctx, r.redis, queue, TypeResourceValidation, candidates)
		}
		if err != nil {
			return err
		}
		recovered := 0
		for _, row := range rows {
			if _, ok := live[row.ID]; ok || !row.UpdatedAt.Before(before) {
				continue
			}
			changed, err := r.releaseValidation(ctx, coreapp.ResourceValidationTask{
				ResourceID: row.ID, ResourceType: resourceType, Enterprise: enterprise,
				OwnerUserID: row.OwnerUserID, ValidationGeneration: row.ValidationGeneration,
				ExpectedCredentialRevision: row.CredentialRevision,
			}, before)
			if err != nil {
				return err
			}
			if changed {
				recovered++
			}
		}
		state.afterID = rows[len(rows)-1].ID
		state.afterTime = rows[len(rows)-1].UpdatedAt
		if len(rows) < 100 {
			state.afterID = 0
		}
		if recovered > 0 {
			slog.InfoContext(ctx, "recovered orphaned validation assignments", "resource_type", resourceType, "recovered", recovered)
		}
	}
	return nil
}
