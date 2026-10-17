package infra

import (
	"context"
	"errors"
	"strings"

	"github.com/donnel666/remail/internal/daum/infra/protocol"
	"github.com/donnel666/remail/internal/mailbox"
	"github.com/donnel666/remail/internal/platform"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

type ProxyProvider interface {
	Acquire(context.Context, proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error)
	ReportSuccess(context.Context, uint) error
	ReportFailure(context.Context, uint, string) error
}

func (s *Service) validateProtocol(ctx context.Context, resource Resource, requestID string) error {
	_, err := s.runProtocol(ctx, resource, protocol.FetchRequest{}, requestID, true)
	return err
}

func (s *Service) fetchProtocol(ctx context.Context, resource Resource, request protocol.FetchRequest) (protocol.FetchResult, error) {
	requestID, _ := ctx.Value(platform.RequestIDKey).(string)
	return s.runProtocol(ctx, resource, request, requestID, false)
}

func (s *Service) runProtocol(ctx context.Context, resource Resource, request protocol.FetchRequest, requestID string, validation bool) (protocol.FetchResult, error) {
	unavailable := &protocol.Failure{Category: "proxy_unavailable", SafeMessage: "Daum mailbox proxy service is temporarily unavailable.", Retryable: true}
	if s.Proxies == nil {
		return protocol.FetchResult{}, unavailable
	}
	ipVersion, purpose := proxydomain.ProxyIPv4, proxydomain.ProxyPurposeFetch
	if validation {
		purpose = proxydomain.ProxyPurposeAuth
	} else if request.FullHistory {
		ipVersion = proxydomain.ProxyIPv6
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		requestID = platform.NewUUIDV7String()
	}
	var avoid []uint
	var last error = unavailable
	attempts := mailbox.FetchAttempts
	if validation {
		attempts = runtimeconfig.Int("max_proxy_attempts", 3, 1)
	}
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return protocol.FetchResult{}, err
		}
		selected, err := s.Proxies.Acquire(ctx, proxyapp.AcquireProxyRequest{
			Key: strings.ToLower(strings.TrimSpace(resource.EmailAddress)), IPVersion: ipVersion,
			Purpose: purpose, AllowSystemFallback: true, Attempt: attempt, RequestID: requestID,
			AvoidProxyServerIDs: avoid,
		})
		if err != nil {
			return protocol.FetchResult{}, &protocol.Failure{Category: unavailable.Category, SafeMessage: unavailable.SafeMessage, Retryable: true, Cause: err}
		}
		// Microsoft IMAP also refuses a direct fallback when no mailbox proxy exists.
		if selected == nil || selected.Direct || strings.TrimSpace(selected.URL) == "" {
			continue
		}
		credentials := protocol.Credentials{Email: resource.EmailAddress, AppPassword: resource.AppPassword, Protocol: resource.Protocol, ProxyURL: selected.URL}
		var result protocol.FetchResult
		streamed := false
		if validation {
			err = s.Protocol.Validate(ctx, credentials)
		} else {
			fetch := request
			if request.OnMessages != nil {
				fetch.OnMessages = func(messages []protocol.Message) error {
					streamed = streamed || len(messages) > 0
					return request.OnMessages(messages)
				}
			}
			result, err = s.Protocol.Fetch(ctx, credentials, fetch)
		}
		if ctx.Err() != nil {
			return protocol.FetchResult{}, ctx.Err()
		}
		if err == nil {
			_ = s.Proxies.ReportSuccess(ctx, selected.ID)
			return result, nil
		}
		last = err
		var failure *protocol.Failure
		proxyFailure := errors.As(err, &failure) && failure.Category == "transport"
		if proxyFailure {
			avoid = proxyapp.AppendAvoidProxyServerID(avoid, selected)
			_ = s.Proxies.ReportFailure(ctx, selected.ID, "Daum mailbox proxy connection failed.")
		} else {
			_ = s.Proxies.ReportSuccess(ctx, selected.ID)
		}
		if streamed {
			if request.OnReset == nil {
				return protocol.FetchResult{}, err
			}
			request.OnReset()
		}
		if validation && !proxyFailure {
			return protocol.FetchResult{}, err
		}
		if failure == nil {
			return protocol.FetchResult{}, err
		}

	}
	return protocol.FetchResult{}, last
}
