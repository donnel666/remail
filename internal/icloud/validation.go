package icloud

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	iCloudValidationMaxFailures   = 3
	iCloudValidationRetryInterval = 30 * time.Second
	iCloudValidationBatchLimit    = 128
	iCloudValidationRunningLease  = 5 * time.Minute
)

var (
	errICloudValidationStale = errors.New("icloud: validation result is stale")
	errICloudAliasConflict   = errors.New("icloud: alias belongs to another resource")
)

// RequestAdminICloudValidation only queues an IMAP health check. Cookie state
// and provisioning are deliberately independent dimensions.
func (s *Service) RequestAdminICloudValidation(ctx context.Context, operatorUserID, resourceID uint, requestID, path string) error {
	if s == nil || s.db == nil || s.operationLogs == nil || operatorUserID == 0 || resourceID == 0 {
		return ErrICloudValidationTemp
	}
	now := s.now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root iCloudRootModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND type = ?", resourceID, "icloud").Take(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrICloudResourceNotFound
			}
			return err
		}
		var resource iCloudResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&resource, resourceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrICloudResourceNotFound
			}
			return err
		}
		if resource.Status == iCloudResourceDeleted {
			return ErrICloudResourceNotFound
		}
		if resource.Status == iCloudResourceDisabled {
			return ErrICloudResourceStatus
		}
		generation := resource.ValidationGeneration + 1
		if generation == 0 {
			generation = 1
		}
		updates := map[string]any{
			"status": iCloudResourcePending, "validation_generation": generation,
			"validation_failures": 0, "next_validation_at": now,
			"next_provision_at": nil,
			"last_safe_error":   "", "updated_at": now,
		}
		if err := tx.Model(&iCloudResourceModel{}).Where("id = ? AND validation_generation = ?", resourceID, resource.ValidationGeneration).Updates(updates).Error; err != nil {
			return err
		}
		if _, err := ensureICloudValidationRunTx(ctx, tx, resourceID, generation, resource.CredentialRevision, now); err != nil {
			return err
		}
		if err := tx.Model(&iCloudRootModel{}).Where("id = ? AND version = ?", root.ID, root.Version).Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
			return err
		}
		return s.operationLogs.CreateInTx(ctx, tx, &governancedomain.OperationLog{
			OperatorUserID: operatorUserID, OperationType: "icloud.admin_resource.validate",
			ResourceType: "icloud_resource", ResourceID: strconv.FormatUint(uint64(resourceID), 10),
			Path: strings.TrimSpace(path), Result: "success",
			SafeSummary: "iCloud IMAP validation queued.", RequestID: strings.TrimSpace(requestID),
		})
	})
	if errors.Is(err, ErrICloudResourceNotFound) || errors.Is(err, ErrICloudResourceStatus) {
		return err
	}
	if err != nil {
		return ErrICloudValidationTemp
	}
	_ = s.ScheduleICloudValidationDispatcher(context.WithoutCancel(ctx), 0)
	return nil
}

func (s *Service) iCloudValidationResource(ctx context.Context, task iCloudValidationTask) (*iCloudResourceModel, bool, error) {
	if s == nil || s.db == nil || task.ResourceID == 0 || task.OwnerUserID == 0 {
		return nil, false, ErrICloudValidationTemp
	}
	var root iCloudRootModel
	if err := s.db.WithContext(ctx).Where("id = ? AND type = ? AND owner_user_id = ?", task.ResourceID, "icloud", task.OwnerUserID).Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, ErrICloudValidationTemp
	}
	var resource iCloudResourceModel
	if err := s.db.WithContext(ctx).Take(&resource, task.ResourceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, ErrICloudValidationTemp
	}
	if resource.ValidationGeneration != task.ValidationGeneration || resource.CredentialRevision != task.ExpectedCredentialRevision || resource.Status != iCloudResourceValidating {
		return nil, false, nil
	}
	return &resource, true, nil
}

