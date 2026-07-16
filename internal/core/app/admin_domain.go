package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/core/domain"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
)

type AdminDomainListFilter struct {
	Search       string
	Status       domain.MailDomainStatus
	Purpose      domain.ResourcePurpose
	TLD          string
	OwnerID      uint
	MailServerID uint
	CreatedFrom  *time.Time
	CreatedTo    *time.Time
	OwnerIDs     []uint
}

type AdminDomainRecord struct {
	ID              uint
	OwnerUserID     uint
	Version         uint64
	Domain          string
	DomainTLD       string
	MailServerID    uint
	Purpose         domain.ResourcePurpose
	Status          domain.MailDomainStatus
	MailboxCount    int64
	LastSafeError   string
	LastAllocatedAt *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type AdminDomainStatusFacets struct {
	All        int64
	Pending    int64
	Validating int64
	Normal     int64
	Abnormal   int64
	Disabled   int64
	Deleted    int64
}

type AdminDomainPurposeFacets struct {
	All     int64
	NotSale int64
	Sale    int64
	Binding int64
}

type AdminDomainFacets struct {
	Status  AdminDomainStatusFacets
	Purpose AdminDomainPurposeFacets
	TLDs    []AdminKeyFacet
}

type AdminDomainItem struct {
	ID              uint
	Version         uint64
	Domain          string
	DomainTLD       string
	Owner           AdminOwnerSummary
	Purpose         string
	Status          string
	MailServerID    uint
	MailboxCount    int64
	LastSafeError   *string
	LastAllocatedAt *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type AdminDomainListResult struct {
	Items       []AdminDomainItem
	Total       int64
	Offset      int
	Limit       int
	NextAfterID *uint
	Facets      AdminDomainFacets
}

type AdminDomainReadRepository interface {
	ListAdminDomains(ctx context.Context, filter AdminDomainListFilter, offset, limit int, afterID uint) ([]AdminDomainRecord, int64, error)
	AdminDomainFacets(ctx context.Context, filter AdminDomainListFilter) (*AdminDomainFacets, error)
	FindAdminDomain(ctx context.Context, resourceID uint) (*AdminDomainRecord, error)
}

type AdminDomainQuery struct {
	repo     AdminDomainReadRepository
	owners   OwnerQueryPort
	bindings BindingQueryPort
}

func NewAdminDomainQuery(repo AdminDomainReadRepository) *AdminDomainQuery {
	return &AdminDomainQuery{repo: repo}
}

func (q *AdminDomainQuery) SetPorts(owners OwnerQueryPort, bindings BindingQueryPort) {
	if q != nil {
		q.owners = owners
		q.bindings = bindings
	}
}

func (q *AdminDomainQuery) List(ctx context.Context, filter AdminDomainListFilter, offset, limit int, afterID uint) (*AdminDomainListResult, error) {
	if q == nil || q.repo == nil || q.owners == nil || q.bindings == nil {
		return nil, domain.ErrResourceDependency
	}
	filter, err := normalizeAdminDomainFilter(ctx, q.owners, filter)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = AdminResourceDefaultLimit
	}
	if limit > AdminResourceMaxLimit || offset < 0 {
		return nil, domain.ErrInvalidResourceFilter
	}
	records, total, err := q.repo.ListAdminDomains(ctx, filter, offset, limit, afterID)
	if err != nil {
		return nil, err
	}
	facets, err := q.repo.AdminDomainFacets(ctx, filter)
	if err != nil {
		return nil, err
	}
	nextAfterID := adminDomainNextAfterID(records, limit)
	if len(records) > limit {
		records = records[:limit]
	}
	items, err := q.enrich(ctx, records)
	if err != nil {
		return nil, err
	}
	return &AdminDomainListResult{
		Items:       items,
		Total:       total,
		Offset:      offset,
		Limit:       limit,
		NextAfterID: nextAfterID,
		Facets:      *facets,
	}, nil
}

func (q *AdminDomainQuery) Get(ctx context.Context, resourceID uint) (*AdminDomainItem, error) {
	if q == nil || q.repo == nil || q.owners == nil || q.bindings == nil || resourceID == 0 {
		return nil, domain.ErrResourceNotFound
	}
	record, err := q.repo.FindAdminDomain(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, domain.ErrResourceNotFound
	}
	items, err := q.enrich(ctx, []AdminDomainRecord{*record})
	if err != nil {
		return nil, err
	}
	return &items[0], nil
}

func (q *AdminDomainQuery) enrich(ctx context.Context, records []AdminDomainRecord) ([]AdminDomainItem, error) {
	ownerIDs := make([]uint, 0, len(records))
	bindingDomains := make([]string, 0, len(records))
	for i := range records {
		ownerIDs = append(ownerIDs, records[i].OwnerUserID)
		if records[i].Purpose == domain.PurposeBinding {
			bindingDomains = append(bindingDomains, records[i].Domain)
		}
	}
	owners, err := q.owners.GetByIDs(ctx, uniqueAdminResourceIDs(ownerIDs))
	if err != nil {
		return nil, fmt.Errorf("load admin domain owners: %w", err)
	}
	bindingCounts, err := q.bindings.CountActiveByDomains(ctx, bindingDomains)
	if err != nil {
		return nil, fmt.Errorf("load admin binding domain usage: %w", err)
	}
	items := make([]AdminDomainItem, len(records))
	for i := range records {
		owner, ok := owners[records[i].OwnerUserID]
		if !ok {
			return nil, fmt.Errorf("%w: owner summary missing", domain.ErrResourceDependency)
		}
		if records[i].Purpose == domain.PurposeBinding {
			records[i].MailboxCount = bindingCounts[strings.ToLower(records[i].Domain)]
		}
		items[i] = adminDomainItem(records[i], owner)
	}
	return items, nil
}

func normalizeAdminDomainFilter(ctx context.Context, owners OwnerQueryPort, filter AdminDomainListFilter) (AdminDomainListFilter, error) {
	filter.Search = strings.TrimSpace(filter.Search)
	if filter.Status != "" && !domain.IsValidDomainStatus(string(filter.Status)) {
		return AdminDomainListFilter{}, domain.ErrInvalidResourceStatus
	}
	if filter.Purpose != "" && !domain.IsValidPurpose(filter.Purpose) {
		return AdminDomainListFilter{}, domain.ErrInvalidPurpose
	}
	if filter.TLD != "" {
		tld, err := domain.NormalizeDomainSuffix(filter.TLD)
		if err != nil {
			return AdminDomainListFilter{}, domain.ErrInvalidResourceFilter
		}
		filter.TLD = tld
	}
	if filter.CreatedFrom != nil && filter.CreatedTo != nil && !filter.CreatedFrom.Before(*filter.CreatedTo) {
		return AdminDomainListFilter{}, domain.ErrInvalidResourceFilter
	}
	filter.OwnerIDs = nil
	if filter.Search != "" {
		matched, err := owners.SearchAdminOwners(ctx, filter.Search, 1000)
		if err != nil {
			return AdminDomainListFilter{}, fmt.Errorf("search admin domain owners: %w", err)
		}
		for i := range matched {
			if matched[i].ID > 0 {
				filter.OwnerIDs = append(filter.OwnerIDs, matched[i].ID)
			}
		}
		filter.OwnerIDs = uniqueAdminResourceIDs(filter.OwnerIDs)
	}
	return filter, nil
}

func adminDomainItem(record AdminDomainRecord, owner AdminOwnerSummary) AdminDomainItem {
	var safeError *string
	if value := strings.TrimSpace(record.LastSafeError); value != "" {
		safeError = &value
	}
	return AdminDomainItem{
		ID: record.ID, Version: record.Version, Domain: record.Domain, DomainTLD: record.DomainTLD,
		Owner: owner, Purpose: string(record.Purpose), Status: string(record.Status), MailServerID: record.MailServerID,
		MailboxCount: record.MailboxCount, LastSafeError: safeError, LastAllocatedAt: record.LastAllocatedAt,
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}

func adminDomainNextAfterID(records []AdminDomainRecord, limit int) *uint {
	if len(records) <= limit {
		return nil
	}
	id := records[limit-1].ID
	return &id
}

type AdminDomainStatusCommand string

const (
	AdminDomainMarkNormal   AdminDomainStatusCommand = "mark_normal"
	AdminDomainMarkAbnormal AdminDomainStatusCommand = "mark_abnormal"
	AdminDomainEnable       AdminDomainStatusCommand = "enable"
	AdminDomainDisable      AdminDomainStatusCommand = "disable"
)

type AdminDomainAction string

const (
	AdminDomainPublish   AdminDomainAction = "publish"
	AdminDomainUnpublish AdminDomainAction = "unpublish"
	AdminDomainDelete    AdminDomainAction = "delete"
	AdminDomainRecover   AdminDomainAction = "recover"
)

type AdminDomainCreateCommand struct {
	Domain         string
	OwnerUserID    uint
	Purpose        domain.ResourcePurpose
	MailServerID   uint
	OperatorUserID uint
	IdempotencyKey string
	RequestID      string
	Path           string
}

type AdminDomainEditCommand struct {
	ResourceID     uint
	Version        uint64
	OwnerUserID    *uint
	Purpose        *domain.ResourcePurpose
	MailServerID   *uint
	StatusCommand  AdminDomainStatusCommand
	Action         AdminDomainAction
	OperatorUserID uint
	IdempotencyKey string
	RequestID      string
	Path           string
}

type AdminDomainMutationResult struct {
	ResourceID uint `json:"resourceId"`
}

type AdminDomainBulkSelectionMode string

const (
	AdminDomainBulkIDs            AdminDomainBulkSelectionMode = "ids"
	AdminDomainBulkFilter         AdminDomainBulkSelectionMode = "filter"
	AdminDomainBulkMaxExplicitIDs                              = 1000
	// ponytail: domain batches stay synchronous up to this bounded dashboard ceiling; move filter batches to the existing durable worker pattern if measured volume exceeds it.
	AdminDomainBulkMaxFilter = 10_000
)

type AdminDomainBulkSelection struct {
	Mode        AdminDomainBulkSelectionMode `json:"mode"`
	ResourceIDs []uint                       `json:"resourceIds,omitempty"`
	Filter      AdminDomainListFilter        `json:"filter,omitempty"`
}

type AdminDomainBulkResult struct {
	Requested int `json:"requested"`
	Affected  int `json:"affected"`
	Skipped   int `json:"skipped"`
}

type AdminDomainCommandRepository interface {
	WithTx(ctx context.Context, fn func(context.Context) error) error
	ReserveAdminCommand(ctx context.Context, receipt AdminResourceCommandReceipt) ([]byte, bool, error)
	CompleteAdminCommand(ctx context.Context, operatorUserID uint, idempotencyKey string, resultJSON []byte) error
	LockAdminDomain(ctx context.Context, resourceID uint) (*domain.EmailResource, *domain.MailDomainResource, error)
	LockAdminDomainMailServer(ctx context.Context, mailServerID uint) (*domain.MailServer, error)
	CreateAdminDomain(ctx context.Context, root *domain.EmailResource, resource *domain.MailDomainResource) error
	SaveAdminDomain(ctx context.Context, root *domain.EmailResource, resource *domain.MailDomainResource, expectedVersion uint64, previousOwnerID uint) error
	ListAdminDomainIDs(ctx context.Context, filter AdminDomainListFilter, limit int) ([]uint, error)
}

type AdminDomainCommandService struct {
	repo        AdminDomainCommandRepository
	owners      OwnerQueryPort
	servers     MailServerRepository
	allocations ResourceAllocationGuardPort
	validation  *ResourceValidationUseCase
	logs        governanceapp.OperationLogPort
}

func NewAdminDomainCommandService(repo AdminDomainCommandRepository, servers MailServerRepository, validation *ResourceValidationUseCase, logs governanceapp.OperationLogPort) *AdminDomainCommandService {
	return &AdminDomainCommandService{repo: repo, servers: servers, validation: validation, logs: logs}
}

func (s *AdminDomainCommandService) SetPorts(owners OwnerQueryPort, allocations ResourceAllocationGuardPort) {
	if s == nil {
		return
	}
	s.owners = owners
	s.allocations = allocations
}

func (s *AdminDomainCommandService) Create(ctx context.Context, command AdminDomainCreateCommand) (*AdminDomainMutationResult, error) {
	if s == nil || s.repo == nil || s.logs == nil || s.owners == nil || s.servers == nil || command.OperatorUserID == 0 || command.OwnerUserID == 0 {
		return nil, domain.ErrInvalidResourceCommand
	}
	key, err := normalizeAdminResourceIdempotencyKey(command.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	domainName, err := domain.NormalizeDomainName(command.Domain)
	if err != nil {
		return nil, err
	}
	purpose := command.Purpose
	if purpose == "" {
		purpose = domain.PurposeNotSale
	}
	owner, err := s.validateOwner(ctx, command.OwnerUserID, purpose)
	if err != nil {
		return nil, err
	}
	mailServerID := command.MailServerID
	if mailServerID == 0 {
		server, err := s.servers.GetOrCreateDefaultInbound(ctx, owner.ID, defaultInboundServerName, defaultInboundMXRecord, defaultInboundMXRecord)
		if err != nil {
			return nil, err
		}
		mailServerID = server.ID
	}
	fingerprint, err := adminResourceCommandFingerprint(struct {
		Domain       string                 `json:"domain"`
		OwnerUserID  uint                   `json:"ownerId"`
		Purpose      domain.ResourcePurpose `json:"purpose"`
		MailServerID uint                   `json:"mailServerId"`
	}{domainName, owner.ID, purpose, mailServerID})
	if err != nil {
		return nil, err
	}
	receipt := AdminResourceCommandReceipt{OperatorUserID: command.OperatorUserID, IdempotencyKey: key, Operation: "core.admin_domain.create", Subject: "domain:" + domainName, RequestFingerprint: fingerprint}
	result := &AdminDomainMutationResult{}
	err = s.repo.WithTx(ctx, func(txCtx context.Context) error {
		replayed, err := s.reserve(txCtx, receipt, result)
		if err != nil || replayed {
			return err
		}
		owner, err = s.validateOwner(txCtx, command.OwnerUserID, purpose)
		if err != nil {
			return err
		}
		server, err := s.repo.LockAdminDomainMailServer(txCtx, mailServerID)
		if err != nil {
			return err
		}
		if server.OwnerUserID != owner.ID || server.Status == domain.MailServerDisabled {
			return domain.ErrMailServerNotFound
		}
		root := &domain.EmailResource{Type: domain.ResourceTypeDomain, OwnerUserID: owner.ID}
		resource := &domain.MailDomainResource{Domain: domainName, MailServerID: mailServerID, Purpose: purpose, Status: domain.DomainStatusAbnormal}
		if err := s.repo.CreateAdminDomain(txCtx, root, resource); err != nil {
			return err
		}
		result.ResourceID = root.ID
		if err := s.logs.Create(txCtx, adminDomainOperationLog(command.OperatorUserID, "core.admin_domain.create", root.ID, command.RequestID, command.Path, "Domain resource created by administrator.")); err != nil {
			return err
		}
		return s.complete(txCtx, command.OperatorUserID, key, result)
	})
	return result, err
}

func (s *AdminDomainCommandService) Edit(ctx context.Context, command AdminDomainEditCommand) (*AdminDomainMutationResult, error) {
	return s.mutate(ctx, command, "core.admin_domain.edit")
}

func (s *AdminDomainCommandService) ApplyAction(ctx context.Context, command AdminDomainEditCommand) (*AdminDomainMutationResult, error) {
	operation := "core.admin_domain." + string(command.Action)
	if command.StatusCommand != "" {
		operation = "core.admin_domain." + string(command.StatusCommand)
	}
	return s.mutate(ctx, command, operation)
}

func (s *AdminDomainCommandService) mutate(ctx context.Context, command AdminDomainEditCommand, operation string) (*AdminDomainMutationResult, error) {
	if s == nil || s.repo == nil || s.logs == nil || s.owners == nil || command.ResourceID == 0 || command.Version == 0 || command.OperatorUserID == 0 || !validAdminDomainMutation(command) {
		return nil, domain.ErrInvalidResourceCommand
	}
	key, err := normalizeAdminResourceIdempotencyKey(command.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	fingerprint, err := adminResourceCommandFingerprint(struct {
		Version       uint64                   `json:"version"`
		OwnerUserID   *uint                    `json:"ownerId"`
		Purpose       *domain.ResourcePurpose  `json:"purpose"`
		MailServerID  *uint                    `json:"mailServerId"`
		StatusCommand AdminDomainStatusCommand `json:"statusCommand"`
		Action        AdminDomainAction        `json:"action"`
	}{command.Version, command.OwnerUserID, command.Purpose, command.MailServerID, command.StatusCommand, command.Action})
	if err != nil {
		return nil, err
	}
	receipt := AdminResourceCommandReceipt{OperatorUserID: command.OperatorUserID, IdempotencyKey: key, Operation: operation, Subject: adminDomainSubject(command.ResourceID), RequestFingerprint: fingerprint}
	result := &AdminDomainMutationResult{ResourceID: command.ResourceID}
	shouldSchedule := false
	err = s.repo.WithTx(ctx, func(txCtx context.Context) error {
		replayed, err := s.reserve(txCtx, receipt, result)
		if err != nil || replayed {
			return err
		}
		root, resource, err := s.repo.LockAdminDomain(txCtx, command.ResourceID)
		if err != nil {
			return err
		}
		if root.Version != command.Version {
			return domain.ErrResourceVersionConflict
		}
		if resource.Status == domain.DomainStatusDeleted && command.Action != AdminDomainRecover {
			return domain.ErrResourceNotFound
		}
		previousOwnerID := root.OwnerUserID
		targetOwnerID := root.OwnerUserID
		if command.OwnerUserID != nil {
			if *command.OwnerUserID == 0 {
				return domain.ErrInvalidResourceOwner
			}
			targetOwnerID = *command.OwnerUserID
		}
		targetPurpose := resource.Purpose
		if command.Purpose != nil {
			targetPurpose = *command.Purpose
		}
		switch command.Action {
		case AdminDomainPublish:
			if resource.Purpose != domain.PurposeNotSale && resource.Purpose != domain.PurposeSale {
				return domain.ErrInvalidPurpose
			}
			targetPurpose = domain.PurposeSale
		case AdminDomainUnpublish:
			if resource.Purpose != domain.PurposeSale && resource.Purpose != domain.PurposeNotSale {
				return domain.ErrInvalidPurpose
			}
			targetPurpose = domain.PurposeNotSale
		case AdminDomainDelete:
			if s.allocations == nil {
				return domain.ErrResourceDependency
			}
			if err := s.allocations.AssertNoActiveAllocations(txCtx, []uint{root.ID}); err != nil {
				return err
			}
		case AdminDomainRecover:
			targetPurpose = domain.PurposeNotSale
		}
		owner := &AdminOwnerSummary{ID: targetOwnerID}
		ownerValidationRequired := targetOwnerID != previousOwnerID || targetPurpose == domain.PurposeSale || targetPurpose == domain.PurposeBinding
		if ownerValidationRequired {
			owner, err = s.validateOwner(txCtx, targetOwnerID, targetPurpose)
			if err != nil {
				return err
			}
		}
		if targetOwnerID != previousOwnerID {
			if s.allocations == nil {
				return domain.ErrResourceDependency
			}
			if err := s.allocations.AssertNoActiveAllocations(txCtx, []uint{root.ID}); err != nil {
				return err
			}
		}
		targetServerID := resource.MailServerID
		if command.MailServerID != nil {
			if *command.MailServerID == 0 {
				return domain.ErrMailServerNotFound
			}
			targetServerID = *command.MailServerID
		}
		if command.MailServerID != nil || targetOwnerID != previousOwnerID {
			server, err := s.repo.LockAdminDomainMailServer(txCtx, targetServerID)
			if err != nil {
				return err
			}
			if server.OwnerUserID != owner.ID || server.Status == domain.MailServerDisabled {
				return domain.ErrMailServerNotFound
			}
		}

		beforeOwner, beforeServer, beforePurpose, beforeStatus := root.OwnerUserID, resource.MailServerID, resource.Purpose, resource.Status
		root.OwnerUserID = owner.ID
		resource.MailServerID = targetServerID
		if command.Purpose != nil || command.Action == AdminDomainPublish || command.Action == AdminDomainUnpublish {
			if err := resource.SetPurposeAdmin(targetPurpose); err != nil {
				return err
			}
		}
		switch command.StatusCommand {
		case AdminDomainMarkNormal:
			err = resource.MarkDNSStatusAdmin(true)
		case AdminDomainMarkAbnormal:
			err = resource.MarkDNSStatusAdmin(false)
		case AdminDomainEnable:
			err = resource.EnableAdmin()
		case AdminDomainDisable:
			err = resource.DisableAdmin()
		}
		if err != nil {
			return err
		}
		switch command.Action {
		case AdminDomainDelete:
			err = resource.DeleteAdmin()
		case AdminDomainRecover:
			err = resource.RecoverAdmin()
		}
		if err != nil {
			return err
		}
		changed := beforeOwner != root.OwnerUserID || beforeServer != resource.MailServerID || beforePurpose != resource.Purpose || beforeStatus != resource.Status
		if changed {
			if err := s.repo.SaveAdminDomain(txCtx, root, resource, command.Version, previousOwnerID); err != nil {
				return err
			}
		}
		if command.StatusCommand == AdminDomainEnable || command.Action == AdminDomainRecover {
			shouldSchedule = true
		}
		summary := "Domain resource command applied."
		if !changed {
			summary = "Domain resource already had the requested state."
		}
		if err := s.logs.Create(txCtx, adminDomainOperationLog(command.OperatorUserID, operation, root.ID, command.RequestID, command.Path, summary)); err != nil {
			return err
		}
		return s.complete(txCtx, command.OperatorUserID, key, result)
	})
	if err == nil && shouldSchedule {
		s.validation.ScheduleDispatcher(ctx, 0)
	}
	return result, err
}

func (s *AdminDomainCommandService) Validate(ctx context.Context, resourceID, operatorUserID uint, idempotencyKey, requestID, path string) (*AdminDomainMutationResult, error) {
	if s == nil || s.repo == nil || s.logs == nil || resourceID == 0 || operatorUserID == 0 {
		return nil, domain.ErrInvalidResourceCommand
	}
	key, err := normalizeAdminResourceIdempotencyKey(idempotencyKey)
	if err != nil {
		return nil, err
	}
	fingerprint, _ := adminResourceCommandFingerprint(struct{}{})
	receipt := AdminResourceCommandReceipt{OperatorUserID: operatorUserID, IdempotencyKey: key, Operation: "core.admin_domain.validate", Subject: adminDomainSubject(resourceID), RequestFingerprint: fingerprint}
	result := &AdminDomainMutationResult{ResourceID: resourceID}
	shouldSchedule := false
	err = s.repo.WithTx(ctx, func(txCtx context.Context) error {
		replayed, err := s.reserve(txCtx, receipt, result)
		if err != nil || replayed {
			return err
		}
		root, resource, err := s.repo.LockAdminDomain(txCtx, resourceID)
		if err != nil {
			return err
		}
		if resource.Status == domain.DomainStatusDeleted {
			return domain.ErrResourceNotFound
		}
		if resource.Status == domain.DomainStatusDisabled {
			return domain.ErrInvalidResourceStatus
		}
		changed, err := resource.QueueValidationAdmin()
		if err != nil {
			return err
		}
		if changed {
			if err := s.repo.SaveAdminDomain(txCtx, root, resource, root.Version, root.OwnerUserID); err != nil {
				return err
			}
		}
		shouldSchedule = true
		if err := s.logs.Create(txCtx, adminDomainOperationLog(operatorUserID, "core.admin_domain.validate", root.ID, requestID, path, "Domain resource validation marked pending.")); err != nil {
			return err
		}
		return s.complete(txCtx, operatorUserID, key, result)
	})
	if err == nil && shouldSchedule {
		s.validation.ScheduleDispatcher(ctx, 0)
	}
	return result, err
}

func (s *AdminDomainCommandService) SubmitValidationBatch(
	ctx context.Context,
	selection AdminDomainBulkSelection,
	operatorUserID uint,
	idempotencyKey, requestID, path string,
) (*ResourceBatchValidationResult, error) {
	if s == nil || s.validation == nil || s.owners == nil || operatorUserID == 0 {
		return nil, domain.ErrInvalidResourceCommand
	}
	key, err := normalizeAdminResourceIdempotencyKey(idempotencyKey)
	if err != nil {
		return nil, err
	}
	selection, fingerprintValue, err := s.normalizeBulkSelection(ctx, selection)
	if err != nil {
		return nil, err
	}
	fingerprint, err := adminResourceCommandFingerprint(struct {
		Selection any `json:"selection"`
	}{fingerprintValue})
	if err != nil {
		return nil, err
	}
	batch := ResourceBulkSelection{
		Mode: ResourceBulkSelectionMode(selection.Mode), ResourceIDs: selection.ResourceIDs,
		AdminScope: true, AllowBinding: true,
		BatchKey: "admin-domain:" + adminSensitiveValueFingerprint(fmt.Sprintf("%d:%s:%s", operatorUserID, key, fingerprint)),
		Filter:   ResourceBulkFilter{ResourceType: domain.ResourceTypeDomain},
	}
	if selection.Mode == AdminDomainBulkFilter {
		batch.Filter = ResourceBulkFilter{
			ResourceType: domain.ResourceTypeDomain,
			Search:       selection.Filter.Search, TLD: selection.Filter.TLD, Status: string(selection.Filter.Status),
			Purpose: string(selection.Filter.Purpose), MailServerID: selection.Filter.MailServerID,
			OwnerID: selection.Filter.OwnerID, OwnerIDs: selection.Filter.OwnerIDs, AdminSearch: true,
			CreatedFrom: selection.Filter.CreatedFrom, CreatedTo: selection.Filter.CreatedTo,
		}
	}
	return s.validation.CreateBatch(ctx, batch, operatorUserID, true, requestID, path)
}

func (s *AdminDomainCommandService) ApplyBulk(ctx context.Context, action string, selection AdminDomainBulkSelection, operatorUserID uint, idempotencyKey, requestID, path string) (*AdminDomainBulkResult, error) {
	if s == nil || s.repo == nil || s.logs == nil || s.owners == nil || operatorUserID == 0 || !validAdminDomainBulkAction(action) {
		return nil, domain.ErrInvalidResourceCommand
	}
	key, err := normalizeAdminResourceIdempotencyKey(idempotencyKey)
	if err != nil {
		return nil, err
	}
	selection, fingerprintValue, err := s.normalizeBulkSelection(ctx, selection)
	if err != nil {
		return nil, err
	}
	fingerprint, err := adminResourceCommandFingerprint(struct {
		Action    string `json:"action"`
		Selection any    `json:"selection"`
	}{action, fingerprintValue})
	if err != nil {
		return nil, err
	}
	receipt := AdminResourceCommandReceipt{OperatorUserID: operatorUserID, IdempotencyKey: key, Operation: "core.admin_domain." + action + "_bulk", Subject: "domain_resources:" + fingerprint, RequestFingerprint: fingerprint}
	result := &AdminDomainBulkResult{}
	shouldSchedule := false
	err = s.repo.WithTx(ctx, func(txCtx context.Context) error {
		replayed, err := s.reserve(txCtx, receipt, result)
		if err != nil || replayed {
			return err
		}
		ids := selection.ResourceIDs
		if selection.Mode == AdminDomainBulkFilter {
			ids, err = s.repo.ListAdminDomainIDs(txCtx, selection.Filter, AdminDomainBulkMaxFilter+1)
			if err != nil {
				return err
			}
			if len(ids) > AdminDomainBulkMaxFilter {
				return domain.ErrResourceSelectionTooLarge
			}
		}
		result.Requested = len(ids)
		for _, resourceID := range ids {
			root, resource, lockErr := s.repo.LockAdminDomain(txCtx, resourceID)
			if errors.Is(lockErr, domain.ErrResourceNotFound) {
				result.Skipped++
				continue
			}
			if lockErr != nil {
				return lockErr
			}
			if action == "validate" {
				changed, queueErr := resource.QueueValidationAdmin()
				if queueErr != nil {
					if errors.Is(queueErr, domain.ErrResourceNotFound) || errors.Is(queueErr, domain.ErrInvalidResourceStatus) {
						result.Skipped++
						continue
					}
					return queueErr
				}
				if changed {
					err = s.repo.SaveAdminDomain(txCtx, root, resource, root.Version, root.OwnerUserID)
				}
				if err != nil {
					return err
				}
				shouldSchedule = shouldSchedule || resource.Status != domain.DomainStatusValidating
				result.Affected++
				continue
			}
			beforePurpose, beforeStatus := resource.Purpose, resource.Status
			switch action {
			case "disable":
				err = resource.DisableAdmin()
			case "publish":
				if resource.Purpose == domain.PurposeSale {
					result.Skipped++
					continue
				}
				if resource.Purpose != domain.PurposeNotSale {
					result.Skipped++
					continue
				}
				_, ownerErr := s.validateOwner(txCtx, root.OwnerUserID, domain.PurposeSale)
				if ownerErr != nil {
					if errors.Is(ownerErr, domain.ErrInvalidResourceOwner) {
						result.Skipped++
						continue
					}
					return ownerErr
				}
				err = resource.SetPurposeAdmin(domain.PurposeSale)
			case "unpublish":
				if resource.Purpose == domain.PurposeNotSale {
					result.Skipped++
					continue
				}
				if resource.Purpose != domain.PurposeSale {
					result.Skipped++
					continue
				}
				err = resource.SetPurposeAdmin(domain.PurposeNotSale)
			case "delete":
				if s.allocations == nil {
					return domain.ErrResourceDependency
				}
				if guardErr := s.allocations.AssertNoActiveAllocations(txCtx, []uint{root.ID}); guardErr != nil {
					if errors.Is(guardErr, domain.ErrResourceHasAllocation) {
						result.Skipped++
						continue
					}
					return guardErr
				}
				err = resource.DeleteAdmin()
			}
			if err != nil {
				if errors.Is(err, domain.ErrResourceNotFound) || errors.Is(err, domain.ErrInvalidResourceStatus) || errors.Is(err, domain.ErrInvalidPurpose) {
					result.Skipped++
					continue
				}
				return err
			}
			if beforePurpose == resource.Purpose && beforeStatus == resource.Status {
				result.Skipped++
				continue
			}
			if err := s.repo.SaveAdminDomain(txCtx, root, resource, root.Version, root.OwnerUserID); err != nil {
				return err
			}
			result.Affected++
		}
		if err := s.logs.Create(txCtx, &governancedomain.OperationLog{
			OperatorUserID: operatorUserID, OperationType: "core.admin_domain." + action + "_bulk", ResourceType: "domain_resource", ResourceID: "batch",
			Path: strings.TrimSpace(path), Result: "success", RequestID: strings.TrimSpace(requestID),
			SafeSummary: fmt.Sprintf("Domain resource batch command completed. Requested: %d; affected: %d; skipped: %d.", result.Requested, result.Affected, result.Skipped),
		}); err != nil {
			return err
		}
		return s.complete(txCtx, operatorUserID, key, result)
	})
	if err == nil && shouldSchedule {
		s.validation.ScheduleDispatcher(ctx, 0)
	}
	return result, err
}

func (s *AdminDomainCommandService) normalizeBulkSelection(ctx context.Context, selection AdminDomainBulkSelection) (AdminDomainBulkSelection, any, error) {
	switch selection.Mode {
	case AdminDomainBulkIDs:
		selection.ResourceIDs = uniqueAdminResourceIDs(selection.ResourceIDs)
		if len(selection.ResourceIDs) == 0 {
			return AdminDomainBulkSelection{}, nil, domain.ErrResourceNotFound
		}
		if len(selection.ResourceIDs) > AdminDomainBulkMaxExplicitIDs {
			return AdminDomainBulkSelection{}, nil, domain.ErrResourceSelectionTooLarge
		}
		return selection, struct {
			Mode string `json:"mode"`
			IDs  []uint `json:"resourceIds"`
		}{string(selection.Mode), selection.ResourceIDs}, nil
	case AdminDomainBulkFilter:
		publicFilter := selection.Filter
		normalized, err := normalizeAdminDomainFilter(ctx, s.owners, selection.Filter)
		if err != nil {
			return AdminDomainBulkSelection{}, nil, err
		}
		selection.Filter = normalized
		return selection, struct {
			Mode   string                `json:"mode"`
			Filter AdminDomainListFilter `json:"filter"`
		}{string(selection.Mode), publicFilter}, nil
	default:
		return AdminDomainBulkSelection{}, nil, domain.ErrInvalidResourceCommand
	}
}

func (s *AdminDomainCommandService) validateOwner(ctx context.Context, ownerID uint, purpose domain.ResourcePurpose) (*AdminOwnerSummary, error) {
	if s.owners == nil || ownerID == 0 || !domain.IsValidPurpose(purpose) {
		return nil, domain.ErrInvalidResourceOwner
	}
	owner, err := s.owners.ValidateTargetOwner(ctx, ownerID)
	if err != nil {
		return nil, fmt.Errorf("validate admin domain owner: %w", err)
	}
	if owner == nil || !owner.Enabled {
		return nil, domain.ErrInvalidResourceOwner
	}
	if purpose == domain.PurposeSale {
		switch owner.Role {
		case "supplier", "admin", "super_admin":
		default:
			return nil, domain.ErrInvalidResourceOwner
		}
	}
	if purpose == domain.PurposeBinding && owner.Role != "admin" && owner.Role != "super_admin" {
		return nil, domain.ErrInvalidResourceOwner
	}
	return owner, nil
}

func (s *AdminDomainCommandService) reserve(ctx context.Context, receipt AdminResourceCommandReceipt, target any) (bool, error) {
	resultJSON, replayed, err := s.repo.ReserveAdminCommand(ctx, receipt)
	if err != nil || !replayed {
		return replayed, err
	}
	if target == nil || len(resultJSON) == 0 {
		return false, domain.ErrResourceDependency
	}
	if err := json.Unmarshal(resultJSON, target); err != nil {
		return false, fmt.Errorf("decode administrator domain command receipt: %w", err)
	}
	return true, nil
}

func (s *AdminDomainCommandService) complete(ctx context.Context, operatorUserID uint, key string, result any) error {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode administrator domain command receipt: %w", err)
	}
	return s.repo.CompleteAdminCommand(ctx, operatorUserID, key, resultJSON)
}

func validAdminDomainMutation(command AdminDomainEditCommand) bool {
	if command.StatusCommand != "" && command.Action != "" {
		return false
	}
	if command.StatusCommand != "" {
		switch command.StatusCommand {
		case AdminDomainMarkNormal, AdminDomainMarkAbnormal, AdminDomainEnable, AdminDomainDisable:
		default:
			return false
		}
	}
	if command.Action != "" {
		switch command.Action {
		case AdminDomainPublish, AdminDomainUnpublish, AdminDomainDelete, AdminDomainRecover:
		default:
			return false
		}
	}
	return command.OwnerUserID != nil || command.Purpose != nil || command.MailServerID != nil || command.StatusCommand != "" || command.Action != ""
}

func validAdminDomainBulkAction(action string) bool {
	switch action {
	case "validate", "disable", "publish", "unpublish", "delete":
		return true
	default:
		return false
	}
}

func adminDomainSubject(resourceID uint) string {
	return "domain_resource:" + strconv.FormatUint(uint64(resourceID), 10)
}

func adminDomainOperationLog(operatorUserID uint, operation string, resourceID uint, requestID, path, summary string) *governancedomain.OperationLog {
	return &governancedomain.OperationLog{
		OperatorUserID: operatorUserID, OperationType: operation, ResourceType: "domain_resource", ResourceID: strconv.FormatUint(uint64(resourceID), 10),
		Path: strings.TrimSpace(path), Result: "success", SafeSummary: strings.TrimSpace(summary), RequestID: strings.TrimSpace(requestID),
	}
}
