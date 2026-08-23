package icloud

import (
	"context"
	"errors"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/platform"
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

	iCloudChannelWeb           = "icloud_web"
	iCloudChannelAppleAccount  = "apple_account"
	iCloudChannelFamilySession = "family_session"

	iCloudImportProcessing = "processing"
	iCloudImportImported   = "imported"
	iCloudImportFailed     = "failed"
)

var (
	ErrICloudImportInvalid             = errors.New("icloud: invalid import command")
	ErrICloudImportInvalidOwner        = errors.New("icloud: invalid resource import owner")
	ErrICloudImportConflict            = errors.New("icloud: import idempotency conflict")
	ErrICloudImportNotFound            = errors.New("icloud: import not found")
	ErrICloudImportDependency          = errors.New("icloud: import dependency unavailable")
	ErrICloudImportStorage             = errors.New("icloud: import storage unavailable")
	ErrICloudImportTemporary           = errors.New("icloud: import temporarily unavailable")
	ErrICloudImportClaim               = errors.New("icloud: import claim is no longer valid")
	ErrICloudImportPreparationNotFound = errors.New("icloud: import preparation not found")
	ErrICloudImportPreparationConflict = errors.New("icloud: import preparation is no longer usable")
	ErrICloudForwardingUnavailable     = errors.New("icloud: no forwarding mailbox domain is available")
	ErrICloudValidationTemp            = errors.New("icloud: validation temporarily unavailable")
	ErrICloudMailUnavailable           = errors.New("icloud: forwarded mail temporarily unavailable")
	ErrICloudResourceNotFound          = errors.New("icloud: resource not found")
	ErrICloudResourceStatus            = errors.New("icloud: invalid resource status")
)

const iCloudMaxAliases = platform.ICloudMaxAliases

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

// Service owns iCloud import, forwarded-mail pickup, and alias provisioning.
type Service struct {
	db                  *gorm.DB
	queue               *asynq.Client
	files               governanceapp.FilePort
	operationLogs       *governanceinfra.OperationLogRepo
	systemLogs          *governanceinfra.SystemLogRepo
	hme                 *HMEClient
	apple               *AppleAccountClient
	family              *iCloudFamilyClient
	appleRoutes         *appleRouteManager
	onboardingApple     AppleOnboardingProvider
	smsPhones           SMSPhoneService
	now                 func() time.Time
	validateImportOwner func(context.Context, uint) (bool, error)
	backgroundExecution BackgroundExecutionGate
}

func NewService(db *gorm.DB, queue *asynq.Client, files governanceapp.FilePort) *Service {
	appleRoutes := newAppleRouteManager()
	return &Service{
		db:              db,
		queue:           queue,
		files:           files,
		operationLogs:   governanceinfra.NewOperationLogRepo(db),
		systemLogs:      governanceinfra.NewSystemLogRepo(db),
		hme:             newRoutedHMEClient(appleRoutes),
		apple:           newRoutedAppleAccountClient(appleRoutes),
		family:          newRoutedICloudFamilyClient(appleRoutes),
		appleRoutes:     appleRoutes,
		onboardingApple: newRoutedAppleOnboardingProvider(appleRoutes),
		now:             time.Now,
	}
}

func (s *Service) SetAppleProxyProvider(provider AppleProxyProvider) {
	if s == nil {
		return
	}
	if s.appleRoutes == nil {
		s.appleRoutes = newAppleRouteManager()
		if s.hme == nil {
			s.hme = newRoutedHMEClient(s.appleRoutes)
		}
		if s.apple == nil {
			s.apple = newRoutedAppleAccountClient(s.appleRoutes)
		}
		if s.family == nil {
			s.family = newRoutedICloudFamilyClient(s.appleRoutes)
		}
		if s.onboardingApple == nil {
			s.onboardingApple = newRoutedAppleOnboardingProvider(s.appleRoutes)
		}
	}
	s.appleRoutes.proxies = provider
}