func ensureICloudValidationRunTx(ctx context.Context, tx *gorm.DB, resourceID uint, generation, credentialRevision uint64, now time.Time) (*iCloudMaintenanceRunModel, error) {
	if tx == nil || resourceID == 0 || generation == 0 || credentialRevision == 0 {
		return nil, ErrICloudValidationTemp
	}
	run := iCloudMaintenanceRunModel{
		ResourceID: resourceID, ValidationGeneration: generation, Kind: iCloudMaintenanceValidation,
		Status: iCloudMaintenanceQueued, Attempts: 0, MaxAttempts: iCloudValidationMaxFailures,
		CredentialRevision: credentialRevision, QueuedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&run).Error; err != nil {
		return nil, err
	}
	if run.ID == 0 {
		if err := tx.WithContext(ctx).Where("resource_id = ? AND validation_generation = ?", resourceID, generation).Take(&run).Error; err != nil {
			return nil, err
		}
	}
	return &run, nil
}

func (s *Service) DispatchICloudValidations(ctx context.Context, limit int) error {
	if s == nil || s.db == nil || s.queue == nil {
		return ErrICloudValidationTemp
	}
	if limit <= 0 || limit > iCloudValidationBatchLimit {
		limit = iCloudValidationBatchLimit
	}
	now := s.now().UTC()
	if err := s.recoverStaleICloudValidations(ctx, now); err != nil {
		return err
	}
	tasks, err := s.iCloudValidationCandidates(ctx, limit)
	if err != nil {
		return err
	}
	var joined error
	for _, task := range tasks {
		claimed, ok, claimErr := s.markICloudValidationDispatched(ctx, task)
		if claimErr != nil {
			joined = errors.Join(joined, claimErr)
			continue
		}
		if !ok {
			continue
		}
		if _, err := s.enqueueICloudValidation(ctx, claimed); err != nil {
			_ = s.releaseICloudValidation(ctx, claimed, "iCloud validation queue is temporarily unavailable.")
			joined = errors.Join(joined, err)
		}
	}
	return joined
}

func (s *Service) recoverStaleICloudValidations(ctx context.Context, now time.Time) error {
	var rows []struct {
		ID uint `gorm:"column:id"`
	}
	if err := s.db.WithContext(ctx).Table("icloud_resources").Select("id").Where("status = ? AND updated_at <= ?", iCloudResourceValidating, now.Add(-iCloudValidationRunningLease)).Find(&rows).Error; err != nil {
		return ErrICloudValidationTemp
	}
	for _, row := range rows {
		if err := s.db.WithContext(ctx).Model(&iCloudResourceModel{}).Where("id = ? AND status = ?", row.ID, iCloudResourceValidating).Updates(map[string]any{
			"status": iCloudResourcePending, "next_validation_at": now, "next_provision_at": nil,
			"updated_at": now, "last_safe_error": "Validation lease expired; retrying IMAP check.",
		}).Error; err != nil {
			return ErrICloudValidationTemp
		}
	}
	return nil
}

func (s *Service) iCloudValidationCandidates(ctx context.Context, limit int) ([]iCloudValidationTask, error) {
	now := s.now().UTC()
	var rows []struct {
		ID                   uint   `gorm:"column:id"`
		OwnerUserID          uint   `gorm:"column:owner_user_id"`
		CredentialRevision   uint64 `gorm:"column:credential_revision"`
		ValidationGeneration uint64 `gorm:"column:validation_generation"`
	}
	if err := s.db.WithContext(ctx).Table("icloud_resources AS ir").Select("ir.id, er.owner_user_id, ir.credential_revision, ir.validation_generation").Joins("JOIN email_resources AS er ON er.id = ir.id AND er.type = ?", "icloud").Where("ir.status IN ? AND ir.next_validation_at IS NOT NULL AND ir.next_validation_at <= ?", []string{iCloudResourcePending, iCloudResourceNormal, iCloudResourceAbnormal}, now).Order("ir.id ASC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, ErrICloudValidationTemp
	}
	tasks := make([]iCloudValidationTask, 0, len(rows))
	for _, row := range rows {
		if row.ID > 0 && row.OwnerUserID > 0 && row.CredentialRevision > 0 && row.ValidationGeneration > 0 {
			tasks = append(tasks, iCloudValidationTask{ResourceID: row.ID, OwnerUserID: row.OwnerUserID, ValidationGeneration: row.ValidationGeneration, ExpectedCredentialRevision: row.CredentialRevision})
		}
	}
	return tasks, nil
}

