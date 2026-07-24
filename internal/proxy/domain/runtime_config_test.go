package domain

import (
	"testing"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

func TestProxyFailureThresholdUsesRuntimeSetting(t *testing.T) {
	runtimeconfig.Set("proxy_failure_threshold", "1")
	t.Cleanup(func() { runtimeconfig.Delete("proxy_failure_threshold") })

	proxy := &Proxy{Status: ProxyStatusNormal}
	require.NoError(t, proxy.ReportFailure("network timeout", true))
	require.Equal(t, ProxyStatusPending, proxy.Status)
}
