package infra

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type adminCommandOwnerPort struct {
	owners map[uint]coreapp.AdminOwnerSummary
}

func (p adminCommandOwnerPort) GetByIDs(_ context.Context, ids []uint) (map[uint]coreapp.AdminOwnerSummary, error) {
	result := make(map[uint]coreapp.AdminOwnerSummary, len(ids))
	for _, id := range ids {
		if owner, ok := p.owners[id]; ok {
			result[id] = owner
		}
	}
	return result, nil
}

func (p adminCommandOwnerPort) SearchAdminOwners(_ context.Context, search string, limit int) ([]coreapp.AdminOwnerSummary, error) {
	search = strings.ToLower(strings.TrimSpace(search))
	result := make([]coreapp.AdminOwnerSummary, 0)
	for _, owner := range p.owners {
		if strconv.FormatUint(uint64(owner.ID), 10) != search &&
			!strings.Contains(strings.ToLower(owner.Email), search) &&
			!strings.Contains(strings.ToLower(owner.Nickname), search) {
			continue
		}
		result = append(result, owner)
		if limit > 0 && len(result) == limit {
			break
		}
	}
	return result, nil
}

func (p adminCommandOwnerPort) ValidateTargetOwner(_ context.Context, id uint) (*coreapp.AdminOwnerSummary, error) {
	owner, ok := p.owners[id]
	if !ok {
		return nil, nil
	}
	return &owner, nil
}

type adminCommandBindingPort struct {
	commands []coreapp.AdminBindingCommand
	err      error
}

type adminCommandBindingQueryPort map[uint]coreapp.AdminBindingSummary

func (p adminCommandBindingQueryPort) GetByResourceIDs(ctx context.Context, ids []uint) (map[uint]coreapp.AdminBindingSummary, error) {
	if _, ok := platform.GormTxFromContext(ctx); !ok {
		return nil, errors.New("binding query is not in the core transaction")
	}
	result := make(map[uint]coreapp.AdminBindingSummary, len(ids))
	for _, id := range ids {
		if binding, ok := p[id]; ok {
			result[id] = binding
		}
	}
	return result, nil
}

func (adminCommandBindingQueryPort) CountActiveByDomains(context.Context, []string) (map[string]int64, error) {
	return map[string]int64{}, nil
}

func (p *adminCommandBindingPort) ReplaceAdminInput(ctx context.Context, command coreapp.AdminBindingCommand) error {
	_, ok := platform.GormTxFromContext(ctx)
	if !ok {
		return errors.New("binding command is not in the core transaction")
	}
	p.commands = append(p.commands, command)
	return p.err
}

type panickingAdminCommandBindingPort struct{}

func (panickingAdminCommandBindingPort) ReplaceAdminInput(ctx context.Context, _ coreapp.AdminBindingCommand) error {
	if _, ok := platform.GormTxFromContext(ctx); !ok {
		panic("binding command is not in the core transaction")
	}
	panic("forced binding panic")
}

type adminCommandAllocationGuard struct {
	called bool
	err    error
}

func (p *adminCommandAllocationGuard) AssertNoActiveAllocations(ctx context.Context, _ []uint) error {
	if _, ok := platform.GormTxFromContext(ctx); !ok {
		return errors.New("allocation guard is not in the core transaction")
	}
	p.called = true
	return p.err
}

type adminCommandValidationQueue struct{}

func (adminCommandValidationQueue) EnqueueResourceValidation(context.Context, coreapp.ResourceValidationTask) (bool, error) {
	return true, nil
}

func (adminCommandValidationQueue) EnqueueResourceValidationBatch(context.Context, coreapp.ResourceValidationBatchTask) error {
	return nil
}

func (adminCommandValidationQueue) EnqueueResourceValidationDispatcher(context.Context, time.Duration) error {
	return nil
}

type failingAdminCommandOperationLogPort struct {
	err error
}

func (p failingAdminCommandOperationLogPort) Create(context.Context, *governancedomain.OperationLog) error {
	return p.err
}

