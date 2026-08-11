package icloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const adminICloudBatchMax = 1000

var (
	ErrICloudResourceVersion    = errors.New("icloud: resource version conflict")
	ErrICloudResourceOwner      = errors.New("icloud: resource owner is not eligible")
	ErrICloudResourceAllocation = errors.New("icloud: resource has an active allocation")
	ErrICloudResourceSelection  = errors.New("icloud: invalid resource selection")
)

type AdminICloudCommand string

const (
	AdminICloudValidate  AdminICloudCommand = "validate"
	AdminICloudAlias     AdminICloudCommand = "alias"
	AdminICloudEnable    AdminICloudCommand = "enable"
	AdminICloudDisable   AdminICloudCommand = "disable"
	AdminICloudPublish   AdminICloudCommand = "publish"
	AdminICloudUnpublish AdminICloudCommand = "unpublish"
	AdminICloudDelete    AdminICloudCommand = "delete"
	AdminICloudRecover   AdminICloudCommand = "recover"
	AdminICloudExpire    AdminICloudCommand = "expire"
)

type AdminICloudMutationResult struct {
	ResourceID uint   `json:"resourceId"`
	Version    uint64 `json:"version"`
	Status     string `json:"status"`
	ForSale    bool   `json:"forSale"`
	Changed    bool   `json:"changed"`
}

type AdminICloudResourceSelection struct {
	Mode        string                         `json:"mode"`
	ResourceIDs []uint                         `json:"resourceIds"`
	Filter      *AdminICloudResourceListFilter `json:"filter"`
}

type AdminICloudReasonCount struct {
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}

type AdminICloudBulkResult struct {
	Requested           int                      `json:"requested"`
	Affected            int                      `json:"affected"`
	Skipped             int                      `json:"skipped"`
	AffectedResourceIDs []uint                   `json:"affectedResourceIds"`
	SkippedResourceIDs  []uint                   `json:"skippedResourceIds"`
	ReasonCounts        []AdminICloudReasonCount `json:"reasonCounts"`
}

func (s *Service) ApplyAdminICloudCommand(
	ctx context.Context,
	command AdminICloudCommand,
	resourceID uint,
	version uint64,
	operatorUserID uint,
	idempotencyKey string,
	requestID string,
	path string,
) (*AdminICloudMutationResult, error) {
	if s == nil || s.db == nil || s.operationLogs == nil || !validAdminICloudCommand(command) ||
		resourceID == 0 || version == 0 || operatorUserID == 0 {
		return nil, ErrICloudResourceQuery
	}
	idempotencyKey, err := normalizeAdminICloudIdempotencyKey(idempotencyKey)
	if err != nil {
		return nil, err
	}
	fingerprint, err := adminICloudCommandFingerprint(struct {
		Version uint64 `json:"version"`
	}{Version: version})
	if err != nil {
		return nil, ErrICloudResourceQueryTemporary
	}
	receipt := coreapp.AdminResourceCommandReceipt{
		OperatorUserID: operatorUserID, IdempotencyKey: idempotencyKey,
		Operation: "icloud.admin_resource." + string(command), Subject: "icloud_resource:" + strconv.FormatUint(uint64(resourceID), 10),
		RequestFingerprint: fingerprint,
	}

	result := &AdminICloudMutationResult{}
	replayed := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		wasReplayed, err := s.reserveAdminICloudCommand(ctx, tx, receipt, result)
		if err != nil || wasReplayed {
			replayed = wasReplayed
			return err
		}
		state, changed, err := mutateAdminICloudResourceTx(ctx, tx, command, resourceID, &version, nil, s.now().UTC())
		if err != nil {
			return err
		}
		*result = *state
		result.Changed = changed
		summary := "iCloud resource already had the requested state."
		if changed {
			summary = "iCloud resource command applied."
		}
		if err := s.operationLogs.CreateInTx(ctx, tx, &governancedomain.OperationLog{
			OperatorUserID: operatorUserID,
			OperationType:  "icloud.admin_resource." + string(command),
			ResourceType:   "icloud_resource",
			ResourceID:     strconv.FormatUint(uint64(resourceID), 10),
			Path:           strings.TrimSpace(path),
			Result:         "success",
			SafeSummary:    summary,
			RequestID:      strings.TrimSpace(requestID),
		}); err != nil {
			return err
		}
		return s.completeAdminICloudCommand(ctx, tx, operatorUserID, idempotencyKey, result)
	})
	if err != nil {
		return nil, normalizeAdminICloudCommandError(err)
	}
	if !replayed && result.Changed && commandQueuesICloudValidation(command) {
		_ = s.ScheduleICloudValidationDispatcher(context.WithoutCancel(ctx), 0)
	}
	return result, nil
}

