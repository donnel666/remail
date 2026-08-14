package icloud

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
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

// RequestAdminICloudValidation queues one create check for every imported
// Apple session channel.
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
			SafeSummary: "iCloud session validation queued.", RequestID: strings.TrimSpace(requestID),
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
			"updated_at": now, "last_safe_error": "Validation lease expired; retrying session checks.",
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
	if err := s.db.WithContext(ctx).Table("icloud_resources AS ir").Select("ir.id, er.owner_user_id, ir.credential_revision, ir.validation_generation").Joins("JOIN email_resources AS er ON er.id = ir.id AND er.type = ?", "icloud").Where("ir.status = ? AND ir.next_validation_at IS NOT NULL AND ir.next_validation_at <= ?", iCloudResourcePending, now).Order("ir.id ASC").Limit(limit).Scan(&rows).Error; err != nil {
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
		result := tx.Model(&iCloudResourceModel{}).Where("id = ? AND status = ? AND validation_generation = ? AND credential_revision = ?", task.ResourceID, iCloudResourcePending, task.ValidationGeneration, task.ExpectedCredentialRevision).Updates(map[string]any{
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
			return errICloudValidationStale
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

func syncICloudAliasesTx(tx *gorm.DB, resourceID uint, aliases []hmeAlias, complete bool, now time.Time) error {
	if tx == nil || resourceID == 0 {
		return errICloudAliasConflict
	}
	allowedDomains := iCloudForwardingDomains(runtimeconfig.String(runtimeconfig.ICloudForwardingSuffixesKey, ""))
	seenIDs, seenEmails := make(map[string]struct{}, len(aliases)), make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		id := strings.TrimSpace(alias.AnonymousID)
		email := strings.ToLower(strings.TrimSpace(alias.Email))
		forwardToEmail := strings.ToLower(strings.TrimSpace(alias.ForwardToEmail))
		if id == "" || email == "" || forwardToEmail == "" {
			return errICloudAliasConflict
		}
		if _, ok := seenIDs[id]; ok {
			return errICloudAliasConflict
		}
		seenIDs[id] = struct{}{}
		if _, ok := seenEmails[email]; ok {
			return errICloudAliasConflict
		}
		seenEmails[email] = struct{}{}
		status := "normal"
		if !alias.Active || !iCloudForwardingDomainAllowed(forwardToEmail, allowedDomains) {
			status = "disabled"
		}
		var current iCloudAliasModel
		err := tx.Where("resource_id = ? AND anonymous_id = ?", resourceID, id).Take(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			var byEmail iCloudAliasModel
			if emailErr := tx.Where("email = ?", email).Take(&byEmail).Error; emailErr == nil || !errors.Is(emailErr, gorm.ErrRecordNotFound) {
				return errICloudAliasConflict
			}
			current = iCloudAliasModel{
				ResourceID: resourceID, AnonymousID: id, Email: email,
				Label: strings.TrimSpace(alias.Label), Note: strings.TrimSpace(alias.Note),
				ForwardToEmail: forwardToEmail, Origin: strings.TrimSpace(alias.Origin),
				ProviderDomain: strings.TrimSpace(alias.ProviderDomain), RecipientMailID: strings.TrimSpace(alias.RecipientMailID),
				Status: status, ProviderCreatedAt: alias.ProviderCreatedAt, LastSeenAt: &now,
				CreatedAt: now, UpdatedAt: now,
			}
			if err := tx.Create(&current).Error; err != nil {
				return errICloudAliasConflict
			}
		} else if err != nil {
			return err
		} else {
			if !strings.EqualFold(current.Email, email) {
				var count int64
				if err := tx.Model(&iCloudAliasModel{}).Where("email = ? AND id <> ?", email, current.ID).Count(&count).Error; err != nil || count > 0 {
					return errICloudAliasConflict
				}
			}
			if err := persistICloudAliasRouteTx(tx, current.ID, resourceID, current.ForwardToEmail, current.RecipientMailID, now); err != nil {
				return err
			}
			updates := map[string]any{
				"email": email, "label": strings.TrimSpace(alias.Label), "note": strings.TrimSpace(alias.Note),
				"forward_to_email": forwardToEmail, "origin": strings.TrimSpace(alias.Origin),
				"provider_domain": strings.TrimSpace(alias.ProviderDomain), "status": status,
				"provider_created_at": alias.ProviderCreatedAt, "last_seen_at": now, "updated_at": now,
			}
			if value := strings.TrimSpace(alias.RecipientMailID); value != "" {
				updates["recipient_mail_id"] = value
				current.RecipientMailID = value
			} else if !strings.EqualFold(current.ForwardToEmail, forwardToEmail) {
				updates["recipient_mail_id"] = ""
				current.RecipientMailID = ""
			}
			if err := tx.Model(&iCloudAliasModel{}).Where("id = ? AND resource_id = ?", current.ID, resourceID).Updates(updates).Error; err != nil {
				return err
			}
		}
		if err := persistICloudAliasRouteTx(tx, current.ID, resourceID, forwardToEmail, current.RecipientMailID, now); err != nil {
			return err
		}
	}
	if complete {
		query := tx.Model(&iCloudAliasModel{}).Where("resource_id = ? AND status <> ?", resourceID, iCloudResourceDeleted)
		if len(seenIDs) > 0 {
			ids := make([]string, 0, len(seenIDs))
			for id := range seenIDs {
				ids = append(ids, id)
			}
			query = query.Where("anonymous_id NOT IN ?", ids)
		}
		if err := query.Updates(map[string]any{"status": "missing", "updated_at": now}).Error; err != nil {
			return err
		}
	}
	return nil
}

func disableUnauthorizedICloudAliasesTx(tx *gorm.DB, resourceID uint, allowedDomains map[string]struct{}, now time.Time) error {
	var aliases []struct {
		ID             uint   `gorm:"column:id"`
		ForwardToEmail string `gorm:"column:forward_to_email"`
	}
	if err := tx.Model(&iCloudAliasModel{}).Select("id, forward_to_email").
		Where("resource_id = ? AND status NOT IN ?", resourceID, []string{"missing", iCloudResourceDeleted}).
		Find(&aliases).Error; err != nil {
		return err
	}
	unauthorized := make([]uint, 0)
	for _, alias := range aliases {
		if !iCloudForwardingDomainAllowed(alias.ForwardToEmail, allowedDomains) {
			unauthorized = append(unauthorized, alias.ID)
		}
	}
	if len(unauthorized) == 0 {
		return nil
	}
	return tx.Model(&iCloudAliasModel{}).Where("id IN ?", unauthorized).
		Updates(map[string]any{"status": iCloudResourceDisabled, "updated_at": now}).Error
}

func persistICloudAliasRouteTx(tx *gorm.DB, aliasID, resourceID uint, forwardToEmail, recipientMailID string, now time.Time) error {
	forwardToEmail = strings.ToLower(strings.TrimSpace(forwardToEmail))
	recipientMailID = strings.ToLower(strings.TrimSpace(recipientMailID))
	if tx == nil || aliasID == 0 || resourceID == 0 || forwardToEmail == "" || recipientMailID == "" {
		return nil
	}
	var route iCloudAliasRouteModel
	err := tx.Where("forward_to_email = ? AND recipient_mail_id = ?", forwardToEmail, recipientMailID).Take(&route).Error
	if err == nil {
		if route.ResourceID != resourceID || route.AliasID != aliasID {
			return errICloudAliasConflict
		}
		return tx.Model(&iCloudAliasRouteModel{}).Where("id = ?", route.ID).Update("last_seen_at", now).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return tx.Create(&iCloudAliasRouteModel{
		ResourceID: resourceID, AliasID: aliasID, ForwardToEmail: forwardToEmail,
		RecipientMailID: recipientMailID, FirstSeenAt: now, LastSeenAt: now,
	}).Error
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

func safeICloudValidationMessage(value string) string {
	if value = safeICloudImportMessage(value); value != "" {
		return value
	}
	return "iCloud validation failed."
}
