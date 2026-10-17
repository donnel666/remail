package infra

import (
	"context"
	"errors"
	"testing"

	"fmt"
	"github.com/donnel666/remail/internal/daum/infra/protocol"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

type daumNetworkProxies struct {
	requests  []proxyapp.AcquireProxyRequest
	good, bad []uint
	direct    bool
}

func (p *daumNetworkProxies) Acquire(_ context.Context, req proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error) {
	p.requests = append(p.requests, req)
	if p.direct {
		return &proxyapp.ProxyConfig{Direct: true}, nil
	}
	return &proxyapp.ProxyConfig{ID: uint(len(p.requests)), ProxyServerID: uint(len(p.requests) + 10), URL: "http://proxy.fixture:3128"}, nil
}
func (p *daumNetworkProxies) ReportSuccess(_ context.Context, id uint) error {
	p.good = append(p.good, id)
	return nil
}
func (p *daumNetworkProxies) ReportFailure(_ context.Context, id uint, _ string) error {
	p.bad = append(p.bad, id)
	return nil
}

func TestDaumNetworkMatchesMicrosoftRoutingAndFailureFeedback(t *testing.T) {
	proxies := &daumNetworkProxies{}
	calls := 0
	s := &Service{Proxies: proxies, Protocol: daumValidationClientStub{fetch: func(_ context.Context, c protocol.Credentials, _ protocol.FetchRequest) (protocol.FetchResult, error) {
		calls++
		require.Equal(t, "http://proxy.fixture:3128", c.ProxyURL)
		require.Equal(t, "app secret", c.AppPassword)
		if calls == 1 {
			return protocol.FetchResult{}, &protocol.Failure{Category: "transport", Retryable: true, Cause: errors.New("temporary")}
		}
		return protocol.FetchResult{Complete: true}, nil
	}}}
	_, err := s.fetchProtocol(context.Background(), Resource{EmailAddress: "USER@daum.net", AppPassword: "app secret", Protocol: "imap"}, protocol.FetchRequest{FullHistory: true})
	require.NoError(t, err)
	require.Len(t, proxies.requests, 2)
	require.Equal(t, "user@daum.net", proxies.requests[0].Key)
	require.Equal(t, proxydomain.ProxyIPv6, proxies.requests[0].IPVersion)
	require.Equal(t, proxydomain.ProxyPurposeFetch, proxies.requests[0].Purpose)
	require.True(t, proxies.requests[0].AllowSystemFallback)
	require.Equal(t, proxies.requests[0].RequestID, proxies.requests[1].RequestID)
	require.Equal(t, []uint{11}, proxies.requests[1].AvoidProxyServerIDs)
	require.Equal(t, []uint{1}, proxies.bad)
	require.Equal(t, []uint{2}, proxies.good)
	proxies.requests = nil
	require.NoError(t, s.validateProtocol(context.Background(), Resource{EmailAddress: "user@daum.net"}, "validation-request"))
	require.Equal(t, proxydomain.ProxyIPv4, proxies.requests[0].IPVersion)
	require.Equal(t, proxydomain.ProxyPurposeAuth, proxies.requests[0].Purpose)
	require.Equal(t, "validation-request", proxies.requests[0].RequestID)
}

func TestDaumPlatformDoesNotBypassMissingProxy(t *testing.T) {
	for _, proxies := range []ProxyProvider{nil, &daumNetworkProxies{direct: true}} {
		s := &Service{Proxies: proxies, Protocol: daumValidationClientStub{fetch: func(context.Context, protocol.Credentials, protocol.FetchRequest) (protocol.FetchResult, error) {
			t.Fatal("platform mailbox must not silently connect directly")
			return protocol.FetchResult{}, nil
		}}}
		_, err := s.fetchProtocol(context.Background(), Resource{EmailAddress: "user@daum.net"}, protocol.FetchRequest{})
		var failure *protocol.Failure
		require.ErrorAs(t, err, &failure)
		require.Equal(t, "proxy_unavailable", failure.Category)
	}
}

func TestDaumNetworkDoesNotBlameProxyForPasswordOrReplayStream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		proxies := &daumNetworkProxies{}
		s := &Service{Proxies: proxies, Protocol: daumValidationClientStub{fetch: func(_ context.Context, _ protocol.Credentials, r protocol.FetchRequest) (protocol.FetchResult, error) {
			if stream {
				require.NoError(t, r.OnMessages([]protocol.Message{{ID: "one"}}))
				return protocol.FetchResult{}, &protocol.Failure{Category: "transport", Retryable: true}
			}
			return protocol.FetchResult{}, &protocol.Failure{Category: "invalid_credentials"}
		}}}
		_, err := s.fetchProtocol(context.Background(), Resource{EmailAddress: "user@daum.net"}, protocol.FetchRequest{OnMessages: func([]protocol.Message) error { return nil }})
		require.Error(t, err)
		require.Len(t, proxies.requests, map[bool]int{true: 1, false: 3}[stream])
		if stream {
			require.Equal(t, []uint{1}, proxies.bad)
		} else {
			require.Empty(t, proxies.bad)
			require.Equal(t, []uint{1, 2, 3}, proxies.good)
		}
	}
}