func TestAdminResourceCommandEditCommitsAggregateBindingPendingStateAndLogMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	adminRepo := NewAdminResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	validation := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, adminCommandValidationQueue{}, nil)
	service := coreapp.NewAdminResourceCommandService(adminRepo, validation, governanceinfra.NewOperationLogRepo(db))
	owners := adminCommandOwners()
	binding := &adminCommandBindingPort{}
	guard := &adminCommandAllocationGuard{}
	service.SetPorts(owners, adminCommandBindingQueryPort{}, binding, guard)
	insertAdminCommandUsers(t, db)

	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &domain.MicrosoftResource{
		EmailAddress: "before-edit@outlook.com",
		Password:     "before-password",
		ClientID:     "before-client",
		RefreshToken: "before-refresh",
		Status:       domain.MicrosoftStatusNormal,
		ForSale:      false,
		QualityScore: 88,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, resource))
	require.EqualValues(t, 1, root.Version)

	email := "after-edit@outlook.com"
	bindingAddress := "auxiliary@example.net"
	ownerID := uint(2)
	quality := 73
	forSale := true
	longLived := true
	result, err := service.Edit(context.Background(), coreapp.AdminMicrosoftEditCommand{
		ResourceID:        root.ID,
		Version:           root.Version,
		EmailAddress:      &email,
		BindingAddressSet: true,
		BindingAddress:    &bindingAddress,
		OwnerUserID:       &ownerID,
		QualityScore:      &quality,
		ForSale:           &forSale,
		LongLived:         &longLived,
		Credentials: &coreapp.AdminMicrosoftCredentials{
			Password: "after-password", ClientID: "after-client", RefreshToken: "after-refresh",
		},
		OperatorUserID: 9,
		IdempotencyKey: "admin-edit-atomic",
		RequestID:      "req-admin-edit-atomic",
		Path:           "/v1/admin/resources/:resourceId",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, guard.called)
	require.Len(t, binding.commands, 1)
	require.True(t, binding.commands[0].BindingAddressSet)
	require.Equal(t, "auxiliary@example.net", *binding.commands[0].BindingAddress)
	require.Equal(t, uint(2), binding.commands[0].OwnerUserID)
	require.Equal(t, "after-edit@outlook.com", binding.commands[0].AccountEmail)

	var storedRoot EmailResourceModel
	require.NoError(t, db.First(&storedRoot, root.ID).Error)
	require.Equal(t, uint(2), storedRoot.OwnerUserID)
	require.EqualValues(t, 2, storedRoot.Version)

	var stored MicrosoftResourceModel
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, "after-edit@outlook.com", stored.EmailAddress)
	require.Equal(t, "outlook.com", stored.EmailDomain)
	require.Equal(t, "after-password", stored.Password)
	require.Equal(t, "after-client", stored.ClientID)
	require.Equal(t, "after-refresh", stored.RefreshToken)
	require.EqualValues(t, 2, stored.CredentialRevision)
	require.Equal(t, string(domain.MicrosoftStatusPending), stored.Status)
	require.True(t, stored.ForSale)
	require.True(t, stored.LongLived)
	require.False(t, stored.GraphAvailable)
	require.Equal(t, 0, stored.QualityScore, "identity invalidation clears the previously derived score")

	var log struct {
		OperationType string
		SafeSummary   string
	}
	require.NoError(t, db.Raw(
		"SELECT operation_type, safe_summary FROM operation_logs WHERE request_id = ?",
		"req-admin-edit-atomic",
	).Scan(&log).Error)
	require.Equal(t, "core.admin_resource.edit", log.OperationType)
	require.Contains(t, log.SafeSummary, "credentials")
	require.NotContains(t, log.SafeSummary, "after-password")
	require.NotContains(t, log.SafeSummary, "after-refresh")
}

func TestAdminResourceCommandEditPreservesUnchangedVerifiedBindingMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	adminRepo := NewAdminResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	validation := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, adminCommandValidationQueue{}, nil)
	service := coreapp.NewAdminResourceCommandService(adminRepo, validation, governanceinfra.NewOperationLogRepo(db))
	insertAdminCommandUsers(t, db)

	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &domain.MicrosoftResource{
		EmailAddress: "same-binding-edit@outlook.com", Password: "password", Status: domain.MicrosoftStatusNormal,
		QualityScore: 50,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, resource))
	bindingWriter := &adminCommandBindingPort{}
	service.SetPorts(
		adminCommandOwners(),
		adminCommandBindingQueryPort{root.ID: {
			ResourceID: root.ID, EmailAddress: "verified-aux@example.net", Status: "verified",
		}},
		bindingWriter,
		&adminCommandAllocationGuard{},
	)

	bindingAddress := " VERIFIED-AUX@EXAMPLE.NET "
	quality := 82
	longLived := true
	result, err := service.Edit(context.Background(), coreapp.AdminMicrosoftEditCommand{
		ResourceID: root.ID, Version: root.Version,
		BindingAddressSet: true, BindingAddress: &bindingAddress,
		QualityScore: &quality, LongLived: &longLived,
		OperatorUserID: 9, IdempotencyKey: "admin-edit-same-binding",
		RequestID: "req-admin-edit-same-binding", Path: "/v1/admin/resources/:resourceId",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Empty(t, bindingWriter.commands, "an unchanged verified binding must not be reset to pending")

	var storedRoot EmailResourceModel
	require.NoError(t, db.First(&storedRoot, root.ID).Error)
	require.EqualValues(t, 2, storedRoot.Version)
	var stored MicrosoftResourceModel
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, string(domain.MicrosoftStatusNormal), stored.Status)
	require.Equal(t, 82, stored.QualityScore)
	require.True(t, stored.LongLived)
	require.EqualValues(t, 1, stored.CredentialRevision)

	var log struct{ SafeSummary string }
	require.NoError(t, db.Raw(
		"SELECT safe_summary FROM operation_logs WHERE request_id = ?",
		"req-admin-edit-same-binding",
	).Scan(&log).Error)
	require.Contains(t, log.SafeSummary, "qualityScore")
	require.Contains(t, log.SafeSummary, "longLived")
	require.NotContains(t, log.SafeSummary, "bindingAddress")
}

