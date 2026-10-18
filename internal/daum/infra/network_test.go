package infra

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/donnel666/remail/internal/daum/infra/protocol"
	"github.com/donnel666/remail/internal/mailbox"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

type daumNetworkProxies struct{ t *testing.T }

func (p *daumNetworkProxies) Acquire(context.Context, proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error) {
	p.t.Fatal("Daum must connect directly without acquiring a proxy")
	return nil, nil
}
func (p *daumNetworkProxies) ReportSuccess(context.Context, uint) error {
	p.t.Fatal("Daum direct operations must not report proxy health")
	return nil
}
func (p *daumNetworkProxies) ReportFailure(context.Context, uint, string) error {
	p.t.Fatal("Daum direct operations must not report proxy health")
	return nil
}

func TestDaumAllOperationsUseDirectEvenWhenProxiesAreConfigured(t *testing.T) {
	for _, protocolName := range []string{"imap", "pop3"} {
		for _, operation := range []string{"validation", "realtime", "history"} {
			t.Run(protocolName+"/"+operation, func(t *testing.T) {
				calls := 0
				check := func(credentials protocol.Credentials) {
					calls++
					require.Equal(t, protocol.Credentials{Email: "fixture@daum.net", AppPassword: "app-secret", Protocol: protocolName}, credentials)
				}
				service := &Service{Proxies: &daumNetworkProxies{t: t}, Protocol: daumValidationClientStub{
					validate: func(_ context.Context, credentials protocol.Credentials) error { check(credentials); return nil },
					fetch: func(_ context.Context, credentials protocol.Credentials, request protocol.FetchRequest) (protocol.FetchResult, error) {
						check(credentials)
						require.Equal(t, operation == "history", request.FullHistory)
						return protocol.FetchResult{Complete: true}, nil
					},
				}}
				result, err := service.runProtocol(context.Background(), Resource{EmailAddress: "fixture@daum.net", AppPassword: "app-secret", Protocol: protocolName}, protocol.FetchRequest{FullHistory: operation == "history"}, "direct-contract", operation == "validation")
				require.NoError(t, err)
				require.Equal(t, 1, calls)
				if operation != "validation" {
					require.True(t, result.Complete)
				}
			})
		}
	}
}

func TestDaumDirectFetchPreservesFailureRoundBudget(t *testing.T) {
	for _, category := range []string{"transport", "invalid_credentials", "identity_mismatch", "account_disabled", "action_required", "protocol_disabled", "protocol", "provider_unavailable", "rate_limited"} {
		t.Run(category, func(t *testing.T) {
			calls := 0
			failure := &protocol.Failure{Category: category}
			service := &Service{Proxies: &daumNetworkProxies{t: t}, Protocol: daumValidationClientStub{fetch: func(_ context.Context, credentials protocol.Credentials, _ protocol.FetchRequest) (protocol.FetchResult, error) {
				calls++
				require.Empty(t, credentials.ProxyURL)
				return protocol.FetchResult{}, failure
			}}}
			_, err := service.fetchProtocol(context.Background(), Resource{EmailAddress: "fixture@daum.net"}, protocol.FetchRequest{})
			require.ErrorIs(t, err, failure)
			require.Equal(t, mailbox.FetchAttempts, calls)
		})
	}
}

func TestDaumDirectFetchIgnoresProxyAttemptSetting(t *testing.T) {
	previous, existed := runtimeconfig.Snapshot()["max_proxy_attempts"]
	t.Cleanup(func() {
		if existed {
			runtimeconfig.Set("max_proxy_attempts", previous)
		} else {
			runtimeconfig.Delete("max_proxy_attempts")
		}
	})
	for _, budget := range []int{1, 2, 4} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			runtimeconfig.Set("max_proxy_attempts", fmt.Sprint(budget))
			calls := 0
			service := &Service{Proxies: &daumNetworkProxies{t: t}, Protocol: daumValidationClientStub{fetch: func(_ context.Context, credentials protocol.Credentials, _ protocol.FetchRequest) (protocol.FetchResult, error) {
				calls++
				require.Empty(t, credentials.ProxyURL)
				return protocol.FetchResult{}, &protocol.Failure{Category: "transport", Retryable: true}
			}}}
			_, err := service.fetchProtocol(context.Background(), Resource{EmailAddress: "fixture@daum.net"}, protocol.FetchRequest{})
			require.Error(t, err)
			require.Equal(t, mailbox.FetchAttempts, calls)
		})
	}
}

