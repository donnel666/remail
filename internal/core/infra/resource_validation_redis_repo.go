package infra

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const validationAssignmentSettleAfter = time.Second

func (r *ResourceValidationRepo) MarkResourcePendingWithLog(
	ctx context.Context,
	resourceID uint,
	resourceType domain.ResourceType,
	ownerUserID uint,
	log *governancedomain.OperationLog,
) error {
	if r == nil || r.db == nil || resourceID == 0 || ownerUserID == 0 || !domain.IsValidResourceType(resourceType) {
		return domain.ErrInvalidResourceCommand
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root EmailResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND type = ? AND owner_user_id = ?", resourceID, string(resourceType), ownerUserID).
			First(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrResourceNotFound
			}
			return fmt.Errorf("lock resource for validation request: %w", err)
		}
		now := time.Now().UTC()
		switch resourceType {
		case domain.ResourceTypeMicrosoft:
			var resource MicrosoftResourceModel
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&resource, resourceID).Error; err != nil {
				return fmt.Errorf("lock microsoft validation request: %w", err)
			}
			switch domain.MicrosoftResourceStatus(resource.Status) {
			case domain.MicrosoftStatusDeleted:
				return domain.ErrResourceNotFound
			case domain.MicrosoftStatusDisabled:
				return domain.ErrInvalidResourceStatus
			}
			if err := tx.Model(&MicrosoftResourceModel{}).Where("id = ?", resourceID).Updates(map[string]any{
				"status": string(domain.MicrosoftStatusPending), "validation_generation": gorm.Expr("validation_generation + 1"),
				"validation_failures": 0, "last_safe_error": "", "updated_at": now,
			}).Error; err != nil {
				return fmt.Errorf("mark microsoft validation pending: %w", err)
			}
		case domain.ResourceTypeDomain:
			var resource DomainResourceModel
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&resource, resourceID).Error; err != nil {
				return fmt.Errorf("lock domain validation request: %w", err)
			}
			switch domain.MailDomainStatus(resource.Status) {
			case domain.DomainStatusDeleted:
				return domain.ErrResourceNotFound
			case domain.DomainStatusDisabled:
				return domain.ErrInvalidResourceStatus
			}
			if err := tx.Model(&DomainResourceModel{}).Where("id = ?", resourceID).Updates(map[string]any{
				"status": string(domain.DomainStatusPending), "validation_generation": gorm.Expr("validation_generation + 1"),
				"validation_failures": 0, "last_safe_error": "", "updated_at": now,
			}).Error; err != nil {
				return fmt.Errorf("mark domain validation pending: %w", err)
			}
		}
		if log != nil {
			if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
				return fmt.Errorf("create resource validation operation log: %w", err)
			}
		}
		return nil
	})
}

