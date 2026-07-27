package infra

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/donnel666/remail/internal/platform"
)

const (
	microsoftFacetsQueryTimeout = 30 * time.Second
	microsoftFacetsSuffixLimit  = 100
)

// ponytail: process-local invalidation is enough while the app has one replica;
// move this generation to a shared store before running multiple app replicas.
var microsoftFacetsGeneration atomic.Uint64

type microsoftFacetsInvalidationKey struct{}

type microsoftFacetsInvalidationState struct {
	dirty bool
}

func currentMicrosoftFacetsGeneration() uint64 {
	return microsoftFacetsGeneration.Load()
}

func invalidateMicrosoftFacets() {
	microsoftFacetsGeneration.Add(1)
}

func markMicrosoftFacetsDirty(ctx context.Context) {
	if state, ok := ctx.Value(microsoftFacetsInvalidationKey{}).(*microsoftFacetsInvalidationState); ok {
		state.dirty = true
		return
	}
	if _, inTx := platform.GormTxFromContext(ctx); inTx {
		// ponytail: cross-context transactions have no post-commit hook; let the
		// configured TTL expire; add a shared tx hook if immediate freshness is required.
		return
	}
	invalidateMicrosoftFacets()
}

type facetAggregateSelect struct {
	expressions []string
	args        []any
}

func (s *facetAggregateSelect) add(alias string, predicate string, args ...any) {
	if predicate == "" {
		predicate = "TRUE"
	}
	s.expressions = append(s.expressions,
		"COALESCE(SUM(CASE WHEN "+predicate+" THEN 1 ELSE 0 END), 0) AS "+alias,
	)
	s.args = append(s.args, args...)
}

func (s facetAggregateSelect) query() (string, []any) {
	return strings.Join(s.expressions, ",\n"), s.args
}

func facetPredicateSQL(conditions []string) string {
	if len(conditions) == 0 {
		return "TRUE"
	}
	return strings.Join(conditions, " AND ")
}
