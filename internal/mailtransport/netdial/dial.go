// Package netdial provides the proxy and TLS connection used by mailbox protocols.
package netdial

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

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"golang.org/x/net/proxy"
)

const OperationTimeout = 60 * time.Second
const ProxyHandshakeTimeout = 30 * time.Second

// DialTLS uses the Microsoft mailbox TLS profile. Empty proxy is reserved for explicit direct callers.
func DialTLS(ctx context.Context, proxyURL, target, alpn string) (net.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var conn net.Conn
	var err error
	if strings.TrimSpace(proxyURL) == "" {
		dialer := &net.Dialer{Timeout: runtimeconfig.Duration("imap_dial_timeout_seconds", 20*time.Second, time.Second, 1), KeepAlive: runtimeconfig.Duration("imap_keepalive_seconds", 30*time.Second, time.Second, 1)}
		conn, err = dialer.DialContext(ctx, "tcp", target)
	} else {
		conn, err = DialProxy(ctx, proxyURL, target)
	}
	if err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		_ = conn.Close()
		return nil, errors.New("invalid mail server address")
	}
	tlsConn := tls.Client(conn, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host, NextProtos: []string{alpn}})
	if err = SetDeadline(ctx, tlsConn, runtimeconfig.Duration("imap_operation_timeout_seconds", OperationTimeout, time.Second, 1)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err = tlsConn.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err = tlsConn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return tlsConn, nil
}

func DialProxy(ctx context.Context, proxyURL, target string) (net.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	proxyURL = NormalizeProxyURL(proxyURL)
	dialer := &net.Dialer{
		Timeout:   runtimeconfig.Duration("imap_dial_timeout_seconds", 20*time.Second, time.Second, 1),
		KeepAlive: runtimeconfig.Duration("imap_keepalive_seconds", 30*time.Second, time.Second, 1),
	}
	if proxyURL == "" {
		return nil, fmt.Errorf("mail proxy URL is required")
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("invalid proxy url")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "socks5", "socks5h":
		auth := (*proxy.Auth)(nil)
		if parsed.User != nil {
			password, _ := parsed.User.Password()
			auth = &proxy.Auth{User: parsed.User.Username(), Password: password}
		}
		socksDialer, err := proxy.SOCKS5("tcp", parsed.Host, auth, dialer)
		if err != nil {
			return nil, err
		}
		contextDialer, ok := socksDialer.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("socks5 proxy does not support context cancellation")
		}
		handshakeCtx, cancel := context.WithTimeout(ctx, runtimeconfig.Duration("proxy_handshake_timeout_seconds", ProxyHandshakeTimeout, time.Second, 1))
		conn, err := contextDialer.DialContext(handshakeCtx, "tcp", target)
		cancel()
		if err != nil {
			return nil, err
		}
		if err := SetDeadline(ctx, conn, runtimeconfig.Duration("imap_operation_timeout_seconds", OperationTimeout, time.Second, 1)); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return conn, nil
	case "http", "https":
		return HTTPConnect(ctx, dialer, parsed, target)
	default:
		return nil, fmt.Errorf("unsupported proxy scheme")
	}
}

func NormalizeProxyURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		return value
	}
	return "http://" + value
}

func HTTPConnect(ctx context.Context, dialer *net.Dialer, parsed *url.URL, target string) (net.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	conn, err := dialer.DialContext(ctx, "tcp", parsed.Host)
	if err != nil {
		return nil, err
	}
	// CONNECT reads must stop on cancellation even when the caller's deadline is later.
	rawConn := conn
	stop := context.AfterFunc(ctx, func() { _ = rawConn.Close() })
	defer stop()
	if err := SetDeadline(ctx, conn, runtimeconfig.Duration("proxy_handshake_timeout_seconds", ProxyHandshakeTimeout, time.Second, 1)); err != nil {
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
	request := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
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
		return nil, fmt.Errorf("proxy connect failed")
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

func SetDeadline(ctx context.Context, conn net.Conn, fallback time.Duration) error {
	if conn == nil {
		return errors.New("network connection is nil")
	}
	if fallback <= 0 {
		fallback = runtimeconfig.Duration("proxy_handshake_timeout_seconds", ProxyHandshakeTimeout, time.Second, 1)
	}
	deadline := time.Now().Add(fallback)
	if ctx != nil {
		if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
			deadline = contextDeadline
		}
	}
	return conn.SetDeadline(deadline)
}
