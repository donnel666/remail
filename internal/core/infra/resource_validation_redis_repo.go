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
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	validationAssignmentSettleAfter = time.Second
	validationDispatchCursorKey     = "core:resource_validation:dispatch_cursor"
	validationDispatchCycle         = platform.BackgroundMicrosoftValidationWeight + platform.BackgroundDomainValidationWeight
)

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
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
	if err == nil && resourceType == domain.ResourceTypeMicrosoft {
		invalidateMicrosoftFacets()
	}
	return err
}

func (r *ResourceValidationRepo) RecordMicrosoftFetchFailure(
	ctx context.Context,
	resourceID uint,
	expectedCredentialRevision uint64,
	refreshToken string,
	safeError string,
	requestID string,
	systemLog *governancedomain.SystemLog,
) (abnormal bool, err error) {
	if r == nil || r.db == nil || resourceID == 0 || expectedCredentialRevision == 0 {
		return false, domain.ErrInvalidResourceCommand
	}
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var resource MicrosoftResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&resource, resourceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrResourceNotFound
			}
			return fmt.Errorf("lock microsoft resource after fetch failure: %w", err)
		}
		status := domain.MicrosoftResourceStatus(resource.Status)
		if status == domain.MicrosoftStatusAbnormal {
			abnormal = true
			return nil
		}
		if (status != domain.MicrosoftStatusNormal && status != domain.MicrosoftStatusIdentifying) || resource.CredentialRevision != expectedCredentialRevision {
			return nil
		}
		now := time.Now().UTC()
		updates := map[string]any{
			"status":              string(domain.MicrosoftStatusAbnormal),
			"graph_available":     false,
			"quality_score":       0,
			"validation_failures": coreapp.ResourceValidationMaxFailuresValue(),
			"last_safe_error":     safeValidationMessage(safeError),
			"updated_at":          now,
		}
		if refreshToken = strings.TrimSpace(refreshToken); refreshToken != "" && refreshToken != strings.TrimSpace(resource.RefreshToken) {
			updates["refresh_token"] = refreshToken
			updates["credential_revision"] = resource.CredentialRevision + 1
			updates["credential_updated_at"] = now
			updates["token_last_refreshed_at"] = now
			updates["token_last_request_id"] = strings.TrimSpace(requestID)
		}
		result := tx.Model(&MicrosoftResourceModel{}).
			Where("id = ? AND status IN ? AND credential_revision = ?", resourceID, []string{
				string(domain.MicrosoftStatusNormal), string(domain.MicrosoftStatusIdentifying),
			}, expectedCredentialRevision).
			Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("mark microsoft resource abnormal after fetch failure: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return nil
		}
		abnormal = true
		return createSystemLogInTx(ctx, tx, systemLog)
	})
	if err == nil && abnormal {
		invalidateMicrosoftFacets()
	}
	return abnormal, err
}

func (r *ResourceValidationRepo) MarkValidationBatchPending(ctx context.Context, task coreapp.ResourceValidationBatchTask, limit int) (*coreapp.ResourceValidationBatchPageResult, error) {
	if r == nil || r.db == nil || task.OwnerUserID == 0 || limit <= 0 {
		return nil, domain.ErrInvalidResourceCommand
	}
	result := &coreapp.ResourceValidationBatchPageResult{AfterID: task.AfterID, ThroughID: task.ThroughID}
	microsoftChanged := false
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
		for _, candidate := range candidates {
			if domain.ResourceType(candidate.ResourceType) == domain.ResourceTypeMicrosoft {
				microsoftChanged = true
				break
			}
		}
		return markValidationCandidatesPendingTx(ctx, tx, candidates)
	})
	if err != nil {
		return nil, err
	}
	if microsoftChanged {
		invalidateMicrosoftFacets()
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
	assigned, err := r.countAssignedValidations(ctx)
	return assigned.total(), err
}

type validationAssignmentCounts struct {
	Microsoft int `gorm:"column:microsoft_assigned"`
	Domain    int `gorm:"column:domain_assigned"`
}

func (c validationAssignmentCounts) total() int {
	return c.Microsoft + c.Domain
}

