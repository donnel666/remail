package gmail

import (
	"encoding/json"
	"errors"
	"time"
)

const (
	SourceSMSBower = "smsbower"
	SourceLocal    = "local"

	LocalResourcePending     = "pending"
	LocalResourceValidating  = "validating"
	LocalResourceIdentifying = "identifying"
	LocalResourceNormal      = "normal"
	LocalResourceAbnormal    = "abnormal"
	LocalResourceDisabled    = "disabled"
	LocalResourceDeleted     = "deleted"
	// Keep healthy rows readable by the previous deploy image until the
	// follow-up contract migration can retire its "available" status.
	localResourceRollbackNormal = "available"
	localResourceRollbackLeased = "leased"
	localResourceRollbackSold   = "sold"

	AllocationStatusAllocated = "allocated"
	AllocationStatusReleased  = "released"
	AllocationSupplyOwned     = "owned"
	AllocationSupplyPublic    = "public"
	GmailMailboxMain          = "main"
	GmailMailboxDot           = "dot"
	GmailMailboxPlus          = "plus"

	SessionPending      = "pending"
	SessionProvisioning = "provisioning"
	SessionActive       = "active"
	SessionCompleting   = "completing"
	SessionCompleted    = "completed"
	SessionCancelling   = "cancelling"
	SessionCancelled    = "cancelled"
	SessionFailed       = "failed"
	SessionUnknown      = "unknown"

	ActionWaitNext = "wait_next"
	ActionComplete = "complete"
	ActionCancel   = "cancel"

	MaxCodes = 3
)

func isLocalResourceHealthy(status string) bool {
	return status == LocalResourceNormal || status == localResourceRollbackNormal
}

var (
	ErrRouteNotFound             = errors.New("gmail: supply route not found")
	ErrInvalidRoute              = errors.New("gmail: invalid supply route")
	ErrSessionMissing            = errors.New("gmail: code session not found")
	ErrPickupInvalid             = errors.New("gmail: pickup credential mismatch")
	ErrInvalidLocalResource      = errors.New("gmail: invalid local resource")
	ErrLocalResourceMissing      = errors.New("gmail: local resource not found")
	ErrLocalResourceBusy         = errors.New("gmail: local resource is leased or sold")
	ErrLocalResourceVersion      = errors.New("gmail: local resource version conflict")
	ErrLocalValidationConflict   = errors.New("gmail: validation idempotency conflict")
	ErrLocalValidationDependency = errors.New("gmail: validation dependency unavailable")
)

type localResourceModel struct {
	ID                    uint       `gorm:"column:id;primaryKey"`
	ResourceType          string     `gorm:"column:resource_type"`
	OwnerUserID           uint       `gorm:"column:owner_user_id"`
	Email                 string     `gorm:"column:email;uniqueIndex"`
	Identity              string     `gorm:"column:identity;uniqueIndex"`
	Password              string     `gorm:"column:password"`
	TwoFactorSecret       string     `gorm:"column:two_factor_secret"`
	AppPassword           string     `gorm:"column:app_password"`
	CredentialRevision    uint64     `gorm:"column:credential_revision"`
	CredentialUpdatedAt   time.Time  `gorm:"column:credential_updated_at"`
	ForSale               bool       `gorm:"column:for_sale"`
	Status                string     `gorm:"column:status"`
	ValidationGeneration  uint64     `gorm:"column:validation_generation"`
	ValidationFailures    int        `gorm:"column:validation_failures"`
	ValidationRequestID   string     `gorm:"column:validation_request_id"`
	ValidationCommandHash string     `gorm:"column:validation_command_hash"`
	AllocBucket           uint16     `gorm:"column:alloc_bucket"`
	LastAllocatedAt       *time.Time `gorm:"column:last_allocated_at"`
	LastSafeError         string     `gorm:"column:last_safe_error"`
	LastCheckedAt         *time.Time `gorm:"column:last_checked_at"`
	CreatedAt             time.Time  `gorm:"column:created_at"`
	UpdatedAt             time.Time  `gorm:"column:updated_at"`
}

