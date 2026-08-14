package icloud

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	stdmail "net/mail"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	iCloudIMAPAddress          = "imap.mail.me.com:993"
	iCloudIMAPServerName       = "imap.mail.me.com"
	iCloudIMAPDefaultBodyBytes = 1 << 20
	iCloudIMAPBatchBodyBytes   = 100 << 20
	iCloudIMAPOperationTimeout = 60 * time.Second
	iCloudIMAPHistoryTimeout   = 15 * time.Minute
	iCloudIMAPBatchSize        = 100
	iCloudIMAPProxyAttempts    = 2
	iCloudIMAPMaxProxyAttempts = 20
)

var errICloudIMAPAuthentication = errors.New("icloud: IMAP authentication failed")

type iCloudIMAPProxyProvider interface {
	Acquire(context.Context, proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error)
	ReportSuccess(context.Context, uint) error
	ReportFailure(context.Context, uint, string) error
}

type iCloudIMAPMessage struct {
	UID               uint64
	Recipient         string
	Sender            string
	Raw               []byte
	ReceivedAt        time.Time
	ProviderMessageID string
}

type iCloudIMAPFetchRequest struct {
	Email       string
	AppPassword string
	UIDValidity string
	LastUID     uint64
	Aliases     []string
	SinceAt     time.Time
	UntilAt     time.Time
	MaxMessages int
	FullHistory bool
}

type iCloudIMAPClient struct {
	proxies iCloudIMAPProxyProvider
}

func (s *Service) SetPickupProxyProvider(proxies *proxyapp.ProxyUseCase) {
	if s != nil {
		s.imap = &iCloudIMAPClient{proxies: proxies}
	}
}

func (c *iCloudIMAPClient) Login(ctx context.Context, email, appPassword string) error {
	operationCtx, cancel := context.WithTimeout(ctx, runtimeconfig.Duration("imap_operation_timeout_seconds", iCloudIMAPOperationTimeout, time.Second, 1))
	defer cancel()
	_, closeClient, err := c.open(operationCtx, email, appPassword, proxydomain.ProxyPurposeAuth, false)
	if closeClient != nil {
		defer closeClient()
	}
	return err
}

