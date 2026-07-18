package infra

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/smtp"
	"net/textproto"
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
	lookupMX    func(context.Context, string) ([]*net.MX, error)
	lookupNetIP func(context.Context, string, string) ([]netip.Addr, error)
	tlsConfig   func(serverName string) *tls.Config
}

type directSMTPTarget struct {
	host    string
	network string
	address string
}

var (
	errDirectSMTPNullMX        = errors.New("recipient domain has a null MX")
	errDirectSMTPNoAddresses   = errors.New("smtp target has no IP addresses")
	errDirectSMTPUnsafeTargets = errors.New("smtp target has no public IP addresses")
)

var nonPublicSMTPPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:10::/28"),
	netip.MustParsePrefix("2001:20::/28"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

func NewDirectSMTPDelivery(cfg DirectSMTPConfig) *DirectSMTPDelivery {
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 15 * time.Second
	}
	dialer := &net.Dialer{Timeout: cfg.DialTimeout}
	return &DirectSMTPDelivery{
		cfg:         cfg,
		dialContext: dialer.DialContext,
		lookupMX:    net.DefaultResolver.LookupMX,
		lookupNetIP: net.DefaultResolver.LookupNetIP,
		tlsConfig:   directSMTPTLSConfig,
	}
}

func (s *DirectSMTPDelivery) Send(ctx context.Context, message domain.OutboundMessage) error {
	from := envelopeAddress(firstNonEmpty(message.From, s.cfg.From))
	to := envelopeAddress(message.To)
	if from == "" || to == "" {
		return permanentOutboundFailure("Outbound mail envelope is invalid.", deliveryError("direct smtp envelope incomplete", nil))
	}

	recipientDomain := recipientDomain(to)
	if recipientDomain == "" {
		return permanentOutboundFailure("Outbound mail recipient is invalid.", deliveryError("direct smtp recipient invalid", nil))
	}
	rawMessage, err := newSignedSMTPMessage(from, to, message, s.cfg.DKIM)
	if err != nil {
		return permanentOutboundFailure("Outbound mail message is invalid.", deliveryError("direct smtp message failed", err))
	}
	hosts, err := lookupMXHosts(ctx, recipientDomain, s.lookupMX)
	if err != nil {
		if errors.Is(err, errDirectSMTPNullMX) {
			return permanentOutboundFailure("Recipient domain does not accept email.", deliveryError("direct smtp null mx", err))
		}
		return deliveryError("direct smtp mx lookup failed", err)
	}
	heloName := firstNonEmpty(firstLineValue(s.cfg.HELODomain), firstLineValue(s.cfg.Domain), "localhost")
	var failures []error
	unsafeOnly := true
	for _, host := range hosts {
		targets, err := lookupSMTPHostTargets(ctx, host, s.lookupNetIP)
		if err != nil {
			failures = append(failures, fmt.Errorf("resolve %s failed: %w", host, err))
			if !errors.Is(err, errDirectSMTPUnsafeTargets) {
				unsafeOnly = false
			}
			continue
		}
		unsafeOnly = false
		for _, target := range targets {
			if err := s.sendRawToTarget(ctx, target.network, target.address, target.host, heloName, from, to, rawMessage); err != nil {
				failures = append(failures, fmt.Errorf("deliver to %s failed: %w", target.host, err))
				continue
			}
			return nil
		}
	}
	if unsafeOnly && len(failures) != 0 {
		return permanentOutboundFailure("Recipient mail server address is not allowed.", deliveryError("direct smtp target rejected", errors.Join(failures...)))
	}
	return classifyDirectSMTPFailures("direct smtp delivery failed", failures)
}

