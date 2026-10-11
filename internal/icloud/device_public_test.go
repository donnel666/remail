package icloud

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

func configureDeviceTestServer(t *testing.T, s *Service, server *httptest.Server) {
	t.Helper()
	origin, err := url.Parse(server.URL)
	require.NoError(t, err)
	setDeviceTestSetting(t, runtimeconfig.ICloudDeviceBaseURLKey, strings.Replace(server.URL, "127.0.0.1", "devices.example", 1))
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		forwarded := request.Clone(request.Context())
		forwarded.URL.Scheme, forwarded.URL.Host = origin.Scheme, origin.Host
		return server.Client().Transport.RoundTrip(forwarded)
	})
	s.device.http.Transport, s.device.publicHTTP.Transport = transport, transport
}

func TestPublicDeviceCodeRejectsPrivateAndSpecialAddresses(t *testing.T) {
	for _, host := range []string{"localhost", "test.localhost", "127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254", "0.1.2.3", "100.64.0.1", "198.18.1.1", "192.0.2.1", "255.255.255.255", "[::1]", "[::ffff:127.0.0.1]", "[fc00::1]", "[fec0::1]", "[64:ff9b::7f00:1]", "[2002:7f00:1::]"} {
		require.False(t, validPublicDeviceCodeURL("https://"+host+"/code"), host)
		s := &Service{device: newDeviceClient()}
		_, err := s.FetchDeviceCode(context.Background(), "https://"+host+"/code")
		require.ErrorIs(t, err, errDeviceResponse, host)
	}
	for _, host := range []string{"8.8.8.8", "codes.example.com", "[2606:4700:4700::1111]"} {
		require.True(t, validPublicDeviceCodeURL("https://"+host+"/code"), host)
	}
	client := newPublicDeviceCodeClient()
	transport := client.Transport.(*http.Transport)
	require.Nil(t, transport.Proxy)
	connection, err := transport.DialContext(context.Background(), "tcp", "localhost:80")
	if connection != nil {
		connection.Close()
	}
	require.Error(t, err)
	require.ErrorIs(t, client.CheckRedirect(&http.Request{}, nil), http.ErrUseLastResponse)
}

func TestPublicDeviceCodeDoesNotSendManagementCredentials(t *testing.T) {
	setDeviceTestSetting(t, runtimeconfig.ICloudDeviceBaseURLKey, "https://codes.example.com")
	setDeviceTestSetting(t, runtimeconfig.ICloudDeviceAPIKey, "management-secret")
	s := &Service{device: newDeviceClient()}
	s.device.publicHTTP.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		require.Empty(t, request.Header.Get("Authorization"))
		require.Equal(t, http.Header{"Accept": {"application/json, text/plain"}}, request.Header)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("123456"))}, nil
	})
	code, err := s.FetchDeviceCode(context.Background(), "https://codes.example.com/code")
	require.NoError(t, err)
	require.Equal(t, "123456", code)
}