func (c *iCloudIMAPClient) Fetch(ctx context.Context, request iCloudIMAPFetchRequest) ([]iCloudIMAPMessage, string, uint64, error) {
	timeout := runtimeconfig.Duration("imap_operation_timeout_seconds", iCloudIMAPOperationTimeout, time.Second, 1)
	if request.FullHistory {
		timeout = runtimeconfig.Duration("imap_full_history_timeout_minutes", iCloudIMAPHistoryTimeout, time.Minute, 1)
	}
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, closeClient, err := c.open(operationCtx, request.Email, request.AppPassword, proxydomain.ProxyPurposeFetch, request.FullHistory)
	if err != nil {
		return nil, request.UIDValidity, request.LastUID, err
	}
	defer closeClient()
	selected, err := client.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return nil, request.UIDValidity, request.LastUID, err
	}
	currentValidity := strconv.FormatUint(uint64(selected.UIDValidity), 10)
	lastUID := request.LastUID
	if request.FullHistory || currentValidity != strings.TrimSpace(request.UIDValidity) {
		lastUID = 0
	}
	criteria := &imap.SearchCriteria{}
	if lastUID > 0 && lastUID < uint64(^uint32(0)) {
		uidSet := imap.UIDSet{}
		uidSet.AddRange(imap.UID(lastUID+1), 0)
		criteria.UID = []imap.UIDSet{uidSet}
	}
	if lastUID == 0 && !request.SinceAt.IsZero() {
		criteria.Since = request.SinceAt.UTC()
	}
	search, err := client.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, request.UIDValidity, request.LastUID, err
	}
	uids := search.AllUIDs()
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	if len(uids) == 0 {
		_ = client.Logout().Wait()
		return nil, currentValidity, lastUID, nil
	}
	bodyLimit := min(runtimeconfig.Int("max_inbound_body_bytes", iCloudIMAPDefaultBodyBytes, 1), iCloudIMAPBatchBodyBytes)
	bodySection := &imap.FetchItemBodySection{Peek: true, Partial: &imap.SectionPartial{Size: int64(bodyLimit)}}
	batchSize := min(runtimeconfig.Int("mail_stream_batch_size", iCloudIMAPBatchSize, 1), 1000)
	if !request.FullHistory {
		scanLimit := iCloudIMAPScanLimit(batchSize, request.MaxMessages)
		uids = iCloudIMAPLatestUIDs(uids, scanLimit)
	}
	messages := make([]iCloudIMAPMessage, 0, len(uids))
	readUIDs := make(map[imap.UID]struct{}, len(uids))
	for start := 0; start < len(uids); start += batchSize {
		if err := operationCtx.Err(); err != nil {
			return nil, request.UIDValidity, request.LastUID, err
		}
		end := min(start+batchSize, len(uids))
		command := client.Fetch(imap.UIDSetNum(uids[start:end]...), &imap.FetchOptions{UID: true, InternalDate: true, BodySection: []*imap.FetchItemBodySection{bodySection}})
		for data := command.Next(); data != nil; data = command.Next() {
			row, collectErr := data.Collect()
			if collectErr != nil {
				_ = command.Close()
				return nil, request.UIDValidity, request.LastUID, collectErr
			}
			raw := row.FindBodySection(bodySection)
			if len(raw) == 0 {
				continue
			}
			readUIDs[row.UID] = struct{}{}
			receivedAt := row.InternalDate.UTC()
			if receivedAt.IsZero() {
				receivedAt = time.Now().UTC()
			}
			if (!request.SinceAt.IsZero() && receivedAt.Before(request.SinceAt.UTC())) ||
				(!request.UntilAt.IsZero() && receivedAt.After(request.UntilAt.UTC())) {
				continue
			}
			recipient := iCloudIMAPRecipient(raw, request.Aliases)
			if recipient == "" {
				continue
			}
			messages = append(messages, iCloudIMAPMessage{
				UID: uint64(row.UID), Recipient: recipient, Sender: iCloudIMAPSender(raw), Raw: raw, ReceivedAt: receivedAt,
				ProviderMessageID: "imap:INBOX:" + currentValidity + ":" + strconv.FormatUint(uint64(row.UID), 10),
			})
		}
		if err := command.Close(); err != nil {
			return nil, request.UIDValidity, request.LastUID, err
		}
	}
	sort.SliceStable(messages, func(i, j int) bool {
		if !messages[i].ReceivedAt.Equal(messages[j].ReceivedAt) {
			return messages[i].ReceivedAt.After(messages[j].ReceivedAt)
		}
		return messages[i].UID > messages[j].UID
	})
	_ = client.Logout().Wait()
	return messages, currentValidity, iCloudIMAPLastReadUID(lastUID, uids, readUIDs), nil
}

func iCloudIMAPScanLimit(batchSize, maxMessages int) int {
	if maxMessages > 0 {
		return min(batchSize, maxMessages)
	}
	return batchSize
}

func iCloudIMAPLatestUIDs(uids []imap.UID, limit int) []imap.UID {
	if limit <= 0 || len(uids) <= limit {
		return uids
	}
	return uids[len(uids)-limit:]
}

func iCloudIMAPLastReadUID(lastUID uint64, uids []imap.UID, readUIDs map[imap.UID]struct{}) uint64 {
	for _, uid := range uids {
		if _, ok := readUIDs[uid]; !ok {
			break
		}
		lastUID = uint64(uid)
	}
	return lastUID
}