func (r *ResourceValidationRepo) countAssignedValidations(ctx context.Context) (validationAssignmentCounts, error) {
	if r == nil || r.db == nil {
		return validationAssignmentCounts{}, coreapp.ErrValidationTemporaryUnavailable
	}
	var assigned validationAssignmentCounts
	if err := r.db.WithContext(ctx).Raw(`
SELECT
	    (SELECT COUNT(*) FROM microsoft_resources WHERE status = ?) AS microsoft_assigned,
	    (SELECT COUNT(*) FROM domain_resources WHERE status = ?) AS domain_assigned`,
		string(domain.MicrosoftStatusValidating),
		string(domain.DomainStatusValidating),
	).Scan(&assigned).Error; err != nil {
		return validationAssignmentCounts{}, fmt.Errorf("count assigned resource validations: %w", err)
	}
	return assigned, nil
}

func (r *ResourceValidationRepo) ClaimPendingValidations(ctx context.Context, windowLimit int) ([]coreapp.ResourceValidationTask, error) {
	if r == nil || r.db == nil || windowLimit <= 0 {
		return nil, nil
	}
	assigned, err := r.countAssignedValidations(ctx)
	if err != nil {
		return nil, err
	}
	available := max(0, windowLimit-assigned.total())
	if available == 0 && assigned.Microsoft > 0 && assigned.Domain > 0 {
		return nil, nil
	}
	scanLimit := max(available, 1)
	now := time.Now().UTC()
	readyBefore := now.Add(-validationAssignmentSettleAfter)
	microsoftTasks, err := r.listPendingMicrosoftValidations(ctx, scanLimit, readyBefore)
	if err != nil {
		return nil, err
	}
	domainTasks, err := r.listPendingDomainValidations(ctx, scanLimit, readyBefore)
	if err != nil {
		return nil, err
	}
	needMicrosoft := assigned.Microsoft == 0 && len(microsoftTasks) > 0
	needDomain := assigned.Domain == 0 && len(domainTasks) > 0
	reservations := 0
	if needMicrosoft {
		reservations++
	}
	if needDomain {
		reservations++
	}
	slots := min(max(available, reservations), len(microsoftTasks)+len(domainTasks))
	if slots == 0 {
		return nil, nil
	}
	start, err := r.advanceValidationDispatchCursor(ctx, slots)
	if err != nil {
		return nil, err
	}
	tasks := interleaveValidationTasks(microsoftTasks, domainTasks, slots, start)
	return ensureValidationTypesPresent(tasks, microsoftTasks, domainTasks, needMicrosoft, needDomain), nil
}

func (r *ResourceValidationRepo) advanceValidationDispatchCursor(ctx context.Context, slots int) (int, error) {
	if slots <= 0 {
		return 0, nil
	}
	if r.redis != nil {
		end, err := r.redis.IncrBy(ctx, validationDispatchCursorKey, int64(slots)).Result()
		if err != nil {
			return 0, fmt.Errorf("advance resource validation dispatch cursor: %w", err)
		}
		start := (end - int64(slots)) % int64(validationDispatchCycle)
		if start < 0 {
			start += int64(validationDispatchCycle)
		}
		return int(start), nil
	}
	start := r.validationDispatchCursor.Add(uint32(slots)) - uint32(slots)
	return int(start % uint32(validationDispatchCycle)), nil
}

func ensureValidationTypesPresent(tasks, microsoftTasks, domainTasks []coreapp.ResourceValidationTask, needMicrosoft, needDomain bool) []coreapp.ResourceValidationTask {
	ensure := func(resourceType domain.ResourceType, candidates []coreapp.ResourceValidationTask, required bool) {
		if !required || len(candidates) == 0 {
			return
		}
		for _, task := range tasks {
			if task.ResourceType == resourceType {
				return
			}
		}
		for index := len(tasks) - 1; index >= 0; index-- {
			if tasks[index].ResourceType != resourceType {
				tasks[index] = candidates[0]
				return
			}
		}
	}
	ensure(domain.ResourceTypeMicrosoft, microsoftTasks, needMicrosoft)
	ensure(domain.ResourceTypeDomain, domainTasks, needDomain)
	return tasks
}