func (s *Service) SetICloudSMSPhoneService(service SMSPhoneService) {
	if s != nil {
		s.smsPhones = service
	}
}

func (s *Service) SetAppleOnboardingProvider(provider AppleOnboardingProvider) {
	if s != nil && provider != nil {
		s.onboardingApple = provider
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
	AccountRole             string     `gorm:"column:account_role"`
	FamilyPrimaryResourceID *uint      `gorm:"column:family_primary_resource_id"`
	Region                  string     `gorm:"column:region"`
	CountryCode             string     `gorm:"column:country_code"`
	ICloudOpened            bool       `gorm:"column:icloud_opened"`
	BoundPhoneNumber        string     `gorm:"column:bound_phone_number"`
	BoundPhoneCountryCode   string     `gorm:"column:bound_phone_country_code"`
	BoundPhoneSource        string     `gorm:"column:bound_phone_source"`
	KitesimPhoneID          *uint      `gorm:"column:kitesim_phone_id"`
	FamilyInviteURL         string     `gorm:"column:family_invite_url"`
	FamilyID                string     `gorm:"column:family_id"`
	FamilyOrganizerDSID     string     `gorm:"column:family_organizer_dsid"`
	FamilyRemoteMemberCount uint8      `gorm:"column:family_remote_member_count"`
	FamilySyncStatus        string     `gorm:"column:family_sync_status"`
	FamilySyncedAt          *time.Time `gorm:"column:family_synced_at"`
	FamilyNextSyncAt        *time.Time `gorm:"column:family_next_sync_at"`
	FamilySyncErrorCategory string     `gorm:"column:family_sync_error_category"`
	SelectedForwardTo       string     `gorm:"column:selected_forward_to"`
	RequiredForwardTo       string     `gorm:"column:required_forward_to"`
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
	// Onboarding state lives on the resource row. Redis/Asynq only carries the
	// resource id and generation; this is the durable source of truth.
	WorkflowImportID             *uint      `gorm:"column:import_id"`
	WorkflowResourceID           *uint      `gorm:"column:resource_id"`
	WorkflowTaskKind             string     `gorm:"column:task_kind"`
	WorkflowLineNumber           int        `gorm:"column:line_number"`
	WorkflowFamilyReservation    bool       `gorm:"column:family_reservation_confirmed"`
	WorkflowSecretPayload        iCloudJSON `gorm:"column:secret_payload;type:json;serializer:json"`
	WorkflowSessionPayload       iCloudJSON `gorm:"column:session_payload;type:json;serializer:json"`
	WorkflowManualCode           string     `gorm:"column:manual_verification_code"`
	WorkflowSMSPurpose           string     `gorm:"column:pending_sms_purpose"`
	WorkflowSMSSentAt            *time.Time `gorm:"column:sms_sent_at"`
	WorkflowSMSPollDeadline      *time.Time `gorm:"column:sms_poll_deadline"`
	WorkflowForwardPreparationID *uint      `gorm:"column:forward_preparation_id"`
	OnboardingStatus             string     `gorm:"column:onboarding_status"`
	WorkflowStage                string     `gorm:"column:stage"`
	WorkflowDispatchStatus       string     `gorm:"column:dispatch_status"`
	WorkflowGeneration           uint64     `gorm:"column:generation"`
	WorkflowExpectedCredential   uint64     `gorm:"column:expected_credential_revision"`
	WorkflowClaimToken           string     `gorm:"column:claim_token"`
	WorkflowAttempts             int        `gorm:"column:attempts"`
	WorkflowMaxAttempts          int        `gorm:"column:max_attempts"`
	WorkflowStageAttempts        int        `gorm:"column:stage_attempts"`
	WorkflowNextAttemptAt        *time.Time `gorm:"column:next_attempt_at"`
	WorkflowLastErrorCategory    string     `gorm:"column:last_error_category"`
	WorkflowStartedAt            *time.Time `gorm:"column:started_at"`
	WorkflowFinishedAt           *time.Time `gorm:"column:finished_at"`
	WorkflowActivationConfirmed  *time.Time `gorm:"column:icloud_activation_confirmed_at"`
	WorkflowOperatorUserID       uint       `gorm:"column:onboarding_operator_user_id"`
	WorkflowRequestID            string     `gorm:"column:onboarding_request_id"`
	WorkflowIdempotencyKey       string     `gorm:"column:onboarding_idempotency_key"`
	WorkflowRequestFingerprint   string     `gorm:"column:onboarding_request_fingerprint"`
	CreatedAt                    time.Time  `gorm:"column:created_at"`
	UpdatedAt                    time.Time  `gorm:"column:updated_at"`
}

func (iCloudResourceModel) TableName() string { return "icloud_resources" }

type iCloudResourceChannelModel struct {
	ID                    uint       `gorm:"column:id;primaryKey;autoIncrement"`
	ResourceID            uint       `gorm:"column:resource_id"`
	Kind                  string     `gorm:"column:kind"`
	Host                  string     `gorm:"column:host"`
	Cookie                string     `gorm:"column:cookie"`
	SetupCookie           string     `gorm:"column:setup_cookie"`
	Origin                string     `gorm:"column:origin"`
	Referer               string     `gorm:"column:referer"`
	UserAgent             string     `gorm:"column:user_agent"`
	FDClientInfo          string     `gorm:"column:fd_client_info"`
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
		Cookie: m.Cookie, SetupCookie: m.SetupCookie, Origin: m.Origin, Referer: m.Referer, UserAgent: m.UserAgent,
	}
}