func TestAdminResourceCommandEditNoOpCompletesWithoutAggregateOrBindingChangesMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	adminRepo := NewAdminResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	validation := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, adminCommandValidationQueue{}, nil)
	service := coreapp.NewAdminResourceCommandService(adminRepo, validation, governanceinfra.NewOperationLogRepo(db))
	insertAdminCommandUsers(t, db)

	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &domain.MicrosoftResource{
		EmailAddress: "noop-edit@outlook.com", Password: "password", Status: domain.MicrosoftStatusNormal,
		QualityScore: 50,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, resource))

	bindingWriter := &adminCommandBindingPort{}
	guard := &adminCommandAllocationGuard{}
	service.SetPorts(
		adminCommandOwners(),
		adminCommandBindingQueryPort{root.ID: {
			ResourceID: root.ID, EmailAddress: "verified-aux@example.net", Status: "verified",
		}},
		bindingWriter,
		guard,
	)

	var rootBefore EmailResourceModel
	require.NoError(t, db.First(&rootBefore, root.ID).Error)
	var resourceBefore MicrosoftResourceModel
	require.NoError(t, db.First(&resourceBefore, root.ID).Error)

	email := " NOOP-EDIT@OUTLOOK.COM "
	ownerID := uint(1)
	quality := 50
	forSale := false
	longLived := false
	unchangedCommand := coreapp.AdminMicrosoftEditCommand{
		ResourceID: root.ID, Version: root.Version,
		EmailAddress: &email, OwnerUserID: &ownerID, QualityScore: &quality,
		ForSale: &forSale, LongLived: &longLived,
		OperatorUserID: 9, IdempotencyKey: "admin-edit-noop-fields",
		RequestID: "req-admin-edit-noop-fields", Path: "/v1/admin/resources/:resourceId",
	}
	result, err := service.Edit(context.Background(), unchangedCommand)
	require.NoError(t, err)
	require.NotNil(t, result)

	// The completed receipt makes an exact retry a pure replay.
	replayed, err := service.Edit(context.Background(), unchangedCommand)
	require.NoError(t, err)
	require.NotNil(t, replayed)

	bindingAddress := " VERIFIED-AUX@EXAMPLE.NET "
	result, err = service.Edit(context.Background(), coreapp.AdminMicrosoftEditCommand{
		ResourceID: root.ID, Version: root.Version,
		BindingAddressSet: true, BindingAddress: &bindingAddress,
		OperatorUserID: 9, IdempotencyKey: "admin-edit-noop-binding",
		RequestID: "req-admin-edit-noop-binding", Path: "/v1/admin/resources/:resourceId",
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	var rootAfter EmailResourceModel
	require.NoError(t, db.First(&rootAfter, root.ID).Error)
	require.Equal(t, rootBefore.Version, rootAfter.Version)
	require.Equal(t, rootBefore.UpdatedAt, rootAfter.UpdatedAt)
	var resourceAfter MicrosoftResourceModel
	require.NoError(t, db.First(&resourceAfter, root.ID).Error)
	require.Equal(t, resourceBefore.UpdatedAt, resourceAfter.UpdatedAt)
	require.Equal(t, resourceBefore.EmailAddress, resourceAfter.EmailAddress)
	require.Equal(t, resourceBefore.QualityScore, resourceAfter.QualityScore)
	require.Empty(t, bindingWriter.commands)
	require.False(t, guard.called)

	var logs []struct {
		RequestID   string
		SafeSummary string
	}
	require.NoError(t, db.Raw(`
SELECT request_id, safe_summary
FROM operation_logs
WHERE request_id IN (?, ?)
ORDER BY request_id`, "req-admin-edit-noop-fields", "req-admin-edit-noop-binding").Scan(&logs).Error)
	require.Len(t, logs, 2, "an idempotent replay must not duplicate the operation log")
	for _, log := range logs {
		require.Equal(t, "No resource fields changed.", log.SafeSummary)
	}

	var receipts []AdminResourceCommandReceiptModel
	require.NoError(t, db.Where("operator_user_id = ? AND idempotency_key IN (?, ?)", 9, "admin-edit-noop-fields", "admin-edit-noop-binding").Find(&receipts).Error)
	require.Len(t, receipts, 2)
	for _, receipt := range receipts {
		require.Equal(t, "succeeded", receipt.Status)
		require.JSONEq(t, `{}`, string(receipt.ResultJSON))
	}
}

func TestAdminResourceCommandEditRollsBackWhenBindingFailsMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	adminRepo := NewAdminResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	validation := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, adminCommandValidationQueue{}, nil)
	service := coreapp.NewAdminResourceCommandService(adminRepo, validation, governanceinfra.NewOperationLogRepo(db))
	bindingFailure := errors.New("binding write failed")
	binding := &adminCommandBindingPort{err: bindingFailure}
	service.SetPorts(adminCommandOwners(), adminCommandBindingQueryPort{}, binding, &adminCommandAllocationGuard{})
	insertAdminCommandUsers(t, db)

	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &domain.MicrosoftResource{EmailAddress: "rollback-edit@outlook.com", Password: "before-password", Status: domain.MicrosoftStatusNormal, QualityScore: 91}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, resource))
	bindingAddress := "rollback-aux@example.net"
	quality := 12

	_, err := service.Edit(context.Background(), coreapp.AdminMicrosoftEditCommand{
		ResourceID: root.ID, Version: root.Version, BindingAddressSet: true, BindingAddress: &bindingAddress,
		QualityScore: &quality, OperatorUserID: 9, IdempotencyKey: "admin-edit-rollback", RequestID: "req-admin-edit-rollback", Path: "/v1/admin/resources/:resourceId",
	})
	require.ErrorIs(t, err, bindingFailure)

	var storedRoot EmailResourceModel
	require.NoError(t, db.First(&storedRoot, root.ID).Error)
	require.EqualValues(t, 1, storedRoot.Version)
	var stored MicrosoftResourceModel
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, 91, stored.QualityScore)
	var logs, receipts int64
	require.NoError(t, db.Table("operation_logs").Where("request_id = ?", "req-admin-edit-rollback").Count(&logs).Error)
	require.NoError(t, db.Model(&AdminResourceCommandReceiptModel{}).Where("operator_user_id = ? AND idempotency_key = ?", 9, "admin-edit-rollback").Count(&receipts).Error)
	require.Zero(t, logs)
	require.Zero(t, receipts)
}

