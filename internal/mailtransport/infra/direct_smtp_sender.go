package infra

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
	gomail "github.com/wneessen/go-mail"
)

type DirectSMTPConfig struct {
	From        string
	Domain      string
	HELODomain  string
	DialTimeout time.Duration
}

type DirectSMTPDelivery struct {
	cfg DirectSMTPConfig
}

func NewDirectSMTPDelivery(cfg DirectSMTPConfig) *DirectSMTPDelivery {
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 15 * time.Second
	}
	return &DirectSMTPDelivery{cfg: cfg}
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
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return err
	}
	mailMessage, err := newSMTPMessage(from, to, message)
	if err != nil {
		return err
	}
	client, err := gomail.NewClient(host,
		gomail.WithPort(portNum),
		gomail.WithTimeout(s.cfg.DialTimeout),
		gomail.WithHELO(heloName),
		gomail.WithTLSPolicy(gomail.TLSOpportunistic),
		gomail.WithTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}),
	)
	if err != nil {
		return err
	}
	return sendMailWithAcceptedClose(ctx, client, mailMessage)
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
