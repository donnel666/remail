package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	coredomain "github.com/donnel666/remail/internal/core/domain"
	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	iaminfra "github.com/donnel666/remail/internal/iam/infra"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	errRecoveryResourceNotFound       = errors.New("microsoft resource was not found")
	errRecoveryResourceChanged        = errors.New("microsoft resource changed while recovery was running")
	errRecoveryOperatorUnauthorized   = errors.New("operator must be an enabled administrator")
	errRecoveryValidationActive       = errors.New("resource validation is queued or running")
	errRecoveryAliasActivityActive    = errors.New("alias activity must be paused before password reset")
	errRecoveryPasswordReconciliation = errors.New("remote password changed but local credentials require reconciliation")
)

type recoverySnapshot struct {
	ResourceID         uint
	OwnerUserID        uint
	ResourceVersion    uint64
	AccountEmail       string
	Password           string
	CredentialRevision uint64
	Status             coredomain.MicrosoftResourceStatus
	Binding            *maildomain.MicrosoftBindingMailbox
}

func (s recoverySnapshot) preferredVerifiedBinding() string {
	if s.Binding == nil || s.Binding.Status != maildomain.MicrosoftBindingVerified {
		return ""
	}
	return normalizeConcreteRecoveryBinding(s.Binding.BindingAddress)
}

func (s recoverySnapshot) recoveredBindingInput(address string) mailinfra.MicrosoftRecoveredBindingInput {
	input := mailinfra.MicrosoftRecoveredBindingInput{
		ResourceID:           s.ResourceID,
		BindingAddress:       address,
		ExpectedOwnerUserID:  s.OwnerUserID,
		ExpectedAccountEmail: s.AccountEmail,
	}
	if s.Binding != nil {
		input.ExpectedBindingID = s.Binding.ID
		input.ExpectedBindingAddress = s.Binding.BindingAddress
		input.ExpectedBindingUpdatedAt = s.Binding.UpdatedAt
	}
	return input
}

type passwordCommitResult struct {
	CredentialRevision uint64
	ValidationJobID    uint
	ValidationCreated  bool
}

type recoveryStore struct {
	db          *gorm.DB
	resources   *coreinfra.ResourceRepo
	admin       *coreinfra.AdminResourceRepo
	validations *coreinfra.ResourceValidationRepo
	bindings    *mailinfra.MicrosoftBindingRepo
	aliases     *mailinfra.MicrosoftAliasStore
	users       *iaminfra.UserRepo
	logs        *governanceinfra.OperationLogRepo
}

func newRecoveryStore(db *gorm.DB) *recoveryStore {
	return &recoveryStore{
		db:          db,
		resources:   coreinfra.NewResourceRepo(db),
		admin:       coreinfra.NewAdminResourceRepo(db),
		validations: coreinfra.NewResourceValidationRepo(db),
		bindings:    mailinfra.NewMicrosoftBindingRepo(db),
		aliases:     mailinfra.NewMicrosoftAliasStore(db),
		users:       iaminfra.NewUserRepo(db),
		logs:        governanceinfra.NewOperationLogRepo(db),
	}
}

func (s *recoveryStore) loadSnapshot(ctx context.Context, resourceID uint, email string) (*recoverySnapshot, error) {
	if s == nil || s.db == nil {
		return nil, errRecoveryResourceNotFound
	}
	var resource *coredomain.MicrosoftResource
	var err error
	if resourceID != 0 {
		resource, err = s.resources.FindMicrosoftByID(ctx, resourceID)
	} else {
		resource, err = s.resources.FindMicrosoftByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	}
	if err != nil {
		return nil, err
	}
	if resource == nil || resource.Status == coredomain.MicrosoftStatusDeleted {
		return nil, errRecoveryResourceNotFound
	}
	root, err := s.resources.FindByID(ctx, resource.ID)
	if err != nil {
		return nil, err
	}
	if root == nil || root.Type != coredomain.ResourceTypeMicrosoft {
		return nil, errRecoveryResourceNotFound
	}
	bindings, err := s.bindings.FindByResourceIDs(ctx, []uint{resource.ID})
	if err != nil {
		return nil, err
	}
	var binding *maildomain.MicrosoftBindingMailbox
	if value, ok := bindings[resource.ID]; ok {
		copyValue := value
		binding = &copyValue
	}
	return &recoverySnapshot{
		ResourceID:         resource.ID,
		OwnerUserID:        root.OwnerUserID,
		ResourceVersion:    root.Version,
		AccountEmail:       strings.ToLower(strings.TrimSpace(resource.EmailAddress)),
		Password:           resource.Password,
		CredentialRevision: resource.CredentialRevision,
		Status:             resource.Status,
		Binding:            binding,
	}, nil
}

func (s *recoveryStore) validateOperator(ctx context.Context, operatorUserID uint) error {
	if operatorUserID == 0 {
		return errRecoveryOperatorUnauthorized
	}
	operator, err := s.users.FindByID(ctx, operatorUserID)
	if err != nil {
		return err
	}
	if operator == nil || !operator.Enabled || !operator.Role.HasAdminAccess() {
		return errRecoveryOperatorUnauthorized
	}
	return nil
}