func TestAdminResourceEditRollsBackAggregateAndReceiptAfterPanicMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	adminRepo := NewAdminResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	validation := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, adminCommandValidationQueue{}, nil)
	service := coreapp.NewAdminResourceCommandService(adminRepo, validation, governanceinfra.NewOperationLogRepo(db))
	service.SetPorts(adminCommandOwners(), adminCommandBindingQueryPort{}, panickingAdminCommandBindingPort{}, &adminCommandAllocationGuard{})
	insertAdminCommandUsers(t, db)

	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &domain.MicrosoftResource{
		EmailAddress: "panic-rollback@outlook.com",
		Password:     "before-password",
		Status:       domain.MicrosoftStatusNormal,
		QualityScore: 91,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, resource))
	bindingAddress := "panic-aux@example.net"
	quality := 12

	require.PanicsWithValue(t, "forced binding panic", func() {
		_, _ = service.Edit(context.Background(), coreapp.AdminMicrosoftEditCommand{
			ResourceID:        root.ID,
			Version:           root.Version,
			BindingAddressSet: true,
			BindingAddress:    &bindingAddress,
			QualityScore:      &quality,
			OperatorUserID:    9,
			IdempotencyKey:    "admin-edit-panic-rollback",
			RequestID:         "req-admin-edit-panic-rollback",
			Path:              "/v1/admin/resources/:resourceId",
		})
	})

	var storedRoot EmailResourceModel
	require.NoError(t, db.First(&storedRoot, root.ID).Error)
	require.EqualValues(t, 1, storedRoot.Version)
	var stored MicrosoftResourceModel
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, 91, stored.QualityScore)

	var logs, receipts int64
	require.NoError(t, db.Table("operation_logs").
		Where("request_id = ?", "req-admin-edit-panic-rollback").
		Count(&logs).Error)
	require.NoError(t, db.Model(&AdminResourceCommandReceiptModel{}).
		Where("operator_user_id = ? AND idempotency_key = ?", 9, "admin-edit-panic-rollback").
		Count(&receipts).Error)
	require.Zero(t, logs)
	require.Zero(t, receipts)
}

