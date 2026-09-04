package gmail

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
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const adminLocalGmailBatchMax = 1000

var (
	ErrLocalResourceSelection = errors.New("gmail: invalid resource selection")
	ErrLocalResourceOwner     = errors.New("gmail: resource owner is not eligible")
	ErrLocalResourceState     = errors.New("gmail: resource status does not allow this operation")
)

type AdminLocalResourceCommand string

const (
	AdminLocalResourceValidate  AdminLocalResourceCommand = "validate"
	AdminLocalResourceHistory   AdminLocalResourceCommand = "history"
	AdminLocalResourceEnable    AdminLocalResourceCommand = "enable"
	AdminLocalResourceDisable   AdminLocalResourceCommand = "disable"
	AdminLocalResourcePublish   AdminLocalResourceCommand = "publish"
	AdminLocalResourceUnpublish AdminLocalResourceCommand = "unpublish"
	AdminLocalResourceDelete    AdminLocalResourceCommand = "delete"
	AdminLocalResourceRecover   AdminLocalResourceCommand = "recover"
)

type AdminLocalResourceMutationResult struct {
	ResourceID uint   `json:"resourceId"`
	Version    uint64 `json:"version"`
	Status     string `json:"status"`
	ForSale    bool   `json:"forSale"`
}

type AdminLocalResourceSelection struct {
	Mode        string                   `json:"mode"`
	ResourceIDs []uint                   `json:"resourceIds"`
	Filter      *LocalResourceListFilter `json:"filter"`
}

type AdminLocalResourceReasonCount struct {
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}

type AdminLocalResourceBulkResult struct {
	Requested           int                             `json:"requested"`
	Affected            int                             `json:"affected"`
	Skipped             int                             `json:"skipped"`
	AffectedResourceIDs []uint                          `json:"affectedResourceIds"`
	SkippedResourceIDs  []uint                          `json:"skippedResourceIds"`
	ReasonCounts        []AdminLocalResourceReasonCount `json:"reasonCounts"`
}

type AdminLocalResourceCredentialsInput struct {
	Password        string `json:"password"`
	TwoFactorSecret string `json:"twoFactorSecret,omitempty"`
	AppPassword     string `json:"appPassword,omitempty"`
}

type AdminLocalResourceEditCommand struct {
	ResourceID            uint
	Version               uint64
	OperatorID            uint
	OwnerUserID           uint
	Email                 string
	BindingEmail          string
	Credentials           *AdminLocalResourceCredentialsInput
	CredentialReplacement bool
	IdempotencyKey        string
	RequestID             string
	Path                  string
}

func (s *Service) ApplyAdminLocalResourceCommand(
	ctx context.Context,
	command AdminLocalResourceCommand,
	resourceID uint,
	version uint64,
	operatorUserID uint,
	idempotencyKey, requestID, path string,
) (*AdminLocalResourceMutationResult, error) {
	if s == nil || s.db == nil || s.logs == nil || !validAdminLocalResourceCommand(command) ||
		resourceID == 0 || operatorUserID == 0 || version == 0 && command != AdminLocalResourceValidate && command != AdminLocalResourceHistory {
		return nil, ErrInvalidLocalResource
	}
	idempotencyKey, err := normalizeAdminLocalResourceIdempotencyKey(idempotencyKey)
	if err != nil {
		return nil, err
	}
	fingerprint, err := adminLocalResourceFingerprint(struct {
		Version uint64 `json:"version"`
	}{Version: version})
	if err != nil {
		return nil, ErrLocalValidationDependency
	}
	receipt := coreapp.AdminResourceCommandReceipt{
		OperatorUserID:     operatorUserID,
		IdempotencyKey:     idempotencyKey,
		Operation:          "gmail.admin_resource." + string(command),
		Subject:            "gmail_resource:" + strconv.FormatUint(uint64(resourceID), 10),
		RequestFingerprint: fingerprint,
	}

	result := &AdminLocalResourceMutationResult{}
	replayed := false
	err = s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		wasReplayed, err := s.reserveAdminLocalResourceCommand(ctx, tx, receipt, result)
		if err != nil || wasReplayed {
			replayed = wasReplayed
			return err
		}
		var expectedVersion *uint64
		if version != 0 {
			expectedVersion = &version
		}
		state, changed, err := s.mutateAdminLocalResourceTx(ctx, tx, command, resourceID, expectedVersion, strings.TrimSpace(requestID), s.now().UTC())
		if err != nil {
			return err
		}
		*result = *state
		summary := "Gmail resource already had the requested state."
		if changed {
			summary = "Gmail resource command applied."
		}
		if err := s.logs.CreateInTx(ctx, tx, &governancedomain.OperationLog{
			OperatorUserID: operatorUserID,
			OperationType:  "gmail.admin_resource." + string(command),
			ResourceType:   "gmail_resource",
			ResourceID:     strconv.FormatUint(uint64(resourceID), 10),
			Path:           strings.TrimSpace(path),
			Result:         "success",
			SafeSummary:    summary,
			RequestID:      strings.TrimSpace(requestID),
		}); err != nil {
			return err
		}
		return s.completeAdminLocalResourceCommand(ctx, tx, operatorUserID, idempotencyKey, result)
	})
	if err != nil {
		return nil, normalizeAdminLocalResourceCommandError(err)
	}
	if !replayed {
		_ = s.scheduleAdminLocalResourceMaintenance(context.WithoutCancel(ctx), command)
	}
	return result, nil
}

