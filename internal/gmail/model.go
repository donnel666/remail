package gmail

import (
	"errors"
	"time"
)

const (
	SourceLocal = "local"

	LocalResourcePending     = "pending"
	LocalResourceValidating  = "validating"
	LocalResourceIdentifying = "identifying"
	LocalResourceNormal      = "normal"
	LocalResourceCooldown    = "cooldown"
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
)

var (
	ErrInvalidRoute              = errors.New("gmail: invalid supply route")
	ErrInvalidLocalResource      = errors.New("gmail: invalid local resource")
	ErrLocalResourceMissing      = errors.New("gmail: local resource not found")
	ErrLocalResourceBusy         = errors.New("gmail: local resource is leased or sold")
	ErrLocalResourceVersion      = errors.New("gmail: local resource version conflict")
	ErrLocalValidationConflict   = errors.New("gmail: validation idempotency conflict")
	ErrLocalValidationDependency = errors.New("gmail: validation dependency unavailable")
	ErrLocalCooldownDependency   = errors.New("gmail: variant cooldown dependency unavailable")
)

type localResourceModel struct {
	ID                    uint       `gorm:"column:id;primaryKey"`
	ResourceType          string     `gorm:"column:resource_type"`
	OwnerUserID           uint       `gorm:"column:owner_user_id"`
	Email                 string     `gorm:"column:email;uniqueIndex"`
	Identity              string     `gorm:"column:identity;uniqueIndex"`
	Password              string     `gorm:"column:password"`
	BindingEmail          string     `gorm:"column:binding_email"`
	TwoFactorSecret       string     `gorm:"column:two_factor_secret"`
	AppPassword           string     `gorm:"column:app_password"`
	CredentialRevision    uint64     `gorm:"column:credential_revision"`
	CredentialUpdatedAt   time.Time  `gorm:"column:credential_updated_at"`
	ProviderCursor        uint64     `gorm:"column:provider_cursor"`
	ProviderSpamCursor    uint64     `gorm:"column:provider_spam_cursor"`
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

type gmailMaintenanceRunModel struct {
	ID                   uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	ResourceID           uint       `gorm:"column:resource_id;not null"`
	ValidationGeneration uint64     `gorm:"column:validation_generation;not null"`
	Kind                 string     `gorm:"column:kind;type:varchar(24);not null"`
	Status               string     `gorm:"column:status;type:varchar(24);not null"`
	Attempts             int        `gorm:"column:attempts;not null"`
	MaxAttempts          int        `gorm:"column:max_attempts;not null"`
	CredentialRevision   uint64     `gorm:"column:credential_revision;not null"`
	QueuedAt             time.Time  `gorm:"column:queued_at;not null"`
	StartedAt            *time.Time `gorm:"column:started_at"`
	FinishedAt           *time.Time `gorm:"column:finished_at"`
	LastSafeError        string     `gorm:"column:last_safe_error;type:varchar(500);not null"`
	CreatedAt            time.Time  `gorm:"column:created_at;not null"`
	UpdatedAt            time.Time  `gorm:"column:updated_at;not null"`
}

func (gmailMaintenanceRunModel) TableName() string { return "gmail_maintenance_runs" }

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
	ID                    uint                   `json:"id"`
	Version               uint64                 `json:"version"`
	OwnerUserID           uint                   `json:"ownerUserId"`
	Owner                 LocalResourceOwner     `json:"owner"`
	Email                 string                 `json:"email"`
	BindingEmail          string                 `json:"bindingEmail,omitempty"`
	Status                string                 `json:"status"`
	ForSale               bool                   `json:"forSale"`
	PasswordConfigured    bool                   `json:"passwordConfigured"`
	TwoFactorConfigured   bool                   `json:"twoFactorConfigured"`
	AppPasswordConfigured bool                   `json:"appPasswordConfigured"`
	CredentialRevision    uint64                 `json:"credentialRevision"`
	CredentialUpdatedAt   time.Time              `json:"credentialUpdatedAt"`
	ValidationFailures    int                    `json:"validationFailures"`
	LastAllocatedAt       *time.Time             `json:"lastAllocatedAt,omitempty"`
	CooldownUntil         *time.Time             `json:"cooldownUntil"`
	ProjectCooldowns      []LocalProjectCooldown `json:"projectCooldowns,omitempty"`
	LastSafeError         string                 `json:"lastSafeError,omitempty"`
	LastCheckedAt         *time.Time             `json:"lastCheckedAt,omitempty"`
	CreatedAt             time.Time              `json:"createdAt"`
	UpdatedAt             time.Time              `json:"updatedAt"`
}

type LocalProjectCooldown struct {
	ProjectID     uint       `json:"projectId"`
	ProjectName   string     `json:"projectName"`
	CooldownUntil *time.Time `json:"cooldownUntil"`
}

type LocalResourceOwner struct {
	ID        uint   `json:"id"`
	Email     string `json:"email"`
	Nickname  string `json:"nickname"`
	GroupName string `json:"groupName"`
	Role      string `json:"role"`
	Enabled   bool   `json:"enabled"`
}

type LocalResourceBooleanFacets struct {
	All int64 `json:"all"`
	Yes int64 `json:"yes"`
	No  int64 `json:"no"`
}

type LocalResourceFacets struct {
	All         int64                      `json:"all"`
	Pending     int64                      `json:"pending"`
	Validating  int64                      `json:"validating"`
	Identifying int64                      `json:"identifying"`
	Normal      int64                      `json:"normal"`
	Cooldown    int64                      `json:"cooldown"`
	Abnormal    int64                      `json:"abnormal"`
	Disabled    int64                      `json:"disabled"`
	Deleted     int64                      `json:"deleted"`
	ForSale     LocalResourceBooleanFacets `json:"forSale"`
}

type LocalResourceList struct {
	Items  []LocalResourceItem `json:"items"`
	Total  int64               `json:"total"`
	Offset int                 `json:"offset"`
	Limit  int                 `json:"limit"`
	Facets LocalResourceFacets `json:"facets"`
}
