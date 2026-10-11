package icloud

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var deviceCodeReservedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("3fff::/20"),
}

func publicDeviceCodeAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() ||
		(address.Is6() && !netip.MustParsePrefix("2000::/3").Contains(address)) {
		return false
	}
	for _, prefix := range deviceCodeReservedNetworks {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func validPublicDeviceCodeURL(raw string) bool {
	if !validDeviceCodeURL(raw) {
		return false
	}
	parsed, _ := url.Parse(raw)
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.Contains(host, "%") {
		return false
	}
	if address, err := netip.ParseAddr(host); err == nil {
		return publicDeviceCodeAddress(address)
	}
	return true
}

func newPublicDeviceCodeClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errDeviceResponse
		}
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(addresses) == 0 {
			return nil, errDeviceUnavailable
		}
		for _, address := range addresses {
			if !publicDeviceCodeAddress(address) {
				return nil, errDeviceResponse
			}
		}
		dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		for _, address := range addresses {
			// Dial the checked IP, so a second DNS lookup cannot reach a private host.
			connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(address.String(), port))
			if dialErr == nil {
				return connection, nil
			}
		}
		return nil, errDeviceUnavailable
	}
	return &http.Client{Transport: transport, Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (c *deviceClient) requestCode(ctx context.Context, codeAPI string) ([]byte, error) {
	if !validPublicDeviceCodeURL(codeAPI) {
		return nil, errDeviceResponse
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, codeAPI, nil)
	if err != nil {
		return nil, errDeviceResponse
	}
	request.Header.Set("Accept", "application/json, text/plain")
	public := *c
	public.http = c.publicHTTP
	if public.http == nil {
		public.http = newPublicDeviceCodeClient()
	}
	return public.do(request)
}
