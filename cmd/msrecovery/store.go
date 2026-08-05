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
	errReauthorizeResourceIneligible  = errors.New("resource must be normal, private, and unallocated")
	errReauthorizeAliasActivity       = errors.New("alias activity is currently running")
)

type recoverySnapshot struct {
	ResourceID         uint
	OwnerUserID        uint
	ResourceVersion    uint64
	AccountEmail       string
	Password           string
	ClientID           string
	RefreshToken       string
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
}

type reauthorizeCommitResult struct {
	CredentialRevision uint64
}

type recoveryStore struct {
	db        *gorm.DB
	resources *coreinfra.ResourceRepo
	admin     *coreinfra.AdminResourceRepo
	bindings  *mailinfra.MicrosoftBindingRepo
	aliases   *mailinfra.MicrosoftAliasStore
	users     *iaminfra.UserRepo
	logs      *governanceinfra.OperationLogRepo
}

func newRecoveryStore(db *gorm.DB) *recoveryStore {
	return &recoveryStore{
		db:        db,
		resources: coreinfra.NewResourceRepo(db),
		admin:     coreinfra.NewAdminResourceRepo(db),
		bindings:  mailinfra.NewMicrosoftBindingRepo(db),
		aliases:   mailinfra.NewMicrosoftAliasStore(db),
		users:     iaminfra.NewUserRepo(db),
		logs:      governanceinfra.NewOperationLogRepo(db),
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
		ClientID:           resource.ClientID,
		RefreshToken:       resource.RefreshToken,
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
	if operator == nil || !operator.IsActive() || !operator.Role.HasAdminAccess() {
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
		return ensureNoActiveValidation(resource)
	})
}

func (s *recoveryStore) preflightReauthorize(ctx context.Context, snapshot recoverySnapshot, operatorUserID uint, requireOperator bool) error {
	return s.admin.WithTx(ctx, func(txCtx context.Context) error {
		if requireOperator {
			if err := s.validateOperator(txCtx, operatorUserID); err != nil {
				return err
			}
		}
		root, resource, err := s.admin.LockAdminMicrosoft(txCtx, snapshot.ResourceID)
		if err != nil {
			return err
		}
		if !sameReauthorizeSnapshot(root, resource, snapshot) {
			return errRecoveryResourceChanged
		}
		if resource.Status != coredomain.MicrosoftStatusNormal || resource.ForSale {
			return errReauthorizeResourceIneligible
		}
		if err := ensureNoActiveMicrosoftAllocation(txCtx, snapshot.ResourceID); err != nil {
			return err
		}
		return ensureNoRunningAliasActivity(txCtx, snapshot.ResourceID)
	})
}

func (s *recoveryStore) commitReauthorization(
	ctx context.Context,
	snapshot recoverySnapshot,
	clientID, refreshToken string,
	graphAvailable, oldRTChecked, oldRTRejected, aliasesComplete bool,
	aliases []string,
	operatorUserID uint,
	requestID, branch string,
) (*reauthorizeCommitResult, error) {
	clientID = strings.TrimSpace(clientID)
	refreshToken = strings.TrimSpace(refreshToken)
	if clientID == "" || refreshToken == "" {
		return nil, errReauthorizeResourceIneligible
	}
	var committed reauthorizeCommitResult
	err := s.admin.WithTx(ctx, func(txCtx context.Context) error {
		if err := s.validateOperator(txCtx, operatorUserID); err != nil {
			return err
		}
		root, resource, err := s.admin.LockAdminMicrosoft(txCtx, snapshot.ResourceID)
		if err != nil {
			return err
		}
		if !sameReauthorizeSnapshot(root, resource, snapshot) {
			return errRecoveryResourceChanged
		}
		if resource.Status != coredomain.MicrosoftStatusNormal || resource.ForSale {
			return errReauthorizeResourceIneligible
		}
		if err := ensureNoActiveMicrosoftAllocation(txCtx, snapshot.ResourceID); err != nil {
			return err
		}
		if err := ensureNoRunningAliasActivity(txCtx, snapshot.ResourceID); err != nil {
			return err
		}

		now := time.Now().UTC()
		credentialsChanged := clientID != strings.TrimSpace(resource.ClientID) || refreshToken != strings.TrimSpace(resource.RefreshToken)
		resource.ClientID = clientID
		resource.RefreshToken = refreshToken
		resource.GraphAvailable = graphAvailable
		resource.LastSafeError = ""
		if !graphAvailable {
			resource.LastSafeError = "Microsoft Graph verification did not complete."
		}
		resource.TokenLastRefreshedAt = &now
		resource.TokenLastRequestID = strings.TrimSpace(requestID)
		if credentialsChanged {
			resource.CredentialRevision++
			resource.CredentialUpdatedAt = now
		}
		if err := s.admin.SaveAdminMicrosoft(txCtx, root, resource, root.Version); err != nil {
			return err
		}
		committed.CredentialRevision = resource.CredentialRevision
		return nil
	})
	if err != nil {
		return nil, err
	}

	aliasErr := s.aliases.BackfillExistingAliases(ctx, snapshot.ResourceID, aliases)
	aliasesStored := aliasErr == nil
	auditResult, summary := reauthorizationAudit(
		snapshot,
		branch,
		graphAvailable,
		oldRTChecked,
		oldRTRejected,
		aliasesComplete,
		aliasesStored,
	)
	logErr := s.logs.Create(ctx, &governancedomain.OperationLog{
		OperatorUserID: operatorUserID,
		OperationType:  "mailtransport.microsoft.hard_reauthorize",
		ResourceType:   "microsoft_resource",
		ResourceID:     strconv.FormatUint(uint64(snapshot.ResourceID), 10),
		Path:           "cmd/msrecovery",
		Result:         auditResult,
		SafeSummary:    summary,
		RequestID:      strings.TrimSpace(requestID),
	})
	if aliasErr != nil {
		aliasErr = errors.New("confirmed Microsoft aliases could not be recorded locally")
	}
	if logErr != nil {
		logErr = fmt.Errorf("record Microsoft reauthorization audit: %w", logErr)
	}
	return &committed, errors.Join(aliasErr, logErr)
}