func (c *iCloudIMAPClient) open(ctx context.Context, email, appPassword string, purpose proxydomain.ProxyPurpose, fullHistory bool) (*imapclient.Client, func(), error) {
	if c == nil || strings.TrimSpace(email) == "" || strings.TrimSpace(appPassword) == "" {
		return nil, nil, errors.New("icloud: IMAP credentials missing")
	}
	attempts := 1
	if c.proxies != nil {
		attempts = min(runtimeconfig.Int("max_proxy_attempts", iCloudIMAPProxyAttempts, 1), iCloudIMAPMaxProxyAttempts)
	}
	var lastErr error
	var avoidServerIDs []uint
	for attempt := 0; attempt < attempts; attempt++ {
		config, err := c.acquireProxy(ctx, email, purpose, fullHistory, attempt, avoidServerIDs)
		if err != nil {
			return nil, nil, err
		}
		proxyURL, proxyID := "", uint(0)
		if config != nil && !config.Direct {
			proxyURL, proxyID = config.URL, config.ID
		}
		client, closeClient, err := openICloudIMAP(ctx, email, appPassword, proxyURL)
		if err == nil {
			c.report(ctx, proxyID, true)
			return client, closeClient, nil
		}
		lastErr = err
		if errors.Is(err, errICloudIMAPAuthentication) {
			c.report(ctx, proxyID, true)
			return nil, nil, err
		}
		c.report(ctx, proxyID, false)
		avoidServerIDs = proxyapp.AppendAvoidProxyServerID(avoidServerIDs, config)
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
	}
	return nil, nil, lastErr
}

func (c *iCloudIMAPClient) acquireProxy(ctx context.Context, email string, purpose proxydomain.ProxyPurpose, fullHistory bool, attempt int, avoidServerIDs []uint) (*proxyapp.ProxyConfig, error) {
	if c.proxies == nil {
		return &proxyapp.ProxyConfig{Direct: true}, nil
	}
	ipVersion := proxydomain.ProxyIPv4
	if fullHistory {
		ipVersion = proxydomain.ProxyIPv6
	}
	return c.proxies.Acquire(ctx, proxyapp.AcquireProxyRequest{
		Key: strings.ToLower(strings.TrimSpace(email)), IPVersion: ipVersion, Purpose: purpose,
		AllowSystemFallback: true, Attempt: attempt, RequestID: iCloudRequestID(ctx), AvoidProxyServerIDs: avoidServerIDs,
	})
}

func (c *iCloudIMAPClient) report(ctx context.Context, proxyID uint, success bool) {
	if c == nil || c.proxies == nil || proxyID == 0 {
		return
	}
	reportCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if success {
		_ = c.proxies.ReportSuccess(reportCtx, proxyID)
	} else {
		_ = c.proxies.ReportFailure(reportCtx, proxyID, "iCloud IMAP transport failed.")
	}
}

