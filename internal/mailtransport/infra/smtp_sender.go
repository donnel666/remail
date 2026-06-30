package infra

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
)

const mixedBoundary = "remail-mail-boundary"

type SMTPConfig struct {
	Addr     string
	Username string
	Password string
	From     string
}

type SMTPDelivery struct {
	cfg SMTPConfig
}

func NewSMTPDelivery(cfg SMTPConfig) *SMTPDelivery {
	return &SMTPDelivery{cfg: cfg}
}

func (s *SMTPDelivery) Send(ctx context.Context, message domain.OutboundMessage) error {
	addr := normalizeSMTPAddr(s.cfg.Addr)
	from := envelopeAddress(s.cfg.From)
	if from == "" {
		from = envelopeAddress(s.cfg.Username)
	}
	to := envelopeAddress(message.To)
	if addr == "" || from == "" || to == "" {
		return deliveryError("smtp config incomplete", nil)
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return deliveryError("invalid smtp addr", err)
	}

	conn, err := dialSMTP(ctx, addr, host, port)
	if err != nil {
		return deliveryError("smtp dial failed", err)
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return deliveryError("smtp client failed", err)
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}); err != nil {
			return deliveryError("smtp starttls failed", err)
		}
	}

	if s.cfg.Username != "" || s.cfg.Password != "" {
		auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, host)
		if err := client.Auth(auth); err != nil {
			return deliveryError("smtp auth failed", err)
		}
	}

	if err := client.Mail(from); err != nil {
		return deliveryError("smtp from rejected", err)
	}
	if err := client.Rcpt(to); err != nil {
		return deliveryError("smtp recipient rejected", err)
	}

	writer, err := client.Data()
	if err != nil {
		return deliveryError("smtp data failed", err)
	}
	message.To = to
	if _, err := writer.Write([]byte(smtpMessage(from, message))); err != nil {
		_ = writer.Close()
		return deliveryError("smtp write failed", err)
	}
	if err := writer.Close(); err != nil {
		return deliveryError("smtp close data failed", err)
	}
	_ = client.Quit()
	return nil
}

func normalizeSMTPAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	if net.ParseIP(addr) != nil {
		return net.JoinHostPort(addr, "587")
	}
	if strings.Contains(addr, ":") {
		return addr
	}
	return net.JoinHostPort(addr, "587")
}

func dialSMTP(ctx context.Context, addr, host, port string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 10 * time.Second}
	if port == "465" {
		return tls.DialWithDialer(&dialer, "tcp", addr, &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: host,
		})
	}
	return dialer.DialContext(ctx, "tcp", addr)
}

func smtpMessage(from string, message domain.OutboundMessage) string {
	return fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=%q\r\n\r\n--%s\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n%s\r\n--%s\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n%s\r\n--%s--\r\n",
		headerValue(from),
		headerValue(message.To),
		subjectHeaderValue(message.Subject),
		mixedBoundary,
		mixedBoundary,
		quotedPrintable(message.TextBody),
		mixedBoundary,
		quotedPrintable(message.HTMLBody),
		mixedBoundary,
	)
}

func deliveryError(stage string, err error) error {
	if err == nil {
		return fmt.Errorf("%w: %s", domain.ErrDeliveryUnavailable, stage)
	}
	return fmt.Errorf("%w: %s: %s", domain.ErrDeliveryUnavailable, stage, safeDiagnostic(err.Error()))
}

func envelopeAddress(value string) string {
	value = firstLineValue(value)
	if value == "" {
		return ""
	}
	address, err := mail.ParseAddress(value)
	if err == nil {
		return address.Address
	}
	return value
}

func firstLineValue(value string) string {
	if idx := strings.IndexAny(value, "\r\n"); idx >= 0 {
		value = value[:idx]
	}
	return strings.TrimSpace(value)
}

func headerValue(value string) string {
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "")
	return strings.TrimSpace(value)
}

func subjectHeaderValue(value string) string {
	value = headerValue(value)
	if value == "" || isASCII(value) {
		return value
	}
	return mime.QEncoding.Encode("UTF-8", value)
}

func isASCII(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] > 127 {
			return false
		}
	}
	return true
}

func safeDiagnostic(value string) string {
	value = headerValue(value)
	const maxLen = 240
	if len(value) > maxLen {
		return value[:maxLen]
	}
	return value
}

func quotedPrintable(value string) string {
	var buf bytes.Buffer
	writer := quotedprintable.NewWriter(&buf)
	_, _ = writer.Write([]byte(value))
	_ = writer.Close()
	return buf.String()
}