func reauthorizationAudit(
	snapshot recoverySnapshot,
	branch string,
	graphAvailable, oldRTChecked, oldRTRejected, aliasesComplete, aliasesStored bool,
) (string, string) {
	complete := graphAvailable
	summary := "Microsoft OAuth credentials were refreshed through the external-binding downgrade path."
	if branch == reauthorizeBranchHard {
		summary = "Microsoft account grants were removed and fresh OAuth credentials were stored."
		oldRTRequired := strings.TrimSpace(snapshot.ClientID) != "" || strings.TrimSpace(snapshot.RefreshToken) != ""
		if oldRTRequired {
			if oldRTChecked && oldRTRejected {
				summary += " Previous refresh-token rejection was verified."
			} else {
				complete = false
				summary += " Previous refresh-token rejection was not verified."
			}
		}
		if !aliasesComplete {
			complete = false
			summary += " Explicit alias creation was incomplete."
		}
		if !aliasesStored {
			complete = false
			summary += " Confirmed aliases were not fully recorded locally."
		}
	} else if branch != reauthorizeBranchExternal {
		complete = false
		summary = "Microsoft OAuth credentials were stored through an unknown reauthorization path."
	}
	if graphAvailable {
		summary += " Graph access was verified."
	} else {
		summary += " Graph access was not verified."
	}
	if !complete {
		return "failure", summary
	}
	return "success", summary
}

func sameReauthorizeSnapshot(root *coredomain.EmailResource, resource *coredomain.MicrosoftResource, snapshot recoverySnapshot) bool {
	return root != nil && resource != nil &&
		root.OwnerUserID == snapshot.OwnerUserID &&
		root.Version == snapshot.ResourceVersion &&
		strings.EqualFold(strings.TrimSpace(resource.EmailAddress), snapshot.AccountEmail) &&
		resource.CredentialRevision == snapshot.CredentialRevision &&
		samePrivateValue(resource.Password, snapshot.Password) &&
		samePrivateValue(resource.ClientID, snapshot.ClientID) &&
		samePrivateValue(resource.RefreshToken, snapshot.RefreshToken)
}

func ensureNoActiveMicrosoftAllocation(ctx context.Context, resourceID uint) error {
	tx, ok := platform.GormTxFromContext(ctx)
	if !ok {
		return errors.New("allocation check requires a transaction")
	}
	var count int64
	if err := tx.WithContext(ctx).Table("microsoft_allocations").
		Where("resource_id = ? AND status = ?", resourceID, "allocated").
		Count(&count).Error; err != nil {
		return fmt.Errorf("check active microsoft allocations: %w", err)
	}
	if count != 0 {
		return errReauthorizeResourceIneligible
	}
	return nil
}

func ensureNoRunningAliasActivity(ctx context.Context, resourceID uint) error {
	tx, ok := platform.GormTxFromContext(ctx)
	if !ok {
		return errors.New("alias activity check requires a transaction")
	}
	var count int64
	if err := tx.WithContext(ctx).Table("microsoft_alias_attempts").
		Where("resource_id = ? AND status = ?", resourceID, "running").
		Count(&count).Error; err != nil {
		return fmt.Errorf("check running microsoft alias attempts: %w", err)
	}
	if count != 0 {
		return errReauthorizeAliasActivity
	}
	count = 0
	if err := tx.WithContext(ctx).Table("microsoft_alias_schedules").
		Where("resource_id = ? AND status IN ?", resourceID, []string{"queued", "running"}).
		Count(&count).Error; err != nil {
		return fmt.Errorf("check running microsoft alias schedule: %w", err)
	}
	if count != 0 {
		return errReauthorizeAliasActivity
	}
	return nil
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
		if err := ensureNoActiveValidation(resource); err != nil {
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
		if err := ensureNoActiveValidation(resource); err != nil {
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
		committed = passwordCommitResult{CredentialRevision: resource.CredentialRevision}
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
	return samePrivateValue(current, expected)
}

func samePrivateValue(current, expected string) bool {
	if len(current) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(current), []byte(expected)) == 1
}

func ensureNoActiveValidation(resource *coredomain.MicrosoftResource) error {
	if resource != nil && resource.Status == coredomain.MicrosoftStatusValidating {
		return errRecoveryValidationActive
	}
	return nil
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
