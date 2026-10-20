package infra

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/platform"
)

const (
	microsoftFacetsQueryTimeout = 30 * time.Second
	microsoftFacetsSuffixLimit  = 100
)

// ponytail: process-local invalidation is enough while the app has one replica;
// move this generation to a shared store before running multiple app replicas.
var microsoftFacetsGeneration [2]atomic.Uint64

type microsoftFacetsInvalidationKey struct{}

type microsoftFacetsInvalidationState struct {
	dirty [2]bool
}

func currentMicrosoftFacetsGeneration(kind domain.ResourceType) uint64 {
	switch kind {
	case domain.ResourceTypeMicrosoftEnterprise:
		return microsoftFacetsGeneration[1].Load()
	case domain.ResourceTypeMicrosoft:
		return microsoftFacetsGeneration[0].Load()
	default:
		return microsoftFacetsGeneration[0].Load() + microsoftFacetsGeneration[1].Load()
	}
}

func microsoftFacetsIndex(kind domain.ResourceType) int {
	if kind == domain.ResourceTypeMicrosoftEnterprise {
		return 1
	}
	return 0
}

func invalidateMicrosoftFacets(kind domain.ResourceType) {
	microsoftFacetsGeneration[microsoftFacetsIndex(kind)].Add(1)
}

func markMicrosoftFacetsDirty(ctx context.Context, kind domain.ResourceType) {
	if state, ok := ctx.Value(microsoftFacetsInvalidationKey{}).(*microsoftFacetsInvalidationState); ok {
		state.dirty[microsoftFacetsIndex(kind)] = true
		return
	}
	if _, inTx := platform.GormTxFromContext(ctx); inTx {
		// shortcut: caller-owned transactions rely on TTL; add a post-commit hook if immediate invalidation is required.
		return
	}
	invalidateMicrosoftFacets(kind)
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
