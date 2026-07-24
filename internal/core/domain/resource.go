package domain

import (
	"strings"
	"time"
	"unicode"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

// ResourceType represents the type of email resource.
type ResourceType string

const (
	ResourceTypeMicrosoft ResourceType = "microsoft"
	ResourceTypeDomain    ResourceType = "domain"
)

const (
	DefaultPlusDailyLimit    = 10000
	DefaultMailboxDailyLimit = 10000
	maxConfiguredDailyLimit  = 2_147_483_647
)

// MicrosoftResourceStatus represents the status of a Microsoft resource.
type MicrosoftResourceStatus string

const (
	MicrosoftStatusPending     MicrosoftResourceStatus = "pending"
	MicrosoftStatusValidating  MicrosoftResourceStatus = "validating"
	MicrosoftStatusIdentifying MicrosoftResourceStatus = "identifying"
	MicrosoftStatusNormal      MicrosoftResourceStatus = "normal"
	MicrosoftStatusAbnormal    MicrosoftResourceStatus = "abnormal"
	MicrosoftStatusDisabled    MicrosoftResourceStatus = "disabled"
	MicrosoftStatusDeleted     MicrosoftResourceStatus = "deleted"
)

// MailDomainStatus represents the status of a domain resource.
type MailDomainStatus string

const (
	DomainStatusPending    MailDomainStatus = "pending"
	DomainStatusValidating MailDomainStatus = "validating"
	DomainStatusNormal     MailDomainStatus = "normal"
	DomainStatusAbnormal   MailDomainStatus = "abnormal"
	DomainStatusDisabled   MailDomainStatus = "disabled"
	DomainStatusDeleted    MailDomainStatus = "deleted"
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
	Version     uint64
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
	ID                   uint // Shared PK = EmailResource.ID
	EmailAddress         string
	Password             string // Original value, never in API responses or logs
	ClientID             string
	RefreshToken         string // Original value, never in API responses or logs
	CredentialRevision   uint64
	CredentialUpdatedAt  time.Time
	LongLived            bool
	GraphAvailable       bool
	RTExpireAt           *time.Time
	TokenLastRefreshedAt *time.Time
	TokenLastRequestID   string
	ForSale              bool
	Status               MicrosoftResourceStatus
	ValidationGeneration uint64
	ValidationFailures   int
	QualityScore         int
	PlusDailyLimit       int
	LastSafeError        string // Sanitized diagnostic summary
	LastAllocatedAt      *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// IsValidMicrosoftStatus returns true if the status is a valid state.
func IsValidMicrosoftStatus(s string) bool {
	switch MicrosoftResourceStatus(s) {
	case MicrosoftStatusPending, MicrosoftStatusValidating, MicrosoftStatusIdentifying, MicrosoftStatusNormal, MicrosoftStatusAbnormal, MicrosoftStatusDisabled, MicrosoftStatusDeleted:
		return true
	default:
		return false
	}
}

// CanTransitionMicrosoftStatus returns true when a status transition follows the Core state machine.
func CanTransitionMicrosoftStatus(from, to MicrosoftResourceStatus) bool {
	switch from {
	case MicrosoftStatusPending:
		return to == MicrosoftStatusValidating || to == MicrosoftStatusDisabled
	case MicrosoftStatusValidating:
		return to == MicrosoftStatusPending || to == MicrosoftStatusIdentifying || to == MicrosoftStatusAbnormal || to == MicrosoftStatusDisabled
	case MicrosoftStatusIdentifying:
		return to == MicrosoftStatusPending || to == MicrosoftStatusNormal || to == MicrosoftStatusAbnormal || to == MicrosoftStatusDisabled
	case MicrosoftStatusNormal:
		return to == MicrosoftStatusPending || to == MicrosoftStatusAbnormal || to == MicrosoftStatusDisabled
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

// QueueValidationAdmin starts a new validation generation. This deliberately
// fences an already-running worker when an administrator explicitly retries.
func (r *MicrosoftResource) QueueValidationAdmin() (bool, error) {
	switch r.Status {
	case MicrosoftStatusDeleted:
		return false, ErrResourceNotFound
	case MicrosoftStatusDisabled:
		return false, ErrInvalidResourceStatus
	}
	r.beginValidationGeneration()
	return true, nil
}

func (r *MicrosoftResource) beginValidationGeneration() {
	if r.ValidationGeneration == 0 {
		r.ValidationGeneration = 1
	} else {
		r.ValidationGeneration++
	}
	r.ValidationFailures = 0
	r.Status = MicrosoftStatusPending
	r.LastSafeError = ""
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

// EnableAdmin is the explicit administrator transition out of disabled. It
// does not claim that Microsoft validation has succeeded; the resource must be
// revalidated from pending before it can be allocated.
func (r *MicrosoftResource) EnableAdmin() error {
	if r.Status == MicrosoftStatusDeleted {
		return ErrResourceNotFound
	}
	if r.Status != MicrosoftStatusDisabled {
		return ErrInvalidResourceStatus
	}
	r.beginValidationGeneration()
	r.GraphAvailable = false
	r.QualityScore = 0
	r.RTExpireAt = nil
	r.TokenLastRefreshedAt = nil
	r.TokenLastRequestID = ""
	r.LastSafeError = ""
	return nil
}

// DisableAdmin blocks new allocation and automatic maintenance. Existing
// allocations remain owned by Alloc/Trade; this command deliberately does not
// terminate or rewrite them.
func (r *MicrosoftResource) DisableAdmin() error {
	if r.Status == MicrosoftStatusDeleted {
		return ErrResourceNotFound
	}
	if r.Status == MicrosoftStatusDisabled {
		return nil
	}
	r.Status = MicrosoftStatusDisabled
	return nil
}

func (r *MicrosoftResource) PublishAdmin() error {
	if r.Status == MicrosoftStatusDeleted {
		return ErrResourceNotFound
	}
	r.ForSale = true
	return nil
}

func (r *MicrosoftResource) UnpublishAdmin() error {
	if r.Status == MicrosoftStatusDeleted {
		return ErrResourceNotFound
	}
	r.ForSale = false
	return nil
}

// DeleteAdmin is the administrator logical-delete command. Unlike the
// supplier self-service delete, it forcefully removes the resource from future
// supply; active-allocation protection is enforced by the application port.
func (r *MicrosoftResource) DeleteAdmin() error {
	if r.Status == MicrosoftStatusDeleted {
		return ErrInvalidResourceStatus
	}
	r.Status = MicrosoftStatusDeleted
	r.ForSale = false
	r.GraphAvailable = false
	r.QualityScore = 0
	r.RTExpireAt = nil
	r.TokenLastRefreshedAt = nil
	r.TokenLastRequestID = ""
	r.LastSafeError = ""
	return nil
}

// RecoverAdmin restores only a logically deleted resource. Recovery is always
// private and pending until a new validation result commits.
func (r *MicrosoftResource) RecoverAdmin() error {
	if r.Status != MicrosoftStatusDeleted {
		return ErrInvalidResourceStatus
	}
	r.beginValidationGeneration()
	r.ForSale = false
	r.GraphAvailable = false
	r.QualityScore = 0
	r.RTExpireAt = nil
	r.TokenLastRefreshedAt = nil
	r.TokenLastRequestID = ""
	r.LastSafeError = ""
	return nil
}

// InvalidateMicrosoftIdentity advances the credential fence whenever the
// mailbox identity or its complete credential set changes. Derived health is
// no longer trustworthy and must be rebuilt asynchronously.
func (r *MicrosoftResource) InvalidateMicrosoftIdentity(now time.Time) error {
	if r.Status == MicrosoftStatusDeleted {
		return ErrResourceNotFound
	}
	if r.CredentialRevision == 0 {
		r.CredentialRevision = 1
	}
	r.CredentialRevision++
	r.CredentialUpdatedAt = now.UTC()
	r.beginValidationGeneration()
	r.GraphAvailable = false
	r.QualityScore = 0
	r.RTExpireAt = nil
	r.TokenLastRefreshedAt = nil
	r.TokenLastRequestID = ""
	r.LastSafeError = ""
	return nil
}

// InvalidateMicrosoftAccountIdentity advances the validation generation after
// the Microsoft account email changes and removes every credential that belongs
// to the previous account. A later explicit credential replacement may install
// a complete new set, but an email-only PATCH must never try the old account's
// password or refresh token against the new identity.
func (r *MicrosoftResource) InvalidateMicrosoftAccountIdentity(now time.Time) error {
	if err := r.InvalidateMicrosoftIdentity(now); err != nil {
		return err
	}
	r.Password = ""
	r.ClientID = ""
	r.RefreshToken = ""
	return nil
}

// InvalidateMicrosoftBinding advances the validation generation when the
// recovery-mailbox input changes. OAuth credentials remain intact, but an
// in-flight worker must no longer be allowed to commit facts collected against
// the previous binding relationship.
func (r *MicrosoftResource) InvalidateMicrosoftBinding(now time.Time) error {
	if r.Status == MicrosoftStatusDeleted {
		return ErrResourceNotFound
	}
	if r.CredentialRevision == 0 {
		r.CredentialRevision = 1
	}
	r.CredentialRevision++
	r.CredentialUpdatedAt = now.UTC()
	r.beginValidationGeneration()
	r.GraphAvailable = false
	r.QualityScore = 0
	r.LastSafeError = ""
	return nil
}

// ReplaceCredentialsAdmin replaces the write-only credential set as one
// logical value. A Microsoft client ID and refresh token are either both
// configured or both omitted.
func (r *MicrosoftResource) ReplaceCredentialsAdmin(password, clientID, refreshToken string, now time.Time) error {
	passwordConfigured := strings.TrimSpace(password) != ""
	clientIDConfigured := strings.TrimSpace(clientID) != ""
	refreshTokenConfigured := strings.TrimSpace(refreshToken) != ""
	if !passwordConfigured || clientIDConfigured != refreshTokenConfigured {
		return ErrInvalidResourceCommand
	}
	if err := r.InvalidateMicrosoftIdentity(now); err != nil {
		return err
	}
	r.Password = password
	r.ClientID = clientID
	r.RefreshToken = refreshToken
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
	ID                   uint // Shared PK = EmailResource.ID
	Domain               string
	MailServerID         uint
	Purpose              ResourcePurpose
	Status               MailDomainStatus
	ValidationGeneration uint64
	ValidationFailures   int
	MailboxDailyLimit    int
	LastSafeError        string // Sanitized diagnostic summary
	LastAllocatedAt      *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// IsValidDomainStatus returns true if the status is a valid state.
func IsValidDomainStatus(s string) bool {
	switch MailDomainStatus(s) {
	case DomainStatusPending, DomainStatusValidating, DomainStatusNormal, DomainStatusAbnormal, DomainStatusDisabled, DomainStatusDeleted:
		return true
	default:
		return false
	}
}

// CanTransitionDomainStatus returns true when a status transition follows the Core state machine.
func CanTransitionDomainStatus(from, to MailDomainStatus) bool {
	switch from {
	case DomainStatusPending:
		return to == DomainStatusValidating || to == DomainStatusNormal || to == DomainStatusAbnormal || to == DomainStatusDisabled
	case DomainStatusValidating:
		return to == DomainStatusPending || to == DomainStatusNormal || to == DomainStatusAbnormal || to == DomainStatusDisabled
	case DomainStatusAbnormal:
		return to == DomainStatusPending || to == DomainStatusNormal || to == DomainStatusDisabled
	case DomainStatusNormal:
		return to == DomainStatusPending || to == DomainStatusAbnormal || to == DomainStatusDisabled
	case DomainStatusDisabled:
		return to == DomainStatusPending
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

// QueueValidationAdmin is the domain counterpart of the Microsoft transition.
func (r *MailDomainResource) QueueValidationAdmin() (bool, error) {
	switch r.Status {
	case DomainStatusDeleted:
		return false, ErrResourceNotFound
	case DomainStatusDisabled:
		return false, ErrInvalidResourceStatus
	}
	r.beginValidationGeneration()
	return true, nil
}

func (r *MailDomainResource) beginValidationGeneration() {
	if r.ValidationGeneration == 0 {
		r.ValidationGeneration = 1
	} else {
		r.ValidationGeneration++
	}
	r.ValidationFailures = 0
	r.Status = DomainStatusPending
	r.LastSafeError = ""
}

// MarkDNSStatusAdmin records a validation result without doubling as an
// enable command. Disabled resources must be enabled explicitly first.
func (r *MailDomainResource) MarkDNSStatusAdmin(normal bool) error {
	if r.Status == DomainStatusDeleted {
		return ErrResourceNotFound
	}
	if r.Status == DomainStatusDisabled {
		return ErrInvalidResourceStatus
	}
	target := DomainStatusAbnormal
	if normal {
		target = DomainStatusNormal
	}
	if r.Status == target {
		if normal {
			r.LastSafeError = ""
		}
		return nil
	}
	if err := r.TransitionStatus(target); err != nil {
		return err
	}
	if normal {
		r.LastSafeError = ""
	}
	return nil
}

func (r *MailDomainResource) DisableAdmin() error {
	if r.Status == DomainStatusDeleted {
		return ErrResourceNotFound
	}
	if r.Status == DomainStatusDisabled {
		return nil
	}
	return r.TransitionStatus(DomainStatusDisabled)
}

func (r *MailDomainResource) EnableAdmin() error {
	if r.Status == DomainStatusDeleted {
		return ErrResourceNotFound
	}
	if r.Status != DomainStatusDisabled {
		return ErrInvalidResourceStatus
	}
	r.beginValidationGeneration()
	return nil
}

func (r *MailDomainResource) SetPurposeAdmin(purpose ResourcePurpose) error {
	if r.Status == DomainStatusDeleted {
		return ErrResourceNotFound
	}
	if !IsValidPurpose(purpose) {
		return ErrInvalidPurpose
	}
	r.Purpose = purpose
	return nil
}

func (r *MailDomainResource) DeleteAdmin() error {
	if r.Status == DomainStatusDeleted {
		return nil
	}
	r.Status = DomainStatusDeleted
	r.Purpose = PurposeNotSale
	r.LastSafeError = ""
	r.LastAllocatedAt = nil
	return nil
}

func (r *MailDomainResource) RecoverAdmin() error {
	if r.Status != DomainStatusDeleted {
		return ErrInvalidResourceStatus
	}
	r.beginValidationGeneration()
	r.Purpose = PurposeNotSale
	r.LastAllocatedAt = nil
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
	r.LastSafeError = ""
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

// microsoftEmailWhitelist is the closed set of Microsoft consumer mailbox domains
// accepted for import (operator-supplied, 32 entries: outlook.com, hotmail.com and
// 30 outlook country variants). It is an EXACT allow-list, not an outlook.* prefix:
// real Microsoft variants absent here (outlook.co.uk, outlook.ca, live.com, msn.com)
// are rejected, as is every non-Microsoft provider (icloud.com, gmail.com, *.edu.cn).
var microsoftEmailWhitelist = map[string]struct{}{
	"outlook.com":    {},
	"hotmail.com":    {},
	"outlook.sa":     {},
	"outlook.com.ar": {},
	"outlook.com.au": {},
	"outlook.at":     {},
	"outlook.be":     {},
	"outlook.com.br": {},
	"outlook.cl":     {},
	"outlook.cz":     {},
	"outlook.fr":     {},
	"outlook.de":     {},
	"outlook.com.gr": {},
	"outlook.co.il":  {},
	"outlook.in":     {},
	"outlook.co.id":  {},
	"outlook.ie":     {},
	"outlook.it":     {},
	"outlook.hu":     {},
	"outlook.jp":     {},
	"outlook.kr":     {},
	"outlook.lv":     {},
	"outlook.my":     {},
	"outlook.co.nz":  {},
	"outlook.ph":     {},
	"outlook.pt":     {},
	"outlook.sg":     {},
	"outlook.sk":     {},
	"outlook.es":     {},
	"outlook.co.th":  {},
	"outlook.com.tr": {},
	"outlook.com.vn": {},
}

// IsMicrosoftEmailDomain reports whether email's domain is a whitelisted Microsoft
// consumer mailbox accepted for import. It gates Microsoft account imports at the
// trust boundary and is deliberately NOT applied to recovery/binding addresses,
// which legitimately live on any provider.
func IsMicrosoftEmailDomain(email string) bool {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(email[at+1:])), ".")
	if configured := strings.TrimSpace(runtimeconfig.String("microsoft_domain_whitelist", "")); configured != "" {
		for _, candidate := range strings.FieldsFunc(configured, func(r rune) bool { return r == ',' || r == '，' || unicode.IsSpace(r) }) {
			if strings.TrimSuffix(strings.ToLower(strings.TrimSpace(candidate)), ".") == host {
				return true
			}
		}
		return false
	}
	_, ok := microsoftEmailWhitelist[host]
	return ok
}

func DefaultPlusDailyLimitValue() int {
	return min(runtimeconfig.Int("default_plus_daily_limit", DefaultPlusDailyLimit, 1), maxConfiguredDailyLimit)
}

func DefaultMailboxDailyLimitValue() int {
	return min(runtimeconfig.Int("default_mailbox_daily_limit", DefaultMailboxDailyLimit, 1), maxConfiguredDailyLimit)
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
	ID          uint
	ResourceID  uint
	OwnerUserID uint
	Email       string
	Status      AliasStatus
	CreatedAt   time.Time
	UpdatedAt   time.Time
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