func TestAdminResourceEnableRollsBackStateValidationReceiptWhenOperationLogFailsMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	adminRepo := NewAdminResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	validation := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, adminCommandValidationQueue{}, nil)
	logFailure := errors.New("operation log write failed")
	service := coreapp.NewAdminResourceCommandService(
		adminRepo,
		validation,
		failingAdminCommandOperationLogPort{err: logFailure},
	)
	service.SetPorts(adminCommandOwners(), adminCommandBindingQueryPort{}, &adminCommandBindingPort{}, &adminCommandAllocationGuard{})
	insertAdminCommandUsers(t, db)

	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &domain.MicrosoftResource{
		EmailAddress: "enable-log-rollback@outlook.com",
		Password:     "before-password",
		Status:       domain.MicrosoftStatusDisabled,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, resource))

	result, err := service.Enable(
		context.Background(),
		root.ID,
		root.Version,
		9,
		"enable-log-rollback-key",
		"req-enable-log-rollback",
		"/v1/admin/resources/:resourceId/enable",
	)
	require.ErrorIs(t, err, logFailure)
	require.Nil(t, result)

	var storedRoot EmailResourceModel
	require.NoError(t, db.First(&storedRoot, root.ID).Error)
	require.EqualValues(t, 1, storedRoot.Version)
	var stored MicrosoftResourceModel
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, string(domain.MicrosoftStatusDisabled), stored.Status)

	var receipts, logs int64
	require.NoError(t, db.Model(&AdminResourceCommandReceiptModel{}).
		Where("operator_user_id = ? AND idempotency_key = ?", 9, "enable-log-rollback-key").
		Count(&receipts).Error)
	require.NoError(t, db.Table("operation_logs").
		Where("request_id = ?", "req-enable-log-rollback").
		Count(&logs).Error)
	require.Zero(t, receipts)
	require.Zero(t, logs)
}

func TestAdminResourceDeleteUsesAllocationGuardAndVersionMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	adminRepo := NewAdminResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	validation := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, adminCommandValidationQueue{}, nil)
	service := coreapp.NewAdminResourceCommandService(adminRepo, validation, governanceinfra.NewOperationLogRepo(db))
	guard := &adminCommandAllocationGuard{err: domain.ErrResourceHasAllocation}
	service.SetPorts(adminCommandOwners(), adminCommandBindingQueryPort{}, &adminCommandBindingPort{}, guard)
	insertAdminCommandUsers(t, db)

	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &domain.MicrosoftResource{EmailAddress: "guard-delete@outlook.com", Password: "password", Status: domain.MicrosoftStatusNormal, ForSale: true}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, resource))

	err := service.ApplyState(context.Background(), coreapp.AdminMicrosoftDelete, root.ID, root.Version, 9, "delete-guarded", "req-delete-guarded", "/v1/admin/resources/:resourceId")
	require.ErrorIs(t, err, domain.ErrResourceHasAllocation)
	var stored MicrosoftResourceModel
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, string(domain.MicrosoftStatusNormal), stored.Status)
	require.True(t, stored.ForSale)

	guard.err = nil
	require.NoError(t, service.ApplyState(context.Background(), coreapp.AdminMicrosoftDelete, root.ID, root.Version, 9, "delete-success", "req-delete-success", "/v1/admin/resources/:resourceId"))
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, string(domain.MicrosoftStatusDeleted), stored.Status)
	require.False(t, stored.ForSale)
	var storedRoot EmailResourceModel
	require.NoError(t, db.First(&storedRoot, root.ID).Error)
	require.EqualValues(t, 2, storedRoot.Version)

	err = service.ApplyState(context.Background(), coreapp.AdminMicrosoftUnpublish, root.ID, root.Version, 9, "unpublish-stale", "req-stale-version", "/v1/admin/resources/:resourceId/unpublish")
	require.ErrorIs(t, err, domain.ErrResourceVersionConflict)
}

