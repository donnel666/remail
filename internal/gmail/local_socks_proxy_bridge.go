package gmail

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	xproxy "golang.org/x/net/proxy"
)

// localGmailSOCKSBridge lets Chrome use an authenticated SOCKS5 route. Chrome
// cannot submit SOCKS credentials through the DevTools HTTP-auth callback.
type localGmailSOCKSBridge struct {
	listener net.Listener
	dialer   xproxy.Dialer
	conns    sync.Map
	wg       sync.WaitGroup
	once     sync.Once
}

func newLocalGmailSOCKSBridge(proxyServer, username, password string) (*localGmailSOCKSBridge, error) {
	parsed, err := url.Parse(proxyServer)
	if err != nil || !strings.EqualFold(parsed.Scheme, "socks5") || parsed.Host == "" {
		return nil, errors.New("invalid SOCKS5 proxy")
	}
	var auth *xproxy.Auth
	if username != "" {
		auth = &xproxy.Auth{User: username, Password: password}
	}
	dialer, err := xproxy.SOCKS5("tcp", parsed.Host, auth, &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second})
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	bridge := &localGmailSOCKSBridge{listener: listener, dialer: dialer}
	bridge.wg.Add(1)
	go bridge.serve()
	return bridge, nil
}

func localGmailBrowserNeedsSOCKSBridge(proxyServer string) bool {
	parsed, err := url.Parse(strings.TrimSpace(proxyServer))
	return err == nil && strings.EqualFold(parsed.Scheme, "socks5") && parsed.Host != ""
}

func (b *localGmailSOCKSBridge) URL() string {
	if b == nil || b.listener == nil {
		return ""
	}
	return "http://" + b.listener.Addr().String()
}

func (b *localGmailSOCKSBridge) Close() {
	if b == nil {
		return
	}
	b.once.Do(func() {
		if b.listener != nil {
			_ = b.listener.Close()
		}
		b.conns.Range(func(key, _ any) bool {
			if conn, ok := key.(net.Conn); ok {
				_ = conn.Close()
			}
			return true
		})
		b.wg.Wait()
	})
}

func (b *localGmailSOCKSBridge) serve() {
	defer b.wg.Done()
	for {
		conn, err := b.listener.Accept()
		if err != nil {
			return
		}
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			b.handle(conn)
		}()
	}
}

func (b *localGmailSOCKSBridge) handle(client net.Conn) {
	defer client.Close()
	b.conns.Store(client, struct{}{})
	defer b.conns.Delete(client)

	reader := bufio.NewReader(client)
	request, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	if request.Method != http.MethodConnect {
		_, _ = io.WriteString(client, "HTTP/1.1 405 Method Not Allowed\r\nContent-Length: 0\r\n\r\n")
		return
	}
	target := strings.TrimSpace(request.Host)
	if target == "" {
		_, _ = io.WriteString(client, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n")
		return
	}
	if _, _, err := net.SplitHostPort(target); err != nil {
		target = net.JoinHostPort(target, "443")
	}
	upstream, err := b.dial(target)
	if err != nil {
		_, _ = io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer upstream.Close()
	b.conns.Store(upstream, struct{}{})
	defer b.conns.Delete(upstream)
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}

	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(upstream, reader)
		_ = upstream.Close()
		close(done)
	}()
	_, _ = io.Copy(client, upstream)
	_ = client.Close()
	<-done
}

func (b *localGmailSOCKSBridge) dial(target string) (net.Conn, error) {
	if b == nil || b.dialer == nil {
		return nil, errors.New("socks5 bridge is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if dialer, ok := b.dialer.(xproxy.ContextDialer); ok {
		return dialer.DialContext(ctx, "tcp", target)
	}
	return b.dialer.Dial("tcp", target)
}