func (r *ResourceValidationRepo) MarkValidationBatchPending(ctx context.Context, task coreapp.ResourceValidationBatchTask, limit int) (*coreapp.ResourceValidationBatchPageResult, error) {
	if r == nil || r.db == nil || task.OwnerUserID == 0 || limit <= 0 {
		return nil, domain.ErrInvalidResourceCommand
	}
	result := &coreapp.ResourceValidationBatchPageResult{AfterID: task.AfterID, ThroughID: task.ThroughID}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		selection := task.Selection
		if !selection.AllowBinding {
			selection.Filter.ExcludeBinding = true
		}
		if selection.Mode == coreapp.ResourceBulkSelectionFilter && result.ThroughID == 0 {
			throughID, err := captureValidationBatchThroughID(ctx, tx, task.OwnerUserID, selection)
			if err != nil {
				return err
			}
			result.ThroughID = throughID
			if throughID == 0 {
				result.Done = true
				return nil
			}
		}

		var candidates []validationCandidateRow
		var err error
		switch selection.Mode {
		case coreapp.ResourceBulkSelectionIDs:
			pageIDs := validationBatchIDPage(selection.ResourceIDs, task.AfterID, limit+1)
			result.Done = len(pageIDs) <= limit
			if len(pageIDs) > limit {
				pageIDs = pageIDs[:limit]
			}
			result.Processed = len(pageIDs)
			if len(pageIDs) == 0 {
				result.Done = true
				return nil
			}
			result.AfterID = pageIDs[len(pageIDs)-1]
			candidates, err = selectAvailableValidationCandidatesByIDs(ctx, tx, task.OwnerUserID, pageIDs, selection.Filter.ResourceType, selection.AllowBinding, selection.AdminScope)
		case coreapp.ResourceBulkSelectionFilter:
			candidates, err = selectValidationCandidatesByFilter(ctx, tx, task.OwnerUserID, selection, task.AfterID, result.ThroughID, limit+1)
			result.Done = len(candidates) <= limit
			if len(candidates) > limit {
				candidates = candidates[:limit]
			}
			result.Processed = len(candidates)
			if len(candidates) == 0 {
				result.Done = true
				return nil
			}
			result.AfterID = candidates[len(candidates)-1].ID
		default:
			return domain.ErrInvalidResourceType
		}
		if err != nil {
			return err
		}
		if result.AfterID == task.AfterID {
			return fmt.Errorf("resource validation Redis batch made no progress")
		}
		return markValidationCandidatesPendingTx(ctx, tx, candidates)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func captureValidationBatchThroughID(ctx context.Context, tx *gorm.DB, ownerUserID uint, selection coreapp.ResourceBulkSelection) (uint, error) {
	query := tx.WithContext(ctx).Table("email_resources AS er").
		Select("COALESCE(MAX(er.id), 0)").
		Where("er.type = ?", string(selection.Filter.ResourceType))
	if !selection.AdminScope {
		query = query.Where("er.owner_user_id = ?", ownerUserID)
	} else if selection.Filter.OwnerID > 0 {
		query = query.Where("er.owner_user_id = ?", selection.Filter.OwnerID)
	}
	var throughID uint
	if err := query.Row().Scan(&throughID); err != nil {
		return 0, fmt.Errorf("capture Redis validation batch high-water mark: %w", err)
	}
	return throughID, nil
}

func (r *ResourceValidationRepo) CountAssignedValidations(ctx context.Context) (int, error) {
	if r == nil || r.db == nil {
		return 0, coreapp.ErrValidationTemporaryUnavailable
	}
	var assigned int
	if err := r.db.WithContext(ctx).Raw(`
SELECT
    (SELECT COUNT(*) FROM microsoft_resources WHERE status = ?) +
    (SELECT COUNT(*) FROM domain_resources WHERE status = ?) AS assigned`,
		string(domain.MicrosoftStatusValidating),
		string(domain.DomainStatusValidating),
	).Scan(&assigned).Error; err != nil {
		return 0, fmt.Errorf("count assigned resource validations: %w", err)
	}
	return assigned, nil
}

func (r *ResourceValidationRepo) ClaimPendingValidations(ctx context.Context, limit int) ([]coreapp.ResourceValidationTask, error) {
	if r == nil || r.db == nil || limit <= 0 {
		return nil, nil
	}
	var candidates []validationCandidateRow
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).
		Table("email_resources AS er").
		Select("er.id, er.type AS resource_type, er.owner_user_id, COALESCE(ms.credential_revision, 0) AS credential_revision, COALESCE(ms.validation_generation, dr.validation_generation, 0) AS validation_generation, COALESCE(ms.status, '') AS microsoft_status, COALESCE(dr.status, '') AS domain_status").
		Joins("LEFT JOIN microsoft_resources AS ms ON ms.id = er.id AND er.type = ?", string(domain.ResourceTypeMicrosoft)).
		Joins("LEFT JOIN domain_resources AS dr ON dr.id = er.id AND er.type = ?", string(domain.ResourceTypeDomain)).
		Where("(er.type = ? AND ms.status = ?) OR (er.type = ? AND dr.status = ?)",
			string(domain.ResourceTypeMicrosoft), string(domain.MicrosoftStatusPending),
			string(domain.ResourceTypeDomain), string(domain.DomainStatusPending)).
		Where("(er.type = ? AND ms.updated_at <= ?) OR (er.type = ? AND dr.updated_at <= ?)",
			string(domain.ResourceTypeMicrosoft), now.Add(-validationAssignmentSettleAfter),
			string(domain.ResourceTypeDomain), now.Add(-validationAssignmentSettleAfter)).
		Order("er.id ASC").Limit(limit).
		Find(&candidates).Error
	if err != nil {
		return nil, fmt.Errorf("list pending resource validations: %w", err)
	}
	tasks := make([]coreapp.ResourceValidationTask, len(candidates))
	for i := range candidates {
		tasks[i] = coreapp.ResourceValidationTask{
			ResourceID: candidates[i].ID, ResourceType: domain.ResourceType(candidates[i].ResourceType),
			OwnerUserID: candidates[i].OwnerUserID, ValidationGeneration: candidates[i].ValidationGeneration,
			ExpectedCredentialRevision: candidates[i].CredentialRevision,
		}
	}
	return tasks, nil
}