func TestAdminResourceCredentialCommandIdempotencyReplaysWithoutSecretsOrSideEffectsMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	adminRepo := NewAdminResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	validation := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, adminCommandValidationQueue{}, nil)
	service := coreapp.NewAdminResourceCommandService(adminRepo, validation, governanceinfra.NewOperationLogRepo(db))
	service.SetPorts(adminCommandOwners(), adminCommandBindingQueryPort{}, &adminCommandBindingPort{}, &adminCommandAllocationGuard{})
	insertAdminCommandUsers(t, db)

	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &domain.MicrosoftResource{
		EmailAddress: "idempotent-credentials@outlook.com", Password: "before-password",
		ClientID: "before-client", RefreshToken: "before-refresh", Status: domain.MicrosoftStatusNormal,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, resource))
	command := coreapp.AdminMicrosoftEditCommand{
		ResourceID: root.ID, Version: root.Version,
		Credentials: &coreapp.AdminMicrosoftCredentials{
			Password: "receipt-secret-password", ClientID: "receipt-secret-client", RefreshToken: "receipt-secret-refresh",
		},
		OperatorUserID: 9, IdempotencyKey: "credentials-replay-key",
		RequestID: "req-credentials-first", Path: "/v1/admin/resources/:resourceId/credentials",
	}
	first, err := service.ReplaceCredentials(context.Background(), command)
	require.NoError(t, err)
	require.NotNil(t, first)

	command.RequestID = "req-credentials-replay"
	replayed, err := service.ReplaceCredentials(context.Background(), command)
	require.NoError(t, err)
	require.NotNil(t, replayed)

	var rootAfter EmailResourceModel
	require.NoError(t, db.First(&rootAfter, root.ID).Error)
	require.EqualValues(t, 2, rootAfter.Version)
	var logs, replayLogs int64
	require.NoError(t, db.Table("operation_logs").Where("operation_type = ? AND resource_id = ?", "core.admin_resource.credentials.replace", root.ID).Count(&logs).Error)
	require.NoError(t, db.Table("operation_logs").Where("request_id = ?", "req-credentials-replay").Count(&replayLogs).Error)
	require.Equal(t, int64(1), logs)
	require.Zero(t, replayLogs)
	var stored MicrosoftResourceModel
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, string(domain.MicrosoftStatusPending), stored.Status)
	require.EqualValues(t, 2, stored.CredentialRevision)

	var receipt AdminResourceCommandReceiptModel
	require.NoError(t, db.Where("operator_user_id = ? AND idempotency_key = ?", 9, command.IdempotencyKey).First(&receipt).Error)
	require.Equal(t, "succeeded", receipt.Status)
	require.Equal(t, "core.admin_resource.credentials.replace", receipt.Operation)
	require.Equal(t, adminResourceSubjectForTest(root.ID), receipt.Subject)
	require.Len(t, receipt.RequestFingerprint, 64)
	storedResult := string(receipt.ResultJSON)
	for _, secret := range []string{"receipt-secret-password", "receipt-secret-client", "receipt-secret-refresh", "before-password", "before-refresh"} {
		require.NotContains(t, storedResult, secret)
		require.NotContains(t, receipt.RequestFingerprint, secret)
	}

	conflict := command
	conflict.Credentials = &coreapp.AdminMicrosoftCredentials{
		Password: "different-password", ClientID: "receipt-secret-client", RefreshToken: "receipt-secret-refresh",
	}
	_, err = service.ReplaceCredentials(context.Background(), conflict)
	require.ErrorIs(t, err, domain.ErrResourceIdempotencyConflict)
	err = service.ApplyState(
		context.Background(), coreapp.AdminMicrosoftDisable, root.ID, rootAfter.Version, 9,
		command.IdempotencyKey, "req-operation-conflict", "/v1/admin/resources/:resourceId/disable",
	)
	require.ErrorIs(t, err, domain.ErrResourceIdempotencyConflict)
}

