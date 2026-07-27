package infra

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProxyServerCheckerProbesSOCKSServiceWithoutCredentials(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverErr <- acceptErr
			return
		}
		defer conn.Close()
		var greeting [4]byte
		if _, readErr := io.ReadFull(conn, greeting[:]); readErr != nil {
			serverErr <- readErr
			return
		}
		if greeting != [4]byte{0x05, 0x02, 0x00, 0x02} {
			serverErr <- fmt.Errorf("unexpected SOCKS5 greeting: %v", greeting)
			return
		}
		_, writeErr := conn.Write([]byte{0x05, 0x02})
		serverErr <- writeErr
	}()

	checker := &ProxyServerChecker{timeout: 200 * time.Millisecond}
	require.NoError(t, checker.Check(context.Background(), []string{
		"socks5://different:credentials@" + listener.Addr().String(),
	}))
	require.NoError(t, <-serverErr)

	plainListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer plainListener.Close()
	go func() {
		conn, acceptErr := plainListener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
	}()
	require.Error(t, checker.Check(context.Background(), []string{
		"socks5://different:credentials@" + plainListener.Addr().String(),
	}))
	require.Error(t, checker.Check(context.Background(), []string{"not-a-proxy-url"}))
}