type pendingValidationRow struct {
	ID                   uint
	OwnerUserID          uint   `gorm:"column:owner_user_id"`
	CredentialRevision   uint64 `gorm:"column:credential_revision"`
	ValidationGeneration uint64 `gorm:"column:validation_generation"`
}

func (r *ResourceValidationRepo) listPendingMicrosoftValidations(ctx context.Context, limit int, readyBefore time.Time) ([]coreapp.ResourceValidationTask, error) {
	var rows []pendingValidationRow
	if err := r.db.WithContext(ctx).
		Table("microsoft_resources AS ms").
		Select("ms.id, er.owner_user_id, ms.credential_revision, ms.validation_generation").
		Joins("JOIN email_resources AS er ON er.id = ms.id AND er.type = ?", string(domain.ResourceTypeMicrosoft)).
		Where("ms.status = ? AND ms.updated_at <= ?", string(domain.MicrosoftStatusPending), readyBefore).
		Order("ms.id ASC").Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list pending Microsoft validations: %w", err)
	}
	tasks := make([]coreapp.ResourceValidationTask, len(rows))
	for i := range rows {
		tasks[i] = coreapp.ResourceValidationTask{
			ResourceID: rows[i].ID, ResourceType: domain.ResourceTypeMicrosoft,
			OwnerUserID: rows[i].OwnerUserID, ValidationGeneration: rows[i].ValidationGeneration,
			ExpectedCredentialRevision: rows[i].CredentialRevision,
		}
	}
	return tasks, nil
}

func (r *ResourceValidationRepo) listPendingDomainValidations(ctx context.Context, limit int, readyBefore time.Time) ([]coreapp.ResourceValidationTask, error) {
	var rows []pendingValidationRow
	if err := r.db.WithContext(ctx).
		Table("domain_resources AS dr").
		Select("dr.id, er.owner_user_id, dr.validation_generation").
		Joins("JOIN email_resources AS er ON er.id = dr.id AND er.type = ?", string(domain.ResourceTypeDomain)).
		Where("dr.status = ? AND dr.updated_at <= ?", string(domain.DomainStatusPending), readyBefore).
		Order("dr.id ASC").Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list pending domain validations: %w", err)
	}
	tasks := make([]coreapp.ResourceValidationTask, len(rows))
	for i := range rows {
		tasks[i] = coreapp.ResourceValidationTask{
			ResourceID: rows[i].ID, ResourceType: domain.ResourceTypeDomain,
			OwnerUserID: rows[i].OwnerUserID, ValidationGeneration: rows[i].ValidationGeneration,
		}
	}
	return tasks, nil
}

// interleaveValidationTasks is a work-conserving weighted round robin.
// Either type borrows the whole batch when it is the only non-empty backlog.
func interleaveValidationTasks(microsoftTasks, domainTasks []coreapp.ResourceValidationTask, limit, start int) []coreapp.ResourceValidationTask {
	result := make([]coreapp.ResourceValidationTask, 0, min(limit, len(microsoftTasks)+len(domainTasks)))
	microsoftIndex, domainIndex := 0, 0
	for slot := 0; len(result) < limit && (microsoftIndex < len(microsoftTasks) || domainIndex < len(domainTasks)); slot++ {
		wantDomain := (start+slot)%validationDispatchCycle >= platform.BackgroundMicrosoftValidationWeight
		if (wantDomain && domainIndex < len(domainTasks)) || microsoftIndex >= len(microsoftTasks) {
			result = append(result, domainTasks[domainIndex])
			domainIndex++
			continue
		}
		result = append(result, microsoftTasks[microsoftIndex])
		microsoftIndex++
	}
	return result
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
	changed := result.RowsAffected == 1
	if changed && task.ResourceType == domain.ResourceTypeMicrosoft {
		invalidateMicrosoftFacets()
	}
	return changed, nil
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
	if result.RowsAffected == 1 && task.ResourceType == domain.ResourceTypeMicrosoft {
		invalidateMicrosoftFacets()
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
	invalidateMicrosoftFacets()
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