type iCloudAliasModel struct {
	ID                uint       `gorm:"column:id;primaryKey;autoIncrement"`
	ResourceID        uint       `gorm:"column:resource_id"`
	AnonymousID       string     `gorm:"column:anonymous_id"`
	Email             string     `gorm:"column:email"`
	Label             string     `gorm:"column:label"`
	Note              string     `gorm:"column:note"`
	ForwardToEmail    string     `gorm:"column:forward_to_email"`
	Origin            string     `gorm:"column:origin"`
	ProviderDomain    string     `gorm:"column:provider_domain"`
	RecipientMailID   string     `gorm:"column:recipient_mail_id"`
	Status            string     `gorm:"column:status"`
	ProviderCreatedAt *time.Time `gorm:"column:provider_created_at"`
	LastSeenAt        *time.Time `gorm:"column:last_seen_at"`
	LastAllocatedAt   *time.Time `gorm:"column:last_allocated_at"`
	CreatedAt         time.Time  `gorm:"column:created_at"`
	UpdatedAt         time.Time  `gorm:"column:updated_at"`
}

func (iCloudAliasModel) TableName() string { return "icloud_aliases" }

type iCloudDotAliasModel struct {
	ID         uint      `gorm:"column:id;primaryKey;autoIncrement"`
	ResourceID uint      `gorm:"column:resource_id;uniqueIndex:uk_icloud_dot_aliases_resource_email"`
	AliasID    uint      `gorm:"column:alias_id"`
	Email      string    `gorm:"column:email;uniqueIndex:uk_icloud_dot_aliases_resource_email"`
	Status     string    `gorm:"column:status"`
	CreatedAt  time.Time `gorm:"column:created_at"`
}

func (iCloudDotAliasModel) TableName() string { return "icloud_dot_aliases" }

type iCloudPlusAliasModel struct {
	ID         uint      `gorm:"column:id;primaryKey;autoIncrement"`
	ResourceID uint      `gorm:"column:resource_id;uniqueIndex:uk_icloud_plus_aliases_resource_email"`
	AliasID    uint      `gorm:"column:alias_id"`
	Email      string    `gorm:"column:email;uniqueIndex:uk_icloud_plus_aliases_resource_email"`
	Status     string    `gorm:"column:status"`
	CreatedAt  time.Time `gorm:"column:created_at"`
}

