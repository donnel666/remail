package infra

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/proxy/domain"
	xproxy "golang.org/x/net/proxy"
)

type ProxyChecker struct {
	timeout   time.Duration
	endpoints []checkEndpoint
	speedURLs []string
}

type checkEndpoint struct {
	name  string
	url   string
	parse func([]byte) (detectedProxy, error)
}

type detectedProxy struct {
	ip      string
	country string
}

func NewProxyChecker() *ProxyChecker {
	return &ProxyChecker{
		timeout: 6 * time.Second,
		endpoints: []checkEndpoint{
			{
				name:  "cloudflare",
				url:   "https://www.cloudflare.com/cdn-cgi/trace",
				parse: parseCloudflareTrace,
			},
			{
				name:  "country.is",
				url:   "https://api.country.is/",
				parse: parseCountryIS,
			},
			{
				name:  "ipinfo",
				url:   "https://ipinfo.io/json",
				parse: parseIPInfo,
			},
		},
		speedURLs: []string{
			"https://www.microsoft.com/favicon.ico",
			"https://www.google.com/generate_204",
		},
	}
}

func (c *ProxyChecker) Check(ctx context.Context, proxyURL string) (domain.CheckResult, error) {
	normalizedURL, err := domain.NormalizeProxyURL(proxyURL)
	if err != nil {
		return domain.CheckResult{NonRetryable: true, LastSafeError: "Invalid proxy URL.", CheckedAt: time.Now().UTC()}, err
	}
	client, err := c.httpClient(normalizedURL)
	if err != nil {
		return domain.CheckResult{NonRetryable: true, LastSafeError: "Invalid proxy URL.", CheckedAt: time.Now().UTC()}, err
	}

	var lastSafeError string
	for _, endpoint := range c.endpoints {
		start := time.Now()
		reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint.url, nil)
		if err != nil {
			cancel()
			lastSafeError = "Proxy check request failed."
			continue
		}
		req.Header.Set("User-Agent", "RemailProxyChecker/1.0")

		resp, err := client.Do(req)
		if err != nil {
			cancel()
			lastSafeError = "Proxy endpoint is unreachable."
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		closeErr := resp.Body.Close()
		cancel()
		if readErr != nil || closeErr != nil {
			lastSafeError = "Proxy endpoint response is unreadable."
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastSafeError = "Proxy endpoint returned an unexpected status."
			continue
		}

		detected, err := endpoint.parse(body)
		if err != nil {
			lastSafeError = "Proxy endpoint response is invalid."
			continue
		}
		ipVersion := domain.IPVersionFromAddress(detected.ip)
		if ipVersion != domain.ProxyIPv4 && ipVersion != domain.ProxyIPv6 {
			lastSafeError = "Proxy endpoint returned an invalid IP."
			continue
		}
		latencyMs := int(time.Since(start).Milliseconds())
		if speedMs, ok := c.measureLatency(ctx, client); ok {
			latencyMs = speedMs
		}
		return domain.CheckResult{
			IPVersion:  ipVersion,
			OutboundIP: detected.ip,
			Country:    domain.NormalizeCountry(detected.country),
			LatencyMs:  latencyMs,
			CheckedAt:  time.Now().UTC(),
		}, nil
	}

	if lastSafeError == "" {
		lastSafeError = "Proxy check failed."
	}
	return domain.CheckResult{
		LastSafeError: lastSafeError,
		CheckedAt:     time.Now().UTC(),
	}, domain.ErrProxyCheckFailed
}

func (c *ProxyChecker) measureLatency(ctx context.Context, client *http.Client) (int, bool) {
	for _, targetURL := range c.speedURLs {
		reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, targetURL, nil)
		if err != nil {
			cancel()
			continue
		}
		req.Header.Set("User-Agent", "RemailProxyChecker/1.0")
		req.Header.Set("Range", "bytes=0-0")
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		_ = resp.Body.Close()
		cancel()
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			return int(time.Since(start).Milliseconds()), true
		}
	}
	return 0, false
}

func (c *ProxyChecker) httpClient(proxyURL string) (*http.Client, error) {
	transport := &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		MaxIdleConns:          2,
		IdleConnTimeout:       15 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: time.Second,
	}

	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		transport.Proxy = http.ProxyURL(parsed)
	case "socks5", "socks5h":
		dialer, err := xproxy.FromURL(parsed, xproxy.Direct)
		if err != nil {
			return nil, err
		}
		contextDialer, ok := dialer.(xproxy.ContextDialer)
		if ok {
			transport.DialContext = contextDialer.DialContext
		} else {
			transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				type dialResult struct {
					conn net.Conn
					err  error
				}
				result := make(chan dialResult, 1)
				go func() {
					conn, err := dialer.Dial(network, address)
					result <- dialResult{conn: conn, err: err}
				}()
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case res := <-result:
					return res.conn, res.err
				}
			}
		}
	default:
		return nil, domain.ErrInvalidProxyURL
	}

	return &http.Client{
		Transport: transport,
		Timeout:   c.timeout + time.Second,
	}, nil
}

func parseCloudflareTrace(body []byte) (detectedProxy, error) {
	lines := strings.Split(string(body), "\n")
	var detected detectedProxy
	for _, line := range lines {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "ip":
			detected.ip = strings.TrimSpace(value)
		case "loc":
			detected.country = strings.TrimSpace(value)
		}
	}
	if detected.ip == "" {
		return detectedProxy{}, errors.New("missing ip")
	}
	return detected, nil
}

func parseCountryIS(body []byte) (detectedProxy, error) {
	var payload struct {
		IP      string `json:"ip"`
		Country string `json:"country"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return detectedProxy{}, err
	}
	if payload.IP == "" {
		return detectedProxy{}, errors.New("missing ip")
	}
	return detectedProxy{ip: payload.IP, country: payload.Country}, nil
}

func parseIPInfo(body []byte) (detectedProxy, error) {
	var payload struct {
		IP      string `json:"ip"`
		Country string `json:"country"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return detectedProxy{}, err
	}
	if payload.IP == "" {
		return detectedProxy{}, errors.New("missing ip")
	}
	return detectedProxy{ip: payload.IP, country: payload.Country}, nil
}