func (s *DirectSMTPDelivery) sendRawToTarget(ctx context.Context, network, target, tlsServerName, heloName, from, to string, rawMessage []byte) error {
	conn, err := s.dialContext(ctx, network, target)
	if err != nil {
		return fmt.Errorf("dial %s failed: %w", network, err)
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
			return fmt.Errorf("direct smtp starttls failed: %w", err)
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

func lookupMXHosts(
	ctx context.Context,
	domainName string,
	lookupMX func(context.Context, string) ([]*net.MX, error),
) ([]string, error) {
	mxs, err := lookupMX(ctx, domainName)
	hosts := make([]string, 0, len(mxs))
	if err != nil {
		if dnsErr, ok := err.(*net.DNSError); ok && dnsErr.IsNotFound {
			hosts = append(hosts, domainName)
		} else {
			return nil, err
		}
	} else if len(mxs) == 0 {
		hosts = append(hosts, domainName)
	} else {
		if len(mxs) == 1 && strings.TrimSpace(mxs[0].Host) == "." {
			return nil, errDirectSMTPNullMX
		}
		sort.SliceStable(mxs, func(i, j int) bool {
			return mxs[i].Pref < mxs[j].Pref
		})
		for _, mx := range mxs {
			host := strings.TrimSuffix(mx.Host, ".")
			if host != "" {
				hosts = append(hosts, host)
			}
		}
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("mx records are empty")
	}
	return hosts, nil
}

func lookupSMTPHostTargets(
	ctx context.Context,
	host string,
	lookupNetIP func(context.Context, string, string) ([]netip.Addr, error),
) ([]directSMTPTarget, error) {
	addresses, err := lookupNetIP(ctx, "ip", host)
	if err != nil {
		var dnsError *net.DNSError
		if errors.As(err, &dnsError) && dnsError.IsNotFound {
			return nil, fmt.Errorf("%w: %s", errDirectSMTPNoAddresses, safeDiagnostic(err.Error()))
		}
		return nil, err
	}
	targets := make([]directSMTPTarget, 0, len(addresses))
	for _, address := range addresses {
		if !isPublicSMTPAddress(address) {
			continue
		}
		network := "tcp6"
		if address.Is4() {
			network = "tcp4"
		}
		targets = append(targets, directSMTPTarget{
			host:    host,
			network: network,
			address: net.JoinHostPort(address.String(), "25"),
		})
	}
	if len(targets) == 0 {
		if len(addresses) != 0 {
			return nil, errDirectSMTPUnsafeTargets
		}
		return nil, errDirectSMTPNoAddresses
	}
	return targets, nil
}

func classifyDirectSMTPFailures(stage string, failures []error) error {
	temporarySMTP := -1
	permanentSMTP := -1
	for i, failure := range failures {
		if errors.Is(failure, errDirectSMTPNoAddresses) || errors.Is(failure, errDirectSMTPUnsafeTargets) {
			continue
		}
		var smtpError *textproto.Error
		if !errors.As(failure, &smtpError) || smtpError.Code < 400 || smtpError.Code >= 600 {
			return deliveryError(stage, errors.Join(failures...))
		}
		if smtpError.Code < 500 && temporarySMTP == -1 {
			temporarySMTP = i
		}
		if smtpError.Code >= 500 && permanentSMTP == -1 {
			permanentSMTP = i
		}
	}
	if temporarySMTP != -1 {
		return classifySMTPFailure(stage, joinWithFirst(failures, temporarySMTP))
	}
	if permanentSMTP != -1 {
		return classifySMTPFailure(stage, joinWithFirst(failures, permanentSMTP))
	}
	return permanentOutboundFailure("Recipient domain has no usable mail server.", deliveryError(stage, errors.Join(failures...)))
}

func joinWithFirst(failures []error, first int) error {
	ordered := make([]error, 0, len(failures))
	ordered = append(ordered, failures[first])
	ordered = append(ordered, failures[:first]...)
	ordered = append(ordered, failures[first+1:]...)
	return errors.Join(ordered...)
}

func isPublicSMTPAddress(address netip.Addr) bool {
	if !address.IsValid() ||
		!address.IsGlobalUnicast() ||
		address.Is4In6() ||
		address.IsPrivate() ||
		address.IsLoopback() ||
		address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() ||
		address.IsMulticast() ||
		address.IsUnspecified() {
		return false
	}
	for _, prefix := range nonPublicSMTPPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
