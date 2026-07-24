package domain

import (
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

type ProxyPool string

const (
	ProxyPoolResource ProxyPool = "resource"
	ProxyPoolSystem   ProxyPool = "system"
)

type ProxyIPVersion string

const (
	ProxyIPAuto ProxyIPVersion = "auto"
	ProxyIPv4   ProxyIPVersion = "ipv4"
	ProxyIPv6   ProxyIPVersion = "ipv6"
)

type ProxyStatus string

const (
	ProxyStatusPending  ProxyStatus = "pending"
	ProxyStatusChecking ProxyStatus = "checking"
	ProxyStatusNormal   ProxyStatus = "normal"
	ProxyStatusAbnormal ProxyStatus = "abnormal"
	ProxyStatusDisabled ProxyStatus = "disabled"
	ProxyStatusExpired  ProxyStatus = "expired"
)

type ProxyPurpose string

const (
	ProxyPurposeAuth    ProxyPurpose = "auth"
	ProxyPurposeFetch   ProxyPurpose = "fetch"
	ProxyPurposeBinding ProxyPurpose = "binding"
)

type Proxy struct {
	ID                  uint
	Pool                ProxyPool
	URL                 string
	ExpireAt            time.Time
	IPVersion           ProxyIPVersion
	OutboundIP          string
	Country             string
	LatencyMs           int
	Status              ProxyStatus
	Errors              int
	LastSafeError       string
	CheckOperatorUserID uint
	CheckRequestID      string
	CheckPath           string
	CheckGeneration     uint64
	LastCheckedAt       *time.Time
	LastUsedAt          *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type Binding struct {
	ID         uint
	Key        string
	ProxyID    uint
	IPVersion  ProxyIPVersion
	ExpireAt   time.Time
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

type CheckResult struct {
	IPVersion     ProxyIPVersion
	OutboundIP    string
	Country       string
	LatencyMs     int
	Attempts      int
	NonRetryable  bool
	LastSafeError string
	CheckedAt     time.Time
}

func IsValidProxyPool(value string) bool {
	switch ProxyPool(value) {
	case ProxyPoolResource, ProxyPoolSystem:
		return true
	default:
		return false
	}
}

func IsValidProxyIPVersion(value string) bool {
	switch ProxyIPVersion(value) {
	case ProxyIPAuto, ProxyIPv4, ProxyIPv6:
		return true
	default:
		return false
	}
}

func IsValidStoredProxyIPVersion(value string) bool {
	return value == "" || value == string(ProxyIPv4) || value == string(ProxyIPv6)
}

func IsValidProxyStatus(value string) bool {
	switch ProxyStatus(value) {
	case ProxyStatusPending, ProxyStatusChecking, ProxyStatusNormal, ProxyStatusAbnormal, ProxyStatusDisabled, ProxyStatusExpired:
		return true
	default:
		return false
	}
}

func CanTransitionProxyStatus(from, to ProxyStatus) bool {
	if from == to {
		return true
	}
	switch from {
	case ProxyStatusPending:
		return to == ProxyStatusChecking || to == ProxyStatusDisabled
	case ProxyStatusChecking:
		return to == ProxyStatusPending || to == ProxyStatusNormal || to == ProxyStatusAbnormal || to == ProxyStatusDisabled
	case ProxyStatusNormal:
		return to == ProxyStatusPending || to == ProxyStatusAbnormal || to == ProxyStatusDisabled || to == ProxyStatusExpired
	case ProxyStatusAbnormal:
		return to == ProxyStatusPending || to == ProxyStatusNormal || to == ProxyStatusDisabled || to == ProxyStatusExpired
	case ProxyStatusDisabled:
		return to == ProxyStatusPending
	case ProxyStatusExpired:
		return to == ProxyStatusPending || to == ProxyStatusDisabled
	default:
		return false
	}
}

func (p *Proxy) MarkPending() error {
	if !CanTransitionProxyStatus(p.Status, ProxyStatusPending) {
		return ErrInvalidProxyStatus
	}
	p.Status = ProxyStatusPending
	p.Errors = 0
	p.LastSafeError = ""
	return nil
}

func (p *Proxy) MarkChecking() error {
	if !CanTransitionProxyStatus(p.Status, ProxyStatusChecking) {
		return ErrInvalidProxyStatus
	}
	p.Status = ProxyStatusChecking
	return nil
}

func (p *Proxy) MarkDisabled(reason string) error {
	if !CanTransitionProxyStatus(p.Status, ProxyStatusDisabled) {
		return ErrInvalidProxyStatus
	}
	p.Status = ProxyStatusDisabled
	p.LastSafeError = SafeProxyError(reason)
	return nil
}

func (p *Proxy) ApplyCheckSuccess(result CheckResult) error {
	if !CanTransitionProxyStatus(p.Status, ProxyStatusNormal) {
		return ErrInvalidProxyStatus
	}
	p.IPVersion = result.IPVersion
	p.OutboundIP = strings.TrimSpace(result.OutboundIP)
	p.Country = NormalizeCountry(result.Country)
	p.LatencyMs = result.LatencyMs
	p.Status = ProxyStatusNormal
	p.Errors = 0
	p.LastSafeError = ""
	checkedAt := result.CheckedAt
	if checkedAt.IsZero() {
		checkedAt = time.Now().UTC()
	}
	p.LastCheckedAt = &checkedAt
	return nil
}

func (p *Proxy) ApplyCheckFailure(result CheckResult) error {
	p.LastSafeError = SafeProxyError(result.LastSafeError)
	checkedAt := result.CheckedAt
	if checkedAt.IsZero() {
		checkedAt = time.Now().UTC()
	}
	p.LastCheckedAt = &checkedAt
	switch p.Status {
	case ProxyStatusChecking, ProxyStatusNormal, ProxyStatusAbnormal, ProxyStatusExpired:
		if !result.NonRetryable {
			attempts := result.Attempts
			if attempts <= 0 {
				attempts = 1
			}
			p.Errors += attempts
		}
		if !CanTransitionProxyStatus(p.Status, ProxyStatusAbnormal) {
			return ErrInvalidProxyStatus
		}
		p.Status = ProxyStatusAbnormal
	case ProxyStatusDisabled:
		return nil
	default:
		return ErrInvalidProxyStatus
	}
	return nil
}

func (p *Proxy) MarkExpired(now time.Time) error {
	if !CanTransitionProxyStatus(p.Status, ProxyStatusExpired) {
		return ErrInvalidProxyStatus
	}
	p.Status = ProxyStatusExpired
	p.LastSafeError = "Proxy has expired."
	if now.IsZero() {
		now = time.Now().UTC()
	}
	p.LastCheckedAt = &now
	return nil
}

// failureThreshold defines how many consecutive retryable errors trigger
// a transition to checking status for asynchronous health verification.
const failureThreshold = 3

func (p *Proxy) ReportFailure(safeError string, retryable bool) error {
	if p.Status == ProxyStatusDisabled {
		return nil
	}
	p.LastSafeError = SafeProxyError(safeError)
	if !retryable {
		if !CanTransitionProxyStatus(p.Status, ProxyStatusAbnormal) {
			return ErrInvalidProxyStatus
		}
		p.Status = ProxyStatusAbnormal
		return nil
	}
	p.Errors++
	if p.Errors >= runtimeconfig.Int("proxy_failure_threshold", failureThreshold, 1) && p.Status == ProxyStatusNormal {
		if !CanTransitionProxyStatus(p.Status, ProxyStatusPending) {
			return ErrInvalidProxyStatus
		}
		p.Status = ProxyStatusPending
		p.Errors = 0
	}
	return nil
}

func (p *Proxy) ReportSuccess(usedAt time.Time) {
	if p.Status == ProxyStatusDisabled {
		return
	}
	if usedAt.IsZero() {
		usedAt = time.Now().UTC()
	}
	p.Errors = 0
	p.LastSafeError = ""
	p.LastUsedAt = &usedAt
}

func (p Proxy) IsExpired(now time.Time) bool {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return !p.ExpireAt.IsZero() && !p.ExpireAt.After(now)
}

func (p Proxy) IsSelectable(now time.Time, requested ProxyIPVersion) bool {
	if p.Status != ProxyStatusNormal || p.IsExpired(now) {
		return false
	}
	return ProxyIPMatches(p.IPVersion, requested)
}

func NormalizeProxyPool(value string) (ProxyPool, bool) {
	normalized := ProxyPool(strings.ToLower(strings.TrimSpace(value)))
	return normalized, IsValidProxyPool(string(normalized))
}

func NormalizeProxyIPVersion(value string) (ProxyIPVersion, bool) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		normalized = string(ProxyIPAuto)
	}
	v := ProxyIPVersion(normalized)
	return v, IsValidProxyIPVersion(string(v))
}

func NormalizeCountry(value string) string {
	normalized := strings.ToUpper(strings.TrimSpace(value))
	if normalized == "" {
		return "UNKNOWN"
	}
	if len(normalized) > 32 {
		return normalized[:32]
	}
	return normalized
}

func NormalizeProxyURL(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", ErrInvalidProxyURL
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", ErrInvalidProxyURL
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", ErrInvalidProxyURL
	}
	if parsed.Port() != "" {
		return parsed.String(), nil
	}
	return "", ErrInvalidProxyURL
}

func RedactProxyURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	if parsed.User != nil {
		hasPassword := false
		if _, ok := parsed.User.Password(); ok {
			hasPassword = true
		}
		parsed.User = nil
		rest := parsed.String()
		prefix := parsed.Scheme + "://"
		if strings.HasPrefix(rest, prefix) {
			if hasPassword {
				return prefix + "***:***@" + strings.TrimPrefix(rest, prefix)
			}
			return prefix + "***@" + strings.TrimPrefix(rest, prefix)
		}
		return rest
	}
	return parsed.String()
}

func ProxyIPMatches(stored ProxyIPVersion, requested ProxyIPVersion) bool {
	if requested == "" || requested == ProxyIPAuto {
		return stored == ProxyIPv4 || stored == ProxyIPv6
	}
	return stored == requested
}

func IPVersionFromAddress(address string) ProxyIPVersion {
	ip := net.ParseIP(strings.TrimSpace(address))
	if ip == nil {
		return ""
	}
	if ip.To4() != nil {
		return ProxyIPv4
	}
	return ProxyIPv6
}

var (
	embeddedProxyURLPattern = regexp.MustCompile(`(?i)\b(?:https?|socks5h?)://[^\s<>"']+`)
	secretKVPattern         = regexp.MustCompile(`(?i)\b(password|passwd|pwd|token|access_token|refresh_token|refreshToken|accessToken)=([^&\s]+)`)
)

func SafeProxyError(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "Proxy check failed."
	}
	redacted := embeddedProxyURLPattern.ReplaceAllStringFunc(trimmed, func(match string) string {
		if safe := RedactProxyURL(match); safe != "" {
			return safe
		}
		return match
	})
	redacted = secretKVPattern.ReplaceAllString(redacted, "$1=***")
	if len(redacted) > 500 {
		return redacted[:500]
	}
	return redacted
}