func (s *Service) ApplyAdminLocalResourceBatch(
	ctx context.Context,
	command AdminLocalResourceCommand,
	selection AdminLocalResourceSelection,
	operatorUserID uint,
	idempotencyKey, requestID, path string,
) (*AdminLocalResourceBulkResult, error) {
	if s == nil || s.db == nil || s.logs == nil || !validAdminLocalResourceBatchCommand(command) || operatorUserID == 0 {
		return nil, ErrLocalResourceSelection
	}
	idempotencyKey, err := normalizeAdminLocalResourceIdempotencyKey(idempotencyKey)
	if err != nil {
		return nil, err
	}
	selection, err = normalizeAdminLocalResourceSelection(selection)
	if err != nil {
		return nil, err
	}
	fingerprint, err := adminLocalResourceFingerprint(selection)
	if err != nil {
		return nil, ErrLocalValidationDependency
	}
	receipt := coreapp.AdminResourceCommandReceipt{
		OperatorUserID:     operatorUserID,
		IdempotencyKey:     idempotencyKey,
		Operation:          "gmail.admin_resource." + string(command) + "_batch",
		Subject:            "gmail_resources:" + fingerprint,
		RequestFingerprint: fingerprint,
	}

	result := &AdminLocalResourceBulkResult{}
	replayed := false
	reasons := make(map[string]int64)
	err = s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		wasReplayed, err := s.reserveAdminLocalResourceCommand(ctx, tx, receipt, result)
		if err != nil || wasReplayed {
			replayed = wasReplayed
			return err
		}
		resourceIDs, err := resolveAdminLocalResourceSelectionTx(ctx, tx, selection)
		if err != nil {
			return err
		}
		result.Requested = len(resourceIDs)
		for _, resourceID := range resourceIDs {
			_, changed, err := s.mutateAdminLocalResourceTx(ctx, tx, command, resourceID, nil, strings.TrimSpace(requestID), s.now().UTC())
			if err != nil {
				reason := adminLocalResourceSkipReason(err)
				if reason == "" {
					return err
				}
				appendAdminLocalResourceSkip(result, reasons, resourceID, reason)
				continue
			}
			if !changed {
				appendAdminLocalResourceSkip(result, reasons, resourceID, "already_target")
				continue
			}
			result.Affected++
			result.AffectedResourceIDs = append(result.AffectedResourceIDs, resourceID)
		}
		result.Skipped = len(result.SkippedResourceIDs)
		result.ReasonCounts = adminLocalResourceReasonCounts(reasons)
		if err := s.logs.CreateInTx(ctx, tx, &governancedomain.OperationLog{
			OperatorUserID: operatorUserID,
			OperationType:  "gmail.admin_resource." + string(command) + "_batch",
			ResourceType:   "gmail_resource",
			ResourceID:     "batch",
			Path:           strings.TrimSpace(path),
			Result:         "success",
			SafeSummary: fmt.Sprintf("Gmail resource batch command completed. Requested: %d; affected: %d; skipped: %d.",
				result.Requested, result.Affected, result.Skipped),
			RequestID: strings.TrimSpace(requestID),
		}); err != nil {
			return err
		}
		return s.completeAdminLocalResourceCommand(ctx, tx, operatorUserID, idempotencyKey, result)
	})
	if err != nil {
		return nil, normalizeAdminLocalResourceCommandError(err)
	}
	if !replayed && result.Affected > 0 {
		_ = s.scheduleAdminLocalResourceMaintenance(context.WithoutCancel(ctx), command)
	}
	return result, nil
}

