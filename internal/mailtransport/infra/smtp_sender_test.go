package infra

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/emersion/go-msgauth/dkim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSMTPMessageUsesMultipartAlternative(t *testing.T) {
	msg, err := newSMTPMessage("no-reply@example.com", "user@example.com", mailapp.VerificationCodeMessage("user@example.com", "123456"))
	require.NoError(t, err)

	raw := renderSMTPMessage(t, msg)
	assert.Contains(t, raw, "Subject:")
	assert.Contains(t, raw, "=?UTF-8?q?ReMail")
	assert.Contains(t, raw, "Content-Type: multipart/alternative")
	assert.Contains(t, raw, "Content-Type: text/plain; charset=UTF-8")
	assert.Contains(t, raw, "Content-Type: text/html; charset=UTF-8")
	assert.Contains(t, raw, "123456")
	assert.Contains(t, raw, "data:image/png;base64,")
	assert.Contains(t, raw, "Remail")
	assert.Contains(t, raw, "linear-gradient")
	assert.Contains(t, raw, "shine-text")
	assert.Contains(t, raw, "sweep-shine")
	assert.Contains(t, raw, "#8a4a34")
	assert.Contains(t, raw, "#ff3d73")
	assert.Contains(t, raw, "<h1")
}

func TestSMTPMessageSanitizesHeadersAndHTML(t *testing.T) {
	msg, err := newSMTPMessage(
		envelopeAddress("sender@example.com\r\nBcc: audit@example.com"),
		envelopeAddress("user@example.com\r\nCc: audit@example.com"),
		mailapp.VerificationCodeMessage("user@example.com\r\nCc: audit@example.com", "<123456>"),
	)
	require.NoError(t, err)

	raw := renderSMTPMessage(t, msg)
	assert.NotContains(t, raw, "\r\nBcc:")
	assert.NotContains(t, raw, "\r\nCc:")
	assert.Contains(t, raw, "&lt;123456&gt;")
}

func TestDKIMSignerSignsAndVerifiesEd25519Message(t *testing.T) {
	signer, dnsRecord := newTestDKIMSigner(t)
	msg, err := newSMTPMessage("no-reply@example.com", "user@example.com", mailapp.VerificationCodeMessage("user@example.com", "123456"))
	require.NoError(t, err)
	raw, err := renderSMTPMessageBytes(msg)
	require.NoError(t, err)

	signed, err := signer.Sign(raw)
	require.NoError(t, err)

	signedText := string(signed)
	assert.Contains(t, signedText, "DKIM-Signature:")
	assert.Contains(t, signedText, "a=ed25519-sha256")
	assert.Contains(t, signedText, "d=example.com")
	assert.Contains(t, signedText, "s=mx")
	assert.Contains(t, signedText, "c=relaxed/relaxed")

	verifications, err := dkim.VerifyWithOptions(bytes.NewReader(signed), &dkim.VerifyOptions{
		LookupTXT: func(domain string) ([]string, error) {
			assert.Equal(t, "mx._domainkey.example.com", domain)
			return []string{dnsRecord}, nil
		},
	})
	require.NoError(t, err)
	require.Len(t, verifications, 1)
	require.NoError(t, verifications[0].Err)
	assert.Equal(t, "example.com", verifications[0].Domain)
}