func (s *Service) markICloudValidationDispatched(ctx context.Context, task iCloudValidationTask) (iCloudValidationTask, bool, error) {
	if s == nil || s.db == nil || task.ResourceID == 0 || task.OwnerUserID == 0 || task.ValidationGeneration == 0 || task.ExpectedCredentialRevision == 0 {
		return task, false, ErrICloudValidationTemp
	}
	now := s.now().UTC()
	claimed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root iCloudRootModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND type = ? AND owner_user_id = ?", task.ResourceID, "icloud", task.OwnerUserID).Take(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		var resource iCloudResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&resource, task.ResourceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if resource.CredentialRevision != task.ExpectedCredentialRevision || resource.ValidationGeneration != task.ValidationGeneration || resource.Status == iCloudResourceDisabled || resource.Status == iCloudResourceDeleted || resource.NextValidationAt == nil || resource.NextValidationAt.After(now) {
			return nil
		}
		result := tx.Model(&iCloudResourceModel{}).Where("id = ? AND status IN ? AND validation_generation = ? AND credential_revision = ?", task.ResourceID, []string{iCloudResourcePending, iCloudResourceNormal, iCloudResourceAbnormal}, task.ValidationGeneration, task.ExpectedCredentialRevision).Updates(map[string]any{
			"status": iCloudResourceValidating, "next_provision_at": nil,
			"last_safe_error": "", "updated_at": now,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}
		run, err := ensureICloudValidationRunTx(ctx, tx, resource.ID, resource.ValidationGeneration, resource.CredentialRevision, now)
		if err != nil {
			return err
		}
		runResult := tx.Model(&iCloudMaintenanceRunModel{}).
			Where("id = ? AND validation_generation = ? AND credential_revision = ?", run.ID, task.ValidationGeneration, task.ExpectedCredentialRevision).
			Updates(map[string]any{
				"status": iCloudMaintenanceRunning, "attempts": gorm.Expr("CASE WHEN attempts < max_attempts THEN attempts + 1 ELSE max_attempts END"),
				"started_at": now, "finished_at": nil, "last_safe_error": "", "updated_at": now,
			})
		if runResult.Error != nil {
			return runResult.Error
		}
		if runResult.RowsAffected != 1 {
			return nil
		}
		task.MaintenanceRunID, task.MaintenanceKind = run.ID, iCloudMaintenanceValidation
		claimed = true
		return nil
	})
	if err != nil {
		return task, false, ErrICloudValidationTemp
	}
	return task, claimed, nil
}

func (s *Service) releaseICloudValidation(ctx context.Context, task iCloudValidationTask, safeError string) error {
	if s == nil || s.db == nil || task.ResourceID == 0 {
		return ErrICloudValidationTemp
	}
	now := s.now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{
			"status": iCloudResourcePending, "next_validation_at": now, "next_provision_at": nil,
			"last_safe_error": safeICloudValidationMessage(safeError), "updated_at": now,
		}
		if err := tx.Model(&iCloudResourceModel{}).Where("id = ? AND status = ? AND validation_generation = ? AND credential_revision = ?", task.ResourceID, iCloudResourceValidating, task.ValidationGeneration, task.ExpectedCredentialRevision).Updates(updates).Error; err != nil {
			return err
		}
		if task.MaintenanceRunID > 0 {
			return finishICloudMaintenanceRunTx(ctx, tx, task.MaintenanceRunID, iCloudMaintenanceFailed, safeError, now)
		}
		return nil
	})
	if err != nil {
		return ErrICloudValidationTemp
	}
	return nil
}