func (s *Service) ApplyAdminICloudBatch(
	ctx context.Context,
	command AdminICloudCommand,
	selection AdminICloudResourceSelection,
	expireAt *time.Time,
	operatorUserID uint,
	idempotencyKey string,
	requestID string,
	path string,
) (*AdminICloudBulkResult, error) {
	if s == nil || s.db == nil || s.operationLogs == nil || !validAdminICloudBatchCommand(command) || operatorUserID == 0 {
		return nil, ErrICloudResourceSelection
	}
	idempotencyKey, err := normalizeAdminICloudIdempotencyKey(idempotencyKey)
	if err != nil {
		return nil, err
	}
	selection, err = normalizeAdminICloudSelection(selection)
	if err != nil {
		return nil, err
	}
	if command == AdminICloudExpire {
		if expireAt == nil {
			return nil, ErrICloudResourceUpdate
		}
		value := normalizeICloudResourceExpireAt(*expireAt)
		expireAt = &value
	} else if expireAt != nil {
		return nil, ErrICloudResourceSelection
	}
	fingerprintValue := any(selection)
	if expireAt != nil {
		fingerprintValue = struct {
			Selection AdminICloudResourceSelection `json:"selection"`
			ExpireAt  time.Time                    `json:"expireAt"`
		}{selection, *expireAt}
	}
	fingerprint, err := adminICloudCommandFingerprint(fingerprintValue)
	if err != nil {
		return nil, ErrICloudResourceQueryTemporary
	}
	receipt := coreapp.AdminResourceCommandReceipt{
		OperatorUserID: operatorUserID, IdempotencyKey: idempotencyKey,
		Operation: "icloud.admin_resource." + string(command) + "_batch", Subject: "icloud_resources:" + fingerprint,
		RequestFingerprint: fingerprint,
	}

	result := &AdminICloudBulkResult{}
	replayed := false
	reasons := make(map[string]int64)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		wasReplayed, err := s.reserveAdminICloudCommand(ctx, tx, receipt, result)
		if err != nil || wasReplayed {
			replayed = wasReplayed
			return err
		}
		now := s.now().UTC()
		if expireAt != nil && !validICloudResourceExpireAt(*expireAt, now) {
			return ErrICloudResourceUpdate
		}
		resourceIDs, err := resolveAdminICloudSelectionTx(ctx, tx, selection)
		if err != nil {
			return err
		}
		result.Requested = len(resourceIDs)
		for _, resourceID := range resourceIDs {
			_, changed, err := mutateAdminICloudResourceTx(ctx, tx, command, resourceID, nil, expireAt, now)
			if err != nil {
				reason := adminICloudSkipReason(err)
				if reason == "" {
					return err
				}
				appendAdminICloudSkip(result, reasons, resourceID, reason)
				continue
			}
			if !changed {
				appendAdminICloudSkip(result, reasons, resourceID, "already_target")
				continue
			}
			result.Affected++
			result.AffectedResourceIDs = append(result.AffectedResourceIDs, resourceID)
		}
		result.Skipped = len(result.SkippedResourceIDs)
		result.ReasonCounts = adminICloudReasonCounts(reasons)
		if err := s.operationLogs.CreateInTx(ctx, tx, &governancedomain.OperationLog{
			OperatorUserID: operatorUserID,
			OperationType:  "icloud.admin_resource." + string(command) + "_batch",
			ResourceType:   "icloud_resource",
			ResourceID:     "batch",
			Path:           strings.TrimSpace(path),
			Result:         "success",
			SafeSummary: fmt.Sprintf(
				"iCloud resource batch command completed. Requested: %d; affected: %d; skipped: %d.",
				result.Requested, result.Affected, result.Skipped,
			),
			RequestID: strings.TrimSpace(requestID),
		}); err != nil {
			return err
		}
		return s.completeAdminICloudCommand(ctx, tx, operatorUserID, idempotencyKey, result)
	})
	if err != nil {
		return nil, normalizeAdminICloudCommandError(err)
	}
	if !replayed && result.Affected > 0 && commandQueuesICloudValidation(command) {
		_ = s.ScheduleICloudValidationDispatcher(context.WithoutCancel(ctx), 0)
	}
	return result, nil
}

func normalizeAdminICloudIdempotencyKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return "", ErrICloudResourceQuery
	}
	return value, nil
}

func normalizeAdminICloudSelection(selection AdminICloudResourceSelection) (AdminICloudResourceSelection, error) {
	selection.Mode = strings.ToLower(strings.TrimSpace(selection.Mode))
	switch selection.Mode {
	case "ids":
		if selection.Filter != nil {
			return selection, ErrICloudResourceSelection
		}
		for _, resourceID := range selection.ResourceIDs {
			if resourceID == 0 {
				return selection, ErrICloudResourceSelection
			}
		}
		selection.ResourceIDs = uniqueAdminICloudResourceIDs(selection.ResourceIDs)
		if len(selection.ResourceIDs) == 0 || len(selection.ResourceIDs) > adminICloudBatchMax {
			return selection, ErrICloudResourceSelection
		}
	case "filter":
		if selection.Filter == nil || len(selection.ResourceIDs) != 0 {
			return selection, ErrICloudResourceSelection
		}
		filter, err := normalizeAdminICloudResourceFilter(*selection.Filter)
		if err != nil {
			return selection, err
		}
		selection.ResourceIDs = nil
		selection.Filter = &filter
	default:
		return selection, ErrICloudResourceSelection
	}
	return selection, nil
}

