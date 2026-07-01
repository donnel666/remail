package domain

import "time"

// ResourceType represents the type of email resource.
type ResourceType string

const (
	ResourceTypeMicrosoft ResourceType = "microsoft"
	ResourceTypeDomain    ResourceType = "domain"
)

// MicrosoftResourceStatus represents the status of a Microsoft resource.
type MicrosoftResourceStatus string

const (
	MicrosoftStatusPending  MicrosoftResourceStatus = "pending"
	MicrosoftStatusNormal   MicrosoftResourceStatus = "normal"
	MicrosoftStatusAbnormal MicrosoftResourceStatus = "abnormal"
	MicrosoftStatusDisabled MicrosoftResourceStatus = "disabled"
)

// MailDomainStatus represents the status of a domain resource.
type MailDomainStatus string

const (
	DomainStatusDNSNormal   MailDomainStatus = "dns_normal"
	DomainStatusDNSAbnormal MailDomainStatus = "dns_abnormal"
	DomainStatusDisabled    MailDomainStatus = "disabled"
)

// ResourcePurpose represents the purpose of a domain resource.
type ResourcePurpose string

const (
	PurposeSale      ResourcePurpose = "sale"
	PurposeAuxiliary ResourcePurpose = "auxiliary"
)

// EmailResource is the root aggregate for all email resources.
// Each EmailResource has exactly one sub-resource record (MicrosoftResource or MailDomainResource).
type EmailResource struct {
	ID          uint
	Type        ResourceType
	OwnerUserID uint
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// IsValidResourceType returns true if the type is recognized.
func IsValidResourceType(t ResourceType) bool {
	return t == ResourceTypeMicrosoft || t == ResourceTypeDomain
}

// IsValidPurpose returns true if the purpose is recognized.
func IsValidPurpose(p ResourcePurpose) bool {
	return p == PurposeSale || p == PurposeAuxiliary
}

// --- MicrosoftResource ---

// MicrosoftResource holds Microsoft-specific resource fields.
type MicrosoftResource struct {
	ID              uint // Shared PK = EmailResource.ID
	EmailAddress    string
	Password        string // Original value, never in API responses or logs
	ClientID        string
	RefreshToken    string // Original value, never in API responses or logs
	LongLived       bool
	RTExpireAt      *time.Time
	ForSale         bool
	Status          MicrosoftResourceStatus
	QualityScore    int
	LastSafeError   string // Sanitized diagnostic summary
	LastAllocatedAt *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// IsValidMicrosoftStatus returns true if the status is a valid state.
func IsValidMicrosoftStatus(s string) bool {
	switch MicrosoftResourceStatus(s) {
	case MicrosoftStatusPending, MicrosoftStatusNormal, MicrosoftStatusAbnormal, MicrosoftStatusDisabled:
		return true
	default:
		return false
	}
}

// CanTransitionMicrosoftStatus returns true when a status transition follows the Core state machine.
func CanTransitionMicrosoftStatus(from, to MicrosoftResourceStatus) bool {
	switch from {
	case MicrosoftStatusPending:
		return to == MicrosoftStatusNormal || to == MicrosoftStatusAbnormal
	case MicrosoftStatusNormal:
		return to == MicrosoftStatusAbnormal || to == MicrosoftStatusDisabled
	case MicrosoftStatusAbnormal:
		return to == MicrosoftStatusPending || to == MicrosoftStatusDisabled
	case MicrosoftStatusDisabled:
		return to == MicrosoftStatusPending
	default:
		return false
	}
}

// TransitionStatus moves the Microsoft resource to a legal next status.
func (r *MicrosoftResource) TransitionStatus(next MicrosoftResourceStatus) error {
	if !CanTransitionMicrosoftStatus(r.Status, next) {
		return ErrInvalidResourceStatus
	}
	r.Status = next
	return nil
}

// IsAllocatable returns true if the Microsoft resource can be allocated.
func (r *MicrosoftResource) IsAllocatable() bool {
	return r.Status == MicrosoftStatusNormal && r.ForSale
}

// MicrosoftImportLine represents a single line from a Microsoft TXT import file.
type MicrosoftImportLine struct {
	LineNumber       int
	Email            string
	Password         string
	ClientID         string
	RefreshToken     string
	AuxiliaryAddress string
}

// --- MailDomainResource ---

// MailDomainResource holds self-hosted domain-specific resource fields.
type MailDomainResource struct {
	ID              uint // Shared PK = EmailResource.ID
	Domain          string
	MailServerID    uint
	Purpose         ResourcePurpose
	Status          MailDomainStatus
	LastAllocatedAt *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// IsValidDomainStatus returns true if the status is a valid state.
func IsValidDomainStatus(s string) bool {
	switch MailDomainStatus(s) {
	case DomainStatusDNSNormal, DomainStatusDNSAbnormal, DomainStatusDisabled:
		return true
	default:
		return false
	}
}

// CanTransitionDomainStatus returns true when a status transition follows the Core state machine.
func CanTransitionDomainStatus(from, to MailDomainStatus) bool {
	switch from {
	case DomainStatusDNSAbnormal:
		return to == DomainStatusDNSNormal || to == DomainStatusDisabled
	case DomainStatusDNSNormal:
		return to == DomainStatusDNSAbnormal || to == DomainStatusDisabled
	case DomainStatusDisabled:
		return to == DomainStatusDNSAbnormal
	default:
		return false
	}
}

// TransitionStatus moves the domain resource to a legal next status.
func (r *MailDomainResource) TransitionStatus(next MailDomainStatus) error {
	if !CanTransitionDomainStatus(r.Status, next) {
		return ErrInvalidResourceStatus
	}
	r.Status = next
	return nil
}

// IsAllocatable returns true if the domain resource can be allocated.
func (r *MailDomainResource) IsAllocatable() bool {
	return r.Purpose == PurposeSale &&
		r.Status == DomainStatusDNSNormal
}

// --- ExplicitAlias ---

type AliasStatus string

const (
	AliasNormal   AliasStatus = "normal"
	AliasDisabled AliasStatus = "disabled"
)

type ExplicitAlias struct {
	ID         uint
	ResourceID uint
	Email      string
	Status     AliasStatus
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type DotAlias struct {
	ID         uint
	ResourceID uint
	Email      string
	Status     AliasStatus
	CreatedAt  time.Time
}

type PlusAlias struct {
	ID         uint
	ResourceID uint
	Email      string
	Status     AliasStatus
	CreatedAt  time.Time
}