func (r *ResourceValidationRepo) MarkValidationDispatched(ctx context.Context, task coreapp.ResourceValidationTask) (bool, error) {
	if r == nil || r.db == nil || task.ResourceID == 0 {
		return false, nil
	}
	now := time.Now().UTC()
	var result *gorm.DB
	switch task.ResourceType {
	case domain.ResourceTypeMicrosoft:
		result = r.db.WithContext(ctx).Model(&MicrosoftResourceModel{}).
			Where("id = ? AND status = ? AND validation_generation = ? AND credential_revision = ?", task.ResourceID, string(domain.MicrosoftStatusPending), task.ValidationGeneration, task.ExpectedCredentialRevision).
			Updates(map[string]any{"status": string(domain.MicrosoftStatusValidating), "updated_at": now})
	case domain.ResourceTypeDomain:
		result = r.db.WithContext(ctx).Model(&DomainResourceModel{}).
			Where("id = ? AND status = ? AND validation_generation = ?", task.ResourceID, string(domain.DomainStatusPending), task.ValidationGeneration).
			Updates(map[string]any{"status": string(domain.DomainStatusValidating), "updated_at": now})
	default:
		return false, domain.ErrInvalidResourceType
	}
	if result.Error != nil {
		return false, fmt.Errorf("activate Redis validation assignment: %w", result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (r *ResourceValidationRepo) ReleaseValidation(ctx context.Context, task coreapp.ResourceValidationTask) error {
	if r == nil || r.db == nil || task.ResourceID == 0 {
		return nil
	}
	now := time.Now().UTC()
	var result *gorm.DB
	switch task.ResourceType {
	case domain.ResourceTypeMicrosoft:
		result = r.db.WithContext(ctx).Model(&MicrosoftResourceModel{}).
			Where("id = ? AND status = ? AND validation_generation = ? AND credential_revision = ?", task.ResourceID, string(domain.MicrosoftStatusValidating), task.ValidationGeneration, task.ExpectedCredentialRevision).
			Updates(map[string]any{"status": string(domain.MicrosoftStatusPending), "validation_generation": gorm.Expr("validation_generation + 1"), "updated_at": now})
	case domain.ResourceTypeDomain:
		result = r.db.WithContext(ctx).Model(&DomainResourceModel{}).
			Where("id = ? AND status = ? AND validation_generation = ?", task.ResourceID, string(domain.DomainStatusValidating), task.ValidationGeneration).
			Updates(map[string]any{"status": string(domain.DomainStatusPending), "validation_generation": gorm.Expr("validation_generation + 1"), "updated_at": now})
	default:
		return domain.ErrInvalidResourceType
	}
	if result.Error != nil {
		return fmt.Errorf("release Redis validation assignment: %w", result.Error)
	}
	return nil
}

func (r *ResourceValidationRepo) ApplyMicrosoftResult(ctx context.Context, task coreapp.ResourceValidationTask, result coreapp.MicrosoftValidationResult, systemLog *governancedomain.SystemLog) error {
	stale := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, ms, err := lockRedisMicrosoftValidationStateTx(tx, task)
		if err != nil {
			if errors.Is(err, coreapp.ErrValidationResultStale) {
				stale = true
				return nil
			}
			return err
		}
		now := time.Now().UTC()
		if _, err := r.commitMicrosoftValidationBindingWithSavepointTx(ctx, tx, root, ms, result); err != nil {
			if errors.Is(err, coreapp.ErrValidationResultStale) {
				stale = true
				return nil
			}
			return err
		}
		safeMessage := safeValidationMessage(result.SafeMessage)
		nextStatus := string(domain.MicrosoftStatusAbnormal)
		maxFailures := coreapp.ResourceValidationMaxFailuresValue()
		nextFailures := min(ms.ValidationFailures+1, maxFailures)
		if result.Valid {
			nextStatus = string(domain.MicrosoftStatusIdentifying)
			nextFailures = 0
			safeMessage = ""
		} else if result.Retryable && nextFailures < maxFailures {
			nextStatus = string(domain.MicrosoftStatusPending)
		}
		updates := map[string]any{
			"status": nextStatus, "quality_score": validationQualityScore(result.Valid),
			"graph_available": false, "validation_failures": nextFailures, "last_safe_error": safeMessage, "updated_at": now,
		}
		if nextStatus == string(domain.MicrosoftStatusPending) {
			updates["validation_generation"] = ms.ValidationGeneration + 1
		}
		credentialsChanged := false
		if result.Valid || result.CredentialsAuthoritative {
			if value := strings.TrimSpace(result.ClientID); value != "" && value != ms.ClientID {
				updates["client_id"] = value
				credentialsChanged = true
			}
			if value := strings.TrimSpace(result.RefreshToken); value != "" && value != ms.RefreshToken {
				updates["refresh_token"] = value
				credentialsChanged = true
			}
		}
		if result.Valid {
			updates["graph_available"] = result.GraphAvailable
			if result.RTExpireAt != nil {
				updates["rt_expire_at"] = result.RTExpireAt
			}
		}
		if credentialsChanged {
			updates["credential_revision"] = ms.CredentialRevision + 1
			updates["credential_updated_at"] = now
			updates["token_last_refreshed_at"] = now
			updates["token_last_request_id"] = task.RequestID
		}
		updated := tx.Model(&MicrosoftResourceModel{}).
			Where("id = ? AND status = ? AND validation_generation = ? AND credential_revision = ?", task.ResourceID, string(domain.MicrosoftStatusValidating), task.ValidationGeneration, task.ExpectedCredentialRevision).
			Updates(updates)
		if updated.Error != nil {
			return fmt.Errorf("apply Redis microsoft validation result: %w", updated.Error)
		}
		if updated.RowsAffected == 0 {
			stale = true
			return nil
		}
		if err := bumpResourceVersionTx(tx, root.ID, now); err != nil {
			return err
		}
		return createSystemLogInTx(ctx, tx, systemLog)
	})
	if err != nil {
		return err
	}
	if stale {
		return coreapp.ErrValidationResultStale
	}
	return nil
}

func (r *ResourceValidationRepo) ApplyDomainResult(ctx context.Context, task coreapp.ResourceValidationTask, result coreapp.DomainValidationResult, systemLog *governancedomain.SystemLog) error {
	stale := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root EmailResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND type = ?", task.ResourceID, string(domain.ResourceTypeDomain)).
			First(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				stale = true
				return nil
			}
			return fmt.Errorf("lock Redis domain validation root: %w", err)
		}
		var resource DomainResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&resource, task.ResourceID).Error; err != nil {
			return fmt.Errorf("lock Redis domain validation resource: %w", err)
		}
		if domain.MailDomainStatus(resource.Status) != domain.DomainStatusValidating || resource.ValidationGeneration != task.ValidationGeneration {
			stale = true
			return nil
		}
		safeMessage := safeValidationMessage(result.SafeMessage)
		nextStatus := string(domain.DomainStatusAbnormal)
		maxFailures := coreapp.ResourceValidationMaxFailuresValue()
		nextFailures := min(resource.ValidationFailures+1, maxFailures)
		if result.Valid {
			nextStatus = string(domain.DomainStatusNormal)
			nextFailures = 0
			safeMessage = ""
		} else if result.Retryable && nextFailures < maxFailures {
			nextStatus = string(domain.DomainStatusPending)
		}
		now := time.Now().UTC()
		updates := map[string]any{"status": nextStatus, "validation_failures": nextFailures, "last_safe_error": safeMessage, "updated_at": now}
		if nextStatus == string(domain.DomainStatusPending) {
			updates["validation_generation"] = resource.ValidationGeneration + 1
		}
		updated := tx.Model(&DomainResourceModel{}).
			Where("id = ? AND status = ? AND validation_generation = ?", task.ResourceID, string(domain.DomainStatusValidating), task.ValidationGeneration).
			Updates(updates)
		if updated.Error != nil {
			return fmt.Errorf("apply Redis domain validation result: %w", updated.Error)
		}
		if updated.RowsAffected == 0 {
			stale = true
			return nil
		}
		if err := bumpResourceVersionTx(tx, root.ID, now); err != nil {
			return err
		}
		return createSystemLogInTx(ctx, tx, systemLog)
	})
	if err != nil {
		return err
	}
	if stale {
		return coreapp.ErrValidationResultStale
	}
	return nil
}

func lockRedisMicrosoftValidationStateTx(tx *gorm.DB, task coreapp.ResourceValidationTask) (*EmailResourceModel, *MicrosoftResourceModel, error) {
	var root EmailResourceModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND type = ? AND owner_user_id = ?", task.ResourceID, string(domain.ResourceTypeMicrosoft), task.OwnerUserID).
		First(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, coreapp.ErrValidationResultStale
		}
		return nil, nil, fmt.Errorf("lock Redis microsoft validation root: %w", err)
	}
	var resource MicrosoftResourceModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&resource, task.ResourceID).Error; err != nil {
		return nil, nil, fmt.Errorf("lock Redis microsoft validation resource: %w", err)
	}
	if domain.MicrosoftResourceStatus(resource.Status) != domain.MicrosoftStatusValidating || resource.ValidationGeneration != task.ValidationGeneration || resource.CredentialRevision != task.ExpectedCredentialRevision {
		return nil, nil, coreapp.ErrValidationResultStale
	}
	return &root, &resource, nil
}

var _ coreapp.ResourceValidationRepository = (*ResourceValidationRepo)(nil)
