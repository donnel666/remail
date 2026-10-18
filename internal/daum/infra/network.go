package infra

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/donnel666/remail/internal/daum/infra/protocol"
	"github.com/donnel666/remail/internal/mailbox"
	"github.com/donnel666/remail/internal/platform"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
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
	operation := "fetch"
	if validation {
		operation = "validation"
	} else if request.FullHistory {
		operation = "history"
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		requestID = platform.NewUUIDV7String()
	}
	attempts := mailbox.FetchAttempts
	if validation {
		attempts = 1
	}
	credentials := protocol.Credentials{Email: resource.EmailAddress, AppPassword: resource.AppPassword, Protocol: resource.Protocol}
	var last error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return protocol.FetchResult{}, err
		}
		var result protocol.FetchResult
		var err error
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
			err = ctx.Err()
		}
		if err != nil {
			stage, reason := protocol.FailureDiagnostics(err)
			slog.WarnContext(ctx, "Daum mailbox operation failed", "operation", operation, "route", "direct_ipv4",
				"resource_id", resource.ID, "request_id", requestID, "credential_revision", resource.CredentialRevision, "validation_generation", resource.ValidationGeneration,
				"attempt", attempt, "stage", stage, "reason", reason)
		}
		if err == nil {
			return result, nil
		}
		last = err
		if streamed {
			if request.OnReset == nil {
				return protocol.FetchResult{}, err
			}
			request.OnReset()
		}
		var failure *protocol.Failure
		if ctx.Err() != nil || !errors.As(err, &failure) {
			return protocol.FetchResult{}, err
		}
	}
	return protocol.FetchResult{}, last
}
