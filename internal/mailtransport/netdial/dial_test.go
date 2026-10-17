package netdial

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMailboxTLSAdvertisesProtocolAndVerifiesCertificate(t *testing.T) {
	for _, alpn := range []string{"imap", "pop3"} {
		t.Run(alpn, func(t *testing.T) {
			hello := make(chan *tls.ClientHelloInfo, 1)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			server.Config.ErrorLog = log.New(io.Discard, "", 0)
			server.TLS = &tls.Config{NextProtos: []string{alpn}, GetConfigForClient: func(info *tls.ClientHelloInfo) (*tls.Config, error) { hello <- info; return nil, nil }}
			server.StartTLS()
			defer server.Close()
			_, port, err := net.SplitHostPort(server.Listener.Addr().String())
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			conn, err := DialTLS(ctx, "", net.JoinHostPort("localhost", port), alpn)
			require.Error(t, err, "the local certificate must not bypass verification")
			require.Nil(t, conn)
			select {
			case observed := <-hello:
				require.Equal(t, "localhost", observed.ServerName)
				require.Equal(t, []string{alpn}, observed.SupportedProtos)
				for _, version := range observed.SupportedVersions {
					require.GreaterOrEqual(t, version, uint16(tls.VersionTLS12))
				}
			case <-time.After(time.Second):
				t.Fatal("no TLS ClientHello received")
			}
		})
	}
}

func TestMailboxHTTPProxyTargetsRequestedServerAndCarriesAuth(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	seen := make(chan []string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		var headers []string
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.TrimSpace(line) == "" {
				break
			}
			headers = append(headers, strings.TrimSpace(line))
		}
		seen <- headers
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
	}()
	proxyURL, err := url.Parse("http://fixture:secret@" + listener.Addr().String())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := HTTPConnect(ctx, &net.Dialer{}, proxyURL, "imap.daum.net:993")
	require.NoError(t, err)
	defer conn.Close()
	headers := <-seen
	require.Equal(t, "CONNECT imap.daum.net:993 HTTP/1.1", headers[0])
	require.Contains(t, headers, "Host: imap.daum.net:993")
	require.Contains(t, headers, "Proxy-Authorization: Basic Zml4dHVyZTpzZWNyZXQ=")
}

func TestMailboxDeadlineHonorsShorterCaller(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	require.NoError(t, SetDeadline(ctx, left, time.Hour))
	started := time.Now()
	_, err := left.Read(make([]byte, 1))
	require.Error(t, err)
	require.Less(t, time.Since(started), time.Second)
}

func TestMailboxHTTPConnectHonorsCancellationBeforeDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	requestRead := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.TrimSpace(line) == "" {
				close(requestRead)
				<-release
				return
			}
		}
	}()
	parsed, err := url.Parse("http://" + listener.Addr().String())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		conn, err := HTTPConnect(ctx, &net.Dialer{}, parsed, "imap.daum.net:993")
		if conn != nil {
			_ = conn.Close()
		}
		done <- err
	}()
	select {
	case <-requestRead:
	case <-time.After(time.Second):
		t.Fatal("CONNECT request did not arrive")
	}
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("canceled CONNECT waited for the handshake timeout")
	}
}