func (s *Service) UpdateAdminLocalResource(ctx context.Context, command AdminLocalResourceEditCommand) (*AdminLocalResourceMutationResult, error) {
	if s == nil || s.db == nil || s.logs == nil || command.ResourceID == 0 || command.Version == 0 ||
		command.OperatorID == 0 || command.OwnerUserID == 0 {
		return nil, ErrInvalidLocalResource
	}
	idempotencyKey, err := normalizeAdminLocalResourceIdempotencyKey(command.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	command.Email = strings.ToLower(strings.TrimSpace(command.Email))
	command.BindingEmail = strings.ToLower(strings.TrimSpace(command.BindingEmail))
	identity, ok := localGmailIdentity(command.Email)
	if !ok || command.BindingEmail != "" && !validLocalGmailEmailAddress(command.BindingEmail) {
		return nil, ErrInvalidLocalResource
	}
	if command.Credentials != nil {
		command.Credentials.TwoFactorSecret = strings.ToUpper(removeWhitespace(command.Credentials.TwoFactorSecret))
		command.Credentials.AppPassword = removeWhitespace(command.Credentials.AppPassword)
		if strings.TrimSpace(command.Credentials.Password) == "" && command.Credentials.TwoFactorSecret == "" && command.Credentials.AppPassword == "" ||
			len(command.Credentials.Password) > 512 ||
			len(command.Credentials.TwoFactorSecret) > 512 ||
			command.Credentials.TwoFactorSecret != "" && !validLocalGmailTOTPSecret(command.Credentials.TwoFactorSecret) ||
			command.Credentials.AppPassword != "" && !validLocalGmailAppPassword(command.Credentials.AppPassword) {
			return nil, ErrInvalidLocalResource
		}
	}
	if command.CredentialReplacement != (command.Credentials != nil) {
		return nil, ErrInvalidLocalResource
	}
	operationType := "gmail.admin_resource.edit"
	safeSummary := "Gmail resource metadata updated and validation queued."
	if command.CredentialReplacement {
		operationType = "gmail.admin_resource.credentials.replace"
		safeSummary = "Gmail resource credentials replaced and validation queued."
	}
	fingerprint, err := adminLocalResourceFingerprint(struct {
		Version               uint64                              `json:"version"`
		OwnerUserID           uint                                `json:"ownerUserId"`
		Email                 string                              `json:"email"`
		BindingEmail          string                              `json:"bindingEmail"`
		Credentials           *AdminLocalResourceCredentialsInput `json:"credentials,omitempty"`
		CredentialReplacement bool                                `json:"credentialReplacement,omitempty"`
	}{command.Version, command.OwnerUserID, command.Email, command.BindingEmail, command.Credentials, command.CredentialReplacement})
	if err != nil {
		return nil, ErrLocalValidationDependency
	}
	receipt := coreapp.AdminResourceCommandReceipt{
		OperatorUserID:     command.OperatorID,
		IdempotencyKey:     idempotencyKey,
		Operation:          operationType,
		Subject:            "gmail_resource:" + strconv.FormatUint(uint64(command.ResourceID), 10),
		RequestFingerprint: fingerprint,
	}

	result := &AdminLocalResourceMutationResult{}
	needsValidation := false
	replayed := false
	err = s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		wasReplayed, err := s.reserveAdminLocalResourceCommand(ctx, tx, receipt, result)
		if err != nil || wasReplayed {
			replayed = wasReplayed
			return err
		}
		var root resourceRootModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND type = ?", command.ResourceID, "gmail").Take(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLocalResourceMissing
			}
			return err
		}
		if root.Version != command.Version {
			return ErrLocalResourceVersion
		}
		var resource localResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&resource, command.ResourceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLocalResourceMissing
			}
			return err
		}
		if resource.Status == LocalResourceDeleted {
			return ErrLocalResourceState
		}
		if err := assertAdminLocalResourceOwnerTx(ctx, tx, command.OwnerUserID, resource.ForSale); err != nil {
			return err
		}
		var duplicate uint
		if err := tx.Model(&localResourceModel{}).Select("id").Where("identity = ? AND id <> ?", identity, command.ResourceID).Limit(1).Scan(&duplicate).Error; err != nil {
			return err
		}
		if duplicate != 0 {
			return ErrInvalidLocalResource
		}
		identityChanged := resource.Email != command.Email || resource.Identity != identity
		bindingChanged := resource.BindingEmail != command.BindingEmail
		ownerChanged := root.OwnerUserID != command.OwnerUserID || resource.OwnerUserID != command.OwnerUserID
		identityDataChanged := identityChanged || bindingChanged || ownerChanged
		credentialsChanged := command.Credentials != nil
		needsValidation = identityDataChanged || credentialsChanged
		if identityDataChanged {
			if err := assertNoActiveAdminLocalResourceAllocationTx(ctx, tx, command.ResourceID); err != nil {
				return err
			}
		}
		if !needsValidation {
			*result = *adminLocalResourceMutationResult(root, resource)
			if err := s.logs.CreateInTx(ctx, tx, &governancedomain.OperationLog{
				OperatorUserID: command.OperatorID,
				OperationType:  operationType,
				ResourceType:   "gmail_resource",
				ResourceID:     strconv.FormatUint(uint64(command.ResourceID), 10),
				Path:           strings.TrimSpace(command.Path),
				Result:         "success",
				SafeSummary:    "Gmail resource already had the submitted metadata.",
				RequestID:      strings.TrimSpace(command.RequestID),
			}); err != nil {
				return err
			}
			return s.completeAdminLocalResourceCommand(ctx, tx, command.OperatorID, idempotencyKey, result)
		}
		now := s.now().UTC()
		updates := map[string]any{
			"owner_user_id": command.OwnerUserID,
			"email":         command.Email, "identity": identity, "binding_email": command.BindingEmail,
			"status": LocalResourcePending, "validation_generation": nextAdminLocalResourceGeneration(resource.ValidationGeneration),
			"validation_failures": 0, "validation_request_id": strings.TrimSpace(command.RequestID),
			"validation_command_hash": "", "last_safe_error": "", "last_checked_at": nil, "updated_at": now,
		}
		if identityChanged {
			updates["provider_cursor"] = 0
			updates["provider_spam_cursor"] = 0
		}
		credentialChanged := identityChanged || bindingChanged || command.Credentials != nil
		if command.Credentials != nil {
			if strings.TrimSpace(command.Credentials.Password) != "" {
				updates["password"] = command.Credentials.Password
			}
			if command.Credentials.TwoFactorSecret != "" {
				updates["two_factor_secret"] = command.Credentials.TwoFactorSecret
			}
			if command.Credentials.AppPassword != "" {
				updates["app_password"] = command.Credentials.AppPassword
			}
		}
		if credentialChanged {
			updates["credential_revision"] = nextAdminLocalResourceGeneration(resource.CredentialRevision)
			updates["credential_updated_at"] = now
		}
		if err := tx.Model(&localResourceModel{}).Where("id = ?", command.ResourceID).Updates(updates).Error; err != nil {
			return err
		}
		queuedGeneration := updates["validation_generation"].(uint64)
		queuedCredentialRevision := resource.CredentialRevision
		if value, ok := updates["credential_revision"]; ok {
			queuedCredentialRevision = value.(uint64)
		}
		if _, err := ensureGmailMaintenanceRunTx(
			ctx, tx, command.ResourceID, queuedGeneration, gmailMaintenanceValidation,
			queuedCredentialRevision, 0, now,
		); err != nil {
			return err
		}
		rootUpdate := tx.Model(&resourceRootModel{}).Where("id = ? AND type = ? AND version = ?", command.ResourceID, "gmail", root.Version).
			Updates(map[string]any{"owner_user_id": command.OwnerUserID, "version": gorm.Expr("version + 1"), "updated_at": now})
		if rootUpdate.Error != nil {
			return rootUpdate.Error
		}
		if rootUpdate.RowsAffected != 1 {
			return ErrLocalResourceVersion
		}
		root.Version++
		resource.OwnerUserID = command.OwnerUserID
		resource.Email, resource.Identity, resource.BindingEmail = command.Email, identity, command.BindingEmail
		resource.Status = LocalResourcePending
		*result = *adminLocalResourceMutationResult(root, resource)
		if err := s.logs.CreateInTx(ctx, tx, &governancedomain.OperationLog{
			OperatorUserID: command.OperatorID,
			OperationType:  operationType,
			ResourceType:   "gmail_resource",
			ResourceID:     strconv.FormatUint(uint64(command.ResourceID), 10),
			Path:           strings.TrimSpace(command.Path),
			Result:         "success",
			SafeSummary:    safeSummary,
			RequestID:      strings.TrimSpace(command.RequestID),
		}); err != nil {
			return err
		}
		return s.completeAdminLocalResourceCommand(ctx, tx, command.OperatorID, idempotencyKey, result)
	})
	if err != nil {
		return nil, normalizeAdminLocalResourceCommandError(err)
	}
	if !replayed && needsValidation {
		_ = s.scheduleLocalResourceValidationDispatcher(context.WithoutCancel(ctx), 0)
	}
	return result, nil
}

func normalizeAdminLocalResourceIdempotencyKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return "", ErrInvalidLocalResource
	}
	return value, nil
}

func normalizeAdminLocalResourceSelection(selection AdminLocalResourceSelection) (AdminLocalResourceSelection, error) {
	selection.Mode = strings.ToLower(strings.TrimSpace(selection.Mode))
	switch selection.Mode {
	case "ids":
		if selection.Filter != nil {
			return selection, ErrLocalResourceSelection
		}
		selection.ResourceIDs = uniqueAdminLocalResourceIDs(selection.ResourceIDs)
		if len(selection.ResourceIDs) == 0 || len(selection.ResourceIDs) > adminLocalResourceBatchLimit() {
			return selection, ErrLocalResourceSelection
		}
	case "filter":
		if selection.Filter == nil || len(selection.ResourceIDs) != 0 {
			return selection, ErrLocalResourceSelection
		}
		filter, err := normalizeLocalResourceListFilter(*selection.Filter)
		if err != nil {
			return selection, err
		}
		filter.Offset, filter.Limit = 0, adminLocalResourceBatchLimit()
		selection.Filter = &filter
	default:
		return selection, ErrLocalResourceSelection
	}
	return selection, nil
}

func adminLocalResourceBatchLimit() int {
	return min(runtimeconfig.Int("admin_resource_bulk_max_ids", adminLocalGmailBatchMax, 1), adminLocalGmailBatchMax)
}