func (iCloudPlusAliasModel) TableName() string { return "icloud_plus_aliases" }

// iCloudAliasRouteModel keeps old Apple relay route pairs addressable when
// Apple changes the selected forwarding mailbox for an existing alias.
type iCloudAliasRouteModel struct {
	ID              uint      `gorm:"column:id;primaryKey;autoIncrement"`
	ResourceID      uint      `gorm:"column:resource_id;not null"`
	AliasID         uint      `gorm:"column:alias_id;not null"`
	ForwardToEmail  string    `gorm:"column:forward_to_email;not null"`
	RecipientMailID string    `gorm:"column:recipient_mail_id;not null"`
	FirstSeenAt     time.Time `gorm:"column:first_seen_at;not null"`
	LastSeenAt      time.Time `gorm:"column:last_seen_at;not null"`
}

func (iCloudAliasRouteModel) TableName() string { return "icloud_alias_routes" }

type iCloudImportModel struct {
	ID                 uint       `gorm:"column:id;primaryKey;autoIncrement"`
	OwnerUserID        uint       `gorm:"column:owner_user_id"`
	OperatorUserID     uint       `gorm:"column:operator_user_id"`
	SourceObjectKey    string     `gorm:"column:source_object_key"`
	FailureObjectKey   string     `gorm:"column:failure_object_key"`
	Status             string     `gorm:"column:status"`
	ErrorStrategy      string     `gorm:"column:error_strategy"`
	ResourceExpireAt   time.Time  `gorm:"column:resource_expire_at"`
	PreparationID      *uint      `gorm:"column:preparation_id"`
	ForwardToEmail     string     `gorm:"column:forward_to_email"`
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

type iCloudImportPreparationModel struct {
	ID                    uint       `gorm:"column:id;primaryKey;autoIncrement"`
	OperatorUserID        *uint      `gorm:"column:operator_user_id"`
	SystemKeyID           *uint      `gorm:"column:system_key_id"`
	DomainResourceID      uint       `gorm:"column:domain_resource_id"`
	ForwardToEmail        string     `gorm:"column:forward_to_email"`
	VerificationMessageID *uint      `gorm:"column:verification_message_id"`
	VerificationCode      string     `gorm:"column:verification_code"`
	VerifiedAt            *time.Time `gorm:"column:verified_at"`
	ExpiresAt             time.Time  `gorm:"column:expires_at"`
	ConsumedAt            *time.Time `gorm:"column:consumed_at"`
	CreatedAt             time.Time  `gorm:"column:created_at"`
	UpdatedAt             time.Time  `gorm:"column:updated_at"`
}

func (iCloudImportPreparationModel) TableName() string {
	return "icloud_import_preparations"
}

type ImportPreparationView struct {
	ID               uint
	ForwardToEmail   string
	Status           string
	VerificationCode string
	ExpiresAt        time.Time
	CreatedAt        time.Time
}

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
	PreserveResourceStatus     bool   `json:"preserveResourceStatus,omitempty"`
	MaintenanceRunID           uint64 `json:"maintenanceRunId,omitempty"`
	MaintenanceKind            string `json:"maintenanceKind,omitempty"`
	RequestID                  string `json:"requestId,omitempty"`
}

type iCloudMaintenanceRunModel struct {
	ID                   uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	ResourceID           uint       `gorm:"column:resource_id;not null;uniqueIndex:uk_icloud_maintenance_resource_generation,priority:1"`
	ValidationGeneration uint64     `gorm:"column:validation_generation;not null;uniqueIndex:uk_icloud_maintenance_resource_generation,priority:3"`
	Kind                 string     `gorm:"column:kind;type:varchar(24);not null;uniqueIndex:uk_icloud_maintenance_resource_generation,priority:2"`
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