func adminICloudCommandFingerprint(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (s *Service) reserveAdminICloudCommand(ctx context.Context, tx *gorm.DB, receipt coreapp.AdminResourceCommandReceipt, target any) (bool, error) {
	repo := coreinfra.NewAdminResourceRepo(s.db)
	resultJSON, replayed, err := repo.ReserveAdminCommand(platform.WithGormTx(ctx, tx), receipt)
	if err != nil || !replayed {
		return replayed, err
	}
	if err := json.Unmarshal(resultJSON, target); err != nil {
		return false, ErrICloudResourceQueryTemporary
	}
	return true, nil
}

func (s *Service) completeAdminICloudCommand(ctx context.Context, tx *gorm.DB, operatorUserID uint, idempotencyKey string, result any) error {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return coreinfra.NewAdminResourceRepo(s.db).CompleteAdminCommand(platform.WithGormTx(ctx, tx), operatorUserID, idempotencyKey, resultJSON)
}

func validAdminICloudCommand(command AdminICloudCommand) bool {
	switch command {
	case AdminICloudValidate, AdminICloudAlias, AdminICloudEnable, AdminICloudDisable, AdminICloudPublish,
		AdminICloudUnpublish, AdminICloudDelete, AdminICloudRecover:
		return true
	default:
		return false
	}
}

func validAdminICloudBatchCommand(command AdminICloudCommand) bool {
	switch command {
	case AdminICloudValidate, AdminICloudAlias, AdminICloudDisable, AdminICloudPublish, AdminICloudUnpublish, AdminICloudDelete, AdminICloudExpire:
		return true
	default:
		return false
	}
}

func commandQueuesICloudValidation(command AdminICloudCommand) bool {
	return command == AdminICloudValidate || command == AdminICloudAlias || command == AdminICloudEnable || command == AdminICloudRecover
}

func resolveAdminICloudSelectionTx(ctx context.Context, tx *gorm.DB, selection AdminICloudResourceSelection) ([]uint, error) {
	switch selection.Mode {
	case "ids":
		ids := selection.ResourceIDs
		if len(ids) == 0 || len(ids) > adminICloudBatchMax {
			return nil, ErrICloudResourceSelection
		}
		return ids, nil
	case "filter":
		if selection.Filter == nil {
			return nil, ErrICloudResourceSelection
		}
		filter := *selection.Filter
		// ponytail: synchronous administrator batches are capped at 1000; move
		// iCloud batch commands to durable tasks when real volumes exceed this.
		var rows []struct {
			ID uint `gorm:"column:id"`
		}
		query := applyAdminICloudResourceFilterDB(
			adminICloudResourceQueryDB(ctx, tx), filter, adminICloudFilterIgnore{},
		)
		if err := query.Select("ir.id").Order("ir.id ASC").Limit(adminICloudBatchMax + 1).Scan(&rows).Error; err != nil {
			return nil, err
		}
		if len(rows) == 0 || len(rows) > adminICloudBatchMax {
			return nil, ErrICloudResourceSelection
		}
		ids := make([]uint, len(rows))
		for index := range rows {
			ids[index] = rows[index].ID
		}
		return ids, nil
	default:
		return nil, ErrICloudResourceSelection
	}
}

func uniqueAdminICloudResourceIDs(values []uint) []uint {
	seen := make(map[uint]struct{}, len(values))
	result := make([]uint, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func mutateAdminICloudResourceTx(
	ctx context.Context,
	tx *gorm.DB,
	command AdminICloudCommand,
	resourceID uint,
	expectedVersion *uint64,
	expireAt *time.Time,
	now time.Time,
) (*AdminICloudMutationResult, bool, error) {
	var root iCloudRootModel
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND type = ?", resourceID, "icloud").Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, ErrICloudResourceNotFound
		}
		return nil, false, err
	}
	if expectedVersion != nil && root.Version != *expectedVersion {
		return nil, false, ErrICloudResourceVersion
	}
	var resource iCloudResourceModel
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&resource, resourceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, ErrICloudResourceNotFound
		}
		return nil, false, err
	}

	updates := make(map[string]any)
	queuedGeneration := uint64(0)
	switch command {
	case AdminICloudValidate:
		if resource.Status == iCloudResourceDeleted {
			return nil, false, ErrICloudResourceNotFound
		}
		if resource.Status == iCloudResourceDisabled {
			return nil, false, ErrICloudResourceStatus
		}
		queuedGeneration = queueAdminICloudValidation(updates, resource, now, false)
	case AdminICloudAlias:
		if resource.Status == iCloudResourceDeleted {
			return nil, false, ErrICloudResourceNotFound
		}
		if resource.Status == iCloudResourceDisabled {
			return nil, false, ErrICloudResourceStatus
		}
		if resource.AliasCount >= iCloudMaxAliases {
			return adminICloudMutationResult(root, resource), false, nil
		}
		queuedGeneration = queueAdminICloudValidation(updates, resource, now, false)
	case AdminICloudEnable:
		if resource.Status != iCloudResourceDisabled {
			return nil, false, ErrICloudResourceStatus
		}
		queuedGeneration = queueAdminICloudValidation(updates, resource, now, true)
	case AdminICloudDisable:
		if resource.Status == iCloudResourceDeleted {
			return nil, false, ErrICloudResourceNotFound
		}
		if resource.Status == iCloudResourceDisabled {
			return adminICloudMutationResult(root, resource), false, nil
		}
		updates["status"] = iCloudResourceDisabled
		updates["next_validation_at"] = nil
		updates["next_keepalive_at"] = nil
	case AdminICloudPublish:
		if resource.Status == iCloudResourceDeleted {
			return nil, false, ErrICloudResourceNotFound
		}
		if resource.ForSale {
			return adminICloudMutationResult(root, resource), false, nil
		}
		if err := assertAdminICloudOwnerEligibleTx(ctx, tx, root.OwnerUserID); err != nil {
			return nil, false, err
		}
		updates["for_sale"] = true
	case AdminICloudUnpublish:
		if resource.Status == iCloudResourceDeleted {
			return nil, false, ErrICloudResourceNotFound
		}
		if !resource.ForSale {
			return adminICloudMutationResult(root, resource), false, nil
		}
		updates["for_sale"] = false
	case AdminICloudDelete:
		if resource.Status == iCloudResourceDeleted {
			return nil, false, ErrICloudResourceStatus
		}
		if err := assertNoActiveAdminICloudAllocationTx(ctx, tx, resourceID); err != nil {
			return nil, false, err
		}
		updates["status"] = iCloudResourceDeleted
		updates["for_sale"] = false
		updates["next_validation_at"] = nil
		updates["next_keepalive_at"] = nil
		updates["last_allocated_at"] = nil
		updates["last_safe_error"] = ""
	case AdminICloudRecover:
		if resource.Status != iCloudResourceDeleted {
			return nil, false, ErrICloudResourceStatus
		}
		queuedGeneration = queueAdminICloudValidation(updates, resource, now, true)
	case AdminICloudExpire:
		if resource.Status == iCloudResourceDeleted {
			return nil, false, ErrICloudResourceNotFound
		}
		if expireAt == nil {
			return nil, false, ErrICloudResourceUpdate
		}
		if resource.ExpireAt.Equal(*expireAt) {
			return adminICloudMutationResult(root, resource), false, nil
		}
		updates["expire_at"] = *expireAt
	default:
		return nil, false, ErrICloudResourceQuery
	}

	updates["updated_at"] = now
	if err := tx.WithContext(ctx).Model(&iCloudResourceModel{}).Where("id = ?", resourceID).Updates(updates).Error; err != nil {
		return nil, false, err
	}
	if queuedGeneration > 0 {
		if _, err := ensureICloudMaintenanceRunTx(
			ctx, tx, resourceID, queuedGeneration, iCloudMaintenanceKindForCommand(command),
			resource.CredentialRevision, 0, now,
		); err != nil {
			return nil, false, err
		}
	} else if command == AdminICloudDisable || command == AdminICloudDelete {
		if err := cancelActiveICloudMaintenanceRunsTx(ctx, tx, resourceID, 0, now); err != nil {
			return nil, false, err
		}
	}
	rootUpdate := tx.WithContext(ctx).Model(&iCloudRootModel{}).
		Where("id = ? AND type = ? AND version = ?", resourceID, "icloud", root.Version).
		Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now})
	if rootUpdate.Error != nil {
		return nil, false, rootUpdate.Error
	}
	if rootUpdate.RowsAffected != 1 {
		return nil, false, ErrICloudResourceVersion
	}

	for key, value := range updates {
		switch key {
		case "status":
			resource.Status = value.(string)
		case "for_sale":
			resource.ForSale = value.(bool)
		case "validation_generation":
			resource.ValidationGeneration = value.(uint64)
		}
	}
	root.Version++
	return adminICloudMutationResult(root, resource), true, nil
}

