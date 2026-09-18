package infra

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tradeapp "github.com/donnel666/remail/internal/trade/app"
)

type orderStatistics struct {
	Total  int64                     `json:"total"`
	Facets *tradeapp.OrderListFacets `json:"facets,omitempty"`
}

func orderStatisticsKey(filter tradeapp.OrderListFilter) string {
	if filter.IsAdmin && filter.Scope == "all" {
		filter.UserID = 0
	} else {
		filter.IsAdmin, filter.Scope = false, "mine"
	}
	filter.Search = strings.TrimSpace(filter.Search)
	filter.Domain = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(filter.Domain)), "@")
	if filter.CreatedFrom != nil {
		value := filter.CreatedFrom.UTC()
		filter.CreatedFrom = &value
	}
	if filter.CreatedTo != nil {
		value := filter.CreatedTo.UTC()
		filter.CreatedTo = &value
	}
	payload, _ := json.Marshal(filter)
	return fmt.Sprintf("trade:order-statistics:v1:%x", sha256.Sum256(payload))
}

// CachedOrderStats keeps pagination-independent aggregates out of the list's
// critical path. Both the limit=1 statistics request and actual pages share it.
func (r *Repo) CachedOrderStats(ctx context.Context, filter tradeapp.OrderListFilter) (int64, *tradeapp.OrderListFacets, error) {
	if r.stats == nil {
		total, err := r.CountOrders(ctx, filter)
		if err != nil {
			return 0, nil, err
		}
		facets, err := r.OrderFacets(ctx, filter)
		return total, facets, err
	}
	key := orderStatisticsKey(filter)
	if cached := r.stats.Get(ctx, key, func(ctx context.Context) (orderStatistics, error) {
		countCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		total, err := r.CountOrders(countCtx, filter)
		data := orderStatistics{Total: total}
		if err != nil {
			return data, err
		}
		r.stats.Seed(ctx, key, data)
		data.Facets, err = r.OrderFacets(ctx, filter)
		return data, err
	}); cached != nil {
		return cached.Total, cached.Facets, nil
	}
	// ponytail: poll Redis only during the first 1.5s of a cold cache; the list
	// uses its observed row count if background capacity is occupied.
	coldCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-coldCtx.Done():
			return 0, nil, nil
		case <-ticker.C:
			if cached := r.stats.Get(coldCtx, key, nil); cached != nil {
				return cached.Total, cached.Facets, nil
			}
		}
	}
}