func TestDKIMSignerRejectsAlgorithmMismatch(t *testing.T) {
	privateKeyPEM, _ := newTestEd25519PrivateKeyPEM(t)

	_, err := NewDKIMSigner(DKIMConfig{
		Enabled:    true,
		Domain:     "example.com",
		Selector:   "mx",
		Algorithm:  "rsa-sha256",
		PrivateKey: string(privateKeyPEM),
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "algorithm mismatch")
}

func TestDKIMSignerRejectsIdentityOutsideSigningDomain(t *testing.T) {
	privateKeyPEM, _ := newTestEd25519PrivateKeyPEM(t)

	_, err := NewDKIMSigner(DKIMConfig{
		Enabled:    true,
		Domain:     "example.com",
		Selector:   "mx",
		Identity:   "no-reply@evil.test",
		PrivateKey: string(privateKeyPEM),
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "identity must belong")
}

func TestDKIMSignerRejectsAmbiguousPrivateKeySource(t *testing.T) {
	privateKeyPEM, _ := newTestEd25519PrivateKeyPEM(t)

	_, err := NewDKIMSigner(DKIMConfig{
		Enabled:        true,
		Domain:         "example.com",
		Selector:       "mx",
		PrivateKey:     string(privateKeyPEM),
		PrivateKeyFile: "/run/secrets/smtp-dkim-private-key.pem",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not both")
}

func TestNormalizeSMTPAddrDefaultsHostOnlyToSubmissionPort(t *testing.T) {
	assert.Equal(t, "my.mailbux.com:587", normalizeSMTPAddr("my.mailbux.com"))
	assert.Equal(t, "my.mailbux.com:465", normalizeSMTPAddr("my.mailbux.com:465"))
	assert.Equal(t, "[::1]:587", normalizeSMTPAddr("::1"))
	assert.Equal(t, "", normalizeSMTPAddr(" "))
}

func TestEnvelopeAddressSanitizesCRLF(t *testing.T) {
	assert.Equal(t, "user@example.com", envelopeAddress(" user@example.com\r\nCc: audit@example.com "))
	assert.Equal(t, "sender@example.com", envelopeAddress("ReMail <sender@example.com>"))
}

func TestDeliveryErrorKeepsSentinelAndSafeDiagnostic(t *testing.T) {
	err := deliveryError("smtp client failed", errors.New("EOF\r\nsecret trailer"))

	assert.ErrorIs(t, err, domain.ErrDeliveryUnavailable)
	assert.Contains(t, err.Error(), "smtp client failed: EOFsecret trailer")
	assert.NotContains(t, err.Error(), "\r")
	assert.NotContains(t, err.Error(), "\n")
}

func TestSMTPDeliveryRequiresSTARTTLSBeforeAuth(t *testing.T) {
	addr, stop := startFakeSMTPServer(t, false)
	defer stop()

	sender := NewSMTPDelivery(SMTPConfig{
		Addr:     addr,
		Username: "no-reply@example.com",
		Password: "secret",
		From:     "no-reply@example.com",
	})
	err := sender.Send(context.Background(), mailapp.VerificationCodeMessage("user@example.com", "123456"))

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrDeliveryUnavailable)
	assert.Contains(t, strings.ToLower(err.Error()), "starttls")
}

func TestRequiresSTARTTLSForSubmissionAndAuth(t *testing.T) {
	assert.True(t, requiresSTARTTLS("587", SMTPConfig{}))
	assert.True(t, requiresSTARTTLS("2525", SMTPConfig{Username: "user"}))
	assert.True(t, requiresSTARTTLS("2525", SMTPConfig{Password: "secret"}))
	assert.False(t, requiresSTARTTLS("2525", SMTPConfig{}))
	assert.False(t, requiresSTARTTLS("465", SMTPConfig{Username: "user", Password: "secret"}))
}

func TestSMTPDeliveryTreatsQuitFailureAfterDataAsAccepted(t *testing.T) {
	addr, stop := startFakeSMTPServer(t, true)
	defer stop()

	sender := NewSMTPDelivery(SMTPConfig{Addr: addr, From: "no-reply@example.com"})
	err := sender.Send(context.Background(), mailapp.VerificationCodeMessage("user@example.com", "123456"))

	require.NoError(t, err)
}

func TestSMTPDeliverySignsMessagesWhenDKIMEnabled(t *testing.T) {
	signer, _ := newTestDKIMSigner(t)
	addr, dataCh, stop := startCapturingSMTPServer(t)
	defer stop()

	sender := NewSMTPDelivery(SMTPConfig{Addr: addr, From: "no-reply@example.com", DKIM: signer})
	err := sender.Send(context.Background(), mailapp.VerificationCodeMessage("user@example.com", "123456"))

	require.NoError(t, err)
	raw := <-dataCh
	assert.Contains(t, raw, "DKIM-Signature:")
	assert.Contains(t, raw, "a=ed25519-sha256")
	assert.Contains(t, raw, "From: <no-reply@example.com>")
}

func startFakeSMTPServer(t *testing.T, closeOnQuit bool) (string, func()) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	done := make(chan struct{})
	var mu sync.Mutex
	var active net.Conn
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		mu.Lock()
		active = conn
		mu.Unlock()
		defer conn.Close()

		rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
		writeSMTPLine(t, rw, "220 remail-test")
		for {
			line, err := rw.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"):
				writeSMTPLine(t, rw, "250-remail-test")
				writeSMTPLine(t, rw, "250 OK")
			case strings.HasPrefix(cmd, "STARTTLS"):
				writeSMTPLine(t, rw, "454 TLS not available")
			case strings.HasPrefix(cmd, "MAIL FROM:"):
				writeSMTPLine(t, rw, "250 OK")
			case strings.HasPrefix(cmd, "RCPT TO:"):
				writeSMTPLine(t, rw, "250 OK")
			case strings.HasPrefix(cmd, "DATA"):
				writeSMTPLine(t, rw, "354 End data with <CR><LF>.<CR><LF>")
				for {
					dataLine, err := rw.ReadString('\n')
					if err != nil {
						return
					}
					if strings.TrimSpace(dataLine) == "." {
						break
					}
				}
				writeSMTPLine(t, rw, "250 queued")
			case strings.HasPrefix(cmd, "QUIT"):
				if closeOnQuit {
					return
				}
				writeSMTPLine(t, rw, "221 bye")
				return
			default:
				writeSMTPLine(t, rw, "250 OK")
			}
		}
	}()

	return ln.Addr().String(), func() {
		_ = ln.Close()
		mu.Lock()
		if active != nil {
			_ = active.Close()
		}
		mu.Unlock()
		<-done
	}
}