func queueAdminICloudValidation(updates map[string]any, resource iCloudResourceModel, now time.Time, resetSession bool) uint64 {
	generation := resource.ValidationGeneration
	if generation == 0 {
		generation = 1
	} else {
		generation++
	}
	updates["status"] = iCloudResourcePending
	updates["validation_generation"] = generation
	updates["validation_failures"] = 0
	updates["next_validation_at"] = now
	updates["last_safe_error"] = ""
	updates["delivery_probe_token"] = ""
	updates["delivery_probe_alias"] = ""
	updates["delivery_probe_started_at"] = nil
	updates["delivery_probe_verified_at"] = nil
	if resetSession {
		updates["for_sale"] = false
		updates["session_status"] = iCloudSessionUnchecked
		updates["session_failures"] = 0
		updates["next_keepalive_at"] = nil
		updates["last_valid_at"] = nil
	}
	return generation
}

func adminICloudMutationResult(root iCloudRootModel, resource iCloudResourceModel) *AdminICloudMutationResult {
	return &AdminICloudMutationResult{
		ResourceID: resource.ID,
		Version:    root.Version,
		Status:     resource.Status,
		ForSale:    resource.ForSale,
	}
}

func assertAdminICloudOwnerEligibleTx(ctx context.Context, tx *gorm.DB, ownerUserID uint) error {
	var owner struct {
		Status string `gorm:"column:status"`
		Role   string `gorm:"column:role"`
	}
	if err := tx.WithContext(ctx).Table("users").Select("status, role").Where("id = ?", ownerUserID).Take(&owner).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrICloudResourceOwner
		}
		return err
	}
	if owner.Status != "active" {
		return ErrICloudResourceOwner
	}
	switch owner.Role {
	case "supplier", "admin", "super_admin":
		return nil
	default:
		return ErrICloudResourceOwner
	}
}