func iCloudValidationFenceMatches(resource iCloudResourceModel, run *iCloudMaintenanceRunModel, task iCloudValidationTask) bool {
	if resource.ID != task.ResourceID || resource.ValidationGeneration != task.ValidationGeneration || resource.CredentialRevision != task.ExpectedCredentialRevision {
		return false
	}
	return run == nil || run.ResourceID == task.ResourceID
}

func syncICloudAliasesTx(tx *gorm.DB, resourceID uint, aliases []hmeAlias, _ string, complete bool, now time.Time) error {
	if tx == nil || resourceID == 0 {
		return errICloudAliasConflict
	}
	seenIDs, seenEmails := make(map[string]struct{}, len(aliases)), make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		id, email := strings.TrimSpace(alias.AnonymousID), strings.ToLower(strings.TrimSpace(alias.Email))
		if email == "" {
			return errICloudAliasConflict
		}
		if id != "" {
			if _, ok := seenIDs[id]; ok {
				return errICloudAliasConflict
			}
			seenIDs[id] = struct{}{}
		}
		if _, ok := seenEmails[email]; ok {
			return errICloudAliasConflict
		}
		seenEmails[email] = struct{}{}
		var current iCloudAliasModel
		err := tx.Where("resource_id = ? AND email = ?", resourceID, email).Take(&current).Error
		values := map[string]any{"label": strings.TrimSpace(alias.Label), "note": strings.TrimSpace(alias.Note), "origin": strings.TrimSpace(alias.Origin), "status": map[bool]string{true: "normal", false: "disabled"}[alias.Active], "provider_created_at": alias.ProviderCreatedAt, "last_seen_at": now, "updated_at": now}
		if id != "" {
			values["anonymous_id"] = id
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			item := iCloudAliasModel{ResourceID: resourceID, AnonymousID: id, Email: email, Label: strings.TrimSpace(alias.Label), Note: strings.TrimSpace(alias.Note), Origin: strings.TrimSpace(alias.Origin), Status: values["status"].(string), ProviderCreatedAt: alias.ProviderCreatedAt, LastSeenAt: &now, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&item).Error; err != nil {
				return errICloudAliasConflict
			}
		} else if err != nil {
			return err
		} else if err := tx.Model(&iCloudAliasModel{}).Where("id = ?", current.ID).Updates(values).Error; err != nil {
			return err
		}
	}
	if complete {
		query := tx.Model(&iCloudAliasModel{}).Where("resource_id = ? AND status <> ?", resourceID, iCloudResourceDeleted)
		if len(seenEmails) > 0 {
			emails := make([]string, 0, len(seenEmails))
			for email := range seenEmails {
				emails = append(emails, email)
			}
			query = query.Where("email NOT IN ?", emails)
		}
		if err := query.Updates(map[string]any{"status": "missing", "updated_at": now}).Error; err != nil {
			return err
		}
	}
	return nil
}

func findICloudAlias(aliases []hmeAlias, email string) *hmeAlias {
	for i := range aliases {
		if strings.EqualFold(strings.TrimSpace(aliases[i].Email), strings.TrimSpace(email)) {
			return &aliases[i]
		}
	}
	return nil
}

func iCloudTimePointer(value time.Time) *time.Time { return &value }

func minICloudValidationFailures(value uint8) uint8 {
	if value > iCloudValidationMaxFailures {
		return iCloudValidationMaxFailures
	}
	return value
}
func safeICloudValidationMessage(value string) string {
	if value = safeICloudImportMessage(value); value != "" {
		return value
	}
	return "iCloud validation failed."
}