func resolveAdminLocalResourceSelectionTx(ctx context.Context, tx *gorm.DB, selection AdminLocalResourceSelection) ([]uint, error) {
	if selection.Mode == "ids" {
		return selection.ResourceIDs, nil
	}
	if selection.Mode != "filter" || selection.Filter == nil {
		return nil, ErrLocalResourceSelection
	}
	limit := adminLocalResourceBatchLimit()
	// ponytail: Gmail admin batches stay synchronous up to the existing 1000-ID
	// ceiling; move them to a durable cursor when real inventory exceeds it.
	var rows []struct {
		ID uint `gorm:"column:id"`
	}
	query := applyLocalResourceListFilter(localResourceAdminQuery(ctx, tx), *selection.Filter, false, false)
	if err := query.Select("gr.id").Order("gr.id ASC").Limit(limit + 1).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) > limit {
		return nil, ErrLocalResourceSelection
	}
	ids := make([]uint, len(rows))
	for index := range rows {
		ids[index] = rows[index].ID
	}
	return ids, nil
}

func (s *Service) mutateAdminLocalResourceTx(
	ctx context.Context,
	tx *gorm.DB,
	command AdminLocalResourceCommand,
	resourceID uint,
	expectedVersion *uint64,
	requestID string,
	now time.Time,
) (*AdminLocalResourceMutationResult, bool, error) {
	var root resourceRootModel
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND type = ?", resourceID, "gmail").Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, ErrLocalResourceMissing
		}
		return nil, false, err
	}
	if expectedVersion != nil && root.Version != *expectedVersion {
		return nil, false, ErrLocalResourceVersion
	}
	var resource localResourceModel
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&resource, resourceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, ErrLocalResourceMissing
		}
		return nil, false, err
	}

	updates := make(map[string]any)
	switch command {
	case AdminLocalResourceValidate:
		if resource.Status == LocalResourceDeleted {
			return nil, false, ErrLocalResourceMissing
		}
		if resource.Status == LocalResourceDisabled {
			return nil, false, ErrLocalResourceState
		}
		queueAdminLocalResourceValidation(updates, resource, requestID)
	case AdminLocalResourceHistory:
		if resource.Status != LocalResourceNormal && resource.Status != LocalResourceIdentifying &&
			resource.Status != localResourceRollbackNormal && resource.Status != localResourceRollbackLeased && resource.Status != localResourceRollbackSold {
			return nil, false, ErrLocalResourceState
		}
		if resource.CredentialRevision == 0 || resource.ValidationGeneration == 0 || strings.TrimSpace(resource.AppPassword) == "" {
			return nil, false, ErrLocalResourceState
		}
		updates["status"] = LocalResourceIdentifying
		updates["validation_request_id"] = requestID
		updates["last_safe_error"] = ""
	case AdminLocalResourceEnable:
		if resource.Status != LocalResourceDisabled {
			return nil, false, ErrLocalResourceState
		}
		queueAdminLocalResourceValidation(updates, resource, requestID)
	case AdminLocalResourceDisable:
		if resource.Status == LocalResourceDeleted {
			return nil, false, ErrLocalResourceMissing
		}
		if resource.Status == LocalResourceDisabled {
			return adminLocalResourceMutationResult(root, resource), false, nil
		}
		updates["status"] = LocalResourceDisabled
	case AdminLocalResourcePublish:
		if resource.Status == LocalResourceDeleted {
			return nil, false, ErrLocalResourceMissing
		}
		if resource.ForSale {
			return adminLocalResourceMutationResult(root, resource), false, nil
		}
		if err := assertAdminLocalResourceOwnerTx(ctx, tx, root.OwnerUserID, true); err != nil {
			return nil, false, err
		}
		updates["for_sale"] = true
	case AdminLocalResourceUnpublish:
		if resource.Status == LocalResourceDeleted {
			return nil, false, ErrLocalResourceMissing
		}
		if !resource.ForSale {
			return adminLocalResourceMutationResult(root, resource), false, nil
		}
		updates["for_sale"] = false
	case AdminLocalResourceDelete:
		if resource.Status == LocalResourceDeleted {
			return nil, false, ErrLocalResourceState
		}
		if err := assertNoActiveAdminLocalResourceAllocationTx(ctx, tx, resourceID); err != nil {
			return nil, false, err
		}
		updates["status"] = LocalResourceDeleted
		updates["for_sale"] = false
		updates["last_allocated_at"] = nil
		updates["last_safe_error"] = ""
		updates["validation_command_hash"] = ""
	case AdminLocalResourceRecover:
		if resource.Status != LocalResourceDeleted {
			return nil, false, ErrLocalResourceState
		}
		queueAdminLocalResourceValidation(updates, resource, requestID)
		updates["for_sale"] = false
	default:
		return nil, false, ErrInvalidLocalResource
	}

	updates["updated_at"] = now
	if err := tx.WithContext(ctx).Model(&localResourceModel{}).Where("id = ?", resourceID).Updates(updates).Error; err != nil {
		return nil, false, err
	}
	switch command {
	case AdminLocalResourceValidate, AdminLocalResourceEnable, AdminLocalResourceRecover:
		if _, err := ensureGmailMaintenanceRunTx(
			ctx, tx, resourceID, updates["validation_generation"].(uint64), gmailMaintenanceValidation,
			resource.CredentialRevision, 0, now,
		); err != nil {
			return nil, false, err
		}
	case AdminLocalResourceHistory:
		if _, err := ensureGmailMaintenanceRunTx(
			ctx, tx, resourceID, resource.ValidationGeneration, gmailMaintenanceHistory,
			resource.CredentialRevision, 0, now,
		); err != nil {
			return nil, false, err
		}
	case AdminLocalResourceDisable, AdminLocalResourceDelete:
		if err := cancelActiveGmailMaintenanceRunsTx(
			ctx, tx, resourceID, now, "Gmail maintenance was canceled because the resource was disabled or deleted.",
		); err != nil {
			return nil, false, err
		}
	}
	rootUpdate := tx.WithContext(ctx).Model(&resourceRootModel{}).
		Where("id = ? AND type = ? AND version = ?", resourceID, "gmail", root.Version).
		Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now})
	if rootUpdate.Error != nil {
		return nil, false, rootUpdate.Error
	}
	if rootUpdate.RowsAffected != 1 {
		return nil, false, ErrLocalResourceVersion
	}
	for key, value := range updates {
		switch key {
		case "status":
			resource.Status = value.(string)
		case "for_sale":
			resource.ForSale = value.(bool)
		}
	}
	root.Version++
	return adminLocalResourceMutationResult(root, resource), true, nil
}