func assertNoActiveAdminICloudAllocationTx(ctx context.Context, tx *gorm.DB, resourceID uint) error {
	var count int64
	if err := tx.WithContext(ctx).Table("icloud_allocations").
		Where("resource_id = ? AND status = ?", resourceID, "allocated").Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrICloudResourceAllocation
	}
	return nil
}

func appendAdminICloudSkip(result *AdminICloudBulkResult, reasons map[string]int64, resourceID uint, reason string) {
	result.SkippedResourceIDs = append(result.SkippedResourceIDs, resourceID)
	reasons[reason]++
}

func adminICloudSkipReason(err error) string {
	switch {
	case errors.Is(err, ErrICloudResourceNotFound):
		return "not_found"
	case errors.Is(err, ErrICloudResourceStatus):
		return "invalid_state"
	case errors.Is(err, ErrICloudResourceOwner):
		return "owner_ineligible"
	case errors.Is(err, ErrICloudResourceAllocation):
		return "active_allocation"
	default:
		return ""
	}
}

func adminICloudReasonCounts(values map[string]int64) []AdminICloudReasonCount {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]AdminICloudReasonCount, 0, len(keys))
	for _, key := range keys {
		result = append(result, AdminICloudReasonCount{Reason: key, Count: values[key]})
	}
	return result
}

func normalizeAdminICloudCommandError(err error) error {
	switch {
	case errors.Is(err, coredomain.ErrResourceIdempotencyConflict):
		return ErrICloudImportConflict
	case errors.Is(err, coredomain.ErrInvalidResourceCommand):
		return ErrICloudResourceQuery
	case errors.Is(err, coredomain.ErrResourceDependency):
		return ErrICloudResourceQueryTemporary
	case errors.Is(err, ErrICloudResourceQuery), errors.Is(err, ErrICloudResourceSelection),
		errors.Is(err, ErrICloudResourceNotFound), errors.Is(err, ErrICloudResourceStatus),
		errors.Is(err, ErrICloudResourceVersion), errors.Is(err, ErrICloudResourceOwner),
		errors.Is(err, ErrICloudResourceAllocation), errors.Is(err, ErrICloudResourceUpdate),
		errors.Is(err, ErrICloudResourceIdentity):
		return err
	default:
		return ErrICloudResourceQueryTemporary
	}
}
