package kitesim

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/donnel666/remail/internal/platform"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
)

type proxyProviderStub struct {
	configs  []*proxyapp.ProxyConfig
	requests []proxyapp.AcquireProxyRequest
	success  []uint
	failure  []uint
}

func (s *proxyProviderStub) Acquire(_ context.Context, request proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error) {
	request.AvoidProxyServerIDs = append([]uint(nil), request.AvoidProxyServerIDs...)
	s.requests = append(s.requests, request)
	index := min(request.Attempt, len(s.configs)-1)
	return s.configs[index], nil
}

func (s *proxyProviderStub) ReportSuccess(_ context.Context, proxyID uint) error {
	s.success = append(s.success, proxyID)
	return nil
}

func (s *proxyProviderStub) ReportFailure(_ context.Context, proxyID uint, _ string) error {
	s.failure = append(s.failure, proxyID)
	return nil
}

func TestUpstreamClientRotatesFailedProxy(t *testing.T) {
	proxies := &proxyProviderStub{configs: []*proxyapp.ProxyConfig{
		{ID: 11, ProxyServerID: 101, URL: "http://proxy-one.invalid:8000"},
		{ID: 12, ProxyServerID: 102, URL: "http://proxy-two.invalid:8000"},
	}}
	service := &Service{client: NewClient(http.DefaultClient), proxies: proxies}
	calls := 0
	err := service.withAuthClient(
		platform.WithRequestID(context.Background(), "request-1"),
		"Owner@Example.com",
		func(client *Client) error {
			calls++
			if client.userAgent == "" || client.headers["sec-ch-ua"] == "" {
				t.Fatal("fingerprint headers were not generated")
			}
			if calls == 1 {
				return &upstreamTransportError{err: errors.New("dial failed"), proxy: client.usesProxy}
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(proxies.requests) != 2 || proxies.requests[0].Key != "owner@example.com" {
		t.Fatalf("unexpected proxy requests: %+v", proxies.requests)
	}
	if proxies.requests[0].Purpose != proxydomain.ProxyPurposeAuth || proxies.requests[0].IPVersion != proxydomain.ProxyIPv4 {
		t.Fatalf("unexpected proxy contract: %+v", proxies.requests[0])
	}
	if got := proxies.requests[1].AvoidProxyServerIDs; len(got) != 1 || got[0] != 101 {
		t.Fatalf("failed proxy was not excluded: %v", got)
	}
	if len(proxies.failure) != 1 || proxies.failure[0] != 11 || len(proxies.success) != 1 || proxies.success[0] != 12 {
		t.Fatalf("unexpected proxy reports: failure=%v success=%v", proxies.failure, proxies.success)
	}
}

func TestUpstreamBusinessErrorDoesNotPenalizeProxy(t *testing.T) {
	proxies := &proxyProviderStub{configs: []*proxyapp.ProxyConfig{
		{ID: 21, ProxyServerID: 201, URL: "http://proxy.invalid:8000"},
	}}
	service := &Service{client: NewClient(http.DefaultClient), proxies: proxies}
	err := service.withFetchClient(context.Background(), "owner@example.com", func(*Client) error {
		return ErrLoginFailed
	})
	if !errors.Is(err, ErrLoginFailed) {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(proxies.requests) != 1 || proxies.requests[0].Purpose != proxydomain.ProxyPurposeFetch {
		t.Fatalf("unexpected proxy requests: %+v", proxies.requests)
	}
	if len(proxies.failure) != 0 || len(proxies.success) != 1 || proxies.success[0] != 21 {
		t.Fatalf("business error changed proxy health: failure=%v success=%v", proxies.failure, proxies.success)
	}
}
