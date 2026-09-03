package gmail

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/platform"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
	"github.com/stretchr/testify/require"
)

type localGmailPickupProxyStub struct {
	requests  []proxyapp.AcquireProxyRequest
	configs   []*proxyapp.ProxyConfig
	successes []uint
	failures  []uint
}

func (s *localGmailPickupProxyStub) Acquire(_ context.Context, request proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error) {
	s.requests = append(s.requests, request)
	index := len(s.requests) - 1
	if index >= len(s.configs) {
		return &proxyapp.ProxyConfig{Direct: true}, nil
	}
	return s.configs[index], nil
}

func (s *localGmailPickupProxyStub) ReportSuccess(_ context.Context, proxyID uint) error {
	s.successes = append(s.successes, proxyID)
	return nil
}

func (s *localGmailPickupProxyStub) ReportFailure(_ context.Context, proxyID uint, _ string) error {
	s.failures = append(s.failures, proxyID)
	return nil
}

func TestLocalGmailPickupClientCopiesMicrosoftProxyRetryContractWithGmailFingerprint(t *testing.T) {
	setGmailRuntime(t, map[string]string{"max_proxy_attempts": "2"})
	proxies := &localGmailPickupProxyStub{configs: []*proxyapp.ProxyConfig{
		{ID: 11, ProxyServerID: 10, URL: "socks5://first.invalid:1080"},
		{ID: 21, ProxyServerID: 20, URL: "http://second.invalid:8080"},
	}}
	client := newLocalGmailPickupClient(proxies)
	var proxyURLs []string
	var fingerprints []localGmailClientFingerprint
	client.fetch = func(
		_ context.Context, _, _ string, cursor localGmailFolderCursors, _ time.Time, proxyURL string, fingerprint localGmailClientFingerprint, fullHistory bool,
	) ([]localGmailFetchedMessage, localGmailFolderCursors, error) {
		require.False(t, fullHistory)
		proxyURLs = append(proxyURLs, proxyURL)
		fingerprints = append(fingerprints, fingerprint)
		if len(proxyURLs) == 1 {
			return nil, cursor, errors.New("proxy connection reset")
		}
		return []localGmailFetchedMessage{{UID: 9}}, localGmailFolderCursors{Inbox: 99, Spam: 88}, nil
	}

	ctx := platform.WithRequestID(context.Background(), "gmail-pickup-request")
	messages, cursor, err := client.Fetch(ctx, " Owner.Name@GMAIL.com ", "app-password", localGmailFolderCursors{Inbox: 7}, time.Time{}, false)

	require.NoError(t, err)
	require.Len(t, messages, 1)
	require.Equal(t, localGmailFolderCursors{Inbox: 99, Spam: 88}, cursor)
	require.Equal(t, []string{"socks5://first.invalid:1080", "http://second.invalid:8080"}, proxyURLs)
	require.Len(t, proxies.requests, 2)
	for attempt, request := range proxies.requests {
		require.Equal(t, "owner.name@gmail.com", request.Key)
		require.Equal(t, []proxydomain.ProxyIPVersion{proxydomain.ProxyIPv6, proxydomain.ProxyIPv4}[attempt], request.IPVersion)
		require.Equal(t, proxydomain.ProxyPurposeFetch, request.Purpose)
		require.True(t, request.AllowSystemFallback)
		require.Zero(t, request.Attempt)
		require.Equal(t, "gmail-pickup-request", request.RequestID)
	}
	require.Empty(t, proxies.requests[0].AvoidProxyServerIDs)
	require.Equal(t, []uint{10}, proxies.requests[1].AvoidProxyServerIDs)
	require.Equal(t, []uint{11}, proxies.failures)
	require.Equal(t, []uint{21}, proxies.successes)

	require.Len(t, fingerprints, 2)
	require.Equal(t, fingerprints[0].ID, fingerprints[1].ID)
	require.Equal(t, "Remail Gmail Pickup", fingerprints[0].ID.Name)
	require.Equal(t, "gmail-imap", fingerprints[0].ID.Environment)
	require.NotNil(t, fingerprints[0].TLSConfig)
	require.Equal(t, localGmailIMAPServerName, fingerprints[0].TLSConfig.ServerName)
	require.Equal(t, uint16(0x0303), fingerprints[0].TLSConfig.MinVersion)
	require.Equal(t, uint16(0x0304), fingerprints[0].TLSConfig.MaxVersion)
}