func TestDaumNetworkPreservesHealthyProxyOnProviderFailure(t *testing.T) {
	for _, category := range []string{"provider_unavailable", "rate_limited", "protocol", "protocol_disabled"} {
		t.Run(category, func(t *testing.T) {
			proxies := &daumNetworkProxies{}
			failure := &protocol.Failure{Category: category, Retryable: category == "provider_unavailable" || category == "rate_limited"}
			service := &Service{Proxies: proxies, Protocol: daumValidationClientStub{fetch: func(context.Context, protocol.Credentials, protocol.FetchRequest) (protocol.FetchResult, error) {
				return protocol.FetchResult{}, failure
			}}}
			_, err := service.fetchProtocol(context.Background(), Resource{EmailAddress: "fixture@daum.net"}, protocol.FetchRequest{})
			require.ErrorIs(t, err, failure)
			require.Len(t, proxies.requests, 3)
			require.Empty(t, proxies.bad)
			require.Equal(t, []uint{1, 2, 3}, proxies.good)
		})
	}
}
func TestDaumFetchUsesMicrosoftFixedAttemptBudget(t *testing.T) {
	defer runtimeconfig.Delete("max_proxy_attempts")
	for _, budget := range []int{1, 2, 4} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			runtimeconfig.Set("max_proxy_attempts", fmt.Sprint(budget))
			proxies := &daumNetworkProxies{}
			service := &Service{Proxies: proxies, Protocol: daumValidationClientStub{fetch: func(context.Context, protocol.Credentials, protocol.FetchRequest) (protocol.FetchResult, error) {
				return protocol.FetchResult{}, &protocol.Failure{Category: "transport", Retryable: true}
			}}}
			_, err := service.fetchProtocol(context.Background(), Resource{EmailAddress: "fixture@daum.net"}, protocol.FetchRequest{})
			require.Error(t, err)
			require.Len(t, proxies.requests, 3)
			require.Len(t, proxies.bad, 3)
			require.Empty(t, proxies.good)
			for i, request := range proxies.requests {
				require.Equal(t, i, request.Attempt)
			}
		})
	}
}

func TestDaumHistoryResetsStreamBeforeRetryLikeMicrosoft(t *testing.T) {
	proxies := &daumNetworkProxies{}
	calls, resets := 0, 0
	var received []protocol.Message
	service := &Service{Proxies: proxies, Protocol: daumValidationClientStub{fetch: func(_ context.Context, _ protocol.Credentials, request protocol.FetchRequest) (protocol.FetchResult, error) {
		calls++
		require.NoError(t, request.OnMessages([]protocol.Message{{ID: fmt.Sprint(calls)}}))
		if calls == 1 {
			return protocol.FetchResult{}, &protocol.Failure{Category: "transport", Retryable: true}
		}
		return protocol.FetchResult{Complete: true}, nil
	}}}
	_, err := service.fetchProtocol(context.Background(), Resource{EmailAddress: "fixture@daum.net"}, protocol.FetchRequest{
		FullHistory: true,
		OnMessages:  func(messages []protocol.Message) error { received = append(received, messages...); return nil },
		OnReset:     func() { resets++; received = nil },
	})
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, 1, resets)
	require.Equal(t, []protocol.Message{{ID: "2"}}, received)
	require.Equal(t, []uint{1}, proxies.bad)
	require.Equal(t, []uint{2}, proxies.good)
}
