package gmail

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLocalGmailSOCKSBridgeForwardsAuthenticatedCONNECT(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = target.Close() })
	go func() {
		conn, err := target.Accept()
		if err == nil {
			_, _ = io.Copy(conn, conn)
			_ = conn.Close()
		}
	}()

	socks, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = socks.Close() })
	authenticated := make(chan bool, 1)
	go serveTestSOCKS5(socks, target.Addr().String(), authenticated)

	bridge, err := newLocalGmailSOCKSBridge("socks5://"+socks.Addr().String(), "proxy-user", "proxy-pass")
	require.NoError(t, err)
	t.Cleanup(bridge.Close)

	client, err := net.Dial("tcp", strings.TrimPrefix(bridge.URL(), "http://"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	_, err = fmt.Fprintf(client, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target.Addr(), target.Addr())
	require.NoError(t, err)
	response, err := http.ReadResponse(bufio.NewReader(client), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	_ = response.Body.Close()

	_, err = client.Write([]byte("ping"))
	require.NoError(t, err)
	echo := make([]byte, 4)
	_, err = io.ReadFull(client, echo)
	require.NoError(t, err)
	require.Equal(t, "ping", string(echo))
	select {
	case ok := <-authenticated:
		require.True(t, ok)
	case <-time.After(2 * time.Second):
		t.Fatal("SOCKS5 authentication was not observed")
	}
}

func TestLocalGmailBrowserNeedsSOCKSBridge(t *testing.T) {
	require.True(t, localGmailBrowserNeedsSOCKSBridge("socks5://127.0.0.1:1080"))
	require.False(t, localGmailBrowserNeedsSOCKSBridge("http://127.0.0.1:8080"))
	require.False(t, localGmailBrowserNeedsSOCKSBridge(""))
}

func serveTestSOCKS5(listener net.Listener, target string, authenticated chan<- bool) {
	conn, err := listener.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		return
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(reader, methods); err != nil {
		return
	}
	if _, err := conn.Write([]byte{5, 2}); err != nil {
		return
	}
	authHeader := make([]byte, 2)
	if _, err := io.ReadFull(reader, authHeader); err != nil {
		return
	}
	user := make([]byte, int(authHeader[1]))
	if _, err := io.ReadFull(reader, user); err != nil {
		return
	}
	passwordLength, err := reader.ReadByte()
	if err != nil {
		return
	}
	password := make([]byte, int(passwordLength))
	if _, err := io.ReadFull(reader, password); err != nil {
		return
	}
	authenticated <- string(user) == "proxy-user" && string(password) == "proxy-pass"
	if _, err := conn.Write([]byte{1, 0}); err != nil {
		return
	}

	requestHeader := make([]byte, 4)
	if _, err := io.ReadFull(reader, requestHeader); err != nil || requestHeader[1] != 1 {
		return
	}
	address, err := readTestSOCKS5Address(reader, requestHeader[3])
	if err != nil {
		return
	}
	port := make([]byte, 2)
	if _, err := io.ReadFull(reader, port); err != nil {
		return
	}
	targetAddress := net.JoinHostPort(address, fmt.Sprint(binary.BigEndian.Uint16(port)))
	if targetAddress != target {
		return
	}
	upstream, err := net.Dial("tcp", targetAddress)
	if err != nil {
		return
	}
	defer upstream.Close()
	if _, err := conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(upstream, reader)
		_ = upstream.Close()
		close(done)
	}()
	_, _ = io.Copy(conn, upstream)
	<-done
}

func readTestSOCKS5Address(reader *bufio.Reader, atyp byte) (string, error) {
	switch atyp {
	case 1:
		address := make([]byte, 4)
		if _, err := io.ReadFull(reader, address); err != nil {
			return "", err
		}
		return net.IP(address).String(), nil
	case 3:
		length, err := reader.ReadByte()
		if err != nil {
			return "", err
		}
		address := make([]byte, int(length))
		if _, err := io.ReadFull(reader, address); err != nil {
			return "", err
		}
		return string(address), nil
	case 4:
		address := make([]byte, 16)
		if _, err := io.ReadFull(reader, address); err != nil {
			return "", err
		}
		return net.IP(address).String(), nil
	default:
		return "", fmt.Errorf("unsupported SOCKS5 address type %d", atyp)
	}
}