func queueAdminLocalResourceValidation(updates map[string]any, resource localResourceModel, requestID string) {
	updates["status"] = LocalResourcePending
	updates["validation_generation"] = nextAdminLocalResourceGeneration(resource.ValidationGeneration)
	updates["validation_failures"] = 0
	updates["validation_request_id"] = requestID
	updates["validation_command_hash"] = ""
	updates["last_safe_error"] = ""
	updates["last_checked_at"] = nil
}

func nextAdminLocalResourceGeneration(value uint64) uint64 {
	if value == 0 {
		return 1
	}
	return value + 1
}

func adminLocalResourceMutationResult(root resourceRootModel, resource localResourceModel) *AdminLocalResourceMutationResult {
	status := resource.Status
	if status == localResourceRollbackNormal || status == localResourceRollbackLeased || status == localResourceRollbackSold {
		status = LocalResourceNormal
	}
	return &AdminLocalResourceMutationResult{ResourceID: resource.ID, Version: root.Version, Status: status, ForSale: resource.ForSale}
}

func assertAdminLocalResourceOwnerTx(ctx context.Context, tx *gorm.DB, ownerUserID uint, public bool) error {
	var owner struct {
		Status string `gorm:"column:status"`
		Role   string `gorm:"column:role"`
	}
	if err := tx.WithContext(ctx).Table("users").Select("status, role").Where("id = ?", ownerUserID).Take(&owner).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrLocalResourceOwner
		}
		return err
	}
	if owner.Status != "active" {
		return ErrLocalResourceOwner
	}
	if !public {
		return nil
	}
	switch owner.Role {
	case "supplier", "admin", "super_admin":
		return nil
	default:
		return ErrLocalResourceOwner
	}
}