func (s *recoveryStore) preflightBindingApply(ctx context.Context, snapshot recoverySnapshot, operatorUserID uint) error {
	return s.admin.WithTx(ctx, func(txCtx context.Context) error {
		if err := s.validateOperator(txCtx, operatorUserID); err != nil {
			return err
		}
		root, resource, err := s.admin.LockAdminMicrosoft(txCtx, snapshot.ResourceID)
		if err != nil {
			return err
		}
		if root.OwnerUserID != snapshot.OwnerUserID || !sameNormalRecoveryAccount(resource, snapshot.AccountEmail) {
			return errRecoveryResourceChanged
		}
		return ensureNoActiveValidation(txCtx, snapshot.ResourceID)
	})
}

func (s *recoveryStore) applyRecoveredBinding(
	ctx context.Context,
	snapshot recoverySnapshot,
	bindingAddress string,
	operatorUserID uint,
	requestID string,
	requireNormalResource bool,
) (*mailinfra.MicrosoftRecoveredBindingResult, error) {
	var applied *mailinfra.MicrosoftRecoveredBindingResult
	err := s.admin.WithTx(ctx, func(txCtx context.Context) error {
		if err := s.validateOperator(txCtx, operatorUserID); err != nil {
			return err
		}
		root, resource, err := s.admin.LockAdminMicrosoft(txCtx, snapshot.ResourceID)
		if err != nil {
			return err
		}
		if root.OwnerUserID != snapshot.OwnerUserID ||
			!sameRecoveryAccount(resource, snapshot.AccountEmail) ||
			(requireNormalResource && resource.Status != coredomain.MicrosoftStatusNormal) {
			return errRecoveryResourceChanged
		}
		if err := ensureNoActiveValidation(txCtx, snapshot.ResourceID); err != nil {
			return err
		}
		applied, err = s.bindings.ApplyRecoveredBinding(txCtx, snapshot.recoveredBindingInput(bindingAddress))
		if err != nil {
			return err
		}
		// A dispatch prefilter may already have paused this resource while its
		// binding was unresolved. Wake (or create) the durable alias schedule in
		// the same transaction as the recovered binding so the server's periodic
		// dispatcher can pick it up immediately after commit.
		if _, err := s.aliases.EnsureScheduleForResource(txCtx, snapshot.ResourceID, time.Now().UTC()); err != nil {
			return err
		}
		summary := "Microsoft recovery-mailbox binding was already verified."
		if applied.Changed {
			summary = "Microsoft recovery-mailbox binding was recovered and verified."
		}
		return s.logs.Create(txCtx, &governancedomain.OperationLog{
			OperatorUserID: operatorUserID,
			OperationType:  "mailtransport.microsoft_binding.recover",
			ResourceType:   "microsoft_resource",
			ResourceID:     strconv.FormatUint(uint64(snapshot.ResourceID), 10),
			Path:           "cmd/msrecovery",
			Result:         "success",
			SafeSummary:    summary,
			RequestID:      requestID,
		})
	})
	if err != nil {
		return nil, err
	}
	return applied, nil
}

func (s *recoveryStore) preflightPasswordReset(ctx context.Context, snapshot recoverySnapshot, operatorUserID uint) error {
	return s.admin.WithTx(ctx, func(txCtx context.Context) error {
		if err := s.validateOperator(txCtx, operatorUserID); err != nil {
			return err
		}
		root, resource, err := s.admin.LockAdminMicrosoft(txCtx, snapshot.ResourceID)
		if err != nil {
			return err
		}
		if root.OwnerUserID != snapshot.OwnerUserID ||
			!sameRecoveryAccount(resource, snapshot.AccountEmail) ||
			resource.CredentialRevision != snapshot.CredentialRevision ||
			!samePrivatePassword(resource.Password, snapshot.Password) {
			return errRecoveryResourceChanged
		}
		if err := ensureNoActiveValidation(txCtx, snapshot.ResourceID); err != nil {
			return err
		}
		return ensureAliasActivityPaused(txCtx, snapshot.ResourceID)
	})
}

