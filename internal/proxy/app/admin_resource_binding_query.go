package app

import (
	"context"
	"strings"
	"time"
)

// AdminResourceProxyBinding is the safe, credential-free binding summary used
// by the administrator Microsoft resource detail. The proxy URL is deliberately
// excluded because it may contain authentication credentials.
type AdminResourceProxyBinding struct {
	BindKey    string
	ProxyID    uint
	Host       string
	OutboundIP string
	Country    string
	IPVersion  string
	Status     string
	ExpireAt   time.Time
}

// AdminResourceProxyBindingRepository is a narrow read boundary for the
// administrator resource detail. Proxy remains the owner of bindings and proxy
// health facts.
type AdminResourceProxyBindingRepository interface {
	ListAdminResourceProxyBindings(ctx context.Context, keys []string) ([]AdminResourceProxyBinding, error)
}

type AdminResourceProxyBindingQuery struct {
	repo AdminResourceProxyBindingRepository
}

func NewAdminResourceProxyBindingQuery(repo AdminResourceProxyBindingRepository) *AdminResourceProxyBindingQuery {
	return &AdminResourceProxyBindingQuery{repo: repo}
}

func (q *AdminResourceProxyBindingQuery) GetByKeys(ctx context.Context, keys []string) (map[string][]AdminResourceProxyBinding, error) {
	result := make(map[string][]AdminResourceProxyBinding)
	if q == nil || q.repo == nil {
		return result, nil
	}
	keys = uniqueAdminResourceProxyBindingKeys(keys)
	if len(keys) == 0 {
		return result, nil
	}
	items, err := q.repo.ListAdminResourceProxyBindings(ctx, keys)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		key := strings.ToLower(strings.TrimSpace(item.BindKey))
		if key == "" {
			continue
		}
		item.BindKey = key
		item.ExpireAt = item.ExpireAt.UTC()
		result[key] = append(result[key], item)
	}
	return result, nil
}

func uniqueAdminResourceProxyBindingKeys(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	keys := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		keys = append(keys, value)
	}
	return keys
}
