package app

import (
	"context"
	"time"

	"github.com/donnel666/remail/internal/alloc/domain"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

const (
	MicrosoftBucketCount         = coredomain.MicrosoftAllocationBucketCount
	DomainBucketCount            = coredomain.DomainAllocationBucketCount
	GeneratedMailboxBucketCount  = coredomain.GeneratedMailboxBucketCount
	DotAliasCapacityPerResource  = 10
	InventoryRefreshInterval     = 10 * time.Minute
	candidateWindowSize          = 4
	globalCandidateWindow        = 8
	bucketProbeCount             = 4
	aliasGenerationWindow        = 32
	candidateRetryCount          = 5
	candidateRetryDelay          = 10 * time.Millisecond
	maxCandidateWindowSize       = 100
	maxBucketProbeCount          = 64
	maxAliasGenerationWindow     = 1000
	maxCandidateRetryCount       = 20
	maxDotAliasCapacity          = 64
	maxInventoryRefreshInterval  = 24 * time.Hour
	maxInventoryCacheActivityTTL = 30 * 24 * time.Hour
	maxInventoryCacheHardTTL     = 365 * 24 * time.Hour
)

func candidateWindowSizeValue() int {
	return min(runtimeconfig.Int("candidate_window_size", candidateWindowSize, 1), maxCandidateWindowSize)
}

func globalCandidateWindowValue() int {
	return min(runtimeconfig.Int("global_candidate_window", globalCandidateWindow, 1), maxCandidateWindowSize)
}

func bucketProbeCountValue() int {
	return min(runtimeconfig.Int("bucket_probe_count", bucketProbeCount, 1), maxBucketProbeCount)
}

func aliasGenerationWindowValue() int {
	return min(runtimeconfig.Int("alias_generation_window", aliasGenerationWindow, 1), maxAliasGenerationWindow)
}

func candidateRetryCountValue() int {
	return min(runtimeconfig.Int("candidate_retry_count", candidateRetryCount, 1), maxCandidateRetryCount)
}

func DotAliasCapacityPerResourceValue() int {
	return min(runtimeconfig.Int("dot_alias_capacity_per_resource", DotAliasCapacityPerResource, 1), maxDotAliasCapacity)
}

func InventoryRefreshIntervalValue() time.Duration {
	return min(runtimeconfig.Duration("inventory_refresh_interval_minutes", InventoryRefreshInterval, time.Minute, 1), maxInventoryRefreshInterval)
}

func inventoryCacheActivityTTLValue() time.Duration {
	return min(runtimeconfig.Duration("inventory_cache_activity_ttl_minutes", inventoryCacheActivityTTL, time.Minute, 1), maxInventoryCacheActivityTTL)
}

func inventoryCacheHardTTLValue() time.Duration {
	return min(runtimeconfig.Duration("inventory_cache_hard_ttl_hours", inventoryCacheHardTTL, time.Hour, 1), maxInventoryCacheHardTTL)
}

type ProductAllocationConfig struct {
	ProjectID   uint
	ProductID   uint
	ProductType domain.AllocationType
	MainWeight  int
	DotWeight   int
	PlusWeight  int
}

type MicrosoftCandidate struct {
	ResourceID     uint
	EmailAddress   string
	QualityScore   int
	PlusDailyLimit int
	MainAllocated  bool
}

type DomainCandidate struct {
	ResourceID        uint
	OwnerUserID       uint
	Domain            string
	MailboxDailyLimit int
}

type AliasCandidate struct {
	ID    uint
	Email string
}

type GeneratedMailboxCandidate struct {
	ID         uint
	ResourceID uint
	Email      string
}

type DailyUsageReservation struct {
	UsageDate      string
	AllocationType domain.AllocationType
	ResourceID     uint
	Kind           domain.DailyUsageKind
	Limit          int
}

type HistoricalMicrosoftAllocationCommand struct {
	AliasOwnerID uint
	ProjectID    uint
	ProductID    uint
	ResourceID   uint
	Mailbox      domain.MicrosoftMailbox
	Email        string
	CreatedAt    time.Time
	ReleasedAt   time.Time
}

type HistoricalMicrosoftAliasPort interface {
	BackfillExistingAliases(ctx context.Context, resourceID uint, aliases []string) error
}

type InventoryStats struct {
	ProjectID                  uint
	Microsoft                  MicrosoftInventoryStats
	Domain                     DomainInventoryStats
	TotalAvailable             int64
	ActiveMicrosoftAllocations int64
	ActiveDomainAllocations    int64
}

type ProductInventoryTotal struct {
	ProductID       uint
	TotalAvailable  int64
	PublicAvailable int64
	Suffixes        []ProductInventorySuffixTotal
}

type ProductInventorySuffixTotal struct {
	Suffix          string
	TotalAvailable  int64
	PublicAvailable int64
}

type ProjectProductInventoryTotals struct {
	ProjectID      uint
	TotalAvailable int64
	Items          []ProductInventoryTotal
	// Cold marks a deliberately seeded zero snapshot whose aggregate refresh
	// has not completed yet. It makes every product/suffix a known zero without
	// requiring synchronous aggregate SQL on a cache miss.
	Cold bool
}

type ProductInventoryAvailabilityRequest struct {
	ProjectID   uint
	ProductID   uint
	EmailSuffix string
	PublicOnly  bool
}

type InventoryCacheKind string

const (
	InventoryCacheStats    InventoryCacheKind = "stats"
	InventoryCacheProducts InventoryCacheKind = "products"
)

type InventoryCacheEntry struct {
	Kind      InventoryCacheKind
	ProjectID uint
}

type InventoryCache interface {
	GetInventoryStats(ctx context.Context, projectID uint) (*InventoryStats, error)
	SetInventoryStats(ctx context.Context, projectID uint, stats *InventoryStats, ttl time.Duration) error
	RefreshInventoryStats(ctx context.Context, projectID uint, stats *InventoryStats, ttl time.Duration) error
	GetProductInventoryTotals(ctx context.Context, projectID uint) (*ProjectProductInventoryTotals, error)
	GetProductInventorySnapshots(ctx context.Context, projectIDs []uint) (map[uint]*ProjectProductInventoryTotals, error)
	InitializeInventory(ctx context.Context, entries []InventoryCacheEntry, ttl time.Duration) error
	SetProductInventoryTotals(ctx context.Context, projectID uint, totals *ProjectProductInventoryTotals, ttl time.Duration) error
	RefreshProductInventoryTotals(ctx context.Context, projectID uint, totals *ProjectProductInventoryTotals, ttl time.Duration) error
	IsProductUnavailable(ctx context.Context, req ProductInventoryAvailabilityRequest) (bool, error)
	MarkProductUnavailable(ctx context.Context, req ProductInventoryAvailabilityRequest) (bool, error)
	ClaimActiveInventory(ctx context.Context, since time.Time, before time.Time, limit int) ([]InventoryCacheEntry, error)
	RequeueInventory(ctx context.Context, entries []InventoryCacheEntry) error
	DeleteInventory(ctx context.Context, entry InventoryCacheEntry) error
	AcquireInventoryRefresh(ctx context.Context, entry InventoryCacheEntry, ttl time.Duration) (token string, acquired bool, err error)
	ReleaseInventoryRefresh(ctx context.Context, entry InventoryCacheEntry, token string) error
}

type InventoryRefreshResult struct {
	Attempted int
	Updated   int
	Removed   int
	Skipped   int
	Failed    int
	LastError error
}

type MicrosoftInventoryStats struct {
	Enabled                bool
	MainEnabled            bool
	DotEnabled             bool
	PlusEnabled            bool
	EligibleResources      int64
	MainAvailable          int64
	ExplicitAliasAvailable int64
	DotCapacity            int64
	ActiveDotAllocations   int64
	DotAvailable           int64
	PlusDailyLimit         int64
	PlusDailyUsed          int64
	PlusDailyAvailable     int64
	TotalAvailable         int64
}

type DomainInventoryStats struct {
	Enabled               bool
	EligibleResources     int64
	MailboxDailyLimit     int64
	MailboxDailyUsed      int64
	MailboxDailyAvailable int64
	TotalAvailable        int64
}

type RoutingCandidate struct {
	ID              uint
	Type            domain.AllocationType
	ProjectID       uint
	ResourceID      uint
	Address         string
	DomainSuffix    string
	ForSale         bool
	QualityScore    int
	Status          string
	Bucket          uint16
	LastAllocatedAt *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type CandidateRefreshTask struct {
	ProjectID  uint   `json:"projectId"`
	Generation uint64 `json:"generation"`
	RequestID  string `json:"requestId"`
}

type CandidateRefreshSubmitResult struct {
	JobID     uint
	ProjectID uint
	Status    domain.CandidateRefreshStatus
	Created   bool
	Message   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type CandidateRefreshDispatchResult struct {
	Attempted int
	Queued    int
	Failed    int
}

type AllocationFilter struct {
	Type       domain.AllocationType
	OrderNo    string
	ProjectID  uint
	ResourceID uint
	Status     domain.AllocationStatus
	Mailbox    string
	Offset     int
	Limit      int
}

type AllocationListResult struct {
	Items  []domain.UnifiedAllocation
	Total  int64
	Offset int
	Limit  int
}

// AdminAllocationEnrichment is a bounded, read-only composition of facts owned
// by Trade, Core, IAM and MailMatch. Alloc first pages its own allocation facts
// and then asks this port to enrich at most one API page, avoiding both N+1
// reads and a second allocation aggregate.
type AdminAllocationEnrichment struct {
	OrderNo          string
	ProjectName      string
	ProjectLogoURL   *string
	DeliveryEmail    string
	ServiceMode      string
	OrderStatus      string
	PayAmount        string
	BuyerEmail       string
	VerificationCode *string
	ReceiveUntil     *time.Time
}

type AdminAllocationEnrichmentPort interface {
	GetAdminAllocationEnrichments(ctx context.Context, orderNos []string) (map[string]AdminAllocationEnrichment, error)
}

type AdminAllocationItem struct {
	Type             domain.AllocationType
	ID               uint
	OrderNo          string
	ProjectID        uint
	ProjectName      string
	ProjectLogoURL   *string
	ResourceID       uint
	Mailbox          string
	SupplyScope      domain.SupplyScope
	DeliveryEmail    string
	ServiceMode      string
	OrderStatus      string
	Status           domain.AllocationStatus
	PayAmount        string
	BuyerEmail       string
	VerificationCode *string
	CreatedAt        time.Time
	ReceiveUntil     *time.Time
}

type AdminAllocationListResult struct {
	Items  []AdminAllocationItem
	Total  int64
	Offset int
	Limit  int
}

type CandidateFilter struct {
	ProjectID uint
	Type      domain.AllocationType
	Offset    int
	Limit     int
}

type CandidateListResult struct {
	Items  []RoutingCandidate
	Total  int64
	Offset int
	Limit  int
}

type CandidateRefreshQueue interface {
	EnqueueCandidateRefresh(ctx context.Context, task CandidateRefreshTask) (bool, error)
	EnqueueCandidateRefreshDispatcher(ctx context.Context, delay time.Duration) error
	EnqueueInventoryRefresh(ctx context.Context) error
}

type Repository interface {
	WithTx(ctx context.Context, fn func(context.Context) error) error
	HasParentTx(ctx context.Context) bool

	FindExistingAllocation(ctx context.Context, orderNo string) (*domain.UnifiedAllocation, error)
	CreateOrderGuard(ctx context.Context, orderNo string, allocationType domain.AllocationType) error
	LoadProductConfig(ctx context.Context, productID uint, buyerUserID uint, fulfillExistingOrder bool) (*ProductAllocationConfig, error)

	ListMicrosoftSourceCandidates(ctx context.Context, projectID uint, buyerUserID uint, scope domain.SupplyScope, mailbox domain.MicrosoftMailbox, bucket *uint16, limit int, emailSuffix string) ([]MicrosoftCandidate, error)
	ListDomainSourceCandidates(ctx context.Context, buyerUserID uint, scope domain.SupplyScope, bucket *uint16, limit int, emailSuffix string) ([]DomainCandidate, error)
	ListGeneratedMailboxCandidates(ctx context.Context, projectID uint, buyerUserID uint, scope domain.SupplyScope, bucket *uint16, limit int, emailSuffix string) ([]GeneratedMailboxCandidate, error)
	LockResourceRoot(ctx context.Context, resourceID uint, allocationType domain.AllocationType) (bool, error)
	TryLockResourceRoot(ctx context.Context, resourceID uint, allocationType domain.AllocationType) (bool, error)
	LockMicrosoftCandidate(ctx context.Context, resourceID uint, projectID uint, buyerUserID uint, scope domain.SupplyScope, mailbox domain.MicrosoftMailbox, emailSuffix string) (*MicrosoftCandidate, error)
	LockDomainCandidate(ctx context.Context, resourceID uint, buyerUserID uint, scope domain.SupplyScope, emailSuffix string) (*DomainCandidate, error)
	LockGeneratedMailboxCandidate(ctx context.Context, mailboxID uint, resourceID uint, projectID uint) (*GeneratedMailboxCandidate, error)
	AssertNoActiveAllocations(ctx context.Context, resourceIDs []uint) error

	IsMicrosoftMailboxHistoricallyMatched(ctx context.Context, projectID uint, mailbox domain.MicrosoftMailbox, mailboxID uint) (bool, error)
	IsDomainMailboxAllocated(ctx context.Context, projectID uint, mailboxID uint) (bool, error)
	FindReusableExplicitAlias(ctx context.Context, projectID uint, resourceID uint, emailSuffix string) (*AliasCandidate, error)
	FindReusableDotAlias(ctx context.Context, projectID uint, resourceID uint) (*AliasCandidate, error)
	FindReusablePlusAlias(ctx context.Context, projectID uint, resourceID uint) (*AliasCandidate, error)
	FindExplicitAlias(ctx context.Context, resourceID uint, email string) (*AliasCandidate, error)
	FindOrCreateDotAlias(ctx context.Context, resourceID uint, email string) (*AliasCandidate, error)
	FindOrCreatePlusAlias(ctx context.Context, resourceID uint, email string) (*AliasCandidate, error)

	FindReusableGeneratedMailbox(ctx context.Context, projectID uint, resourceID uint) (*GeneratedMailboxCandidate, error)
	FindOrCreateGeneratedMailbox(ctx context.Context, resourceID uint, ownerUserID uint, email string) (*GeneratedMailboxCandidate, error)

	EnsureDailyUsageAvailable(ctx context.Context, usageDate string, allocationType domain.AllocationType, resourceID uint, kind domain.DailyUsageKind, limit int) error
	ConsumeDailyUsage(ctx context.Context, usageDate string, allocationType domain.AllocationType, resourceID uint, kind domain.DailyUsageKind, limit int) error

	CreateMicrosoftAllocation(ctx context.Context, allocation *domain.MicrosoftAllocation) error
	CreateDomainAllocation(ctx context.Context, allocation *domain.GeneratedMailboxAllocation) error
	TouchMicrosoftAllocated(ctx context.Context, resourceID uint, allocatedAt time.Time) error
	TouchDomainAllocated(ctx context.Context, resourceID uint, mailboxID uint, allocatedAt time.Time) error

	ReleaseByOrder(ctx context.Context, orderNo string, releasedAt time.Time) (*domain.UnifiedAllocation, error)
	ListAllocations(ctx context.Context, filter AllocationFilter) (*AllocationListResult, error)
	FindAllocationDetail(ctx context.Context, allocationType domain.AllocationType, allocationID uint) (*domain.UnifiedAllocation, error)
	FindAllocationByOrder(ctx context.Context, orderNo string) (*domain.UnifiedAllocation, error)
	ListActiveByRecipient(ctx context.Context, recipient string) ([]domain.UnifiedAllocation, error)

	AssertProjectInventoryAccess(ctx context.Context, projectID uint, buyerUserID uint) error
	GetInventoryStats(ctx context.Context, projectID uint) (*InventoryStats, error)
	GetProductInventoryTotals(ctx context.Context, projectID uint) (*ProjectProductInventoryTotals, error)
	RefreshRoutingCandidates(ctx context.Context, projectID uint) (int, error)
	ListRoutingCandidates(ctx context.Context, filter CandidateFilter) (*CandidateListResult, error)

	RequestCandidateRefresh(ctx context.Context, projectID uint, operatorUserID uint, requestID string, path string) (*domain.CandidateRefresh, error)
	ListPendingCandidateRefreshes(ctx context.Context, limit int) ([]domain.CandidateRefresh, error)
	MarkCandidateRefreshProcessing(ctx context.Context, projectID uint, generation uint64) (bool, error)
	RunCandidateRefresh(ctx context.Context, projectID uint, generation uint64) (affected int, current bool, err error)
	ReleaseCandidateRefreshInfrastructureFailure(ctx context.Context, projectID uint, generation uint64, safeError string) (bool, error)
	RecordCandidateRefreshFailure(ctx context.Context, projectID uint, generation uint64, safeError string) (recorded bool, abnormal bool, err error)
}
