package app

import (
	"context"
	"time"

	"github.com/donnel666/remail/internal/alloc/domain"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

const (
	MicrosoftBucketCount        = coredomain.MicrosoftAllocationBucketCount
	DomainBucketCount           = coredomain.DomainAllocationBucketCount
	GmailBucketCount            = 2048
	GeneratedMailboxBucketCount = coredomain.GeneratedMailboxBucketCount
	DotAliasCapacityPerResource = 10
	InventoryRefreshInterval    = 10 * time.Minute
	candidateWindowSize         = 4
	globalCandidateWindow       = 8
	bucketProbeCount            = 4
	aliasGenerationWindow       = 32
	candidateRetryCount         = 5
	candidateRetryDelay         = 10 * time.Millisecond
	maxCandidateWindowSize      = 100
	maxBucketProbeCount         = 64
	maxAliasGenerationWindow    = 1000
	maxCandidateRetryCount      = 20
	maxDotAliasCapacity         = 64
	maxInventoryRefreshInterval = 24 * time.Hour
	maxInventoryCacheHardTTL    = 365 * 24 * time.Hour
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

func inventoryCacheHardTTLValue() time.Duration {
	return min(runtimeconfig.Duration("inventory_cache_hard_ttl_hours", inventoryCacheHardTTL, time.Hour, 1), maxInventoryCacheHardTTL)
}

type ProductAllocationConfig struct {
	ProjectID             uint
	ProductID             uint
	ProductType           coredomain.ProductType
	CodeEnabled           bool
	PurchaseEnabled       bool
	CodeSupplierPrice     string
	PurchaseSupplierPrice string
	MainWeight            int
	DotWeight             int
	PlusWeight            int
}

type MicrosoftCandidate struct {
	ResourceID     uint
	EmailAddress   string
	QualityScore   int
	PlusDailyLimit int
	MainAllocated  bool
}

type ICloudCandidate struct {
	ResourceID uint
	AliasID    uint
	Email      string
}

type GmailCandidate struct {
	ResourceID uint
	Email      string
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

type HistoricalGmailAllocationCommand struct {
	ProjectID  uint
	ProductID  uint
	ResourceID uint
	Mailbox    domain.GmailMailbox
	Email      string
	CreatedAt  time.Time
	ReleasedAt time.Time
}

type HistoricalMicrosoftAliasPort interface {
	BackfillExistingAliases(ctx context.Context, resourceID uint, aliases []string) error
}

type InventoryStats struct {
	ProjectID                  uint
	Microsoft                  MicrosoftInventoryStats
	Domain                     DomainInventoryStats
	Gmail                      GmailInventoryStats
	ICloud                     ICloudInventoryStats
	TotalAvailable             int64
	ActiveMicrosoftAllocations int64
	ActiveDomainAllocations    int64
	ActiveGmailAllocations     int64
	ActiveICloudAllocations    int64
	// Cold distinguishes an unrefreshed placeholder from a real zero inventory.
	Cold bool
}

type GmailInventoryStats struct {
	Enabled                 bool
	CodeEnabled             bool
	PurchaseEnabled         bool
	MainEnabled             bool
	DotEnabled              bool
	PlusEnabled             bool
	EligibleResources       int64
	PublicEligibleResources int64
	MainAvailable           int64
	MainPublicAvailable     int64
	DotAvailable            int64
	DotPublicAvailable      int64
	PlusAvailable           int64
	PlusPublicAvailable     int64
	TotalAvailable          int64
	PublicAvailable         int64
}

type ICloudInventoryStats struct {
	Enabled           bool
	EligibleResources int64
	AliasAvailable    int64
	TotalAvailable    int64
}

type ProductInventoryTotal struct {
	ProductID               uint
	TotalAvailable          int64
	PublicAvailable         int64
	CodeAvailable           *int64
	CodePublicAvailable     *int64
	PurchaseAvailable       *int64
	PurchasePublicAvailable *int64
	Suffixes                []ProductInventorySuffixTotal
}

type ProductInventorySuffixTotal struct {
	Suffix          string
	TotalAvailable  int64
	PublicAvailable int64
}

type UserICloudInventoryTotal struct {
	ProductID            uint
	OwnedAvailable       int64
	OwnedPublicAvailable int64
}

type ProjectProductInventoryTotals struct {
	ProjectID      uint
	TotalAvailable int64
	Items          []ProductInventoryTotal
	// RefreshedAt is internal cache metadata, not part of the public API.
	RefreshedAt *time.Time `json:"refreshedAt,omitempty"`
	// Cold marks a deliberately seeded zero snapshot whose aggregate refresh
	// has not completed yet. It makes every product/suffix a known zero without
	// requiring synchronous aggregate SQL on a cache miss.
	Cold bool
}

// ProductInventoryOverlay merges inventory owned outside Allocation into the
// shared product snapshot used by every inventory endpoint.
type ProductInventoryOverlay interface {
	OverlayProductInventory(ctx context.Context, projectIDs []uint, snapshots map[uint]*ProjectProductInventoryTotals) error
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

type InventoryProject struct {
	ID   uint
	Name string
}

type InventoryRefreshStatus string

const (
	InventoryRefreshQueued    InventoryRefreshStatus = "queued"
	InventoryRefreshRunning   InventoryRefreshStatus = "running"
	InventoryRefreshScheduled InventoryRefreshStatus = "scheduled"
	InventoryRefreshFailed    InventoryRefreshStatus = "failed"
)

type InventoryRefreshState struct {
	ProjectID       uint
	Status          InventoryRefreshStatus
	TotalAvailable  int64
	LastRefreshedAt *time.Time
	NextRefreshAt   *time.Time
	LastAttemptAt   *time.Time
	LastError       string
}

type InventoryRefreshItem struct {
	InventoryRefreshState
	ProjectName string
}

type InventoryRefreshParameters struct {
	RefreshInterval time.Duration
	CacheHardTTL    time.Duration
	BatchSize       int
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
	ListInventoryRefreshStates(ctx context.Context, projectIDs []uint) (map[uint]InventoryRefreshState, error)
	RecordInventoryRefreshFailure(ctx context.Context, entry InventoryCacheEntry, err error) error
	ClearInventoryRefreshFailure(ctx context.Context, entry InventoryCacheEntry) error
	ClearInventoryRefreshFailures(ctx context.Context, entries []InventoryCacheEntry) error
	ClaimDueInventory(ctx context.Context, before time.Time, limit int) ([]InventoryCacheEntry, error)
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

type AllocationFilter struct {
	Type       domain.AllocationType
	OrderNo    string
	OrderNos   []string
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

type InventoryRefreshQueue interface {
	EnqueueInventoryRefresh(ctx context.Context) error
	EnqueueInventoryRefreshContinuation(ctx context.Context) error
}

type Repository interface {
	WithTx(ctx context.Context, fn func(context.Context) error) error
	HasParentTx(ctx context.Context) bool

	FindExistingAllocation(ctx context.Context, orderNo string) (*domain.UnifiedAllocation, error)
	CreateOrderGuard(ctx context.Context, orderNo string, allocationType domain.AllocationType) error
	LoadProductConfig(ctx context.Context, productID uint, buyerUserID uint, fulfillExistingOrder bool) (*ProductAllocationConfig, error)

	ListMicrosoftSourceCandidates(ctx context.Context, projectID uint, buyerUserID uint, scope domain.SupplyScope, mailbox domain.MicrosoftMailbox, bucket *uint16, limit int, emailSuffix string) ([]MicrosoftCandidate, error)
	ListGmailSourceCandidates(ctx context.Context, projectID uint, buyerUserID uint, scope domain.SupplyScope, mailbox domain.GmailMailbox, bucket *uint16, limit int) ([]GmailCandidate, error)
	ListICloudSourceCandidates(ctx context.Context, projectID uint, buyerUserID uint, scope domain.SupplyScope, requiredUntil time.Time, limit int) ([]ICloudCandidate, error)
	ListDomainSourceCandidates(ctx context.Context, buyerUserID uint, scope domain.SupplyScope, bucket *uint16, limit int, emailSuffix string) ([]DomainCandidate, error)
	ListGeneratedMailboxCandidates(ctx context.Context, projectID uint, buyerUserID uint, scope domain.SupplyScope, bucket *uint16, limit int, emailSuffix string) ([]GeneratedMailboxCandidate, error)
	LockResourceRoot(ctx context.Context, resourceID uint, allocationType domain.AllocationType) (bool, error)
	TryLockResourceRoot(ctx context.Context, resourceID uint, allocationType domain.AllocationType) (bool, error)
	LockMicrosoftCandidate(ctx context.Context, resourceID uint, projectID uint, buyerUserID uint, scope domain.SupplyScope, mailbox domain.MicrosoftMailbox, emailSuffix string) (*MicrosoftCandidate, error)
	LockGmailCandidate(ctx context.Context, resourceID uint, projectID uint, buyerUserID uint, scope domain.SupplyScope, mailbox domain.GmailMailbox) (*GmailCandidate, error)
	LockICloudCandidate(ctx context.Context, resourceID uint, aliasID uint, projectID uint, buyerUserID uint, scope domain.SupplyScope, requiredUntil time.Time) (*ICloudCandidate, error)
	LockDomainCandidate(ctx context.Context, resourceID uint, buyerUserID uint, scope domain.SupplyScope, emailSuffix string) (*DomainCandidate, error)
	LockGeneratedMailboxCandidate(ctx context.Context, mailboxID uint, resourceID uint, projectID uint) (*GeneratedMailboxCandidate, error)
	AssertNoActiveAllocations(ctx context.Context, resourceIDs []uint) error

	IsMicrosoftMailboxHistoricallyMatched(ctx context.Context, projectID uint, mailbox domain.MicrosoftMailbox, mailboxID uint) (bool, error)
	IsGmailMailboxAvailable(ctx context.Context, resourceID uint, projectID uint, mailbox domain.GmailMailbox, email string) (bool, error)
	IsDomainEmailHistoricallyAllocated(ctx context.Context, projectID uint, email string) (bool, error)
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
	CreateGmailAllocation(ctx context.Context, allocation *domain.GmailAllocation) error
	CreateICloudAllocation(ctx context.Context, allocation *domain.ICloudAllocation) error
	CreateDomainAllocation(ctx context.Context, allocation *domain.GeneratedMailboxAllocation) error
	TouchMicrosoftAllocated(ctx context.Context, resourceID uint, allocatedAt time.Time) error
	TouchGmailAllocated(ctx context.Context, resourceID uint, allocatedAt time.Time) error
	TouchICloudAllocated(ctx context.Context, resourceID uint, aliasID uint, allocatedAt time.Time) error
	TouchDomainAllocated(ctx context.Context, resourceID uint, mailboxID uint, allocatedAt time.Time) error

	ReleaseByOrder(ctx context.Context, orderNo string, releasedAt time.Time) (*domain.UnifiedAllocation, error)
	ListAllocations(ctx context.Context, filter AllocationFilter) (*AllocationListResult, error)
	FindAllocationDetail(ctx context.Context, allocationType domain.AllocationType, allocationID uint) (*domain.UnifiedAllocation, error)
	FindAllocationByOrder(ctx context.Context, orderNo string) (*domain.UnifiedAllocation, error)
	ListActiveByRecipient(ctx context.Context, recipient string) ([]domain.UnifiedAllocation, error)

	AssertProjectInventoryAccess(ctx context.Context, projectID uint, buyerUserID uint) error
	ListInventoryProjects(ctx context.Context) ([]InventoryProject, error)
	ListInventoryProjectIDs(ctx context.Context) ([]uint, error)
	GetInventoryStats(ctx context.Context, projectID uint) (*InventoryStats, error)
	GetProductInventoryTotals(ctx context.Context, projectID uint) (*ProjectProductInventoryTotals, error)
	ListUserICloudInventoryTotals(ctx context.Context, projectID uint, buyerUserID uint) ([]UserICloudInventoryTotal, error)
}
