package gmail

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/platform"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	xproxy "golang.org/x/net/proxy"
)

const (
	localGmailIMAPAddress                 = "imap.gmail.com:993"
	localGmailIMAPServerName              = "imap.gmail.com"
	localGmailPickupProxyAttempts         = 2
	localGmailPickupMaxProxyAttempts      = 20
	localGmailPickupProxyHandshakeTimeout = 30 * time.Second
)

var errLocalGmailAuthentication = errors.New("gmail: local IMAP authentication failed")

type localGmailPickupProxyProvider interface {
	Acquire(context.Context, proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error)
	ReportSuccess(context.Context, uint) error
	ReportFailure(context.Context, uint, string) error
}

type localGmailClientFingerprint struct {
	TLSConfig *tls.Config
	ID        imap.IDData
}

type localGmailRoutedFetchFunc func(
	ctx context.Context,
	email, appPassword string,
	cursors localGmailFolderCursors,
	since time.Time,
	proxyURL string,
	fingerprint localGmailClientFingerprint,
	fullHistory bool,
) ([]localGmailFetchedMessage, localGmailFolderCursors, error)

type localGmailPickupClient struct {
	proxies localGmailPickupProxyProvider
	fetch   localGmailRoutedFetchFunc
}

func newLocalGmailPickupClient(proxies localGmailPickupProxyProvider) *localGmailPickupClient {
	return &localGmailPickupClient{proxies: proxies, fetch: fetchLocalGmailMessagesThrough}
}

// SetPickupProxyProvider injects only the shared proxy pool. Gmail keeps its
// own fingerprint, dialing, retry and provider protocol implementation.
func (s *Service) SetPickupProxyProvider(proxies *proxyapp.ProxyUseCase) {
	if s != nil {
		s.validationProxies = proxies
		if s.pickup != nil {
			s.pickup.proxies = proxies
		}
	}
}

