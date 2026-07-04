package infra

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	stdmail "net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
	gomail "github.com/wneessen/go-mail"
)

var diagnosticEmailPattern = regexp.MustCompile(`(?i)\b([a-z0-9._%+\-])[a-z0-9._%+\-]*@([a-z0-9.\-]+\.[a-z]{2,})\b`)

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
	from := envelopeAddress(firstNonEmpty(message.From, s.cfg.From))
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
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return deliveryError("invalid smtp port", err)
	}
	mailMessage, err := newSMTPMessage(from, to, message)
	if err != nil {
		return deliveryError("smtp message failed", err)
	}

	options := []gomail.Option{
		gomail.WithPort(portNum),
		gomail.WithTimeout(30 * time.Second),
		gomail.WithTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}),
	}
	if port == "465" {
		options = append(options, gomail.WithSSL(), gomail.WithTLSPolicy(gomail.TLSMandatory))
	} else if requiresSTARTTLS(port, s.cfg) {
		options = append(options, gomail.WithTLSPolicy(gomail.TLSMandatory))
	} else {
		options = append(options, gomail.WithTLSPolicy(gomail.TLSOpportunistic))
	}
	if s.cfg.Username != "" || s.cfg.Password != "" {
		options = append(options,
			gomail.WithSMTPAuth(gomail.SMTPAuthAutoDiscover),
			gomail.WithUsername(s.cfg.Username),
			gomail.WithPassword(s.cfg.Password),
		)
	}

	client, err := gomail.NewClient(host, options...)
	if err != nil {
		return deliveryError("smtp client failed", err)
	}
	if err := sendMailWithAcceptedClose(ctx, client, mailMessage); err != nil {
		return deliveryError("smtp send failed", err)
	}
	return nil
}

func sendMailWithAcceptedClose(ctx context.Context, client *gomail.Client, message *gomail.Msg) error {
	smtpClient, err := client.DialToSMTPClientWithContext(ctx)
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}
	if err := client.SendWithSMTPClient(smtpClient, message); err != nil {
		_ = client.CloseWithSMTPClient(smtpClient)
		return fmt.Errorf("send failed: %w", err)
	}
	_ = client.CloseWithSMTPClient(smtpClient)
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

func requiresSTARTTLS(port string, cfg SMTPConfig) bool {
	if port == "465" {
		return false
	}
	return port == "587" || cfg.Username != "" || cfg.Password != ""
}

func newSMTPMessage(from string, to string, message domain.OutboundMessage) (*gomail.Msg, error) {
	msg := gomail.NewMsg()
	if err := msg.EnvelopeFrom(from); err != nil {
		return nil, err
	}
	if err := msg.From(from); err != nil {
		return nil, err
	}
	if err := msg.To(to); err != nil {
		return nil, err
	}
	msg.SetDate()
	msg.SetMessageID()
	msg.Subject(headerValue(message.Subject))
	if strings.TrimSpace(message.TextBody) != "" {
		msg.SetBodyString(gomail.TypeTextPlain, message.TextBody)
	}
	if strings.TrimSpace(message.HTMLBody) != "" {
		if strings.TrimSpace(message.TextBody) == "" {
			msg.SetBodyString(gomail.TypeTextHTML, message.HTMLBody)
		} else {
			msg.AddAlternativeString(gomail.TypeTextHTML, message.HTMLBody)
		}
	}
	return msg, nil
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
	address, err := stdmail.ParseAddress(value)
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

func safeDiagnostic(value string) string {
	value = headerValue(value)
	value = diagnosticEmailPattern.ReplaceAllString(value, "$1***@$2")
	const maxLen = 240
	if len(value) > maxLen {
		return value[:maxLen]
	}
	return value
}
