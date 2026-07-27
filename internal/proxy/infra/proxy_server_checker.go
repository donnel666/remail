package infra

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/proxy/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

const proxyServerCheckTargetLimit = 3

type ProxyServerChecker struct {
	timeout time.Duration
}

func NewProxyServerChecker() *ProxyServerChecker {
	return &ProxyServerChecker{timeout: 3 * time.Second}
}

// Check probes only the physical proxy service (including a SOCKS5 greeting).
// It does not authenticate or contact a target, so credential/exit/target
// failures cannot mark the whole server unhealthy.
func (c *ProxyServerChecker) Check(ctx context.Context, proxyURLs []string) error {
	timeout := c.timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	timeout = runtimeconfig.Duration("proxy_server_health_timeout_seconds", timeout, time.Second, 1)
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dialer := net.Dialer{Timeout: timeout}
	seen := make(map[string]struct{}, len(proxyURLs))
	for _, proxyURL := range proxyURLs {
		normalized, err := domain.NormalizeProxyURL(proxyURL)
		if err != nil {
			continue
		}
		parsed, err := url.Parse(normalized)
		if err != nil || strings.TrimSpace(parsed.Host) == "" {
			continue
		}
		address := strings.ToLower(strings.TrimSpace(parsed.Host))
		if _, ok := seen[address]; ok {
			continue
		}
		seen[address] = struct{}{}
		if probeProxyServer(checkCtx, &dialer, address, strings.ToLower(parsed.Scheme)) == nil {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if checkCtx.Err() != nil {
			break
		}
	}
	return errors.New("proxy server transport probe failed")
}

func probeProxyServer(ctx context.Context, dialer *net.Dialer, address, scheme string) error {
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	if scheme != "socks5" && scheme != "socks5h" {
		return nil
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return err
		}
	}
	if _, err := conn.Write([]byte{0x05, 0x02, 0x00, 0x02}); err != nil {
		return err
	}
	var response [2]byte
	if _, err := io.ReadFull(conn, response[:]); err != nil {
		return err
	}
	if response[0] != 0x05 {
		return errors.New("proxy server returned an invalid SOCKS5 greeting")
	}
	return nil
}