func (c *localGmailPickupClient) Fetch(
	ctx context.Context,
	email, appPassword string,
	cursors localGmailFolderCursors,
	since time.Time,
	fullHistory bool,
) ([]localGmailFetchedMessage, localGmailFolderCursors, error) {
	if c == nil || c.fetch == nil {
		return nil, cursors, errors.New("gmail: pickup client unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	fingerprint := newLocalGmailClientFingerprint()
	requestID := localGmailPickupRequestID(ctx)
	proxyAttempts := min(
		runtimeconfig.Int("max_proxy_attempts", localGmailPickupProxyAttempts, 1),
		localGmailPickupMaxProxyAttempts,
	)
	attemptTimeout := localGmailFetchTimeout
	if fullHistory {
		attemptTimeout = min(
			runtimeconfig.Duration("imap_full_history_timeout_minutes", localGmailHistoryMailboxTimeout, time.Minute, 1),
			localGmailHistoryMaxTimeout,
		)
	}
	var (
		avoidServerIDs []uint
		lastErr        error
		ipVersion      = proxydomain.ProxyIPv6
	)
	for attempt := 0; attempt < proxyAttempts; attempt++ {
		for {
			if err := ctx.Err(); err != nil {
				return nil, cursors, err
			}
			proxyConfig, err := c.acquireProxy(ctx, email, requestID, ipVersion, attempt, avoidServerIDs)
			if err != nil && ipVersion == proxydomain.ProxyIPv6 && errors.Is(err, proxydomain.ErrProxyUnavailable) {
				ipVersion = proxydomain.ProxyIPv4
				continue
			}
			if err != nil {
				return nil, cursors, fmt.Errorf("acquire Gmail pickup proxy: %w", err)
			}
			if ipVersion == proxydomain.ProxyIPv6 && c.proxies != nil && (proxyConfig == nil || proxyConfig.Direct) {
				// Keep the family fallback in this proxy attempt so IPv4 resource bindings remain eligible.
				ipVersion = proxydomain.ProxyIPv4
				continue
			}
			proxyURL := ""
			proxyID := uint(0)
			if proxyConfig != nil && !proxyConfig.Direct {
				proxyURL = proxyConfig.URL
				proxyID = proxyConfig.ID
			}
			attemptCtx, cancelAttempt := context.WithTimeout(ctx, attemptTimeout)
			messages, nextCursors, err := c.fetch(
				attemptCtx, email, appPassword, cursors, since, proxyURL, fingerprint, fullHistory,
			)
			attemptErr := attemptCtx.Err()
			cancelAttempt()
			if err == nil {
				_ = c.reportProxySuccess(ctx, proxyID)
				return messages, nextCursors, nil
			}
			lastErr = errors.Join(err, attemptErr)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, cursors, errors.Join(err, ctxErr)
			}
			if errors.Is(err, errLocalGmailAuthentication) {
				_ = c.reportProxySuccess(ctx, proxyID)
				return nil, cursors, err
			}
			if proxyURL != "" && (errors.Is(attemptErr, context.DeadlineExceeded) || isLocalGmailPickupProxyFailure(err, proxyURL)) {
				avoidServerIDs = proxyapp.AppendAvoidProxyServerID(avoidServerIDs, proxyConfig)
				_ = c.reportProxyFailure(ctx, proxyID, "Gmail IMAP transport failed.")
				if ipVersion == proxydomain.ProxyIPv6 {
					// Try IPv4 before this task consumes another proxy attempt.
					ipVersion = proxydomain.ProxyIPv4
					continue
				}
				break
			}
			_ = c.reportProxySuccess(ctx, proxyID)
			return nil, cursors, lastErr
		}
	}
	if lastErr != nil {
		return nil, cursors, lastErr
	}
	return nil, cursors, errors.New("gmail: pickup client unavailable")
}

func (c *localGmailPickupClient) acquireProxy(
	ctx context.Context,
	email, requestID string,
	ipVersion proxydomain.ProxyIPVersion,
	attempt int,
	avoidServerIDs []uint,
) (*proxyapp.ProxyConfig, error) {
	if c == nil || c.proxies == nil {
		return &proxyapp.ProxyConfig{Direct: true}, nil
	}
	return c.proxies.Acquire(ctx, proxyapp.AcquireProxyRequest{
		Key:                 strings.ToLower(strings.TrimSpace(email)),
		IPVersion:           ipVersion,
		Purpose:             proxydomain.ProxyPurposeFetch,
		AllowSystemFallback: true,
		Attempt:             attempt,
		RequestID:           requestID,
		AvoidProxyServerIDs: avoidServerIDs,
	})
}

func (c *localGmailPickupClient) reportProxySuccess(ctx context.Context, proxyID uint) error {
	if c == nil || c.proxies == nil || proxyID == 0 || (ctx != nil && ctx.Err() != nil) {
		return nil
	}
	return c.proxies.ReportSuccess(ctx, proxyID)
}

func (c *localGmailPickupClient) reportProxyFailure(ctx context.Context, proxyID uint, safeError string) error {
	if c == nil || c.proxies == nil || proxyID == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reportCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return c.proxies.ReportFailure(reportCtx, proxyID, safeError)
}

func localGmailPickupRequestID(ctx context.Context) string {
	if ctx != nil {
		if requestID, ok := ctx.Value(platform.RequestIDKey).(string); ok && strings.TrimSpace(requestID) != "" {
			return strings.TrimSpace(requestID)
		}
	}
	return platform.NewUUIDV7String()
}

func newLocalGmailClientFingerprint() localGmailClientFingerprint {
	return localGmailClientFingerprint{
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			MaxVersion: tls.VersionTLS13,
			ServerName: localGmailIMAPServerName,
			NextProtos: []string{"imap"},
			CurvePreferences: []tls.CurveID{
				tls.X25519,
				tls.CurveP256,
				tls.CurveP384,
			},
			CipherSuites: []uint16{
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
				tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
				tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			},
		},
		ID: imap.IDData{
			Name: "Remail Gmail Pickup", Version: "1.0", OS: "Linux", Vendor: "Remail",
			Environment: "gmail-imap",
		},
	}
}

func isDefinitiveLocalGmailAuthFailure(err error) bool {
	var imapErr *imap.Error
	if !errors.As(err, &imapErr) || imapErr == nil {
		return false
	}
	if imapErr.Code == imap.ResponseCodeAuthenticationFailed || imapErr.Code == imap.ResponseCodeAuthorizationFailed {
		return true
	}
	if imapErr.Type != imap.StatusResponseTypeNo {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(imapErr.Text))
	return strings.Contains(text, "authentication failed") || strings.Contains(text, "login failed") ||
		strings.Contains(text, "invalid credentials") || strings.Contains(text, "app password")
}

