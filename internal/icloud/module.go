package icloud

import (
	"context"
	"errors"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

const (
	iCloudResourcePending    = "pending"
	iCloudResourceValidating = "validating"
	iCloudResourceNormal     = "normal"
	iCloudResourceAbnormal   = "abnormal"
	iCloudResourceDisabled   = "disabled"
	iCloudResourceDeleted    = "deleted"

	iCloudSessionUnchecked = "unchecked"
	iCloudSessionValid     = "valid"
	iCloudSessionInvalid   = "invalid"

	iCloudChannelWeb          = "icloud_web"
	iCloudChannelAppleAccount = "apple_account"

	iCloudImportProcessing = "processing"
	iCloudImportImported   = "imported"
	iCloudImportFailed     = "failed"
)

var (
	ErrICloudImportInvalid      = errors.New("icloud: invalid import command")
	ErrICloudImportInvalidOwner = errors.New("icloud: invalid resource import owner")
	ErrICloudImportConflict     = errors.New("icloud: import idempotency conflict")
	ErrICloudImportNotFound     = errors.New("icloud: import not found")
	ErrICloudImportDependency   = errors.New("icloud: import dependency unavailable")
	ErrICloudImportStorage      = errors.New("icloud: import storage unavailable")
	ErrICloudImportTemporary    = errors.New("icloud: import temporarily unavailable")
	ErrICloudImportClaim        = errors.New("icloud: import claim is no longer valid")
	ErrICloudValidationTemp     = errors.New("icloud: validation temporarily unavailable")
	ErrICloudMailUnavailable    = errors.New("icloud: IMAP mail temporarily unavailable")
	ErrICloudResourceNotFound   = errors.New("icloud: resource not found")
	ErrICloudResourceStatus     = errors.New("icloud: invalid resource status")
)

const iCloudMaxAliases = 750

func normalizeICloudResourceExpireAt(value time.Time) time.Time {
	return value.UTC().Truncate(time.Millisecond)
}

func validICloudResourceExpireAt(value, now time.Time) bool {
	return !value.IsZero() && value.After(now.UTC())
}

// BackgroundExecutionGate is the shared admission-control contract used by
// other background resource workers.
type BackgroundExecutionGate interface {
	TryAcquire() (release func(), admitted bool)
}

// Module is deliberately separate from Core so its import and validation
// contracts cannot change the stable Microsoft implementation.
type Module struct {
	Service *Service
}

func NewModule(db *gorm.DB, queue *asynq.Client, files governanceapp.FilePort) *Module {
	return &Module{Service: NewService(db, queue, files)}
}

// Service owns iCloud import, IMAP health, mail pickup, and alias provisioning.
type Service struct {
	db                  *gorm.DB
	queue               *asynq.Client
	files               governanceapp.FilePort
	operationLogs       *governanceinfra.OperationLogRepo
	systemLogs          *governanceinfra.SystemLogRepo
	hme                 *HMEClient
	apple               *AppleAccountClient
	imap                *iCloudIMAPClient
	now                 func() time.Time
	validateImportOwner func(context.Context, uint) (bool, error)
	backgroundExecution BackgroundExecutionGate
}

func NewService(db *gorm.DB, queue *asynq.Client, files governanceapp.FilePort) *Service {
	return &Service{
		db:            db,
		queue:         queue,
		files:         files,
		operationLogs: governanceinfra.NewOperationLogRepo(db),
		systemLogs:    governanceinfra.NewSystemLogRepo(db),
		hme:           NewHMEClient(nil),
		apple:         NewAppleAccountClient(nil),
		imap:          &iCloudIMAPClient{},
		now:           time.Now,
	}
}

func (s *Service) SetImportOwnerValidator(validate func(context.Context, uint) (bool, error)) {
	if s != nil {
		s.validateImportOwner = validate
	}
}

func (s *Service) SetBackgroundExecutionGate(gate BackgroundExecutionGate) {
	if s != nil {
		s.backgroundExecution = gate
	}
}

type iCloudRootModel struct {
	ID          uint      `gorm:"column:id;primaryKey;autoIncrement"`
	Type        string    `gorm:"column:type"`
	OwnerUserID uint      `gorm:"column:owner_user_id"`
	Version     uint64    `gorm:"column:version"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (iCloudRootModel) TableName() string { return "email_resources" }

// iCloudResourceModel holds only account state. Provider sessions belong to
// iCloudResourceChannelModel and are never exposed through an API.
type iCloudResourceModel struct {
	ID                      uint       `gorm:"column:id;primaryKey"`
	ResourceType            string     `gorm:"column:resource_type"`
	PrimaryEmail            string     `gorm:"column:primary_email"`
	IMAPAppPassword         string     `gorm:"column:imap_app_password"`
	IMAPUIDValidity         string     `gorm:"column:imap_uid_validity"`
	IMAPLastUID             uint64     `gorm:"column:imap_last_uid"`
	IMAPLastSyncAt          *time.Time `gorm:"column:imap_last_sync_at"`
	ExpireAt                time.Time  `gorm:"column:expire_at"`
	ForSale                 bool       `gorm:"column:for_sale"`
	Status                  string     `gorm:"column:status"`
	CredentialRevision      uint64     `gorm:"column:credential_revision"`
	CredentialUpdatedAt     time.Time  `gorm:"column:credential_updated_at"`
	ValidationGeneration    uint64     `gorm:"column:validation_generation"`
	ValidationFailures      uint8      `gorm:"column:validation_failures"`
	AliasCount              uint       `gorm:"column:alias_count"`
	AliasProvisionCandidate string     `gorm:"column:alias_provision_candidate"`
	AliasProvisionReconcile bool       `gorm:"column:alias_provision_reconcile"`
	NextValidationAt        *time.Time `gorm:"column:next_validation_at"`
	NextProvisionAt         *time.Time `gorm:"column:next_provision_at"`
	LastCheckedAt           *time.Time `gorm:"column:last_checked_at"`
	LastValidAt             *time.Time `gorm:"column:last_valid_at"`
	LastAliasSyncAt         *time.Time `gorm:"column:last_alias_sync_at"`
	LastAllocatedAt         *time.Time `gorm:"column:last_allocated_at"`
	LastSafeError           string     `gorm:"column:last_safe_error"`
	CreatedAt               time.Time  `gorm:"column:created_at"`
	UpdatedAt               time.Time  `gorm:"column:updated_at"`
}

func (iCloudResourceModel) TableName() string { return "icloud_resources" }

type iCloudResourceChannelModel struct {
	ID                    uint       `gorm:"column:id;primaryKey;autoIncrement"`
	ResourceID            uint       `gorm:"column:resource_id"`
	Kind                  string     `gorm:"column:kind"`
	Host                  string     `gorm:"column:host"`
	Cookie                string     `gorm:"column:cookie"`
	Origin                string     `gorm:"column:origin"`
	Referer               string     `gorm:"column:referer"`
	UserAgent             string     `gorm:"column:user_agent"`
	DSID                  string     `gorm:"column:dsid"`
	ClientID              string     `gorm:"column:client_id"`
	ClientBuildNumber     string     `gorm:"column:client_build_number"`
	ClientMasteringNumber string     `gorm:"column:client_mastering_number"`
	Scnt                  string     `gorm:"column:scnt"`
	SessionID             string     `gorm:"column:session_id"`
	APIKey                string     `gorm:"column:api_key"`
	DataAccessToken       string     `gorm:"column:data_access_token"`
	ManageExpiresAt       *time.Time `gorm:"column:manage_expires_at"`
	SessionStatus         string     `gorm:"column:session_status"`
	SessionFailures       uint8      `gorm:"column:session_failures"`
	CooldownUntil         *time.Time `gorm:"column:cooldown_until"`
	CooldownStage         uint8      `gorm:"column:cooldown_stage"`
	NextKeepaliveAt       *time.Time `gorm:"column:next_keepalive_at"`
	LastCheckedAt         *time.Time `gorm:"column:last_checked_at"`
	LastValidAt           *time.Time `gorm:"column:last_valid_at"`
	ProvisionWindowAt     *time.Time `gorm:"column:provision_window_at"`
	ProvisionWindowCount  uint8      `gorm:"column:provision_window_count"`
	CreatedAt             time.Time  `gorm:"column:created_at"`
	UpdatedAt             time.Time  `gorm:"column:updated_at"`
}

func (iCloudResourceChannelModel) TableName() string { return "icloud_resource_channels" }

func (m iCloudResourceChannelModel) hmeConfig() hmeConfig {
	return hmeConfig{
		Host: m.Host, DSID: m.DSID, ClientID: m.ClientID,
		ClientBuildNumber: m.ClientBuildNumber, ClientMasteringNumber: m.ClientMasteringNumber,
		Cookie: m.Cookie, Origin: m.Origin, Referer: m.Referer, UserAgent: m.UserAgent,
	}
}

type iCloudAliasModel struct {
	ID                uint       `gorm:"column:id;primaryKey;autoIncrement"`
	ResourceID        uint       `gorm:"column:resource_id"`
	AnonymousID       string     `gorm:"column:anonymous_id"`
	Email             string     `gorm:"column:email"`
	Label             string     `gorm:"column:label"`
	Note              string     `gorm:"column:note"`
	Origin            string     `gorm:"column:origin"`
	Status            string     `gorm:"column:status"`
	ProviderCreatedAt *time.Time `gorm:"column:provider_created_at"`
	LastSeenAt        *time.Time `gorm:"column:last_seen_at"`
	LastAllocatedAt   *time.Time `gorm:"column:last_allocated_at"`
	CreatedAt         time.Time  `gorm:"column:created_at"`
	UpdatedAt         time.Time  `gorm:"column:updated_at"`
}

func (iCloudAliasModel) TableName() string { return "icloud_aliases" }

type iCloudImportModel struct {
	ID                 uint       `gorm:"column:id;primaryKey;autoIncrement"`
	OwnerUserID        uint       `gorm:"column:owner_user_id"`
	OperatorUserID     uint       `gorm:"column:operator_user_id"`
	SourceObjectKey    string     `gorm:"column:source_object_key"`
	FailureObjectKey   string     `gorm:"column:failure_object_key"`
	Status             string     `gorm:"column:status"`
	ErrorStrategy      string     `gorm:"column:error_strategy"`
	ResourceExpireAt   time.Time  `gorm:"column:resource_expire_at"`
	ImportedCount      int        `gorm:"column:imported_count"`
	AcceptedCount      int        `gorm:"column:accepted_count"`
	SkippedCount       int        `gorm:"column:skipped_count"`
	LastSafeError      string     `gorm:"column:last_safe_error"`
	RequestID          string     `gorm:"column:request_id"`
	Path               string     `gorm:"column:path"`
	IdempotencyKey     string     `gorm:"column:idempotency_key"`
	RequestFingerprint string     `gorm:"column:request_fingerprint"`
	DispatchStatus     string     `gorm:"column:dispatch_status"`
	Generation         uint64     `gorm:"column:generation"`
	Attempts           int        `gorm:"column:attempts"`
	MaxAttempts        int        `gorm:"column:max_attempts"`
	ClaimToken         string     `gorm:"column:claim_token"`
	StartedAt          *time.Time `gorm:"column:started_at"`
	FinishedAt         *time.Time `gorm:"column:finished_at"`
	CreatedAt          time.Time  `gorm:"column:created_at"`
	UpdatedAt          time.Time  `gorm:"column:updated_at"`
}

func (iCloudImportModel) TableName() string { return "icloud_resource_imports" }

type iCloudImportItemModel struct {
	ID            uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	ImportID      uint      `gorm:"column:import_id"`
	ResourceID    *uint     `gorm:"column:resource_id"`
	LineNumber    int       `gorm:"column:line_number"`
	Outcome       string    `gorm:"column:outcome"`
	Category      string    `gorm:"column:category"`
	LastSafeError string    `gorm:"column:last_safe_error"`
	CreatedAt     time.Time `gorm:"column:created_at"`
}

func (iCloudImportItemModel) TableName() string { return "icloud_resource_import_items" }

// ImportStatusView is deliberately artifact- and secret-free.
type ImportStatusView struct {
	ImportID      uint
	Status        string
	Accepted      int
	Imported      int
	Skipped       int
	TaskStatus    string
	Attempts      int
	MaxAttempts   int
	StartedAt     *time.Time
	FinishedAt    *time.Time
	LastSafeError string
	RequestID     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (m iCloudImportModel) statusView() *ImportStatusView {
	return &ImportStatusView{
		ImportID: m.ID, Status: m.Status, Accepted: m.AcceptedCount, Imported: m.ImportedCount,
		Skipped: m.SkippedCount, TaskStatus: m.DispatchStatus, Attempts: m.Attempts,
		MaxAttempts: m.MaxAttempts, StartedAt: m.StartedAt, FinishedAt: m.FinishedAt,
		LastSafeError: m.LastSafeError, RequestID: m.RequestID, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
}

type iCloudImportTask struct {
	ImportID   uint   `json:"importId"`
	Generation uint64 `json:"generation"`
}

type iCloudValidationTask struct {
	ResourceID                 uint   `json:"resourceId"`
	OwnerUserID                uint   `json:"ownerUserId"`
	ValidationGeneration       uint64 `json:"validationGeneration"`
	ExpectedCredentialRevision uint64 `json:"expectedCredentialRevision"`
	MaintenanceRunID           uint64 `json:"maintenanceRunId,omitempty"`
	MaintenanceKind            string `json:"maintenanceKind,omitempty"`
	RequestID                  string `json:"requestId,omitempty"`
}

type iCloudMaintenanceRunModel struct {
	ID                   uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	ResourceID           uint       `gorm:"column:resource_id;not null;uniqueIndex:uk_icloud_maintenance_resource_generation,priority:1"`
	ValidationGeneration uint64     `gorm:"column:validation_generation;not null;uniqueIndex:uk_icloud_maintenance_resource_generation,priority:2"`
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

func (iCloudMaintenanceRunModel) TableName() string { return "icloud_maintenance_runs" }
