package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNormalizeAndRedactProxyURL(t *testing.T) {
	normalized, err := NormalizeProxyURL(" socks5://user:pass@127.0.0.1:1080 ")
	require.NoError(t, err)
	require.Equal(t, "socks5://user:pass@127.0.0.1:1080", normalized)
	require.Equal(t, "socks5://***:***@127.0.0.1:1080", RedactProxyURL(normalized))

	_, err = NormalizeProxyURL("socks5://user:pass@127.0.0.1")
	require.ErrorIs(t, err, ErrInvalidProxyURL)

	_, err = NormalizeProxyURL("ftp://127.0.0.1:21")
	require.ErrorIs(t, err, ErrInvalidProxyURL)
}

func TestSafeProxyErrorRedactsSecrets(t *testing.T) {
	safe := SafeProxyError("dial socks5://user:pass@127.0.0.1:1080 failed password=secret refreshToken=rt")

	require.NotContains(t, safe, "user:pass")
	require.NotContains(t, safe, "password=secret")
	require.NotContains(t, safe, "refreshToken=rt")
	require.Contains(t, safe, "socks5://***:***@127.0.0.1:1080")
	require.Contains(t, safe, "password=***")
	require.Contains(t, safe, "refreshToken=***")
}

func TestProxyStatusTransitions(t *testing.T) {
	proxy := &Proxy{
		Status: ProxyStatusChecking,
	}
	checkedAt := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	require.NoError(t, proxy.ApplyCheckSuccess(CheckResult{
		IPVersion:  ProxyIPv4,
		OutboundIP: "203.0.113.10",
		Country:    "us",
		LatencyMs:  120,
		CheckedAt:  checkedAt,
	}))
	require.Equal(t, ProxyStatusNormal, proxy.Status)
	require.Equal(t, ProxyIPv4, proxy.IPVersion)
	require.Equal(t, "US", proxy.Country)
	require.Equal(t, 0, proxy.Errors)

	require.NoError(t, proxy.ReportFailure("network timeout", true))
	require.Equal(t, ProxyStatusNormal, proxy.Status)
	require.Equal(t, 1, proxy.Errors)
	require.NoError(t, proxy.ReportFailure("network timeout", true))
	require.Equal(t, ProxyStatusNormal, proxy.Status)
	require.Equal(t, 2, proxy.Errors)

	require.NoError(t, proxy.MarkChecking())
	require.Equal(t, 0, proxy.Errors)
	require.NoError(t, proxy.ApplyCheckSuccess(CheckResult{
		IPVersion:  ProxyIPv6,
		OutboundIP: "2001:db8::1",
		Country:    "JP",
		LatencyMs:  80,
		CheckedAt:  checkedAt,
	}))
	require.Equal(t, ProxyStatusNormal, proxy.Status)
	require.Equal(t, ProxyIPv6, proxy.IPVersion)
}

