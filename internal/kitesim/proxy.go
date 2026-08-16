package kitesim

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/platform"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

const (
	defaultProxyAttempts = 3
	maxProxyAttempts     = 20
)

type ProxyProvider interface {
	Acquire(context.Context, proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error)
	ReportSuccess(context.Context, uint) error
	ReportFailure(context.Context, uint, string) error
}

type upstreamTransportError struct {
	err   error
	proxy bool
}

func (e *upstreamTransportError) Error() string {
	return fmt.Sprintf("kitesim: request failed: %v", e.err)
}

func (e *upstreamTransportError) Unwrap() error { return e.err }

type upstreamStatusError struct {
	status int
	proxy  bool
}

func (e *upstreamStatusError) Error() string {
	return fmt.Sprintf("kitesim: unexpected HTTP status %d", e.status)
}

func (s *Service) SetProxyProvider(proxies ProxyProvider) {
	if s != nil {
		s.proxies = proxies
	}
}

func (s *Service) withAuthClient(ctx context.Context, account string, run func(*Client) error) error {
	return s.withUpstreamClient(ctx, account, proxydomain.ProxyPurposeAuth, run)
}

func (s *Service) withFetchClient(ctx context.Context, account string, run func(*Client) error) error {
	return s.withUpstreamClient(ctx, account, proxydomain.ProxyPurposeFetch, run)
}

// withSingleUpstreamClient keeps one fingerprint, proxy and cookie jar for a
// chargeable flow. It deliberately never replays run after a transport error.
func (s *Service) withSingleUpstreamClient(
	ctx context.Context,
	account string,
	purpose proxydomain.ProxyPurpose,
	run func(*Client) error,
) error {
	if s == nil || s.client == nil || run == nil {
		return errors.New("kitesim: client unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	proxyConfig, err := s.acquireProxy(ctx, account, purpose, requestIDFromContext(ctx), 0, nil)
	if err != nil {
		return fmt.Errorf("acquire Kitesim proxy: %w", err)
	}
	proxyURL := ""
	proxyID := uint(0)
	if proxyConfig != nil && !proxyConfig.Direct {
		proxyURL, proxyID = proxyConfig.URL, proxyConfig.ID
	}
	client, err := s.client.withProxy(proxyURL)
	if err == nil {
		err = run(client)
	}
	if err == nil {
		_ = s.reportProxySuccess(ctx, proxyID)
		return nil
	}
	if proxyURL != "" && (client == nil || isProxyFailure(err)) {
		_ = s.reportProxyFailure(ctx, proxyID)
	} else {
		_ = s.reportProxySuccess(ctx, proxyID)
	}
	return err
}

func (s *Service) withUpstreamClient(
	ctx context.Context,
	account string,
	purpose proxydomain.ProxyPurpose,
	run func(*Client) error,
) error {
	if s == nil || s.client == nil || run == nil {
		return errors.New("kitesim: client unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	attemptLimit := min(
		runtimeconfig.Int("max_proxy_attempts", defaultProxyAttempts, 1),
		maxProxyAttempts,
	)
	requestID := requestIDFromContext(ctx)
	var avoidServerIDs []uint
	var lastErr error
	for attempt := 0; attempt <= attemptLimit; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		proxyConfig, err := s.acquireProxy(ctx, account, purpose, requestID, attempt, avoidServerIDs)
		if err != nil {
			return fmt.Errorf("acquire Kitesim proxy: %w", err)
		}
		proxyURL := ""
		proxyID := uint(0)
		if proxyConfig != nil && !proxyConfig.Direct {
			proxyURL = proxyConfig.URL
			proxyID = proxyConfig.ID
		}
		client, err := s.client.withProxy(proxyURL)
		if err == nil {
			err = run(client)
		}
		if err == nil {
			_ = s.reportProxySuccess(ctx, proxyID)
			return nil
		}
		lastErr = err
		if ctxErr := ctx.Err(); ctxErr != nil {
			return errors.Join(err, ctxErr)
		}
		if proxyURL != "" && (client == nil || isProxyFailure(err)) {
			avoidServerIDs = proxyapp.AppendAvoidProxyServerID(avoidServerIDs, proxyConfig)
			_ = s.reportProxyFailure(ctx, proxyID)
			continue
		}
		_ = s.reportProxySuccess(ctx, proxyID)
		return err
	}
	if lastErr != nil {
		return lastErr
	}
	return errors.New("kitesim: upstream unavailable")
}

func (s *Service) acquireProxy(
	ctx context.Context,
	account string,
	purpose proxydomain.ProxyPurpose,
	requestID string,
	attempt int,
	avoidServerIDs []uint,
) (*proxyapp.ProxyConfig, error) {
	if s == nil || s.proxies == nil {
		return &proxyapp.ProxyConfig{Direct: true}, nil
	}
	return s.proxies.Acquire(ctx, proxyapp.AcquireProxyRequest{
		Key:                 strings.ToLower(strings.TrimSpace(account)),
		IPVersion:           proxydomain.ProxyIPv4,
		Purpose:             purpose,
		AllowSystemFallback: true,
		Attempt:             attempt,
		RequestID:           requestID,
		AvoidProxyServerIDs: avoidServerIDs,
	})
}

func (s *Service) reportProxySuccess(ctx context.Context, proxyID uint) error {
	if s == nil || s.proxies == nil || proxyID == 0 || ctx.Err() != nil {
		return nil
	}
	return s.proxies.ReportSuccess(ctx, proxyID)
}

func (s *Service) reportProxyFailure(ctx context.Context, proxyID uint) error {
	if s == nil || s.proxies == nil || proxyID == 0 {
		return nil
	}
	reportCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.proxies.ReportFailure(reportCtx, proxyID, "Kitesim upstream transport failed.")
}

func requestIDFromContext(ctx context.Context) string {
	if ctx != nil {
		if requestID, ok := ctx.Value(platform.RequestIDKey).(string); ok && strings.TrimSpace(requestID) != "" {
			return strings.TrimSpace(requestID)
		}
	}
	return platform.NewUUIDV7String()
}

func isProxyFailure(err error) bool {
	var transportErr *upstreamTransportError
	if errors.As(err, &transportErr) {
		return transportErr.proxy
	}
	var statusErr *upstreamStatusError
	if !errors.As(err, &statusErr) || !statusErr.proxy {
		return false
	}
	switch statusErr.status {
	case 407, 502, 503, 504:
		return true
	default:
		return false
	}
}
