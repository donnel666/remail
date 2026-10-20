package infra

import (
	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMicrosoftEnterpriseFacetInvalidationIsIndependent(t *testing.T) {
	personal := coreapp.ResourceListFilter{ResourceType: domain.ResourceTypeMicrosoft}
	enterprise := coreapp.ResourceListFilter{ResourceType: domain.ResourceTypeMicrosoftEnterprise}
	legacy := coreapp.ResourceListFilter{ResourceType: domain.ResourceTypeMicrosoft, Enterprise: true}
	p, e, l := resourceFacetsCacheKey(1, personal), resourceFacetsCacheKey(1, enterprise), resourceFacetsCacheKey(1, legacy)
	invalidateMicrosoftFacets(domain.ResourceTypeMicrosoft)
	require.NotEqual(t, p, resourceFacetsCacheKey(1, personal))
	require.Equal(t, e, resourceFacetsCacheKey(1, enterprise))
	require.Equal(t, l, resourceFacetsCacheKey(1, legacy))
	p = resourceFacetsCacheKey(1, personal)
	invalidateMicrosoftFacets(domain.ResourceTypeMicrosoftEnterprise)
	require.Equal(t, p, resourceFacetsCacheKey(1, personal))
	require.NotEqual(t, e, resourceFacetsCacheKey(1, enterprise))
	require.NotEqual(t, l, resourceFacetsCacheKey(1, legacy))
}
