package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/core/domain"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
)

const (
	AdminResourceDefaultLimit = 20
	AdminResourceMaxLimit     = 100
	adminTokenExpiringWindow  = 7 * 24 * time.Hour
	adminIdempotencyKeyMaxLen = 128
)

var adminMicrosoftACLRequestedScopes = [...]string{
	"https://graph.microsoft.com/Mail.Read",
	"https://graph.microsoft.com/Mail.Send",
	"User.Read",
	"offline_access",
}

// AdminOwnerSummary is the IAM-owned safe owner summary used by the Core
// administrator read composition. It intentionally contains no authentication
// or permission-policy facts.
type AdminOwnerSummary struct {
	ID        uint
	Email     string
	Nickname  string
	GroupName string
	Role      string
	Enabled   bool
}

// OwnerQueryPort is published by IAM. All list enrichment is batched and owner
// eligibility is revalidated on every mutation.
type OwnerQueryPort interface {
	GetByIDs(ctx context.Context, ids []uint) (map[uint]AdminOwnerSummary, error)
	SearchAdminOwners(ctx context.Context, search string, limit int) ([]AdminOwnerSummary, error)
	ValidateTargetOwner(ctx context.Context, id uint) (*AdminOwnerSummary, error)
}

type AdminBindingSummary struct {
	ID            uint
	ResourceID    uint
	EmailAddress  string
	Status        string
	LastSafeError string
	UpdatedAt     time.Time
}

// BindingQueryPort is published by MailTransport and only exposes the current
// binding's safe summary. Auxiliary message bodies stay in MailTransport.
type BindingQueryPort interface {
	GetByResourceIDs(ctx context.Context, resourceIDs []uint) (map[uint]AdminBindingSummary, error)
}

type AdminBindingCommand struct {
	ResourceID        uint
	OwnerUserID       uint
	AccountEmail      string
	BindingAddressSet bool
	BindingAddress    *string
}

// BindingAdminPort participates in the caller's short transaction through the
// tx-bound context. It cannot set a verified/status result directly.
type BindingAdminPort interface {
	ReplaceAdminInput(ctx context.Context, command AdminBindingCommand) error
}

// AdminProxyBindingSummary is Proxy-owned, credential-free binding data shown
// only in an administrator resource detail. Raw proxy URLs are intentionally
// never part of this cross-context view.
type AdminProxyBindingSummary struct {
	ProxyID    uint
	Host       string
	OutboundIP string
	Country    string
	IPVersion  string
	Status     string
	ExpireAt   time.Time
}

// AdminProxyBindingQueryPort is implemented by BC-PROXY. Email addresses are
// used because proxy bindings are keyed by the Microsoft account address.
type AdminProxyBindingQueryPort interface {
	GetByEmailAddresses(ctx context.Context, addresses []string) (map[string][]AdminProxyBindingSummary, error)
}

// ResourceAllocationGuardPort is implemented by Alloc. Core locks the resource
// root first; the adapter then checks active allocations in the same tx.
type ResourceAllocationGuardPort interface {
	AssertNoActiveAllocations(ctx context.Context, resourceIDs []uint) error
}

type AdminAliasScheduleSummary struct {
	WeekCreated int
	WeekLimit   int
	YearCreated int
	YearLimit   int
	NextRunAt   *time.Time
}

// AliasScheduleQueryPort is the MailTransport-owned safe schedule view used by
// the explicit-alias Tab. Attempts, claims and fencing tokens are not exposed.
type AliasScheduleQueryPort interface {
	GetAdminAliasSchedule(ctx context.Context, resourceID uint) (*AdminAliasScheduleSummary, error)
}

type AdminTaskSummary struct {
	TaskID             string
	Kind               string
	Status             string
	CredentialRevision *uint64
	UpdatedAt          time.Time
}

// TaskQueryPort is the Governance composition boundary for the bounded recent
// task list shown in the single-resource overview.
type TaskQueryPort interface {
	GetRecentByResourceID(ctx context.Context, resourceID uint, limit int) ([]AdminTaskSummary, error)
}

type AdminMicrosoftListFilter struct {
	Search         string
	Suffix         string
	Status         domain.MicrosoftResourceStatus
	ForSale        *bool
	LongLived      *bool
	GraphAvailable *bool
	TokenHealth    string
	CreatedFrom    *time.Time
	CreatedTo      *time.Time
	OwnerIDs       []uint
}

