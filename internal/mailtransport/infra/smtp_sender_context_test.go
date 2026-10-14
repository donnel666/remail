package infra

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/stretchr/testify/require"
)

// The fixture pauses an actual connection at one protocol stage until the client closes it.
func smtpContextFixture(t *testing.T, blockAt string, implicitTLS bool, auth string) (*SMTPDelivery, <-chan struct{}, <-chan struct{}) {
	t.Helper()
	certificate, roots := newTestServerTLS(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	reached, done := make(chan struct{}), make(chan struct{})
	accepted := make(chan net.Conn, 1)
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		accepted <- conn
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		pause := func(stage string) bool {
			if stage != blockAt {
				return false
			}
			close(reached)
			_, _ = io.Copy(io.Discard, conn)
			return true
		}
		secure := implicitTLS
		if implicitTLS {
			if pause("tls") {
				return
			}
			conn = tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
		}
		if pause("greeting") {
			return
		}
		_, _ = io.WriteString(conn, "220 remail-test\r\n")
		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			command := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(command, "EHLO"):
				if pause("hello") {
					return
				}
				_, _ = io.WriteString(conn, "250-remail-test\r\n")
				if !secure {
					_, _ = io.WriteString(conn, "250-STARTTLS\r\n")
				}
				_, _ = fmt.Fprintf(conn, "250 AUTH %s\r\n", auth)
			case command == "STARTTLS":
				if pause("starttls") {
					return
				}
				_, _ = io.WriteString(conn, "220 Begin TLS\r\n")
				conn = tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
				reader = bufio.NewReader(conn)
				secure = true
			case strings.HasPrefix(command, "AUTH "):
				if pause("auth") {
					return
				}
				if !secure {
					t.Error("credentials sent without TLS")
					return
				}
				if auth == "LOGIN" {
					_, _ = io.WriteString(conn, "334 VXNlcm5hbWU6\r\n")
					encoded, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					username, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
					if err != nil || string(username) != "sender@example.com" {
						t.Error("LOGIN did not send the configured username")
						return
					}
					_, _ = io.WriteString(conn, "334 UGFzc3dvcmQ6\r\n")
					encoded, err = reader.ReadString('\n')
					if err != nil {
						return
					}
					password, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
					if err != nil || string(password) != "test-only" {
						t.Error("LOGIN did not send the configured password")
						return
					}
				} else {
					encoded, ok := strings.CutPrefix(strings.TrimSpace(line), "AUTH PLAIN ")
					credentials, err := base64.StdEncoding.DecodeString(encoded)
					if !ok || err != nil || string(credentials) != "\x00sender@example.com\x00test-only" {
						t.Error("PLAIN did not send the configured credentials")
						return
					}
				}
				_, _ = io.WriteString(conn, "235 authenticated\r\n")
			case strings.HasPrefix(command, "MAIL FROM:"):
				if pause("mail") {
					return
				}
				_, _ = io.WriteString(conn, "250 OK\r\n")
			case strings.HasPrefix(command, "RCPT TO:"):
				_, _ = io.WriteString(conn, "250 OK\r\n")
			case command == "DATA":
				if pause("data") {
					return
				}
				_, _ = io.WriteString(conn, "354 Send data\r\n")
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if line == ".\r\n" {
						break
					}
				}
				if pause("acknowledgement") {
					return
				}
				_, _ = io.WriteString(conn, "250 accepted\r\n")
			case command == "QUIT":
				if !pause("quit") {
					_, _ = io.WriteString(conn, "221 bye\r\n")
				}
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case conn := <-accepted:
			_ = conn.Close()
		default:
		}
		<-done
	})
	port := "587"
	if implicitTLS {
		port = "465"
	}
	sender := NewSMTPDelivery(SMTPConfig{Addr: "localhost:" + port, From: "sender@example.com", Username: "sender@example.com", Password: "test-only"})
	sender.dialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
	}
	sender.tlsConfig = func(host string) *tls.Config {
		return &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host, RootCAs: roots}
	}
	return sender, reached, done
}

func TestSMTPDeliveryCancellationClosesProtocolConnection(t *testing.T) {
	for _, stage := range []string{"tls", "greeting", "hello", "starttls", "auth", "mail", "data", "acknowledgement", "quit"} {
		t.Run(stage, func(t *testing.T) {
			sender, reached, done := smtpContextFixture(t, stage, stage == "tls", "PLAIN")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- sender.Send(ctx, mailapp.VerificationCodeMessage("recipient@example.com", "123456")) }()
			select {
			case <-reached:
			case <-time.After(3 * time.Second):
				t.Fatal("SMTP did not reach the expected stage")
			}
			cancel()
			select {
			case err := <-result:
				if stage == "quit" {
					require.NoError(t, err, "cancellation after acceptance must not invite a duplicate send")
				} else {
					require.Error(t, err)
					var unaccepted *mailapp.OutboundNotAcceptedError
					require.Equal(t, stage != "acknowledgement", errors.As(err, &unaccepted), "an unconfirmed DATA result must not permit failover")
				}
			case <-time.After(time.Second):
				t.Fatal("SMTP send did not stop after cancellation")
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("SMTP connection remained open after cancellation")
			}
		})
	}
}

func TestSMTPDeliveryDeadlineClosesConnection(t *testing.T) {
	sender, _, done := smtpContextFixture(t, "greeting", false, "PLAIN")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := sender.Send(ctx, mailapp.VerificationCodeMessage("recipient@example.com", "123456"))
	require.Error(t, err)
	require.Less(t, time.Since(started), time.Second)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SMTP connection remained open after the deadline")
	}
}

func TestSMTPDeliveryTLSAuthenticationStillWorks(t *testing.T) {
	for _, implicit := range []bool{true, false} {
		for _, auth := range []string{"PLAIN", "LOGIN"} {
			t.Run(fmt.Sprintf("implicit=%t/auth=%s", implicit, auth), func(t *testing.T) {
				sender, _, _ := smtpContextFixture(t, "", implicit, auth)
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				require.NoError(t, sender.Send(ctx, mailapp.VerificationCodeMessage("recipient@example.com", "123456")))
			})
		}
	}
}