func iCloudRequestID(ctx context.Context) string {
	if ctx != nil {
		if value, ok := ctx.Value(platform.RequestIDKey).(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return platform.NewUUIDV7String()
}

func openICloudIMAP(ctx context.Context, email, appPassword, proxyURL string) (*imapclient.Client, func(), error) {
	conn, err := dialICloudIMAPConn(ctx, proxyURL)
	if err != nil {
		return nil, nil, err
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: iCloudIMAPServerName, NextProtos: []string{"imap"}}
	tlsConn := tls.Client(conn, tlsConfig)
	if err := setICloudIMAPDeadline(ctx, tlsConn, runtimeconfig.Duration("imap_operation_timeout_seconds", iCloudIMAPOperationTimeout, time.Second, 1)); err != nil {
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
	var closeOnce sync.Once
	closeClient := func() { closeOnce.Do(func() { _ = client.Close() }) }
	stopClose := context.AfterFunc(ctx, closeClient)
	closeClientWithStop := func() { stopClose(); closeClient() }
	if err := client.Login(strings.TrimSpace(email), strings.TrimSpace(appPassword)).Wait(); err != nil {
		closeClientWithStop()
		if isICloudIMAPAuthFailure(err) {
			return nil, nil, fmt.Errorf("%w: %v", errICloudIMAPAuthentication, err)
		}
		return nil, nil, err
	}
	return client, closeClientWithStop, nil
}

func isICloudIMAPAuthFailure(err error) bool {
	var imapErr *imap.Error
	if !errors.As(err, &imapErr) || imapErr == nil {
		return false
	}
	if imapErr.Code == imap.ResponseCodeAuthenticationFailed || imapErr.Code == imap.ResponseCodeAuthorizationFailed {
		return true
	}
	text := strings.ToLower(strings.TrimSpace(imapErr.Text))
	return imapErr.Type == imap.StatusResponseTypeNo && (strings.Contains(text, "authentication failed") || strings.Contains(text, "login failed") || strings.Contains(text, "invalid credentials") || strings.Contains(text, "app password"))
}

func dialICloudIMAPConn(ctx context.Context, proxyURL string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: runtimeconfig.Duration("imap_dial_timeout_seconds", 20*time.Second, time.Second, 1), KeepAlive: runtimeconfig.Duration("imap_keepalive_seconds", 30*time.Second, time.Second, 1)}
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return dialer.DialContext(ctx, "tcp", iCloudIMAPAddress)
	}
	if !strings.Contains(proxyURL, "://") {
		proxyURL = "http://" + proxyURL
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil || parsed.Host == "" {
		return nil, errors.New("invalid proxy url")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "socks5", "socks5h":
		var auth *xproxy.Auth
		if parsed.User != nil {
			password, _ := parsed.User.Password()
			auth = &xproxy.Auth{User: parsed.User.Username(), Password: password}
		}
		d, err := xproxy.SOCKS5("tcp", parsed.Host, auth, dialer)
		if err != nil {
			return nil, err
		}
		contextDialer, ok := d.(xproxy.ContextDialer)
		if !ok {
			return nil, errors.New("socks5 proxy lacks context support")
		}
		return contextDialer.DialContext(ctx, "tcp", iCloudIMAPAddress)
	case "http", "https":
		return dialICloudHTTPConnect(ctx, dialer, parsed)
	default:
		return nil, errors.New("unsupported proxy scheme")
	}
}

func dialICloudHTTPConnect(ctx context.Context, dialer *net.Dialer, parsed *url.URL) (net.Conn, error) {
	conn, err := dialer.DialContext(ctx, "tcp", parsed.Host)
	if err != nil {
		return nil, err
	}
	if err := setICloudIMAPDeadline(ctx, conn, runtimeconfig.Duration("imap_operation_timeout_seconds", iCloudIMAPOperationTimeout, time.Second, 1)); err != nil {
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
	request := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", iCloudIMAPAddress, iCloudIMAPAddress)
	if parsed.User != nil {
		password, _ := parsed.User.Password()
		request += "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(parsed.User.Username()+":"+password)) + "\r\n"
	}
	if _, err := conn.Write([]byte(request + "\r\n")); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, " 200 ") {
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

func setICloudIMAPDeadline(ctx context.Context, conn net.Conn, fallback time.Duration) error {
	deadline := time.Now().Add(fallback)
	if ctx != nil {
		if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
			deadline = value
		}
	}
	return conn.SetDeadline(deadline)
}

func iCloudIMAPRecipient(raw []byte, aliases []string) string {
	message, err := stdmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	known := make(map[string]string, len(aliases))
	for _, alias := range aliases {
		alias = strings.ToLower(strings.TrimSpace(alias))
		if alias != "" {
			known[alias] = alias
		}
	}
	for _, name := range []string{"Delivered-To", "X-Original-To", "Envelope-To", "To", "Cc"} {
		matches := make(map[string]struct{})
		header := strings.ToLower(message.Header.Get(name))
		for alias := range known {
			if strings.Contains(header, alias) {
				matches[alias] = struct{}{}
			}
		}
		if len(matches) == 1 {
			for alias := range matches {
				return alias
			}
		}
		if len(matches) > 1 {
			return ""
		}
	}
	return ""
}

func iCloudIMAPSender(raw []byte) string {
	message, err := stdmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	addresses, err := stdmail.ParseAddressList(message.Header.Get("From"))
	if err == nil && len(addresses) == 1 {
		return strings.ToLower(strings.TrimSpace(addresses[0].Address))
	}
	return strings.TrimSpace(message.Header.Get("From"))
}