func TestAdminResourceStateAndBatchIdempotencyReplayStableResultsMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	adminRepo := NewAdminResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	validation := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, adminCommandValidationQueue{}, nil)
	service := coreapp.NewAdminResourceCommandService(adminRepo, validation, governanceinfra.NewOperationLogRepo(db))
	service.SetPorts(adminCommandOwners(), adminCommandBindingQueryPort{}, &adminCommandBindingPort{}, &adminCommandAllocationGuard{})
	insertAdminCommandUsers(t, db)

	firstRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	firstResource := &domain.MicrosoftResource{EmailAddress: "idempotent-state@outlook.com", Password: "password", Status: domain.MicrosoftStatusNormal}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), firstRoot, firstResource))
	require.NoError(t, service.ApplyState(
		context.Background(), coreapp.AdminMicrosoftDisable, firstRoot.ID, firstRoot.Version, 9,
		"state-replay-key", "req-state-first", "/v1/admin/resources/:resourceId/disable",
	))
	require.NoError(t, service.ApplyState(
		context.Background(), coreapp.AdminMicrosoftDisable, firstRoot.ID, firstRoot.Version, 9,
		"state-replay-key", "req-state-replay", "/v1/admin/resources/:resourceId/disable",
	))
	var firstStored EmailResourceModel
	require.NoError(t, db.First(&firstStored, firstRoot.ID).Error)
	require.EqualValues(t, 2, firstStored.Version)
	var stateLogs int64
	require.NoError(t, db.Table("operation_logs").Where("operation_type = ? AND resource_id = ?", "core.admin_resource.disable", firstRoot.ID).Count(&stateLogs).Error)
	require.Equal(t, int64(1), stateLogs)

	secondRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	secondResource := &domain.MicrosoftResource{EmailAddress: "idempotent-batch-two@outlook.com", Password: "password", Status: domain.MicrosoftStatusNormal}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), secondRoot, secondResource))
	thirdRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	thirdResource := &domain.MicrosoftResource{EmailAddress: "idempotent-batch-three@outlook.com", Password: "password", Status: domain.MicrosoftStatusNormal}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), thirdRoot, thirdResource))

	ids := []uint{thirdRoot.ID, secondRoot.ID, thirdRoot.ID}
	firstBatch, err := service.ApplyStateBatch(
		context.Background(), coreapp.AdminMicrosoftPublish, ids, 9,
		"batch-replay-key", "req-batch-first", "/v1/admin/resources/publish",
	)
	require.NoError(t, err)
	require.Equal(t, 2, firstBatch.Requested)
	require.Equal(t, 2, firstBatch.Affected)
	replayedBatch, err := service.ApplyStateBatch(
		context.Background(), coreapp.AdminMicrosoftPublish, []uint{secondRoot.ID, thirdRoot.ID}, 9,
		"batch-replay-key", "req-batch-replay", "/v1/admin/resources/publish",
	)
	require.NoError(t, err)
	require.Equal(t, firstBatch, replayedBatch)

	var batchLogs int64
	require.NoError(t, db.Table("operation_logs").Where("operation_type = ?", "core.admin_resource.publish_batch").Count(&batchLogs).Error)
	require.Equal(t, int64(1), batchLogs)
	var secondStored, thirdStored EmailResourceModel
	require.NoError(t, db.First(&secondStored, secondRoot.ID).Error)
	require.NoError(t, db.First(&thirdStored, thirdRoot.ID).Error)
	require.EqualValues(t, 2, secondStored.Version)
	require.EqualValues(t, 2, thirdStored.Version)

	_, err = service.ApplyStateBatch(
		context.Background(), coreapp.AdminMicrosoftPublish, []uint{secondRoot.ID}, 9,
		"batch-replay-key", "req-batch-conflict", "/v1/admin/resources/publish",
	)
	require.ErrorIs(t, err, domain.ErrResourceIdempotencyConflict)
}

func TestAdminResourceValidateIdempotencyKeepsPendingStateAndSingleLogMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	adminRepo := NewAdminResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	validation := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, adminCommandValidationQueue{}, nil)
	service := coreapp.NewAdminResourceCommandService(adminRepo, validation, governanceinfra.NewOperationLogRepo(db))
	service.SetPorts(adminCommandOwners(), adminCommandBindingQueryPort{}, &adminCommandBindingPort{}, &adminCommandAllocationGuard{})
	insertAdminCommandUsers(t, db)

	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &domain.MicrosoftResource{EmailAddress: "idempotent-validate@outlook.com", Password: "password", Status: domain.MicrosoftStatusNormal}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, resource))

	first, err := service.Validate(
		context.Background(), root.ID, 9, "validate-replay-key", "req-validate-first", "/v1/admin/resources/:resourceId/validate",
	)
	require.NoError(t, err)
	require.Equal(t, 1, first.Accepted)
	replayed, err := service.Validate(
		context.Background(), root.ID, 9, "validate-replay-key", "req-validate-replay", "/v1/admin/resources/:resourceId/validate",
	)
	require.NoError(t, err)
	require.Equal(t, first, replayed)

	var logs int64
	require.NoError(t, db.Table("operation_logs").Where("operation_type = ? AND resource_id = ?", "core.admin_resource.validate", root.ID).Count(&logs).Error)
	require.Equal(t, int64(1), logs)
	var stored MicrosoftResourceModel
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, string(domain.MicrosoftStatusPending), stored.Status)
}