func TestLocalGmailPickupClientTreatsAuthenticationAsHealthyProxy(t *testing.T) {
	proxies := &localGmailPickupProxyStub{configs: []*proxyapp.ProxyConfig{{
		ID: 31, ProxyServerID: 30, URL: "socks5://proxy.invalid:1080",
	}}}
	client := newLocalGmailPickupClient(proxies)
	client.fetch = func(
		context.Context, string, string, localGmailFolderCursors, time.Time, string, localGmailClientFingerprint, bool,
	) ([]localGmailFetchedMessage, localGmailFolderCursors, error) {
		return nil, localGmailFolderCursors{}, errLocalGmailAuthentication
	}

	_, _, err := client.Fetch(context.Background(), "owner@gmail.com", "bad-app-password", localGmailFolderCursors{}, time.Time{}, false)

	require.ErrorIs(t, err, errLocalGmailAuthentication)
	require.Len(t, proxies.requests, 1)
	require.Equal(t, proxydomain.ProxyIPv6, proxies.requests[0].IPVersion)
	require.Empty(t, proxies.failures)
	require.Equal(t, []uint{31}, proxies.successes)
}

func TestLocalGmailPickupFullHistoryFallsBackToIPv4InSameAttempt(t *testing.T) {
	setGmailRuntime(t, map[string]string{"max_proxy_attempts": "1"})
	proxies := &localGmailPickupProxyStub{configs: []*proxyapp.ProxyConfig{
		{ID: 41, ProxyServerID: 40, URL: "socks5://first.invalid:1080"},
		{ID: 51, ProxyServerID: 50, URL: "socks5://second.invalid:1080"},
	}}
	client := newLocalGmailPickupClient(proxies)
	attempts := 0
	client.fetch = func(
		context.Context, string, string, localGmailFolderCursors, time.Time, string, localGmailClientFingerprint, bool,
	) ([]localGmailFetchedMessage, localGmailFolderCursors, error) {
		attempts++
		if attempts == 1 {
			return nil, localGmailFolderCursors{}, context.DeadlineExceeded
		}
		return nil, localGmailFolderCursors{Inbox: 1, Spam: 2}, nil
	}

	_, _, err := client.Fetch(context.Background(), "owner@gmail.com", "app-password", localGmailFolderCursors{}, time.Time{}, true)

	require.NoError(t, err)
	require.Len(t, proxies.requests, 2)
	require.Equal(t, proxydomain.ProxyIPv6, proxies.requests[0].IPVersion)
	require.Equal(t, proxydomain.ProxyIPv4, proxies.requests[1].IPVersion)
	require.Zero(t, proxies.requests[0].Attempt)
	require.Zero(t, proxies.requests[1].Attempt)
	require.Equal(t, []uint{41}, proxies.failures)
	require.Equal(t, []uint{51}, proxies.successes)
	require.Equal(t, []uint{40}, proxies.requests[1].AvoidProxyServerIDs)
}

func TestLocalGmailPickupFallsBackToIPv4WhenIPv6RouteIsUnavailable(t *testing.T) {
	proxies := &localGmailPickupProxyStub{configs: []*proxyapp.ProxyConfig{
		{Direct: true},
		{ID: 61, ProxyServerID: 60, URL: "socks5://ipv4.invalid:1080"},
	}}
	client := newLocalGmailPickupClient(proxies)
	var proxyURL string
	client.fetch = func(
		_ context.Context, _, _ string, _ localGmailFolderCursors, _ time.Time, routedProxyURL string, _ localGmailClientFingerprint, _ bool,
	) ([]localGmailFetchedMessage, localGmailFolderCursors, error) {
		proxyURL = routedProxyURL
		return nil, localGmailFolderCursors{Inbox: 1, Spam: 2}, nil
	}

	_, _, err := client.Fetch(context.Background(), "owner@gmail.com", "app-password", localGmailFolderCursors{}, time.Time{}, true)

	require.NoError(t, err)
	require.Equal(t, "socks5://ipv4.invalid:1080", proxyURL)
	require.Len(t, proxies.requests, 2)
	require.Equal(t, proxydomain.ProxyIPv6, proxies.requests[0].IPVersion)
	require.Equal(t, proxydomain.ProxyIPv4, proxies.requests[1].IPVersion)
	require.Equal(t, 0, proxies.requests[0].Attempt)
	require.Equal(t, 0, proxies.requests[1].Attempt)
	require.Equal(t, []uint{61}, proxies.successes)
}
