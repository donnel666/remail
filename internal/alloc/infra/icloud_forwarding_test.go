package infra

import (
	"testing"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

func TestICloudForwardingDomainConditionUsesCurrentAuthorizedDomains(t *testing.T) {
	previous := runtimeconfig.String(runtimeconfig.ICloudForwardingSuffixesKey, "")
	t.Cleanup(func() { runtimeconfig.Set(runtimeconfig.ICloudForwardingSuffixesKey, previous) })

	runtimeconfig.Set(runtimeconfig.ICloudForwardingSuffixesKey, " RELAY.EXAMPLE. ")
	condition, args := iCloudForwardingDomainCondition("ia.forward_to_email")
	require.Contains(t, condition, "ia.forward_to_email")
	require.Contains(t, condition, "domain_resources")
	require.Equal(t, []any{[]string{"relay.example"}}, args)

	runtimeconfig.Set(runtimeconfig.ICloudForwardingSuffixesKey, "")
	condition, args = iCloudForwardingDomainCondition("ia.forward_to_email")
	require.Equal(t, "1 = 0", condition)
	require.Empty(t, args)
}