func (s *recoveryStore) commitPasswordReset(
	ctx context.Context,
	snapshot recoverySnapshot,
	newPassword string,
	operatorUserID uint,
	requestID string,
) (*passwordCommitResult, error) {
	var committed passwordCommitResult
	err := s.admin.WithTx(ctx, func(txCtx context.Context) error {
		if err := s.validateOperator(txCtx, operatorUserID); err != nil {
			return fmt.Errorf("%w: %v", errRecoveryPasswordReconciliation, err)
		}
		root, resource, err := s.admin.LockAdminMicrosoft(txCtx, snapshot.ResourceID)
		if err != nil {
			return fmt.Errorf("%w: %v", errRecoveryPasswordReconciliation, err)
		}
		if root.OwnerUserID != snapshot.OwnerUserID ||
			!sameRecoveryAccount(resource, snapshot.AccountEmail) ||
			!samePrivatePassword(resource.Password, snapshot.Password) {
			return errRecoveryPasswordReconciliation
		}
		if resource.CredentialRevision != snapshot.CredentialRevision {
			return errRecoveryPasswordReconciliation
		}
		if err := ensureAliasActivityPaused(txCtx, snapshot.ResourceID); err != nil {
			return fmt.Errorf("%w: %v", errRecoveryPasswordReconciliation, err)
		}
		expectedVersion := root.Version
		now := time.Now().UTC()
		if err := resource.ReplaceCredentialsAdmin(newPassword, "", "", now); err != nil {
			return fmt.Errorf("%w: %v", errRecoveryPasswordReconciliation, err)
		}
		if err := s.admin.SaveAdminMicrosoft(txCtx, root, resource, expectedVersion); err != nil {
			return fmt.Errorf("%w: %v", errRecoveryPasswordReconciliation, err)
		}

		job := &coredomain.ResourceValidation{
			ResourceID:                 snapshot.ResourceID,
			ResourceType:               coredomain.ResourceTypeMicrosoft,
			OwnerUserID:                root.OwnerUserID,
			ExpectedCredentialRevision: resource.CredentialRevision,
			Status:                     coredomain.ResourceValidationQueued,
			MaxAttempts:                coredomain.ResourceValidationDefaultMaxAttempts,
			RequestID:                  requestID,
			Path:                       "cmd/msrecovery",
		}
		created, err := s.validations.CreateWithLog(txCtx, job, nil)
		if err != nil {
			return fmt.Errorf("%w: %v", errRecoveryPasswordReconciliation, err)
		}
		if err := s.logs.Create(txCtx, &governancedomain.OperationLog{
			OperatorUserID: operatorUserID,
			OperationType:  "core.microsoft_password.reset",
			ResourceType:   "microsoft_resource",
			ResourceID:     strconv.FormatUint(uint64(snapshot.ResourceID), 10),
			Path:           "cmd/msrecovery",
			Result:         "success",
			SafeSummary:    "Microsoft password was reset and local credentials were replaced.",
			RequestID:      requestID,
		}); err != nil {
			return fmt.Errorf("%w: %v", errRecoveryPasswordReconciliation, err)
		}
		committed = passwordCommitResult{
			CredentialRevision: resource.CredentialRevision,
			ValidationJobID:    job.ID,
			ValidationCreated:  created,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &committed, nil
}

func sameRecoveryAccount(resource *coredomain.MicrosoftResource, expected string) bool {
	return resource != nil &&
		resource.Status != coredomain.MicrosoftStatusDeleted &&
		resource.Status != coredomain.MicrosoftStatusDisabled &&
		strings.EqualFold(strings.TrimSpace(resource.EmailAddress), strings.TrimSpace(expected))
}

func sameNormalRecoveryAccount(resource *coredomain.MicrosoftResource, expected string) bool {
	return sameRecoveryAccount(resource, expected) && resource.Status == coredomain.MicrosoftStatusNormal
}

func samePrivatePassword(current, expected string) bool {
	if len(current) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(current), []byte(expected)) == 1
}

func ensureNoActiveValidation(ctx context.Context, resourceID uint) error {
	tx, ok := platform.GormTxFromContext(ctx)
	if !ok {
		return errors.New("active validation check requires a transaction")
	}
	var job coreinfra.ResourceValidationModel
	err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("resource_id = ? AND status IN ?", resourceID, []string{
			string(coredomain.ResourceValidationQueued),
			string(coredomain.ResourceValidationRunning),
		}).
		Order("id DESC").
		Take(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check active resource validation: %w", err)
	}
	return fmt.Errorf("%w: job_id=%d status=%s", errRecoveryValidationActive, job.ID, job.Status)
}

func ensureAliasActivityPaused(ctx context.Context, resourceID uint) error {
	tx, ok := platform.GormTxFromContext(ctx)
	if !ok {
		return errors.New("alias activity check requires a transaction")
	}
	var schedule struct {
		Status string `gorm:"column:status"`
	}
	err := tx.WithContext(ctx).
		Table("microsoft_alias_schedules").
		Select("status").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("resource_id = ?", resourceID).
		Take(&schedule).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("check microsoft alias schedule: %w", err)
	}
	if err == nil && schedule.Status != "paused" {
		return fmt.Errorf("%w: schedule_status=%s", errRecoveryAliasActivityActive, schedule.Status)
	}

	var runningAttempt struct {
		ID uint `gorm:"column:id"`
	}
	err = tx.WithContext(ctx).
		Table("microsoft_alias_attempts").
		Select("id").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("resource_id = ? AND status = ?", resourceID, "running").
		Order("id DESC").
		Take(&runningAttempt).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check microsoft alias attempts: %w", err)
	}
	return fmt.Errorf("%w: running_attempt_id=%d", errRecoveryAliasActivityActive, runningAttempt.ID)
}
