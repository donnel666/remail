package runtimeconfig

import (
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/domain"
)

// ponytail: this monolith uses a process-local snapshot; add Redis pub/sub if
// system-setting writes can be served by multiple API replicas.
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
		key := strings.TrimSpace(setting.Key)
		if Validate(key, setting.Value) == nil {
			values[key] = setting.Value
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
		values[strings.TrimSpace(setting.Key)] = setting.Value
	}
	current.Store(values)
}

func Delete(key string) {
	updateMu.Lock()
	defer updateMu.Unlock()
	values := clone()
	delete(values, strings.TrimSpace(key))
	current.Store(values)
}

func String(key, fallback string) string {
	value, ok := current.Load().(map[string]string)[key]
	if !ok {
		return fallback
	}
	return value
}

func Snapshot() Values {
	return Values(clone())
}

func (values Values) String(key, fallback string) string {
	value, ok := values[key]
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