func assertNoActiveAdminLocalResourceAllocationTx(ctx context.Context, tx *gorm.DB, resourceID uint) error {
	var count int64
	if err := tx.WithContext(ctx).Model(&allocationModel{}).Where("resource_id = ? AND status = ?", resourceID, AllocationStatusAllocated).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrLocalResourceBusy
	}
	return nil
}

func validAdminLocalResourceCommand(command AdminLocalResourceCommand) bool {
	switch command {
	case AdminLocalResourceValidate, AdminLocalResourceHistory, AdminLocalResourceEnable, AdminLocalResourceDisable,
		AdminLocalResourcePublish, AdminLocalResourceUnpublish, AdminLocalResourceDelete, AdminLocalResourceRecover:
		return true
	default:
		return false
	}
}

func validAdminLocalResourceBatchCommand(command AdminLocalResourceCommand) bool {
	switch command {
	case AdminLocalResourceValidate, AdminLocalResourceHistory, AdminLocalResourceDisable,
		AdminLocalResourcePublish, AdminLocalResourceUnpublish, AdminLocalResourceDelete:
		return true
	default:
		return false
	}
}

func (s *Service) scheduleAdminLocalResourceMaintenance(ctx context.Context, command AdminLocalResourceCommand) error {
	switch command {
	case AdminLocalResourceHistory:
		return s.scheduleLocalGmailProjectHistoryDispatcher(ctx, 0)
	case AdminLocalResourceValidate, AdminLocalResourceEnable, AdminLocalResourceRecover:
		return s.scheduleLocalResourceValidationDispatcher(ctx, 0)
	default:
		return nil
	}
}