type AdminMicrosoftRecord struct {
	ID                     uint
	OwnerUserID            uint
	Version                uint64
	EmailAddress           string
	EmailDomain            string
	Status                 domain.MicrosoftResourceStatus
	ForSale                bool
	LongLived              bool
	GraphAvailable         bool
	QualityScore           int
	RefreshTokenConfigured bool
	PasswordConfigured     bool
	ClientIDConfigured     bool
	CredentialRevision     uint64
	CredentialUpdatedAt    time.Time
	RTExpireAt             *time.Time
	TokenLastRefreshedAt   *time.Time
	TokenLastRequestID     string
	LastAllocatedAt        *time.Time
	LastSafeError          string
	ExplicitAliasCount     int64
	DotAliasCount          int64
	PlusAliasCount         int64
	ActiveTask             *AdminTaskSummary
	RecentTasks            []AdminTaskSummary
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

type AdminFacetCounts struct {
	All      int64
	Pending  int64
	Normal   int64
	Abnormal int64
	Disabled int64
	Deleted  int64
}

type AdminBooleanFacets struct {
	All int64
	Yes int64
	No  int64
}

type AdminTokenHealthFacets struct {
	All      int64
	Valid    int64
	Expiring int64
	Expired  int64
	Missing  int64
}

type AdminKeyFacet struct {
	Key   string
	Count int64
}

type AdminMicrosoftFacets struct {
	Status         AdminFacetCounts
	ForSale        AdminBooleanFacets
	LongLived      AdminBooleanFacets
	GraphAvailable AdminBooleanFacets
	TokenHealth    AdminTokenHealthFacets
	Suffixes       []AdminKeyFacet
}

type AdminMicrosoftReadRepository interface {
	ListAdminMicrosoft(ctx context.Context, filter AdminMicrosoftListFilter, offset, limit int, afterID uint, now time.Time) ([]AdminMicrosoftRecord, int64, error)
	AdminMicrosoftFacets(ctx context.Context, filter AdminMicrosoftListFilter, now time.Time) (*AdminMicrosoftFacets, error)
	FindAdminMicrosoft(ctx context.Context, resourceID uint) (*AdminMicrosoftRecord, error)
	ListAdminMicrosoftAliases(ctx context.Context, resourceID uint, kind string, offset, limit int) ([]AdminMicrosoftAliasItem, int64, error)
}

type AdminMicrosoftResourceItem struct {
	ID              uint
	Version         uint64
	EmailAddress    string
	Suffix          string
	BindingAddress  *string
	Owner           AdminOwnerSummary
	Status          string
	ForSale         bool
	LongLived       bool
	GraphAvailable  bool
	MailProtocol    string
	QualityScore    int
	TokenHealth     string
	RTExpireAt      *time.Time
	LastAllocatedAt *time.Time
	LastSafeError   *string
	ActiveTask      *AdminTaskSummary
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type AdminCredentialConfiguration struct {
	PasswordConfigured     bool
	ClientIDConfigured     bool
	RefreshTokenConfigured bool
	Revision               uint64
	UpdatedAt              *time.Time
}

type AdminTokenDiagnostic struct {
	Health           string
	RTExpireAt       *time.Time
	RemainingSeconds *int64
	LastRefreshedAt  *time.Time
	// Scopes is the Microsoft ACL request allowlist when OAuth credentials are
	// configured; it is not persisted remote grant state.
	Scopes               []string
	LastRefreshRequestID *string
	LastSafeError        *string
}

type AdminMicrosoftResourceDetail struct {
	AdminMicrosoftResourceItem
	AliasCounts   AdminMicrosoftAliasCounts
	RecentTasks   []AdminTaskSummary
	Credentials   AdminCredentialConfiguration
	Token         AdminTokenDiagnostic
	ProxyBindings []AdminProxyBindingSummary
}

type AdminMicrosoftAliasCounts struct {
	Explicit int64
	Dot      int64
	Plus     int64
}

type AdminMicrosoftListResult struct {
	Items       []AdminMicrosoftResourceItem
	Total       int64
	Offset      int
	Limit       int
	NextAfterID *uint
	Facets      AdminMicrosoftFacets
}

type AdminMicrosoftAliasItem struct {
	ID           uint64
	Kind         string
	EmailAddress string
	CreatedAt    time.Time
}

type AdminMicrosoftAliasListResult struct {
	Items    []AdminMicrosoftAliasItem
	Total    int64
	Offset   int
	Limit    int
	Schedule *AdminAliasScheduleSummary
}

type AdminResourceQuery struct {
	repo     AdminMicrosoftReadRepository
	owners   OwnerQueryPort
	bindings BindingQueryPort
	tasks    TaskQueryPort
	aliases  AliasScheduleQueryPort
	proxies  AdminProxyBindingQueryPort
	now      func() time.Time
}

func NewAdminResourceQuery(repo AdminMicrosoftReadRepository) *AdminResourceQuery {
	return &AdminResourceQuery{repo: repo, now: func() time.Time { return time.Now().UTC() }}
}

func (q *AdminResourceQuery) SetPorts(owners OwnerQueryPort, bindings BindingQueryPort, tasks TaskQueryPort, aliases AliasScheduleQueryPort) {
	if q == nil {
		return
	}
	q.owners = owners
	q.bindings = bindings
	q.tasks = tasks
	q.aliases = aliases
}

func (q *AdminResourceQuery) SetProxyBindings(port AdminProxyBindingQueryPort) {
	if q != nil {
		q.proxies = port
	}
}

func (q *AdminResourceQuery) List(ctx context.Context, filter AdminMicrosoftListFilter, offset, limit int, afterID uint) (*AdminMicrosoftListResult, error) {
	if q == nil || q.repo == nil || q.owners == nil || q.bindings == nil {
		return nil, domain.ErrResourceDependency
	}
	filter, err := normalizeAdminMicrosoftFilter(filter)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = AdminResourceDefaultLimit
	}
	if limit > AdminResourceMaxLimit {
		return nil, domain.ErrInvalidResourceFilter
	}
	if offset < 0 {
		return nil, domain.ErrInvalidResourceFilter
	}
	if filter.Search != "" {
		matched, err := q.owners.SearchAdminOwners(ctx, filter.Search, 1000)
		if err != nil {
			return nil, fmt.Errorf("search admin resource owners: %w", err)
		}
		filter.OwnerIDs = make([]uint, 0, len(matched))
		for _, owner := range matched {
			if owner.ID > 0 {
				filter.OwnerIDs = append(filter.OwnerIDs, owner.ID)
			}
		}
		filter.OwnerIDs = uniqueAdminResourceIDs(filter.OwnerIDs)
	}
	now := q.now()
	records, total, err := q.repo.ListAdminMicrosoft(ctx, filter, offset, limit, afterID, now)
	if err != nil {
		return nil, err
	}
	facets, err := q.repo.AdminMicrosoftFacets(ctx, filter, now)
	if err != nil {
		return nil, err
	}
	ids, ownerIDs := adminRecordIDs(records)
	owners, err := q.owners.GetByIDs(ctx, ownerIDs)
	if err != nil {
		return nil, fmt.Errorf("load admin resource owners: %w", err)
	}
	bindings, err := q.bindings.GetByResourceIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load admin resource bindings: %w", err)
	}
	items := make([]AdminMicrosoftResourceItem, len(records))
	for i := range records {
		owner, ok := owners[records[i].OwnerUserID]
		if !ok {
			return nil, fmt.Errorf("%w: owner summary missing", domain.ErrResourceDependency)
		}
		items[i] = adminMicrosoftItem(records[i], owner, bindings[records[i].ID], now)
	}
	return &AdminMicrosoftListResult{
		Items:       items,
		Total:       total,
		Offset:      offset,
		Limit:       limit,
		NextAfterID: adminNextAfterID(records, limit),
		Facets:      *facets,
	}, nil
}

func (q *AdminResourceQuery) Get(ctx context.Context, resourceID uint) (*AdminMicrosoftResourceDetail, error) {
	if q == nil || q.repo == nil || q.owners == nil || q.bindings == nil || resourceID == 0 {
		return nil, domain.ErrResourceNotFound
	}
	record, err := q.repo.FindAdminMicrosoft(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, domain.ErrResourceNotFound
	}
	owners, err := q.owners.GetByIDs(ctx, []uint{record.OwnerUserID})
	if err != nil {
		return nil, fmt.Errorf("load admin resource owner: %w", err)
	}
	owner, ok := owners[record.OwnerUserID]
	if !ok {
		return nil, fmt.Errorf("%w: owner summary missing", domain.ErrResourceDependency)
	}
	bindings, err := q.bindings.GetByResourceIDs(ctx, []uint{resourceID})
	if err != nil {
		return nil, fmt.Errorf("load admin resource binding: %w", err)
	}
	proxyBindings := []AdminProxyBindingSummary{}
	if q.proxies != nil {
		items, proxyErr := q.proxies.GetByEmailAddresses(ctx, []string{record.EmailAddress})
		if proxyErr != nil {
			return nil, fmt.Errorf("load admin resource proxy bindings: %w", proxyErr)
		}
		proxyBindings = append(proxyBindings, items[strings.ToLower(strings.TrimSpace(record.EmailAddress))]...)
	}
	if q.tasks != nil {
		recent, taskErr := q.tasks.GetRecentByResourceID(ctx, resourceID, 5)
		if taskErr != nil {
			return nil, fmt.Errorf("load admin resource recent tasks: %w", taskErr)
		}
		record.RecentTasks = recent
		for i := range recent {
			if recent[i].Status == "queued" || recent[i].Status == "running" || recent[i].Status == "uncertain" {
				record.ActiveTask = &recent[i]
				break
			}
		}
	}
	now := q.now()
	item := adminMicrosoftItem(*record, owner, bindings[resourceID], now)
	remaining := adminTokenRemaining(record.RTExpireAt, now)
	updatedAt := record.CredentialUpdatedAt
	var refreshRequestID *string
	if strings.TrimSpace(record.TokenLastRequestID) != "" {
		value := strings.TrimSpace(record.TokenLastRequestID)
		refreshRequestID = &value
	}
	return &AdminMicrosoftResourceDetail{
		AdminMicrosoftResourceItem: item,
		AliasCounts: AdminMicrosoftAliasCounts{
			Explicit: record.ExplicitAliasCount,
			Dot:      record.DotAliasCount,
			Plus:     record.PlusAliasCount,
		},
		RecentTasks: append([]AdminTaskSummary(nil), record.RecentTasks...),
		Credentials: AdminCredentialConfiguration{
			PasswordConfigured:     record.PasswordConfigured,
			ClientIDConfigured:     record.ClientIDConfigured,
			RefreshTokenConfigured: record.RefreshTokenConfigured,
			Revision:               record.CredentialRevision,
			UpdatedAt:              &updatedAt,
		},
		Token: AdminTokenDiagnostic{
			Health:               adminTokenHealth(*record, now),
			RTExpireAt:           record.RTExpireAt,
			RemainingSeconds:     remaining,
			LastRefreshedAt:      record.TokenLastRefreshedAt,
			Scopes:               adminMicrosoftACLRequestedScopesFor(*record),
			LastRefreshRequestID: refreshRequestID,
			LastSafeError:        safeOptionalString(record.LastSafeError),
		},
		ProxyBindings: proxyBindings,
	}, nil
}