func openLocalGmailPickupIMAP(
	ctx context.Context,
	email, appPassword, proxyURL string,
	fingerprint localGmailClientFingerprint,
) (*imapclient.Client, func(), error) {
	conn, err := dialLocalGmailPickupConn(ctx, proxyURL)
	if err != nil {
		return nil, nil, err
	}
	tlsConfig := fingerprint.TLSConfig
	if tlsConfig == nil {
		tlsConfig = newLocalGmailClientFingerprint().TLSConfig
	}
	tlsConfig = tlsConfig.Clone()
	tlsConn := tls.Client(conn, tlsConfig)
	if err := setLocalGmailPickupDeadline(ctx, tlsConn, localGmailFetchTimeout); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if err := tlsConn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}

	client := imapclient.New(tlsConn, &imapclient.Options{TLSConfig: tlsConfig})
	stopClose := context.AfterFunc(ctx, func() { _ = client.Close() })
	closeClient := func() {
		stopClose()
		_ = client.Close()
	}
	if client.Caps().Has(imap.CapID) {
		if _, err := client.ID(&fingerprint.ID).Wait(); err != nil {
			closeClient()
			return nil, nil, err
		}
	}
	if err := client.Login(strings.TrimSpace(email), appPassword).Wait(); err != nil {
		closeClient()
		if isDefinitiveLocalGmailAuthFailure(err) {
			return nil, nil, fmt.Errorf("%w: %v", errLocalGmailAuthentication, err)
		}
		return nil, nil, err
	}
	return client, closeClient, nil
}

func isLocalGmailPickupProxyFailure(err error, proxyURL string) bool {
	if err == nil || strings.TrimSpace(proxyURL) == "" ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, errLocalGmailAuthentication) {
		return false
	}
	var imapErr *imap.Error
	return !errors.As(err, &imapErr)
}

func dialLocalGmailPickupConn(ctx context.Context, proxyURL string) (net.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	dialer := &net.Dialer{
		Timeout:   runtimeconfig.Duration("imap_dial_timeout_seconds", 20*time.Second, time.Second, 1),
		KeepAlive: runtimeconfig.Duration("imap_keepalive_seconds", 30*time.Second, time.Second, 1),
	}
	proxyURL = normalizeLocalGmailPickupProxyURL(proxyURL)
	if proxyURL == "" {
		return dialer.DialContext(ctx, "tcp", localGmailIMAPAddress)
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil || parsed.Host == "" {
		return nil, errors.New("invalid proxy url")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "socks5", "socks5h":
		auth := (*xproxy.Auth)(nil)
		if parsed.User != nil {
			password, _ := parsed.User.Password()
			auth = &xproxy.Auth{User: parsed.User.Username(), Password: password}
		}
		socksDialer, err := xproxy.SOCKS5("tcp", parsed.Host, auth, dialer)
		if err != nil {
			return nil, err
		}
		contextDialer, ok := socksDialer.(xproxy.ContextDialer)
		if !ok {
			return nil, errors.New("socks5 proxy does not support context cancellation")
		}
		handshakeCtx, cancel := context.WithTimeout(ctx, runtimeconfig.Duration(
			"proxy_handshake_timeout_seconds", localGmailPickupProxyHandshakeTimeout, time.Second, 1,
		))
		conn, err := contextDialer.DialContext(handshakeCtx, "tcp", localGmailIMAPAddress)
		cancel()
		return conn, err
	case "http", "https":
		return dialLocalGmailHTTPConnect(ctx, dialer, parsed)
	default:
		return nil, errors.New("unsupported proxy scheme")
	}
}

func normalizeLocalGmailPickupProxyURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "://") {
		return value
	}
	return "http://" + value
}

func dialLocalGmailHTTPConnect(ctx context.Context, dialer *net.Dialer, parsed *url.URL) (net.Conn, error) {
	conn, err := dialer.DialContext(ctx, "tcp", parsed.Host)
	if err != nil {
		return nil, err
	}
	if err := setLocalGmailPickupDeadline(ctx, conn, runtimeconfig.Duration(
		"proxy_handshake_timeout_seconds", localGmailPickupProxyHandshakeTimeout, time.Second, 1,
	)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if strings.EqualFold(parsed.Scheme, "https") {
		tlsConn := tls.Client(conn, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: parsed.Hostname()})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		conn = tlsConn
	}
	request := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", localGmailIMAPAddress, localGmailIMAPAddress)
	if parsed.User != nil {
		password, _ := parsed.User.Password()
		credential := base64.StdEncoding.EncodeToString([]byte(parsed.User.Username() + ":" + password))
		request += "Proxy-Authorization: Basic " + credential + "\r\n"
	}
	request += "\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !strings.Contains(line, " 200 ") {
		_ = conn.Close()
		return nil, errors.New("proxy connect failed")
	}
	for {
		header, err := reader.ReadString('\n')
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		if strings.TrimSpace(header) == "" {
			break
		}
	}
	return conn, nil
}

func setLocalGmailPickupDeadline(ctx context.Context, conn net.Conn, fallback time.Duration) error {
	if conn == nil {
		return errors.New("network connection is nil")
	}
	deadline := time.Now().Add(fallback)
	if ctx != nil {
		if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
			deadline = contextDeadline
		}
	}
	return conn.SetDeadline(deadline)
}