func TestAdminResourceEnableAndRecoverIdempotencyKeepPendingStateMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	adminRepo := NewAdminResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	validation := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, adminCommandValidationQueue{}, nil)
	service := coreapp.NewAdminResourceCommandService(adminRepo, validation, governanceinfra.NewOperationLogRepo(db))
	service.SetPorts(adminCommandOwners(), adminCommandBindingQueryPort{}, &adminCommandBindingPort{}, &adminCommandAllocationGuard{})
	insertAdminCommandUsers(t, db)

	disabledRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	disabledResource := &domain.MicrosoftResource{EmailAddress: "idempotent-enable@outlook.com", Password: "password", Status: domain.MicrosoftStatusDisabled}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), disabledRoot, disabledResource))
	firstEnable, err := service.Enable(
		context.Background(), disabledRoot.ID, disabledRoot.Version, 9,
		"enable-replay-key", "req-enable-first", "/v1/admin/resources/:resourceId/enable",
	)
	require.NoError(t, err)
	replayedEnable, err := service.Enable(
		context.Background(), disabledRoot.ID, disabledRoot.Version, 9,
		"enable-replay-key", "req-enable-replay", "/v1/admin/resources/:resourceId/enable",
	)
	require.NoError(t, err)
	require.NotNil(t, firstEnable)
	require.NotNil(t, replayedEnable)
	var enabled MicrosoftResourceModel
	require.NoError(t, db.First(&enabled, disabledRoot.ID).Error)
	require.Equal(t, string(domain.MicrosoftStatusPending), enabled.Status)

	deletedRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	deletedResource := &domain.MicrosoftResource{EmailAddress: "idempotent-recover@outlook.com", Password: "password", Status: domain.MicrosoftStatusDeleted, ForSale: false}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), deletedRoot, deletedResource))
	firstRecover, err := service.Recover(
		context.Background(), deletedRoot.ID, deletedRoot.Version, 9,
		"recover-replay-key", "req-recover-first", "/v1/admin/resources/:resourceId/recover",
	)
	require.NoError(t, err)
	replayedRecover, err := service.Recover(
		context.Background(), deletedRoot.ID, deletedRoot.Version, 9,
		"recover-replay-key", "req-recover-replay", "/v1/admin/resources/:resourceId/recover",
	)
	require.NoError(t, err)
	require.NotNil(t, firstRecover)
	require.NotNil(t, replayedRecover)
	var recovered MicrosoftResourceModel
	require.NoError(t, db.First(&recovered, deletedRoot.ID).Error)
	require.Equal(t, string(domain.MicrosoftStatusPending), recovered.Status)

	var enableLogs, recoverLogs int64
	require.NoError(t, db.Table("operation_logs").Where("operation_type = ? AND resource_id = ?", "core.admin_resource.enable", disabledRoot.ID).Count(&enableLogs).Error)
	require.NoError(t, db.Table("operation_logs").Where("operation_type = ? AND resource_id = ?", "core.admin_resource.recover", deletedRoot.ID).Count(&recoverLogs).Error)
	require.Equal(t, int64(1), enableLogs)
	require.Equal(t, int64(1), recoverLogs)
}

func TestAdminResourceStateIdempotencySerializesConcurrentReplayMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	adminRepo := NewAdminResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	validation := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, adminCommandValidationQueue{}, nil)
	service := coreapp.NewAdminResourceCommandService(adminRepo, validation, governanceinfra.NewOperationLogRepo(db))
	service.SetPorts(adminCommandOwners(), adminCommandBindingQueryPort{}, &adminCommandBindingPort{}, &adminCommandAllocationGuard{})
	insertAdminCommandUsers(t, db)

	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &domain.MicrosoftResource{EmailAddress: "concurrent-idempotency@outlook.com", Password: "password", Status: domain.MicrosoftStatusNormal}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), root, resource))

	start := make(chan struct{})
	const workerCount = 10
	errorsByWorker := make([]error, workerCount)
	var workers sync.WaitGroup
	for index := range errorsByWorker {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			<-start
			errorsByWorker[worker] = service.ApplyState(
				context.Background(), coreapp.AdminMicrosoftDisable, root.ID, root.Version, 9,
				"concurrent-state-key", "req-concurrent-"+strconv.Itoa(worker), "/v1/admin/resources/:resourceId/disable",
			)
		}(index)
	}
	close(start)
	workers.Wait()
	for _, err := range errorsByWorker {
		require.NoError(t, err)
	}

	var storedRoot EmailResourceModel
	require.NoError(t, db.First(&storedRoot, root.ID).Error)
	require.EqualValues(t, 2, storedRoot.Version)
	var logs, receipts int64
	require.NoError(t, db.Table("operation_logs").Where("operation_type = ? AND resource_id = ?", "core.admin_resource.disable", root.ID).Count(&logs).Error)
	require.NoError(t, db.Model(&AdminResourceCommandReceiptModel{}).Where("operator_user_id = ? AND idempotency_key = ?", 9, "concurrent-state-key").Count(&receipts).Error)
	require.Equal(t, int64(1), logs)
	require.Equal(t, int64(1), receipts)
}

func adminResourceSubjectForTest(resourceID uint) string {
	return "microsoft_resource:" + strconv.FormatUint(uint64(resourceID), 10)
}

func adminCommandOwners() adminCommandOwnerPort {
	return adminCommandOwnerPort{owners: map[uint]coreapp.AdminOwnerSummary{
		1: {ID: 1, Email: "owner-one@test.local", Role: "supplier", Enabled: true},
		2: {ID: 2, Email: "owner-two@test.local", Role: "supplier", Enabled: true},
	}}
}

func insertAdminCommandUsers(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, role, enabled)
VALUES
    (1, 'owner-one@test.local', 'hash', 'supplier', TRUE),
    (2, 'owner-two@test.local', 'hash', 'supplier', TRUE),
    (9, 'operator@test.local', 'hash', 'admin', TRUE)`).Error)
}