func (q *AdminResourceQuery) ListAliases(ctx context.Context, resourceID uint, kind string, offset, limit int) (*AdminMicrosoftAliasListResult, error) {
	if q == nil || q.repo == nil || resourceID == 0 {
		return nil, domain.ErrResourceNotFound
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "explicit" && kind != "other" {
		return nil, domain.ErrInvalidResourceFilter
	}
	if offset < 0 || limit <= 0 || limit > AdminResourceMaxLimit {
		return nil, domain.ErrInvalidResourceFilter
	}
	items, total, err := q.repo.ListAdminMicrosoftAliases(ctx, resourceID, kind, offset, limit)
	if err != nil {
		return nil, err
	}
	var schedule *AdminAliasScheduleSummary
	if kind == "explicit" {
		if q.aliases == nil {
			return nil, domain.ErrResourceDependency
		}
		schedule, err = q.aliases.GetAdminAliasSchedule(ctx, resourceID)
		if err != nil {
			return nil, fmt.Errorf("load admin alias schedule: %w", err)
		}
	}
	return &AdminMicrosoftAliasListResult{Items: items, Total: total, Offset: offset, Limit: limit, Schedule: schedule}, nil
}

func normalizeAdminMicrosoftFilter(filter AdminMicrosoftListFilter) (AdminMicrosoftListFilter, error) {
	filter.Search = strings.TrimSpace(filter.Search)
	if len([]rune(filter.Search)) > 120 {
		return filter, domain.ErrInvalidResourceFilter
	}
	filter.Suffix = strings.ToLower(strings.TrimSpace(filter.Suffix))
	filter.Suffix = strings.TrimPrefix(filter.Suffix, "@")
	if len(filter.Suffix) > 255 {
		return filter, domain.ErrInvalidResourceFilter
	}
	if filter.Status != "" && !domain.IsValidMicrosoftStatus(string(filter.Status)) {
		return filter, domain.ErrInvalidResourceFilter
	}
	switch filter.TokenHealth {
	case "", "valid", "expiring", "expired", "missing":
	default:
		return filter, domain.ErrInvalidResourceFilter
	}
	if filter.CreatedFrom != nil && filter.CreatedTo != nil && !filter.CreatedFrom.Before(*filter.CreatedTo) {
		return filter, domain.ErrInvalidResourceFilter
	}
	filter.OwnerIDs = uniqueAdminResourceIDs(filter.OwnerIDs)
	return filter, nil
}

func adminRecordIDs(records []AdminMicrosoftRecord) ([]uint, []uint) {
	resourceIDs := make([]uint, 0, len(records))
	ownerIDs := make([]uint, 0, len(records))
	for _, record := range records {
		resourceIDs = append(resourceIDs, record.ID)
		ownerIDs = append(ownerIDs, record.OwnerUserID)
	}
	return uniqueAdminResourceIDs(resourceIDs), uniqueAdminResourceIDs(ownerIDs)
}

func uniqueAdminResourceIDs(ids []uint) []uint {
	seen := make(map[uint]struct{}, len(ids))
	result := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func adminMicrosoftItem(record AdminMicrosoftRecord, owner AdminOwnerSummary, binding AdminBindingSummary, now time.Time) AdminMicrosoftResourceItem {
	var bindingAddress *string
	if strings.TrimSpace(binding.EmailAddress) != "" {
		value := strings.TrimSpace(binding.EmailAddress)
		bindingAddress = &value
	}
	return AdminMicrosoftResourceItem{
		ID:              record.ID,
		Version:         record.Version,
		EmailAddress:    record.EmailAddress,
		Suffix:          "@" + strings.TrimPrefix(strings.ToLower(strings.TrimSpace(record.EmailDomain)), "@"),
		BindingAddress:  bindingAddress,
		Owner:           owner,
		Status:          string(record.Status),
		ForSale:         record.ForSale,
		LongLived:       record.LongLived,
		GraphAvailable:  record.GraphAvailable,
		MailProtocol:    adminMailProtocol(record),
		QualityScore:    record.QualityScore,
		TokenHealth:     adminTokenHealth(record, now),
		RTExpireAt:      record.RTExpireAt,
		LastAllocatedAt: record.LastAllocatedAt,
		LastSafeError:   safeOptionalString(record.LastSafeError),
		ActiveTask:      record.ActiveTask,
		CreatedAt:       record.CreatedAt,
		UpdatedAt:       record.UpdatedAt,
	}
}

func adminMailProtocol(record AdminMicrosoftRecord) string {
	if record.GraphAvailable {
		return "graph"
	}
	if record.PasswordConfigured {
		return "imap"
	}
	return "unavailable"
}

func adminTokenHealth(record AdminMicrosoftRecord, now time.Time) string {
	if !record.ClientIDConfigured || !record.RefreshTokenConfigured {
		return "missing"
	}
	if record.RTExpireAt == nil {
		return "valid"
	}
	if !record.RTExpireAt.After(now) {
		return "expired"
	}
	if !record.RTExpireAt.After(now.Add(adminTokenExpiringWindow)) {
		return "expiring"
	}
	return "valid"
}

func adminTokenRemaining(expireAt *time.Time, now time.Time) *int64 {
	if expireAt == nil {
		return nil
	}
	seconds := int64(expireAt.Sub(now).Seconds())
	if seconds < 0 {
		seconds = 0
	}
	return &seconds
}

func adminMicrosoftACLRequestedScopesFor(record AdminMicrosoftRecord) []string {
	if !record.ClientIDConfigured || !record.RefreshTokenConfigured {
		return []string{}
	}
	return append([]string(nil), adminMicrosoftACLRequestedScopes[:]...)
}

func safeOptionalString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func adminNextAfterID(records []AdminMicrosoftRecord, limit int) *uint {
	if len(records) == 0 || len(records) < limit {
		return nil
	}
	id := records[len(records)-1].ID
	return &id
}

// AdminMicrosoftCommandRepository owns the Core aggregate transaction. Cross-
// context ports receive the tx-bound context and may only update facts they
// own; no handler or Core service reaches into another BC's ORM model.
type AdminMicrosoftCommandRepository interface {
	WithTx(ctx context.Context, fn func(context.Context) error) error
	ReserveAdminCommand(ctx context.Context, receipt AdminResourceCommandReceipt) ([]byte, bool, error)
	CompleteAdminCommand(ctx context.Context, operatorUserID uint, idempotencyKey string, resultJSON []byte) error
	LockAdminMicrosoft(ctx context.Context, resourceID uint) (*domain.EmailResource, *domain.MicrosoftResource, error)
	SaveAdminMicrosoft(ctx context.Context, root *domain.EmailResource, resource *domain.MicrosoftResource, expectedVersion uint64) error
}

// AdminResourceCommandReceipt is the safe, durable idempotency identity of a
// synchronous administrator command. RequestFingerprint is a SHA-256 digest of
// canonical input; credentials and other write-only values never cross this
// repository boundary as plaintext.
type AdminResourceCommandReceipt struct {
	OperatorUserID     uint
	IdempotencyKey     string
	Operation          string
	Subject            string
	RequestFingerprint string
}

type AdminMicrosoftCredentials struct {
	Password     string
	ClientID     string
	RefreshToken string
}

type AdminMicrosoftEditCommand struct {
	ResourceID        uint
	Version           uint64
	EmailAddress      *string
	BindingAddressSet bool
	BindingAddress    *string
	OwnerUserID       *uint
	QualityScore      *int
	ForSale           *bool
	LongLived         *bool
	Credentials       *AdminMicrosoftCredentials
	OperatorUserID    uint
	IdempotencyKey    string
	RequestID         string
	Path              string
}

type AdminMicrosoftMutationResult struct {
	ValidationTask *ValidationResultView `json:"validationTask,omitempty"`
}

type AdminMicrosoftValidationResult struct {
	Task   *ValidationResultView `json:"task"`
	Reused bool                  `json:"reused"`
}

type AdminReasonCount struct {
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}

type AdminMicrosoftBulkResult struct {
	Requested           int                `json:"requested"`
	Affected            int                `json:"affected"`
	Skipped             int                `json:"skipped"`
	AffectedResourceIDs []uint             `json:"affectedResourceIds,omitempty"`
	SkippedResourceIDs  []uint             `json:"skippedResourceIds,omitempty"`
	ReasonCounts        []AdminReasonCount `json:"reasonCounts"`
}

type AdminMicrosoftStateCommand string

const (
	AdminMicrosoftDisable   AdminMicrosoftStateCommand = "disable"
	AdminMicrosoftPublish   AdminMicrosoftStateCommand = "publish"
	AdminMicrosoftUnpublish AdminMicrosoftStateCommand = "unpublish"
	AdminMicrosoftDelete    AdminMicrosoftStateCommand = "delete"
)

type AdminResourceCommandService struct {
	repo          AdminMicrosoftCommandRepository
	owners        OwnerQueryPort
	bindingReader BindingQueryPort
	bindings      BindingAdminPort
	allocations   ResourceAllocationGuardPort
	validation    *ResourceValidationUseCase
	logs          governanceapp.OperationLogPort
	now           func() time.Time
}

func NewAdminResourceCommandService(
	repo AdminMicrosoftCommandRepository,
	validation *ResourceValidationUseCase,
	logs governanceapp.OperationLogPort,
) *AdminResourceCommandService {
	return &AdminResourceCommandService{
		repo:       repo,
		validation: validation,
		logs:       logs,
		now:        func() time.Time { return time.Now().UTC() },
	}
}

type adminMicrosoftCredentialFingerprint struct {
	PasswordSHA256     string `json:"passwordSha256"`
	ClientIDSHA256     string `json:"clientIdSha256"`
	RefreshTokenSHA256 string `json:"refreshTokenSha256"`
}

type adminMicrosoftEditFingerprint struct {
	Version           uint64                               `json:"version"`
	EmailAddress      *string                              `json:"emailAddress"`
	BindingAddressSet bool                                 `json:"bindingAddressSet"`
	BindingAddress    *string                              `json:"bindingAddress"`
	OwnerUserID       *uint                                `json:"ownerId"`
	QualityScore      *int                                 `json:"qualityScore"`
	ForSale           *bool                                `json:"forSale"`
	LongLived         *bool                                `json:"longLived"`
	Credentials       *adminMicrosoftCredentialFingerprint `json:"credentials"`
}

type adminMicrosoftStateFingerprint struct {
	Version uint64 `json:"version"`
}

type adminMicrosoftBatchFingerprint struct {
	ResourceIDs []uint `json:"resourceIds"`
}

type adminMicrosoftEmptyReceiptResult struct{}

func normalizeAdminResourceIdempotencyKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > adminIdempotencyKeyMaxLen {
		return "", domain.ErrInvalidResourceCommand
	}
	return value, nil
}

func adminResourceCommandFingerprint(value any) (string, error) {
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal administrator resource command fingerprint: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func adminSensitiveValueFingerprint(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func adminEditCommandFingerprint(command AdminMicrosoftEditCommand) (string, error) {
	value := adminMicrosoftEditFingerprint{
		Version: command.Version, EmailAddress: command.EmailAddress,
		BindingAddressSet: command.BindingAddressSet, BindingAddress: command.BindingAddress,
		OwnerUserID: command.OwnerUserID, QualityScore: command.QualityScore,
		ForSale: command.ForSale, LongLived: command.LongLived,
	}
	if command.Credentials != nil {
		value.Credentials = &adminMicrosoftCredentialFingerprint{
			PasswordSHA256:     adminSensitiveValueFingerprint(command.Credentials.Password),
			ClientIDSHA256:     adminSensitiveValueFingerprint(command.Credentials.ClientID),
			RefreshTokenSHA256: adminSensitiveValueFingerprint(command.Credentials.RefreshToken),
		}
	}
	return adminResourceCommandFingerprint(value)
}

func adminResourceSubject(resourceID uint) string {
	return fmt.Sprintf("microsoft_resource:%d", resourceID)
}

func adminResourceBatchSubject(resourceIDs []uint) (string, error) {
	fingerprint, err := adminResourceCommandFingerprint(adminMicrosoftBatchFingerprint{ResourceIDs: resourceIDs})
	if err != nil {
		return "", err
	}
	return "microsoft_resources:" + fingerprint, nil
}

func (s *AdminResourceCommandService) reserveAdminCommand(
	ctx context.Context,
	receipt AdminResourceCommandReceipt,
	replayTarget any,
) (bool, error) {
	resultJSON, replayed, err := s.repo.ReserveAdminCommand(ctx, receipt)
	if err != nil || !replayed {
		return replayed, err
	}
	if replayTarget == nil || len(resultJSON) == 0 {
		return false, domain.ErrResourceDependency
	}
	if err := json.Unmarshal(resultJSON, replayTarget); err != nil {
		return false, fmt.Errorf("decode administrator resource command receipt: %w", err)
	}
	return true, nil
}

func (s *AdminResourceCommandService) completeAdminCommand(ctx context.Context, operatorUserID uint, idempotencyKey string, result any) error {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode administrator resource command receipt: %w", err)
	}
	return s.repo.CompleteAdminCommand(ctx, operatorUserID, idempotencyKey, resultJSON)
}

func (s *AdminResourceCommandService) SetPorts(owners OwnerQueryPort, bindingReader BindingQueryPort, bindings BindingAdminPort, allocations ResourceAllocationGuardPort) {
	if s == nil {
		return
	}
	s.owners = owners
	s.bindingReader = bindingReader
	s.bindings = bindings
	s.allocations = allocations
}

func (s *AdminResourceCommandService) Edit(ctx context.Context, command AdminMicrosoftEditCommand) (*AdminMicrosoftMutationResult, error) {
	return s.edit(ctx, command, "core.admin_resource.edit")
}

func (s *AdminResourceCommandService) ReplaceCredentials(ctx context.Context, command AdminMicrosoftEditCommand) (*AdminMicrosoftMutationResult, error) {
	if command.Credentials == nil || command.EmailAddress != nil || command.OwnerUserID != nil || command.QualityScore != nil || command.ForSale != nil || command.LongLived != nil || command.BindingAddressSet {
		return nil, domain.ErrInvalidResourceCommand
	}
	return s.edit(ctx, command, "core.admin_resource.credentials.replace")
}

func (s *AdminResourceCommandService) edit(ctx context.Context, command AdminMicrosoftEditCommand, operationType string) (*AdminMicrosoftMutationResult, error) {
	if s == nil || s.repo == nil || s.validation == nil || s.logs == nil || command.ResourceID == 0 || command.Version == 0 || command.OperatorUserID == 0 {
		return nil, domain.ErrInvalidResourceCommand
	}
	idempotencyKey, err := normalizeAdminResourceIdempotencyKey(command.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	command.IdempotencyKey = idempotencyKey
	if command.QualityScore != nil && (*command.QualityScore < 0 || *command.QualityScore > 100) {
		return nil, domain.ErrInvalidResourceCommand
	}
	var normalizedEmail *string
	if command.EmailAddress != nil {
		value, err := normalizeAdminMicrosoftEmail(*command.EmailAddress)
		if err != nil {
			return nil, err
		}
		normalizedEmail = &value
		command.EmailAddress = normalizedEmail
	}
	if command.BindingAddressSet && command.BindingAddress != nil {
		value, err := normalizeAdminMicrosoftEmail(*command.BindingAddress)
		if err != nil {
			return nil, err
		}
		command.BindingAddress = &value
	}
	if command.Credentials != nil {
		if err := validateAdminMicrosoftCredentials(*command.Credentials); err != nil {
			return nil, err
		}
	}
	fingerprint, err := adminEditCommandFingerprint(command)
	if err != nil {
		return nil, err
	}
	receipt := AdminResourceCommandReceipt{
		OperatorUserID: command.OperatorUserID, IdempotencyKey: command.IdempotencyKey,
		Operation: operationType, Subject: adminResourceSubject(command.ResourceID),
		RequestFingerprint: fingerprint,
	}

	result := &AdminMicrosoftMutationResult{}
	shouldSchedule := false
	err = s.repo.WithTx(ctx, func(txCtx context.Context) error {
		replayed, err := s.reserveAdminCommand(txCtx, receipt, result)
		if err != nil || replayed {
			return err
		}
		root, resource, err := s.repo.LockAdminMicrosoft(txCtx, command.ResourceID)
		if err != nil {
			return err
		}
		if root.Version != command.Version {
			return domain.ErrResourceVersionConflict
		}
		if resource.Status == domain.MicrosoftStatusDeleted {
			return domain.ErrResourceNotFound
		}
		bindingAddressChanged := false
		if command.BindingAddressSet {
			if s.bindingReader == nil {
				return domain.ErrResourceDependency
			}
			currentBindings, bindingErr := s.bindingReader.GetByResourceIDs(txCtx, []uint{root.ID})
			if bindingErr != nil {
				return fmt.Errorf("load current administrator binding: %w", bindingErr)
			}
			currentAddress := strings.TrimSpace(currentBindings[root.ID].EmailAddress)
			targetAddress := ""
			if command.BindingAddress != nil {
				targetAddress = *command.BindingAddress
			}
			bindingAddressChanged = !strings.EqualFold(currentAddress, targetAddress)
		}

		changedFields := make([]string, 0, 7)
		identityChanged := false
		accountEmailChanged := false
		if normalizedEmail != nil && resource.EmailAddress != *normalizedEmail {
			resource.EmailAddress = *normalizedEmail
			identityChanged = true
			accountEmailChanged = true
			changedFields = append(changedFields, "emailAddress")
		}

		targetOwner := root.OwnerUserID
		if command.OwnerUserID != nil {
			if *command.OwnerUserID == 0 {
				return domain.ErrInvalidResourceOwner
			}
			targetOwner = *command.OwnerUserID
			if targetOwner != root.OwnerUserID {
				identityChanged = true
				changedFields = append(changedFields, "ownerId")
			}
		}
		effectiveForSale := resource.ForSale
		if command.ForSale != nil {
			effectiveForSale = *command.ForSale
		}
		owner := &AdminOwnerSummary{ID: targetOwner}
		if (command.OwnerUserID != nil && targetOwner != root.OwnerUserID) || effectiveForSale {
			owner, err = s.validateOwner(txCtx, targetOwner, effectiveForSale)
			if err != nil {
				return err
			}
		}
		if identityChanged {
			if s.allocations == nil {
				return domain.ErrResourceDependency
			}
			if err := s.allocations.AssertNoActiveAllocations(txCtx, []uint{root.ID}); err != nil {
				return err
			}
		}
		root.OwnerUserID = owner.ID

		if command.QualityScore != nil && resource.QualityScore != *command.QualityScore {
			resource.QualityScore = *command.QualityScore
			changedFields = append(changedFields, "qualityScore")
		}
		if command.LongLived != nil && resource.LongLived != *command.LongLived {
			resource.LongLived = *command.LongLived
			changedFields = append(changedFields, "longLived")
		}
		if command.ForSale != nil && resource.ForSale != *command.ForSale {
			if *command.ForSale {
				if err := resource.PublishAdmin(); err != nil {
					return err
				}
			} else if err := resource.UnpublishAdmin(); err != nil {
				return err
			}
			changedFields = append(changedFields, "forSale")
		}

		now := s.now()
		validationRequired := identityChanged || bindingAddressChanged || command.Credentials != nil
		if command.Credentials != nil {
			if err := resource.ReplaceCredentialsAdmin(
				command.Credentials.Password,
				command.Credentials.ClientID,
				command.Credentials.RefreshToken,
				now,
			); err != nil {
				return err
			}
			changedFields = append(changedFields, "credentials")
		} else if accountEmailChanged {
			if err := resource.InvalidateMicrosoftAccountIdentity(now); err != nil {
				return err
			}
		} else if identityChanged {
			if err := resource.InvalidateMicrosoftIdentity(now); err != nil {
				return err
			}
		} else if bindingAddressChanged {
			if err := resource.InvalidateMicrosoftBinding(now); err != nil {
				return err
			}
		}
		if bindingAddressChanged {
			changedFields = append(changedFields, "bindingAddress")
		}
		if len(changedFields) == 0 {
			if err := s.logs.Create(txCtx, adminResourceOperationLog(
				command.OperatorUserID,
				operationType,
				root.ID,
				command.RequestID,
				command.Path,
				"No resource fields changed.",
			)); err != nil {
				return err
			}
			return s.completeAdminCommand(txCtx, command.OperatorUserID, command.IdempotencyKey, result)
		}

		if err := s.repo.SaveAdminMicrosoft(txCtx, root, resource, command.Version); err != nil {
			return err
		}
		// Keep the cross-context mutation order aligned with validation result
		// commits: resource root -> Microsoft subtype -> validation job -> binding.
		// Both writes still share this caller-owned transaction, so a later binding
		// or audit failure rolls the saved resource and queued job back together.
		if validationRequired {
			task, created, err := s.createValidationTx(txCtx, root, resource, command.RequestID, command.Path)
			if err != nil {
				return err
			}
			result.ValidationTask = task
			shouldSchedule = created
		}
		if identityChanged || bindingAddressChanged {
			if s.bindings == nil {
				return domain.ErrResourceDependency
			}
			if err := s.bindings.ReplaceAdminInput(txCtx, AdminBindingCommand{
				ResourceID:        root.ID,
				OwnerUserID:       root.OwnerUserID,
				AccountEmail:      resource.EmailAddress,
				BindingAddressSet: bindingAddressChanged,
				BindingAddress:    command.BindingAddress,
			}); err != nil {
				return err
			}
		}
		if err := s.logs.Create(txCtx, adminResourceOperationLog(
			command.OperatorUserID,
			operationType,
			root.ID,
			command.RequestID,
			command.Path,
			"Updated fields: "+strings.Join(changedFields, ", ")+".",
		)); err != nil {
			return err
		}
		return s.completeAdminCommand(txCtx, command.OperatorUserID, command.IdempotencyKey, result)
	})
	if err != nil {
		return nil, err
	}
	if shouldSchedule {
		s.validation.ScheduleDispatcher(ctx, 0)
	}
	return result, nil
}

func (s *AdminResourceCommandService) Enable(ctx context.Context, resourceID uint, version uint64, operatorUserID uint, idempotencyKey, requestID, path string) (*AdminMicrosoftMutationResult, error) {
	if s == nil || s.repo == nil || s.validation == nil || s.logs == nil || resourceID == 0 || version == 0 || operatorUserID == 0 {
		return nil, domain.ErrInvalidResourceCommand
	}
	idempotencyKey, err := normalizeAdminResourceIdempotencyKey(idempotencyKey)
	if err != nil {
		return nil, err
	}
	fingerprint, err := adminResourceCommandFingerprint(adminMicrosoftStateFingerprint{Version: version})
	if err != nil {
		return nil, err
	}
	receipt := AdminResourceCommandReceipt{
		OperatorUserID: operatorUserID, IdempotencyKey: idempotencyKey,
		Operation: "core.admin_resource.enable", Subject: adminResourceSubject(resourceID),
		RequestFingerprint: fingerprint,
	}
	result := &AdminMicrosoftMutationResult{}
	shouldSchedule := false
	err = s.repo.WithTx(ctx, func(txCtx context.Context) error {
		replayed, err := s.reserveAdminCommand(txCtx, receipt, result)
		if err != nil || replayed {
			return err
		}
		root, resource, err := s.repo.LockAdminMicrosoft(txCtx, resourceID)
		if err != nil {
			return err
		}
		if root.Version != version {
			return domain.ErrResourceVersionConflict
		}
		if err := resource.EnableAdmin(); err != nil {
			return err
		}
		if err := s.repo.SaveAdminMicrosoft(txCtx, root, resource, version); err != nil {
			return err
		}
		task, created, err := s.createValidationTx(txCtx, root, resource, requestID, path)
		if err != nil {
			return err
		}
		result.ValidationTask = task
		shouldSchedule = created
		if err := s.logs.Create(txCtx, adminResourceOperationLog(operatorUserID, "core.admin_resource.enable", root.ID, requestID, path, "Microsoft resource enabled and queued for validation.")); err != nil {
			return err
		}
		return s.completeAdminCommand(txCtx, operatorUserID, idempotencyKey, result)
	})
	if err != nil {
		return nil, err
	}
	if shouldSchedule {
		s.validation.ScheduleDispatcher(ctx, 0)
	}
	return result, nil
}

func (s *AdminResourceCommandService) Recover(ctx context.Context, resourceID uint, version uint64, operatorUserID uint, idempotencyKey, requestID, path string) (*AdminMicrosoftMutationResult, error) {
	if s == nil || s.repo == nil || s.validation == nil || s.logs == nil || resourceID == 0 || version == 0 || operatorUserID == 0 {
		return nil, domain.ErrInvalidResourceCommand
	}
	idempotencyKey, err := normalizeAdminResourceIdempotencyKey(idempotencyKey)
	if err != nil {
		return nil, err
	}
	fingerprint, err := adminResourceCommandFingerprint(adminMicrosoftStateFingerprint{Version: version})
	if err != nil {
		return nil, err
	}
	receipt := AdminResourceCommandReceipt{
		OperatorUserID: operatorUserID, IdempotencyKey: idempotencyKey,
		Operation: "core.admin_resource.recover", Subject: adminResourceSubject(resourceID),
		RequestFingerprint: fingerprint,
	}
	result := &AdminMicrosoftMutationResult{}
	shouldSchedule := false
	err = s.repo.WithTx(ctx, func(txCtx context.Context) error {
		replayed, err := s.reserveAdminCommand(txCtx, receipt, result)
		if err != nil || replayed {
			return err
		}
		root, resource, err := s.repo.LockAdminMicrosoft(txCtx, resourceID)
		if err != nil {
			return err
		}
		if root.Version != version {
			return domain.ErrResourceVersionConflict
		}
		if err := resource.RecoverAdmin(); err != nil {
			return err
		}
		if err := s.repo.SaveAdminMicrosoft(txCtx, root, resource, version); err != nil {
			return err
		}
		task, created, err := s.createValidationTx(txCtx, root, resource, requestID, path)
		if err != nil {
			return err
		}
		result.ValidationTask = task
		shouldSchedule = created
		if err := s.logs.Create(txCtx, adminResourceOperationLog(operatorUserID, "core.admin_resource.recover", root.ID, requestID, path, "Microsoft resource recovered and queued for validation.")); err != nil {
			return err
		}
		return s.completeAdminCommand(txCtx, operatorUserID, idempotencyKey, result)
	})
	if err != nil {
		return nil, err
	}
	if shouldSchedule {
		s.validation.ScheduleDispatcher(ctx, 0)
	}
	return result, nil
}

func (s *AdminResourceCommandService) ApplyState(ctx context.Context, command AdminMicrosoftStateCommand, resourceID uint, version uint64, operatorUserID uint, idempotencyKey, requestID, path string) error {
	if s == nil || s.repo == nil || s.logs == nil || resourceID == 0 || version == 0 || operatorUserID == 0 {
		return domain.ErrInvalidResourceCommand
	}
	if command != AdminMicrosoftDisable && command != AdminMicrosoftPublish && command != AdminMicrosoftUnpublish && command != AdminMicrosoftDelete {
		return domain.ErrInvalidResourceCommand
	}
	idempotencyKey, err := normalizeAdminResourceIdempotencyKey(idempotencyKey)
	if err != nil {
		return err
	}
	fingerprint, err := adminResourceCommandFingerprint(adminMicrosoftStateFingerprint{Version: version})
	if err != nil {
		return err
	}
	receipt := AdminResourceCommandReceipt{
		OperatorUserID: operatorUserID, IdempotencyKey: idempotencyKey,
		Operation: "core.admin_resource." + string(command), Subject: adminResourceSubject(resourceID),
		RequestFingerprint: fingerprint,
	}
	return s.repo.WithTx(ctx, func(txCtx context.Context) error {
		replayResult := adminMicrosoftEmptyReceiptResult{}
		replayed, err := s.reserveAdminCommand(txCtx, receipt, &replayResult)
		if err != nil || replayed {
			return err
		}
		root, resource, err := s.repo.LockAdminMicrosoft(txCtx, resourceID)
		if err != nil {
			return err
		}
		if root.Version != version {
			return domain.ErrResourceVersionConflict
		}
		beforeStatus, beforeForSale := resource.Status, resource.ForSale
		switch command {
		case AdminMicrosoftDisable:
			err = resource.DisableAdmin()
		case AdminMicrosoftPublish:
			if _, ownerErr := s.validateOwner(txCtx, root.OwnerUserID, true); ownerErr != nil {
				return ownerErr
			}
			err = resource.PublishAdmin()
		case AdminMicrosoftUnpublish:
			err = resource.UnpublishAdmin()
		case AdminMicrosoftDelete:
			if s.allocations == nil {
				return domain.ErrResourceDependency
			}
			if guardErr := s.allocations.AssertNoActiveAllocations(txCtx, []uint{root.ID}); guardErr != nil {
				return guardErr
			}
			err = resource.DeleteAdmin()
		}
		if err != nil {
			return err
		}
		changed := beforeStatus != resource.Status || beforeForSale != resource.ForSale
		if changed {
			if err := s.repo.SaveAdminMicrosoft(txCtx, root, resource, version); err != nil {
				return err
			}
		}
		summary := "Microsoft resource command applied."
		if !changed {
			summary = "Microsoft resource already had the requested state."
		}
		if err := s.logs.Create(txCtx, adminResourceOperationLog(operatorUserID, "core.admin_resource."+string(command), root.ID, requestID, path, summary)); err != nil {
			return err
		}
		return s.completeAdminCommand(txCtx, operatorUserID, idempotencyKey, adminMicrosoftEmptyReceiptResult{})
	})
}

func (s *AdminResourceCommandService) ApplyStateBatch(ctx context.Context, command AdminMicrosoftStateCommand, resourceIDs []uint, operatorUserID uint, idempotencyKey, requestID, path string) (*AdminMicrosoftBulkResult, error) {
	if s == nil || s.repo == nil || s.logs == nil || operatorUserID == 0 {
		return nil, domain.ErrInvalidResourceCommand
	}
	ids := uniqueAdminResourceIDs(resourceIDs)
	if len(ids) == 0 {
		return nil, domain.ErrResourceNotFound
	}
	if len(ids) > 1000 {
		return nil, domain.ErrResourceSelectionTooLarge
	}
	if command != AdminMicrosoftDisable && command != AdminMicrosoftPublish && command != AdminMicrosoftUnpublish && command != AdminMicrosoftDelete {
		return nil, domain.ErrInvalidResourceCommand
	}
	idempotencyKey, err := normalizeAdminResourceIdempotencyKey(idempotencyKey)
	if err != nil {
		return nil, err
	}
	fingerprint, err := adminResourceCommandFingerprint(adminMicrosoftBatchFingerprint{ResourceIDs: ids})
	if err != nil {
		return nil, err
	}
	subject, err := adminResourceBatchSubject(ids)
	if err != nil {
		return nil, err
	}
	receipt := AdminResourceCommandReceipt{
		OperatorUserID: operatorUserID, IdempotencyKey: idempotencyKey,
		Operation: "core.admin_resource." + string(command) + "_batch", Subject: subject,
		RequestFingerprint: fingerprint,
	}
	result := &AdminMicrosoftBulkResult{Requested: len(ids)}
	reasons := make(map[string]int64)
	err = s.repo.WithTx(ctx, func(txCtx context.Context) error {
		replayed, err := s.reserveAdminCommand(txCtx, receipt, result)
		if err != nil || replayed {
			return err
		}
		for _, resourceID := range ids {
			root, resource, err := s.repo.LockAdminMicrosoft(txCtx, resourceID)
			if err != nil {
				if errors.Is(err, domain.ErrResourceNotFound) {
					appendAdminBulkSkip(result, reasons, resourceID, "not_found")
					continue
				}
				return err
			}
			beforeStatus, beforeForSale := resource.Status, resource.ForSale
			switch command {
			case AdminMicrosoftDisable:
				err = resource.DisableAdmin()
			case AdminMicrosoftPublish:
				if _, ownerErr := s.validateOwner(txCtx, root.OwnerUserID, true); ownerErr != nil {
					if errors.Is(ownerErr, domain.ErrInvalidResourceOwner) {
						appendAdminBulkSkip(result, reasons, resourceID, "owner_ineligible")
						continue
					}
					return ownerErr
				}
				err = resource.PublishAdmin()
			case AdminMicrosoftUnpublish:
				err = resource.UnpublishAdmin()
			case AdminMicrosoftDelete:
				if s.allocations == nil {
					return domain.ErrResourceDependency
				}
				if guardErr := s.allocations.AssertNoActiveAllocations(txCtx, []uint{root.ID}); guardErr != nil {
					if errors.Is(guardErr, domain.ErrResourceHasAllocation) {
						appendAdminBulkSkip(result, reasons, resourceID, "active_allocation")
						continue
					}
					return guardErr
				}
				err = resource.DeleteAdmin()
			}
			if err != nil {
				if errors.Is(err, domain.ErrResourceNotFound) || errors.Is(err, domain.ErrInvalidResourceStatus) {
					appendAdminBulkSkip(result, reasons, resourceID, "invalid_state")
					continue
				}
				return err
			}
			changed := beforeStatus != resource.Status || beforeForSale != resource.ForSale
			if !changed {
				appendAdminBulkSkip(result, reasons, resourceID, "already_target")
				continue
			}
			if err := s.repo.SaveAdminMicrosoft(txCtx, root, resource, root.Version); err != nil {
				return err
			}
			result.Affected++
			result.AffectedResourceIDs = append(result.AffectedResourceIDs, resourceID)
		}
		result.Skipped = len(result.SkippedResourceIDs)
		result.ReasonCounts = adminReasonCounts(reasons)
		if err := s.logs.Create(txCtx, &governancedomain.OperationLog{
			OperatorUserID: operatorUserID,
			OperationType:  "core.admin_resource." + string(command) + "_batch",
			ResourceType:   "microsoft_resource",
			ResourceID:     "batch",
			Path:           strings.TrimSpace(path),
			Result:         "success",
			SafeSummary:    fmt.Sprintf("Microsoft resource batch command completed. Requested: %d; affected: %d; skipped: %d.", result.Requested, result.Affected, result.Skipped),
			RequestID:      strings.TrimSpace(requestID),
		}); err != nil {
			return err
		}
		return s.completeAdminCommand(txCtx, operatorUserID, idempotencyKey, result)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *AdminResourceCommandService) Validate(ctx context.Context, resourceID uint, operatorUserID uint, idempotencyKey, requestID, path string) (*AdminMicrosoftValidationResult, error) {
	if s == nil || s.repo == nil || s.validation == nil || s.logs == nil || resourceID == 0 || operatorUserID == 0 {
		return nil, domain.ErrInvalidResourceCommand
	}
	idempotencyKey, err := normalizeAdminResourceIdempotencyKey(idempotencyKey)
	if err != nil {
		return nil, err
	}
	fingerprint, err := adminResourceCommandFingerprint(adminMicrosoftEmptyReceiptResult{})
	if err != nil {
		return nil, err
	}
	receipt := AdminResourceCommandReceipt{
		OperatorUserID: operatorUserID, IdempotencyKey: idempotencyKey,
		Operation: "core.admin_resource.validate", Subject: adminResourceSubject(resourceID),
		RequestFingerprint: fingerprint,
	}
	result := &AdminMicrosoftValidationResult{}
	shouldSchedule := false
	err = s.repo.WithTx(ctx, func(txCtx context.Context) error {
		replayed, err := s.reserveAdminCommand(txCtx, receipt, result)
		if err != nil {
			return err
		}
		if replayed {
			if result.Task == nil || result.Task.ValidationID == 0 {
				return domain.ErrResourceDependency
			}
			result.Reused = true
			return nil
		}
		root, resource, err := s.repo.LockAdminMicrosoft(txCtx, resourceID)
		if err != nil {
			return err
		}
		if resource.Status == domain.MicrosoftStatusDeleted {
			return domain.ErrResourceNotFound
		}
		if resource.Status == domain.MicrosoftStatusDisabled {
			return domain.ErrInvalidResourceStatus
		}
		task, created, err := s.createValidationTx(txCtx, root, resource, requestID, path)
		if err != nil {
			return err
		}
		result.Task = task
		result.Reused = !created
		shouldSchedule = created
		if err := s.logs.Create(txCtx, adminResourceOperationLog(operatorUserID, "core.admin_resource.validate", root.ID, requestID, path, "Microsoft resource validation queued.")); err != nil {
			return err
		}
		return s.completeAdminCommand(txCtx, operatorUserID, idempotencyKey, result)
	})
	if err != nil {
		return nil, err
	}
	if shouldSchedule {
		s.validation.ScheduleDispatcher(ctx, 0)
	}
	return result, nil
}

func (s *AdminResourceCommandService) ValidateImportOwner(ctx context.Context, ownerID uint) (*AdminOwnerSummary, error) {
	return s.validateOwner(ctx, ownerID, false)
}

func (s *AdminResourceCommandService) createValidationTx(ctx context.Context, root *domain.EmailResource, resource *domain.MicrosoftResource, requestID, path string) (*ValidationResultView, bool, error) {
	job := &domain.ResourceValidation{
		ResourceID:   root.ID,
		ResourceType: domain.ResourceTypeMicrosoft,
		OwnerUserID:  root.OwnerUserID,
		Status:       domain.ResourceValidationQueued,
		MaxAttempts:  domain.ResourceValidationDefaultMaxAttempts,
		RequestID:    strings.TrimSpace(requestID),
		Path:         strings.TrimSpace(path),
	}
	created, err := s.validation.validations.CreateWithLog(ctx, job, nil)
	if err != nil {
		return nil, false, err
	}
	_ = resource
	return validationView(job), created, nil
}

func (s *AdminResourceCommandService) validateOwner(ctx context.Context, ownerID uint, requireSupplier bool) (*AdminOwnerSummary, error) {
	if s.owners == nil {
		return nil, domain.ErrResourceDependency
	}
	owner, err := s.owners.ValidateTargetOwner(ctx, ownerID)
	if err != nil {
		return nil, fmt.Errorf("validate admin resource owner: %w", err)
	}
	if owner == nil || owner.ID == 0 || !owner.Enabled {
		return nil, domain.ErrInvalidResourceOwner
	}
	if requireSupplier {
		switch owner.Role {
		case "supplier", "admin", "super_admin":
		default:
			return nil, domain.ErrInvalidResourceOwner
		}
	}
	return owner, nil
}

func normalizeAdminMicrosoftEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 255 {
		return "", domain.ErrInvalidResourceCommand
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value || strings.Count(value, "@") != 1 {
		return "", domain.ErrInvalidResourceCommand
	}
	parts := strings.SplitN(value, "@", 2)
	if parts[0] == "" || parts[1] == "" {
		return "", domain.ErrInvalidResourceCommand
	}
	return value, nil
}

func validateAdminMicrosoftCredentials(value AdminMicrosoftCredentials) error {
	password := strings.TrimSpace(value.Password)
	clientID := strings.TrimSpace(value.ClientID)
	refreshToken := strings.TrimSpace(value.RefreshToken)
	if password == "" || len(value.Password) > 1024 || len(value.ClientID) > 1024 || len(value.RefreshToken) > 8192 || (clientID == "") != (refreshToken == "") {
		return domain.ErrInvalidResourceCommand
	}
	return nil
}

func adminResourceOperationLog(operatorUserID uint, operationType string, resourceID uint, requestID, path, summary string) *governancedomain.OperationLog {
	return &governancedomain.OperationLog{
		OperatorUserID: operatorUserID,
		OperationType:  operationType,
		ResourceType:   "microsoft_resource",
		ResourceID:     fmt.Sprintf("%d", resourceID),
		Path:           strings.TrimSpace(path),
		Result:         "success",
		SafeSummary:    strings.TrimSpace(summary),
		RequestID:      strings.TrimSpace(requestID),
	}
}

func appendAdminBulkSkip(result *AdminMicrosoftBulkResult, reasons map[string]int64, resourceID uint, reason string) {
	result.SkippedResourceIDs = append(result.SkippedResourceIDs, resourceID)
	reasons[reason]++
}

func adminReasonCounts(values map[string]int64) []AdminReasonCount {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]AdminReasonCount, 0, len(keys))
	for _, key := range keys {
		result = append(result, AdminReasonCount{Reason: key, Count: values[key]})
	}
	return result
}