func uniqueAdminLocalResourceIDs(values []uint) []uint {
	seen := make(map[uint]struct{}, len(values))
	result := make([]uint, 0, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func appendAdminLocalResourceSkip(result *AdminLocalResourceBulkResult, reasons map[string]int64, resourceID uint, reason string) {
	result.SkippedResourceIDs = append(result.SkippedResourceIDs, resourceID)
	reasons[reason]++
}

func adminLocalResourceSkipReason(err error) string {
	switch {
	case errors.Is(err, ErrLocalResourceMissing):
		return "not_found"
	case errors.Is(err, ErrLocalResourceState):
		return "invalid_state"
	case errors.Is(err, ErrLocalResourceOwner):
		return "owner_ineligible"
	case errors.Is(err, ErrLocalResourceBusy):
		return "active_allocation"
	default:
		return ""
	}
}

func adminLocalResourceReasonCounts(values map[string]int64) []AdminLocalResourceReasonCount {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]AdminLocalResourceReasonCount, 0, len(keys))
	for _, key := range keys {
		result = append(result, AdminLocalResourceReasonCount{Reason: key, Count: values[key]})
	}
	return result
}

func adminLocalResourceFingerprint(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (s *Service) reserveAdminLocalResourceCommand(ctx context.Context, tx *gorm.DB, receipt coreapp.AdminResourceCommandReceipt, target any) (bool, error) {
	repo := coreinfra.NewAdminResourceRepo(s.db)
	resultJSON, replayed, err := repo.ReserveAdminCommand(platform.WithGormTx(ctx, tx), receipt)
	if err != nil || !replayed {
		return replayed, err
	}
	if err := json.Unmarshal(resultJSON, target); err != nil {
		return false, ErrLocalValidationDependency
	}
	return true, nil
}

func (s *Service) completeAdminLocalResourceCommand(ctx context.Context, tx *gorm.DB, operatorUserID uint, idempotencyKey string, result any) error {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return coreinfra.NewAdminResourceRepo(s.db).CompleteAdminCommand(platform.WithGormTx(ctx, tx), operatorUserID, idempotencyKey, resultJSON)
}

func normalizeAdminLocalResourceCommandError(err error) error {
	switch {
	case errors.Is(err, coredomain.ErrResourceIdempotencyConflict):
		return ErrLocalValidationConflict
	case errors.Is(err, coredomain.ErrInvalidResourceCommand):
		return ErrInvalidLocalResource
	case errors.Is(err, coredomain.ErrResourceDependency):
		return ErrLocalValidationDependency
	case errors.Is(err, ErrInvalidLocalResource), errors.Is(err, ErrLocalResourceSelection),
		errors.Is(err, ErrLocalResourceMissing), errors.Is(err, ErrLocalResourceState),
		errors.Is(err, ErrLocalResourceVersion), errors.Is(err, ErrLocalResourceOwner),
		errors.Is(err, ErrLocalResourceBusy), errors.Is(err, ErrLocalValidationConflict):
		return err
	default:
		return ErrLocalValidationDependency
	}
}
