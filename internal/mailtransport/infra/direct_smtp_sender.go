package infra

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"sort"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
)

type DirectSMTPConfig struct {
	From        string
	Domain      string
	HELODomain  string
	DialTimeout time.Duration
	DKIM        *DKIMSigner
}

type DirectSMTPDelivery struct {
	cfg         DirectSMTPConfig
	dialContext func(context.Context, string, string) (net.Conn, error)
	tlsConfig   func(serverName string) *tls.Config
}

func NewDirectSMTPDelivery(cfg DirectSMTPConfig) *DirectSMTPDelivery {
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 15 * time.Second
	}
	dialer := &net.Dialer{Timeout: cfg.DialTimeout}
	return &DirectSMTPDelivery{
		cfg:         cfg,
		dialContext: dialer.DialContext,
		tlsConfig:   directSMTPTLSConfig,
	}
}

func (s *DirectSMTPDelivery) Send(ctx context.Context, message domain.OutboundMessage) error {
	from := envelopeAddress(firstNonEmpty(message.From, s.cfg.From))
	to := envelopeAddress(message.To)
	if from == "" || to == "" {
		return deliveryError("direct smtp envelope incomplete", nil)
	}

	recipientDomain := recipientDomain(to)
	if recipientDomain == "" {
		return deliveryError("direct smtp recipient invalid", nil)
	}
	targets, err := lookupMXTargets(ctx, recipientDomain)
	if err != nil {
		return deliveryError("direct smtp mx lookup failed", err)
	}

	heloName := firstNonEmpty(firstLineValue(s.cfg.HELODomain), firstLineValue(s.cfg.Domain), "localhost")
	var lastErr error
	for _, target := range targets {
		if err := s.sendToTarget(ctx, target, heloName, from, to, message); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return deliveryError("direct smtp delivery failed", lastErr)
}

func (s *DirectSMTPDelivery) sendToTarget(ctx context.Context, target, heloName, from, to string, message domain.OutboundMessage) error {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	if port == "" {
		return fmt.Errorf("smtp target port is empty")
	}
	rawMessage, err := newSignedSMTPMessage(from, to, message, s.cfg.DKIM)
	if err != nil {
		return err
	}
	return s.sendRawToTarget(ctx, target, host, heloName, from, to, rawMessage)
}

func (s *DirectSMTPDelivery) sendRawToTarget(ctx context.Context, target, tlsServerName, heloName, from, to string, rawMessage []byte) error {
	conn, err := s.dialContext(ctx, "tcp4", target)
	if err != nil {
		return fmt.Errorf("dial tcp4 failed: %w", err)
	}
	if deadline, ok := directSMTPDeadline(ctx, s.cfg.DialTimeout); ok {
		_ = conn.SetDeadline(deadline)
	}

	client, err := smtp.NewClient(conn, tlsServerName)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp client failed: %w", err)
	}
	defer client.Close()

	if err := client.Hello(heloName); err != nil {
		return fmt.Errorf("hello failed: %w", err)
	}
	if ok, _ := client.Extension("STARTTLS"); ok {
		tlsConfig := s.tlsConfig
		if tlsConfig == nil {
			tlsConfig = directSMTPTLSConfig
		}
		if err := client.StartTLS(tlsConfig(tlsServerName)); err != nil {
			return fmt.Errorf("starttls failed: %w", err)
		}
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("mail from failed: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("rcpt to failed: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("data failed: %w", err)
	}
	if _, err := writer.Write(rawMessage); err != nil {
		_ = writer.Close()
		return fmt.Errorf("write failed: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("data close failed: %w", err)
	}
	_ = client.Quit()
	return nil
}

func directSMTPDeadline(ctx context.Context, fallback time.Duration) (time.Time, bool) {
	if deadline, ok := ctx.Deadline(); ok {
		return deadline, true
	}
	if fallback <= 0 {
		return time.Time{}, false
	}
	return time.Now().Add(fallback * 2), true
}

func directSMTPTLSConfig(serverName string) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
}

func lookupMXTargets(ctx context.Context, domainName string) ([]string, error) {
	mxs, err := net.DefaultResolver.LookupMX(ctx, domainName)
	if err != nil {
		if dnsErr, ok := err.(*net.DNSError); ok && dnsErr.IsNotFound {
			return []string{net.JoinHostPort(domainName, "25")}, nil
		}
		return nil, err
	}
	if len(mxs) == 0 {
		return []string{net.JoinHostPort(domainName, "25")}, nil
	}
	sort.SliceStable(mxs, func(i, j int) bool {
		return mxs[i].Pref < mxs[j].Pref
	})
	targets := make([]string, 0, len(mxs))
	for _, mx := range mxs {
		host := strings.TrimSuffix(mx.Host, ".")
		if host == "" {
			continue
		}
		targets = append(targets, net.JoinHostPort(host, "25"))
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("mx records are empty")
	}
	return targets, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
