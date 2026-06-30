package infra

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSMTPMessageUsesMultipartAlternative(t *testing.T) {
	msg := smtpMessage("no-reply@example.com", mailapp.VerificationCodeMessage("user@example.com", "123456"))

	assert.Contains(t, msg, "Subject: =?UTF-8?")
	assert.Contains(t, msg, "Content-Type: multipart/alternative")
	assert.Contains(t, msg, "Content-Type: text/plain; charset=UTF-8")
	assert.Contains(t, msg, "Content-Type: text/html; charset=UTF-8")
	assert.Contains(t, msg, "123456")
	assert.Contains(t, msg, "data:image/png;base64,")
	assert.Contains(t, msg, "Remail")
	assert.Contains(t, msg, "linear-gradient")
	assert.Contains(t, msg, "shine-text")
	assert.Contains(t, msg, "sweep-shine")
	assert.Contains(t, msg, "#8a4a34")
	assert.Contains(t, msg, "#ff3d73")
	assert.Contains(t, msg, "<h1")
	assert.True(t, strings.HasSuffix(msg, "--"+mixedBoundary+"--\r\n"))
}

func TestSMTPMessageSanitizesHeadersAndHTML(t *testing.T) {
	msg := smtpMessage(
		"sender@example.com\r\nBcc: audit@example.com",
		mailapp.VerificationCodeMessage("user@example.com\r\nCc: audit@example.com", "<123456>"),
	)

	assert.NotContains(t, msg, "\r\nBcc:")
	assert.NotContains(t, msg, "\r\nCc:")
	assert.Contains(t, msg, "&lt;123456&gt;")
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

func TestSMTPDeliveryTreatsQuitFailureAfterDataAsAccepted(t *testing.T) {
	addr, stop := startFakeSMTPServer(t, true)
	defer stop()

	sender := NewSMTPDelivery(SMTPConfig{Addr: addr, From: "no-reply@example.com"})
	err := sender.Send(context.Background(), mailapp.VerificationCodeMessage("user@example.com", "123456"))

	require.NoError(t, err)
}

func startFakeSMTPServer(t *testing.T, closeOnQuit bool) (string, func()) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
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
		<-done
	}
}

func writeSMTPLine(t *testing.T, rw *bufio.ReadWriter, line string) {
	t.Helper()

	_, err := rw.WriteString(line + "\r\n")
	require.NoError(t, err)
	require.NoError(t, rw.Flush())
}