func (localResourceModel) TableName() string { return "gmail_resources" }

type resourceRootModel struct {
	ID          uint      `gorm:"column:id;primaryKey;autoIncrement"`
	Type        string    `gorm:"column:type"`
	OwnerUserID uint      `gorm:"column:owner_user_id"`
	Version     uint64    `gorm:"column:version;default:1"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (resourceRootModel) TableName() string { return "email_resources" }

type allocationModel struct {
	ID                 uint       `gorm:"column:id;primaryKey"`
	OrderNo            string     `gorm:"column:order_no"`
	GuardType          string     `gorm:"column:guard_type"`
	ProjectID          uint       `gorm:"column:project_id"`
	ProductID          uint       `gorm:"column:product_id"`
	Source             string     `gorm:"column:source"`
	SourceRef          string     `gorm:"column:source_ref"`
	ProviderCursor     uint64     `gorm:"column:provider_cursor"`
	ProviderSpamCursor uint64     `gorm:"column:provider_spam_cursor"`
	ServiceMode        string     `gorm:"column:service_mode"`
	ResourceID         *uint      `gorm:"column:resource_id"`
	SupplyScope        string     `gorm:"column:supply_scope"`
	Mailbox            string     `gorm:"column:mailbox;default:main"`
	Email              string     `gorm:"column:email"`
	Status             string     `gorm:"column:status"`
	CostPointsSnapshot string     `gorm:"column:cost_points_snapshot"`
	CreatedAt          time.Time  `gorm:"column:created_at"`
	ReleasedAt         *time.Time `gorm:"column:released_at"`
}

func (allocationModel) TableName() string { return "gmail_allocations" }

type LocalResourceItem struct {
	ID                    uint       `json:"id"`
	Version               uint64     `json:"version"`
	OwnerUserID           uint       `json:"ownerUserId"`
	Email                 string     `json:"email"`
	Status                string     `json:"status"`
	ForSale               bool       `json:"forSale"`
	PasswordConfigured    bool       `json:"passwordConfigured"`
	TwoFactorConfigured   bool       `json:"twoFactorConfigured"`
	AppPasswordConfigured bool       `json:"appPasswordConfigured"`
	CredentialRevision    uint64     `json:"credentialRevision"`
	ValidationFailures    int        `json:"validationFailures"`
	LastAllocatedAt       *time.Time `json:"lastAllocatedAt,omitempty"`
	LastSafeError         string     `json:"lastSafeError,omitempty"`
	LastCheckedAt         *time.Time `json:"lastCheckedAt,omitempty"`
	CreatedAt             time.Time  `json:"createdAt"`
	UpdatedAt             time.Time  `json:"updatedAt"`
}

type LocalResourceFacets struct {
	All         int64 `json:"all"`
	Pending     int64 `json:"pending"`
	Validating  int64 `json:"validating"`
	Identifying int64 `json:"identifying"`
	Normal      int64 `json:"normal"`
	Abnormal    int64 `json:"abnormal"`
	Disabled    int64 `json:"disabled"`
}

type LocalResourceList struct {
	Items  []LocalResourceItem `json:"items"`
	Total  int64               `json:"total"`
	Offset int                 `json:"offset"`
	Limit  int                 `json:"limit"`
	Facets LocalResourceFacets `json:"facets"`
}

type accountStateModel struct {
	ID                  uint       `gorm:"column:id;primaryKey"`
	Balance             string     `gorm:"column:balance"`
	HealthStatus        string     `gorm:"column:health_status"`
	ConsecutiveFailures uint       `gorm:"column:consecutive_failures"`
	LastSafeError       string     `gorm:"column:last_safe_error"`
	BalanceAlertActive  bool       `gorm:"column:balance_alert_active"`
	FailureAlertActive  bool       `gorm:"column:failure_alert_active"`
	Generation          uint64     `gorm:"column:generation"`
	LastSyncedAt        *time.Time `gorm:"column:last_synced_at"`
	LastSuccessAt       *time.Time `gorm:"column:last_success_at"`
	CreatedAt           time.Time  `gorm:"column:created_at"`
	UpdatedAt           time.Time  `gorm:"column:updated_at"`
}

func (accountStateModel) TableName() string { return "smsbower_account_state" }

type serviceModel struct {
	Code              string     `gorm:"column:code;primaryKey"`
	Name              string     `gorm:"column:name"`
	GmailPrice        string     `gorm:"column:gmail_price"`
	GmailStock        uint       `gorm:"column:gmail_stock"`
	PreviousPrice     *string    `gorm:"column:previous_price"`
	LastNotifiedPrice *string    `gorm:"column:last_notified_price"`
	Active            bool       `gorm:"column:active"`
	PriceChangedAt    *time.Time `gorm:"column:price_changed_at"`
	LastSeenAt        time.Time  `gorm:"column:last_seen_at"`
	CreatedAt         time.Time  `gorm:"column:created_at"`
	UpdatedAt         time.Time  `gorm:"column:updated_at"`
}

func (serviceModel) TableName() string { return "smsbower_services" }

type routeModel struct {
	ID                  uint      `gorm:"column:id;primaryKey"`
	ProjectID           uint      `gorm:"column:project_id"`
	Source              string    `gorm:"column:source"`
	ProviderServiceCode string    `gorm:"column:provider_service_code"`
	CreatedAt           time.Time `gorm:"column:created_at"`
	UpdatedAt           time.Time `gorm:"column:updated_at"`
}

func (routeModel) TableName() string { return "gmail_supply_routes" }

type sessionModel struct {
	ID                    uint       `gorm:"column:id;primaryKey"`
	OrderNo               string     `gorm:"column:order_no"`
	Source                string     `gorm:"column:source"`
	SourceRef             string     `gorm:"column:source_ref"`
	ProviderServiceCode   string     `gorm:"column:provider_service_code"`
	ServiceMode           string     `gorm:"column:service_mode"`
	Email                 string     `gorm:"column:email"`
	Status                string     `gorm:"column:status"`
	ReceivedCount         uint8      `gorm:"column:received_count"`
	CodesJSON             []byte     `gorm:"column:codes_json"`
	UpstreamPriceSnapshot string     `gorm:"column:upstream_price_snapshot"`
	PointsPerUnitSnapshot string     `gorm:"column:points_per_unit_snapshot"`
	CostPointsSnapshot    string     `gorm:"column:cost_points_snapshot"`
	MaxPriceSnapshot      string     `gorm:"column:max_price_snapshot"`
	PendingRemoteAction   string     `gorm:"column:pending_remote_action"`
	ProviderCursor        uint64     `gorm:"column:provider_cursor"`
	ProviderSpamCursor    uint64     `gorm:"column:provider_spam_cursor"`
	NextPollAt            *time.Time `gorm:"column:next_poll_at"`
	LastSafeError         string     `gorm:"column:last_safe_error"`
	Version               uint       `gorm:"column:version"`
	StartedAt             *time.Time `gorm:"column:started_at"`
	ExpiresAt             *time.Time `gorm:"column:expires_at"`
	CompletedAt           *time.Time `gorm:"column:completed_at"`
	CreatedAt             time.Time  `gorm:"column:created_at"`
	UpdatedAt             time.Time  `gorm:"column:updated_at"`
}

func (sessionModel) TableName() string { return "gmail_code_sessions" }

type Code struct {
	Seq        int       `json:"seq"`
	Code       string    `json:"code"`
	ReceivedAt time.Time `json:"receivedAt"`
}

func decodeCodes(raw []byte) ([]Code, error) {
	if len(raw) == 0 {
		return []Code{}, nil
	}
	var codes []Code
	if err := json.Unmarshal(raw, &codes); err != nil || len(codes) > MaxCodes {
		return nil, errors.New("gmail: invalid stored codes")
	}
	return codes, nil
}

type CodeOnlyPickup struct {
	Email         string
	Codes         []Code
	ReceivedCount int
	MaxCodes      int
	ExpiresAt     *time.Time
}

type AccountStatus struct {
	Enabled             bool       `json:"enabled"`
	Configured          bool       `json:"configured"`
	Balance             string     `json:"balance"`
	HealthStatus        string     `json:"healthStatus"`
	ConsecutiveFailures uint       `json:"consecutiveFailures"`
	LastSafeError       string     `json:"lastSafeError,omitempty"`
	LastSyncedAt        *time.Time `json:"lastSyncedAt,omitempty"`
	LastSuccessAt       *time.Time `json:"lastSuccessAt,omitempty"`
}

type ServiceItem struct {
	Code           string     `json:"code"`
	Name           string     `json:"name"`
	GmailPrice     string     `json:"gmailPrice"`
	GmailStock     uint       `json:"gmailStock"`
	PreviousPrice  *string    `json:"previousPrice,omitempty"`
	Active         bool       `json:"active"`
	PriceChangedAt *time.Time `json:"priceChangedAt,omitempty"`
	LastSeenAt     time.Time  `json:"lastSeenAt"`
}

type InventoryItem struct {
	ProjectID         uint
	ProductID         uint
	CodeAvailable     int64
	PurchaseAvailable int64
}

type MappingItem struct {
	ProjectID           uint   `json:"projectId"`
	ProjectName         string `json:"projectName"`
	ProviderServiceCode string `json:"providerServiceCode"`
	ProviderServiceName string `json:"providerServiceName,omitempty"`
	UpstreamPrice       string `json:"upstreamPrice"`
	CostPoints          string `json:"costPoints"`
	CodePrice           string `json:"codePrice"`
	PurchasePrice       string `json:"purchasePrice"`
}

type FinanceOverview struct {
	OrderCount             int64  `json:"orderCount"`
	ActivationCount        int64  `json:"activationCount"`
	ZeroCodeCount          int64  `json:"zeroCodeCount"`
	OneCodeCount           int64  `json:"oneCodeCount"`
	TwoCodeCount           int64  `json:"twoCodeCount"`
	ThreeCodeCount         int64  `json:"threeCodeCount"`
	Sales                  string `json:"sales"`
	Refunds                string `json:"refunds"`
	NetRevenue             string `json:"netRevenue"`
	SettledCost            string `json:"settledCost"`
	ReservedCost           string `json:"reservedCost"`
	UnknownCost            string `json:"unknownCost"`
	ConservativeCost       string `json:"conservativeCost"`
	ConservativeProfit     string `json:"conservativeProfit"`
	ConservativeMarginRate string `json:"conservativeMarginRate"`
}

type FinanceBreakdown struct {
	Key        string `json:"key"`
	Name       string `json:"name"`
	OrderCount int64  `json:"orderCount"`
	NetRevenue string `json:"netRevenue"`
	Cost       string `json:"cost"`
	Profit     string `json:"profit"`
}

type FinanceReport struct {
	Overview  FinanceOverview    `json:"overview"`
	ByProject []FinanceBreakdown `json:"byProject"`
	ByService []FinanceBreakdown `json:"byService"`
	BySource  []FinanceBreakdown `json:"bySource"`
}

type ActivationItem struct {
	ID                  uint       `json:"id"`
	OrderNo             string     `json:"orderNo"`
	ProjectID           uint       `json:"projectId"`
	ProjectName         string     `json:"projectName"`
	Source              string     `json:"source"`
	ProviderServiceCode string     `json:"providerServiceCode"`
	Email               string     `json:"email,omitempty"`
	Status              string     `json:"status"`
	ReceivedCount       uint8      `json:"receivedCount"`
	CostPoints          string     `json:"costPoints"`
	LastSafeError       string     `json:"lastSafeError,omitempty"`
	StartedAt           *time.Time `json:"startedAt,omitempty"`
	ExpiresAt           *time.Time `json:"expiresAt,omitempty"`
	CompletedAt         *time.Time `json:"completedAt,omitempty"`
	CreatedAt           time.Time  `json:"createdAt"`
}