func startCapturingSMTPServer(t *testing.T) (string, <-chan string, func()) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	dataCh := make(chan string, 1)
	done := make(chan struct{})
	var mu sync.Mutex
	var active net.Conn
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		mu.Lock()
		active = conn
		mu.Unlock()
		defer conn.Close()

		rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
		writeSMTPLine(t, rw, "220 remail-test")
		for {
			line, err := rw.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"):
				writeSMTPLine(t, rw, "250-remail-test")
				writeSMTPLine(t, rw, "250 OK")
			case strings.HasPrefix(cmd, "MAIL FROM:"):
				writeSMTPLine(t, rw, "250 OK")
			case strings.HasPrefix(cmd, "RCPT TO:"):
				writeSMTPLine(t, rw, "250 OK")
			case strings.HasPrefix(cmd, "DATA"):
				writeSMTPLine(t, rw, "354 End data with <CR><LF>.<CR><LF>")
				var data strings.Builder
				for {
					dataLine, err := rw.ReadString('\n')
					if err != nil {
						return
					}
					if strings.TrimSpace(dataLine) == "." {
						break
					}
					data.WriteString(dataLine)
				}
				dataCh <- data.String()
				writeSMTPLine(t, rw, "250 queued")
			case strings.HasPrefix(cmd, "QUIT"):
				writeSMTPLine(t, rw, "221 bye")
				return
			default:
				writeSMTPLine(t, rw, "250 OK")
			}
		}
	}()

	return ln.Addr().String(), dataCh, func() {
		_ = ln.Close()
		mu.Lock()
		if active != nil {
			_ = active.Close()
		}
		mu.Unlock()
		<-done
	}
}

func newTestDKIMSigner(t *testing.T) (*DKIMSigner, string) {
	t.Helper()

	privateKeyPEM, publicKey := newTestEd25519PrivateKeyPEM(t)
	signer, err := NewDKIMSigner(DKIMConfig{
		Enabled:    true,
		Domain:     "example.com",
		Selector:   "mx",
		Algorithm:  "ed25519-sha256",
		PrivateKey: string(privateKeyPEM),
	})
	require.NoError(t, err)

	dnsRecord := "v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(publicKey)
	return signer, dnsRecord
}

func newTestEd25519PrivateKeyPEM(t *testing.T) ([]byte, ed25519.PublicKey) {
	t.Helper()

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), publicKey
}

func writeSMTPLine(t *testing.T, rw *bufio.ReadWriter, line string) {
	t.Helper()

	_, err := rw.WriteString(line + "\r\n")
	require.NoError(t, err)
	require.NoError(t, rw.Flush())
}

func renderSMTPMessage(t *testing.T, msg interface {
	WriteTo(io.Writer) (int64, error)
}) string {
	t.Helper()
	var buf bytes.Buffer
	_, err := msg.WriteTo(&buf)
	require.NoError(t, err)
	return buf.String()
}
