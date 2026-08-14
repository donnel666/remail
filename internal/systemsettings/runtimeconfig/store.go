package runtimeconfig

import (
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/donnel666/remail/internal/systemsettings/domain"
)

// The process-local snapshot is refreshed by the system-settings API's Redis
// invalidation subscriber when the service runs with multiple replicas.
var current atomic.Value
var updateMu sync.Mutex

type Values map[string]string

func init() {
	current.Store(map[string]string{})
}

func Replace(settings []domain.Setting) {
	updateMu.Lock()
	defer updateMu.Unlock()
	values := make(map[string]string, len(settings))
	for _, setting := range settings {
		key := canonicalKey(setting.Key)
		value := NormalizeValue(key, setting.Value)
		if Validate(key, value) == nil {
			values[key] = value
		}
	}
	sanitizeRelationships(values)
	current.Store(values)
}

func Set(key, value string) {
	SetMany([]domain.Setting{{Key: key, Value: value}})
}

func SetMany(settings []domain.Setting) {
	updateMu.Lock()
	defer updateMu.Unlock()
	values := clone()
	for _, setting := range settings {
		key := canonicalKey(setting.Key)
		values[key] = NormalizeValue(key, setting.Value)
	}
	current.Store(values)
}

func Delete(key string) {
	updateMu.Lock()
	defer updateMu.Unlock()
	values := clone()
	delete(values, canonicalKey(key))
	current.Store(values)
}

func String(key, fallback string) string {
	value, ok := current.Load().(map[string]string)[canonicalKey(key)]
	if !ok {
		return fallback
	}
	return value
}

func ProductPriceMultiplier(productType string) string {
	var key string
	switch canonicalKey(productType) {
	case "microsoft":
		key = MicrosoftPriceMultiplierKey
	case "gmail":
		key = GmailPriceMultiplierKey
	case "icloud":
		key = ICloudPriceMultiplierKey
	case "domain":
		key = DomainPriceMultiplierKey
	default:
		return "1"
	}
	return strings.TrimSpace(String(key, "1"))
}

func Bool(key string, fallback bool) bool {
	value, err := strconv.ParseBool(strings.TrimSpace(String(key, "")))
	if err != nil {
		return fallback
	}
	return value
}

func Snapshot() Values {
	return Values(clone())
}

func (values Values) String(key, fallback string) string {
	value, ok := values[canonicalKey(key)]
	if !ok {
		return fallback
	}
	return value
}

func (values Values) Int(key string, fallback, minimum int) int {
	value, err := strconv.Atoi(strings.TrimSpace(values.String(key, "")))
	if err != nil || value < minimum {
		return fallback
	}
	return value
}

func (values Values) Duration(key string, fallback, unit time.Duration, minimum int) time.Duration {
	if unit <= 0 {
		return fallback
	}
	value, err := strconv.Atoi(strings.TrimSpace(values.String(key, "")))
	if err != nil || value < minimum || int64(value) > math.MaxInt64/int64(unit) {
		return fallback
	}
	return time.Duration(value) * unit
}

func Int(key string, fallback, minimum int) int {
	value, err := strconv.Atoi(strings.TrimSpace(String(key, "")))
	if err != nil || value < minimum {
		return fallback
	}
	return value
}

func Duration(key string, fallback, unit time.Duration, minimum int) time.Duration {
	if unit <= 0 {
		return fallback
	}
	value, err := strconv.Atoi(strings.TrimSpace(String(key, "")))
	if err != nil || value < minimum || int64(value) > math.MaxInt64/int64(unit) {
		return fallback
	}
	return time.Duration(value) * unit
}

func clone() map[string]string {
	snapshot := current.Load().(map[string]string)
	values := make(map[string]string, len(snapshot)+1)
	for key, value := range snapshot {
		values[key] = value
	}
	return values
}

func canonicalKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

func NormalizeValue(key, value string) string {
	if canonicalKey(key) != ICloudForwardingSuffixesKey {
		return value
	}
	seen := make(map[string]struct{})
	domains := make([]string, 0)
	for _, candidate := range strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '，' || unicode.IsSpace(r)
	}) {
		domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(candidate)), ".")
		if domain == "" {
			continue
		}
		if _, exists := seen[domain]; exists {
			continue
		}
		seen[domain] = struct{}{}
		domains = append(domains, domain)
	}
	return strings.Join(domains, ",")
}

func ICloudForwardingSuffixes(value string) []string {
	value = NormalizeValue(ICloudForwardingSuffixesKey, value)
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}