func TestProxyCheckFailureRetryability(t *testing.T) {
	checkedAt := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	retryable := &Proxy{Status: ProxyStatusNormal}

	require.NoError(t, retryable.ApplyCheckFailure(CheckResult{
		LastSafeError: "Proxy endpoint is unreachable.",
		CheckedAt:     checkedAt,
	}))
	require.Equal(t, ProxyStatusAbnormal, retryable.Status)
	require.Equal(t, 1, retryable.Errors)

	require.NoError(t, retryable.ApplyCheckFailure(CheckResult{
		LastSafeError: "Proxy endpoint is unreachable.",
		CheckedAt:     checkedAt,
	}))
	require.Equal(t, ProxyStatusAbnormal, retryable.Status)
	require.Equal(t, 2, retryable.Errors)
	require.NoError(t, retryable.ApplyCheckFailure(CheckResult{
		LastSafeError: "Proxy endpoint is unreachable.",
		CheckedAt:     checkedAt,
	}))
	require.Equal(t, ProxyStatusAbnormal, retryable.Status)
	require.Equal(t, 3, retryable.Errors)
	require.NoError(t, retryable.ApplyCheckFailure(CheckResult{
		LastSafeError: "Proxy endpoint is unreachable.",
		CheckedAt:     checkedAt,
	}))
	require.Equal(t, ProxyStatusAbnormal, retryable.Status)
	require.Equal(t, 4, retryable.Errors)

	nonRetryable := &Proxy{Status: ProxyStatusNormal}
	require.NoError(t, nonRetryable.ApplyCheckFailure(CheckResult{
		NonRetryable:  true,
		LastSafeError: "Invalid proxy URL.",
		CheckedAt:     checkedAt,
	}))
	require.Equal(t, ProxyStatusAbnormal, nonRetryable.Status)
	require.Equal(t, 0, nonRetryable.Errors)

	mixed := &Proxy{Status: ProxyStatusNormal}
	require.NoError(t, mixed.ApplyCheckFailure(CheckResult{
		LastSafeError: "Proxy endpoint is unreachable.",
		CheckedAt:     checkedAt,
	}))
	require.Equal(t, 1, mixed.Errors)
	require.NoError(t, mixed.ApplyCheckFailure(CheckResult{
		NonRetryable:  true,
		LastSafeError: "Invalid proxy URL.",
		CheckedAt:     checkedAt,
	}))
	require.Equal(t, ProxyStatusAbnormal, mixed.Status)
	require.Equal(t, 1, mixed.Errors)
}

func TestProxyReportFailureRetryability(t *testing.T) {
	retryable := &Proxy{Status: ProxyStatusNormal}
	require.NoError(t, retryable.ReportFailure("network timeout", true))
	require.Equal(t, ProxyStatusNormal, retryable.Status)
	require.Equal(t, 1, retryable.Errors)
	require.NoError(t, retryable.ReportFailure("network timeout", true))
	require.Equal(t, ProxyStatusNormal, retryable.Status)
	require.Equal(t, 2, retryable.Errors)
	require.NoError(t, retryable.ReportFailure("network timeout", true))
	require.Equal(t, ProxyStatusChecking, retryable.Status)
	require.Equal(t, 3, retryable.Errors)
	require.NoError(t, retryable.ReportFailure("network timeout", true))
	require.Equal(t, ProxyStatusChecking, retryable.Status)
	require.Equal(t, 4, retryable.Errors)

	nonRetryable := &Proxy{Status: ProxyStatusNormal}
	require.NoError(t, nonRetryable.ReportFailure("invalid proxy url", false))
	require.Equal(t, ProxyStatusAbnormal, nonRetryable.Status)
	require.Equal(t, 0, nonRetryable.Errors)
}

func TestProxyRuntimeReportsDoNotMutateDisabledProxy(t *testing.T) {
	proxy := &Proxy{
		Status:        ProxyStatusDisabled,
		Errors:        2,
		LastSafeError: "Proxy disabled by administrator.",
	}

	require.NoError(t, proxy.ReportFailure("network timeout", true))
	require.Equal(t, ProxyStatusDisabled, proxy.Status)
	require.Equal(t, 2, proxy.Errors)
	require.Equal(t, "Proxy disabled by administrator.", proxy.LastSafeError)

	proxy.ReportSuccess(time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC))
	require.Equal(t, ProxyStatusDisabled, proxy.Status)
	require.Equal(t, 2, proxy.Errors)
	require.Equal(t, "Proxy disabled by administrator.", proxy.LastSafeError)
	require.Nil(t, proxy.LastUsedAt)
}

func TestProxyExpiredTransitionsRequireCheckingBeforeResult(t *testing.T) {
	proxy := &Proxy{Status: ProxyStatusExpired}

	require.False(t, CanTransitionProxyStatus(ProxyStatusExpired, ProxyStatusNormal))
	require.False(t, CanTransitionProxyStatus(ProxyStatusExpired, ProxyStatusAbnormal))
	require.NoError(t, proxy.MarkChecking())

	require.NoError(t, proxy.ApplyCheckSuccess(CheckResult{
		IPVersion:  ProxyIPv4,
		OutboundIP: "198.51.100.20",
		Country:    "US",
		LatencyMs:  42,
		CheckedAt:  time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC),
	}))
	require.Equal(t, ProxyStatusNormal, proxy.Status)
}
