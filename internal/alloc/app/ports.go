package app

import (
	"context"
	"time"

	"github.com/donnel666/remail/internal/alloc/domain"
)

const (
	BucketCount                 = 64
	DotAliasCapacityPerResource = 10
	candidateWindowSize         = 4
	globalCandidateWindow       = 8
	bucketProbeCount            = 4
	aliasGenerationWindow       = 32
	candidateRetryCount         = 5
	candidateRetryDelay         = 10 * time.Millisecond
)

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
	ID    uint
	Email string
}

type DailyUsageReservation struct {
	UsageDate      string
	AllocationType domain.AllocationType
	ResourceID     uint
	Kind           domain.DailyUsageKind
	Limit          int
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
	Bucket          uint8
	LastAllocatedAt *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type CandidateRefreshTask struct {
	JobID     uint   `json:"jobId"`
	RequestID string `json:"requestId"`
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
	Expired   int
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
	EnqueueCandidateRefresh(ctx context.Context, task CandidateRefreshTask) error
	EnqueueCandidateRefreshDispatcher(ctx context.Context, delay time.Duration) error
}

type Repository interface {
	WithTx(ctx context.Context, fn func(context.Context) error) error
	HasParentTx(ctx context.Context) bool

	FindExistingAllocation(ctx context.Context, orderNo string) (*domain.UnifiedAllocation, error)
	CreateOrderGuard(ctx context.Context, orderNo string, allocationType domain.AllocationType) error
	LoadProductConfig(ctx context.Context, productID uint, buyerUserID uint) (*ProductAllocationConfig, error)

	ListMicrosoftSourceCandidates(ctx context.Context, buyerUserID uint, scope domain.SupplyScope, bucket *uint8, limit int, emailSuffix string) ([]MicrosoftCandidate, error)
	ListDomainSourceCandidates(ctx context.Context, buyerUserID uint, scope domain.SupplyScope, bucket *uint8, limit int, emailSuffix string) ([]DomainCandidate, error)
	LockMicrosoftCandidate(ctx context.Context, resourceID uint, buyerUserID uint, scope domain.SupplyScope, emailSuffix string) (*MicrosoftCandidate, error)
	LockDomainCandidate(ctx context.Context, resourceID uint, buyerUserID uint, scope domain.SupplyScope, emailSuffix string) (*DomainCandidate, error)

	FindReusableExplicitAlias(ctx context.Context, resourceID uint) (*AliasCandidate, error)
	FindReusableDotAlias(ctx context.Context, projectID uint, resourceID uint) (*AliasCandidate, error)
	FindReusablePlusAlias(ctx context.Context, projectID uint, resourceID uint) (*AliasCandidate, error)
	FindOrCreateDotAlias(ctx context.Context, resourceID uint, email string) (*AliasCandidate, error)
	FindOrCreatePlusAlias(ctx context.Context, resourceID uint, email string) (*AliasCandidate, error)

	FindReusableGeneratedMailbox(ctx context.Context, projectID uint, resourceID uint) (*GeneratedMailboxCandidate, error)
	FindOrCreateGeneratedMailbox(ctx context.Context, resourceID uint, ownerUserID uint, email string) (*GeneratedMailboxCandidate, error)

	EnsureDailyUsageAvailable(ctx context.Context, usageDate string, allocationType domain.AllocationType, resourceID uint, kind domain.DailyUsageKind, limit int) error
	ConsumeDailyUsage(ctx context.Context, usageDate string, allocationType domain.AllocationType, resourceID uint, kind domain.DailyUsageKind, limit int) error

	CreateMicrosoftAllocation(ctx context.Context, allocation *domain.MicrosoftAllocation) error
	CreateDomainAllocation(ctx context.Context, allocation *domain.GeneratedMailboxAllocation) error
	TouchMicrosoftAllocated(ctx context.Context, projectID uint, resourceID uint, allocatedAt time.Time) error
	TouchDomainAllocated(ctx context.Context, resourceID uint, mailboxID uint, allocatedAt time.Time) error

	ReleaseByOrder(ctx context.Context, orderNo string, releasedAt time.Time) (*domain.UnifiedAllocation, error)
	ListAllocations(ctx context.Context, filter AllocationFilter) (*AllocationListResult, error)
	FindAllocationDetail(ctx context.Context, allocationType domain.AllocationType, allocationID uint) (*domain.UnifiedAllocation, error)
	FindAllocationByOrder(ctx context.Context, orderNo string) (*domain.UnifiedAllocation, error)
	ListActiveByRecipient(ctx context.Context, recipient string) ([]domain.UnifiedAllocation, error)

	GetInventoryStats(ctx context.Context, projectID uint, buyerUserID uint) (*InventoryStats, error)
	GetProductInventoryTotals(ctx context.Context, projectID uint, buyerUserID uint) (*ProjectProductInventoryTotals, error)
	RefreshRoutingCandidates(ctx context.Context, projectID uint) (int, error)
	ListRoutingCandidates(ctx context.Context, filter CandidateFilter) (*CandidateListResult, error)

	CreateCandidateRefreshJobWithLog(ctx context.Context, job *domain.CandidateRefreshJob) (bool, error)
	FindCandidateRefreshJob(ctx context.Context, jobID uint) (*domain.CandidateRefreshJob, error)
	ExpireStaleCandidateRefreshJobs(ctx context.Context, staleBefore time.Time) (int, error)
	ClaimDispatchableCandidateRefreshJobs(ctx context.Context, limit int, staleBefore time.Time) ([]domain.CandidateRefreshJob, error)
	MarkCandidateRefreshJobQueued(ctx context.Context, jobID uint) (bool, error)
	MarkCandidateRefreshJobDispatchFailed(ctx context.Context, jobID uint, safeError string) error
	MarkCandidateRefreshJobRunning(ctx context.Context, jobID uint) (bool, error)
	MarkCandidateRefreshJobSucceeded(ctx context.Context, jobID uint, affected int) error
	MarkCandidateRefreshJobFailed(ctx context.Context, jobID uint, safeError string) error
}