func TestDaumDirectNetworkStopsOnCancellation(t *testing.T) {
	for _, validation := range []bool{false, true} {
		for _, preCanceled := range []bool{false, true} {
			t.Run(fmt.Sprintf("validation=%t/pre-canceled=%t", validation, preCanceled), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				calls := 0
				attempt := func() { calls++; cancel() }
				s := &Service{Proxies: &daumNetworkProxies{t: t}, Protocol: daumValidationClientStub{
					validate: func(context.Context, protocol.Credentials) error { attempt(); return nil },
					fetch: func(context.Context, protocol.Credentials, protocol.FetchRequest) (protocol.FetchResult, error) {
						attempt()
						return protocol.FetchResult{Complete: true}, nil
					},
				}}
				if preCanceled {
					cancel()
				}
				_, err := s.runProtocol(ctx, Resource{}, protocol.FetchRequest{}, "canceled", validation)
				require.ErrorIs(t, err, context.Canceled)
				require.Equal(t, map[bool]int{true: 0, false: 1}[preCanceled], calls)
			})
		}
	}
}

func TestDaumDirectNetworkDoesNotRetryInternalErrors(t *testing.T) {
	calls := 0
	failure := errors.New("internal failure")
	s := &Service{Proxies: &daumNetworkProxies{t: t}, Protocol: daumValidationClientStub{fetch: func(context.Context, protocol.Credentials, protocol.FetchRequest) (protocol.FetchResult, error) {
		calls++
		return protocol.FetchResult{}, failure
	}}}
	_, err := s.fetchProtocol(context.Background(), Resource{}, protocol.FetchRequest{})
	require.ErrorIs(t, err, failure)
	require.Equal(t, 1, calls)
}

func TestDaumDirectHistoryRequiresResetBeforeRetry(t *testing.T) {
	for _, mode := range []string{"no_reset", "reset_then_success", "reset_until_exhausted"} {
		t.Run(mode, func(t *testing.T) {
			calls, resets := 0, 0
			var received []protocol.Message
			request := protocol.FetchRequest{FullHistory: true, OnMessages: func(messages []protocol.Message) error { received = append(received, messages...); return nil }}
			if mode != "no_reset" {
				request.OnReset = func() { resets++; received = nil }
			}
			service := &Service{Proxies: &daumNetworkProxies{t: t}, Protocol: daumValidationClientStub{fetch: func(_ context.Context, credentials protocol.Credentials, request protocol.FetchRequest) (protocol.FetchResult, error) {
				calls++
				require.Empty(t, credentials.ProxyURL)
				require.NoError(t, request.OnMessages([]protocol.Message{{ID: fmt.Sprint(calls)}}))
				if mode == "reset_then_success" && calls == mailbox.FetchAttempts {
					return protocol.FetchResult{Complete: true}, nil
				}
				return protocol.FetchResult{}, &protocol.Failure{Category: "transport", Retryable: true}
			}}}
			_, err := service.fetchProtocol(context.Background(), Resource{EmailAddress: "fixture@daum.net"}, request)
			switch mode {
			case "no_reset":
				require.Error(t, err)
				require.Equal(t, 1, calls)
				require.Zero(t, resets)
			case "reset_then_success":
				require.NoError(t, err)
				require.Equal(t, mailbox.FetchAttempts, calls)
				require.Equal(t, mailbox.FetchAttempts-1, resets)
				require.Equal(t, []protocol.Message{{ID: fmt.Sprint(mailbox.FetchAttempts)}}, received)
			case "reset_until_exhausted":
				require.Error(t, err)
				require.Equal(t, mailbox.FetchAttempts, calls)
				require.Equal(t, mailbox.FetchAttempts, resets)
				require.Empty(t, received)
			}
		})
	}
}
