package domain

import (
	"strings"
	"time"
	"unicode"
)

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
	MicrosoftStatusDeleted  MicrosoftResourceStatus = "deleted"
)

// MailDomainStatus represents the status of a domain resource.
type MailDomainStatus string

const (
	DomainStatusNormal   MailDomainStatus = "normal"
	DomainStatusAbnormal MailDomainStatus = "abnormal"
	DomainStatusDisabled MailDomainStatus = "disabled"
	DomainStatusDeleted  MailDomainStatus = "deleted"
)

// ResourcePurpose represents the purpose of a domain resource.
type ResourcePurpose string

const (
	PurposeNotSale ResourcePurpose = "not_sale"
	PurposeSale    ResourcePurpose = "sale"
	PurposeBinding ResourcePurpose = "binding"
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
	return p == PurposeNotSale || p == PurposeSale || p == PurposeBinding
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
	case MicrosoftStatusPending, MicrosoftStatusNormal, MicrosoftStatusAbnormal, MicrosoftStatusDisabled, MicrosoftStatusDeleted:
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
	case MicrosoftStatusDeleted:
		return false
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

// MarkDeleted applies the user private-delete command. deleted is a terminal
// command state, not a normal validation-state transition.
func (r *MicrosoftResource) MarkDeleted() error {
	if r.Status == MicrosoftStatusDeleted {
		return ErrResourceNotFound
	}
	if r.ForSale {
		return ErrResourceNotPrivate
	}
	r.Status = MicrosoftStatusDeleted
	r.LastSafeError = ""
	r.LastAllocatedAt = nil
	return nil
}

// IsAllocatable returns true if the Microsoft resource can be allocated.
func (r *MicrosoftResource) IsAllocatable() bool {
	return r.Status == MicrosoftStatusNormal && r.ForSale
}

// MicrosoftImportLine represents a single line from a Microsoft TXT import file.
type MicrosoftImportLine struct {
	LineNumber     int
	Email          string
	Password       string
	ClientID       string
	RefreshToken   string
	BindingAddress string
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
	case DomainStatusNormal, DomainStatusAbnormal, DomainStatusDisabled, DomainStatusDeleted:
		return true
	default:
		return false
	}
}

// CanTransitionDomainStatus returns true when a status transition follows the Core state machine.
func CanTransitionDomainStatus(from, to MailDomainStatus) bool {
	switch from {
	case DomainStatusAbnormal:
		return to == DomainStatusNormal || to == DomainStatusDisabled
	case DomainStatusNormal:
		return to == DomainStatusAbnormal || to == DomainStatusDisabled
	case DomainStatusDisabled:
		return to == DomainStatusAbnormal
	case DomainStatusDeleted:
		return false
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

// MarkDeleted applies the user private-delete command. deleted is a terminal
// command state, not a normal validation-state transition.
func (r *MailDomainResource) MarkDeleted() error {
	if r.Status == DomainStatusDeleted {
		return ErrResourceNotFound
	}
	if r.Purpose != PurposeNotSale {
		return ErrResourceNotPrivate
	}
	r.Status = DomainStatusDeleted
	r.LastAllocatedAt = nil
	return nil
}

// IsAllocatable returns true if the domain resource can be allocated.
func (r *MailDomainResource) IsAllocatable() bool {
	return r.Purpose == PurposeSale &&
		r.Status == DomainStatusNormal
}

var knownTwoPartTLDs = map[string]struct{}{
	"ac.jp":  {},
	"ac.th":  {},
	"ac.uk":  {},
	"co.in":  {},
	"co.jp":  {},
	"co.kr":  {},
	"co.nz":  {},
	"co.th":  {},
	"co.uk":  {},
	"co.za":  {},
	"com.ar": {},
	"com.au": {},
	"com.br": {},
	"com.cn": {},
	"com.hk": {},
	"com.mx": {},
	"com.sg": {},
	"com.tw": {},
	"edu.cn": {},
	"edu.hk": {},
	"gen.in": {},
	"go.jp":  {},
	"go.th":  {},
	"gov.cn": {},
	"gov.uk": {},
	"ne.jp":  {},
	"ne.kr":  {},
	"net.au": {},
	"net.ar": {},
	"net.br": {},
	"net.cn": {},
	"net.hk": {},
	"net.in": {},
	"net.nz": {},
	"net.sg": {},
	"net.th": {},
	"net.tw": {},
	"net.za": {},
	"or.jp":  {},
	"or.kr":  {},
	"or.th":  {},
	"org.ar": {},
	"org.au": {},
	"org.br": {},
	"org.cn": {},
	"org.hk": {},
	"org.in": {},
	"org.mx": {},
	"org.nz": {},
	"org.sg": {},
	"org.tw": {},
	"org.uk": {},
	"org.za": {},
}

// NormalizeDomainName returns the canonical ASCII domain form accepted by Core.
func NormalizeDomainName(value string) (string, error) {
	canonical := normalizeDomainInput(value)
	if canonical == "" || !strings.Contains(canonical, ".") {
		return "", ErrInvalidDomain
	}
	if err := validateDomainLabels(canonical); err != nil {
		return "", ErrInvalidDomain
	}
	return canonical, nil
}

// NormalizeDomainSuffix returns a canonical suffix with a leading dot.
func NormalizeDomainSuffix(value string) (string, error) {
	suffix := normalizeDomainInput(strings.TrimPrefix(strings.TrimSpace(value), "."))
	if suffix == "" {
		return "", ErrInvalidDomain
	}
	if err := validateDomainLabels(suffix); err != nil {
		return "", ErrInvalidDomain
	}
	return "." + suffix, nil
}

// TLD extracts the normalized suffix used by resource filters.
func TLD(value string) string {
	canonical, err := NormalizeDomainName(value)
	if err != nil {
		return ""
	}

	parts := strings.Split(canonical, ".")
	if len(parts) < 2 {
		return ""
	}

	lastTwo := strings.Join(parts[len(parts)-2:], ".")
	if _, ok := knownTwoPartTLDs[lastTwo]; ok {
		return "." + lastTwo
	}

	return "." + parts[len(parts)-1]
}

func normalizeDomainInput(value string) string {
	canonical := strings.ToLower(strings.TrimSpace(value))
	canonical = strings.TrimSuffix(canonical, ".")
	return canonical
}

func validateDomainLabels(value string) error {
	if len(value) == 0 || len(value) > 253 {
		return ErrInvalidDomain
	}
	if strings.Contains(value, "://") ||
		strings.ContainsAny(value, `/\:*@`) ||
		strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		return ErrInvalidDomain
	}

	labels := strings.Split(value, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return ErrInvalidDomain
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return ErrInvalidDomain
		}
		for i := 0; i < len(label); i++ {
			ch := label[i]
			if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' {
				continue
			}
			return ErrInvalidDomain
		}
	}
	return nil
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
