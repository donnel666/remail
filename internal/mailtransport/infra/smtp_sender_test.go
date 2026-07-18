package infra

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/netip"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestClassifySMTPFailureSeparatesRemoteBusinessResultsFromInfrastructure(t *testing.T) {
	retryable := classifySMTPFailure("smtp send failed", &textproto.Error{Code: 451, Msg: "try later"})
	var retryableFailure *mailapp.OutboundSendFailure
	require.ErrorAs(t, retryable, &retryableFailure)
	require.True(t, retryableFailure.Retryable)

	permanent := classifySMTPFailure("smtp send failed", &textproto.Error{Code: 550, Msg: "mailbox unavailable"})
	var permanentFailure *mailapp.OutboundSendFailure
	require.ErrorAs(t, permanent, &permanentFailure)
	require.False(t, permanentFailure.Retryable)

	infrastructure := classifySMTPFailure("smtp send failed", errors.New("dial timeout"))
	var businessFailure *mailapp.OutboundSendFailure
	require.False(t, errors.As(infrastructure, &businessFailure))
	require.ErrorIs(t, infrastructure, domain.ErrDeliveryUnavailable)
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

func TestDirectSMTPDeliveryUsesTCP4AndStandardSMTPFlow(t *testing.T) {
	addr, dataCh, commands, stop := startDirectCapturingSMTPServer(t, false, tls.Certificate{})
	defer stop()

	sender := NewDirectSMTPDelivery(DirectSMTPConfig{
		From:       "no-reply@example.com",
		Domain:     "example.com",
		HELODomain: "mx.example.com",
	})
	var dialNetworks []string
	dialer := &net.Dialer{}
	sender.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		dialNetworks = append(dialNetworks, network)
		return dialer.DialContext(ctx, network, address)
	}

	err := sender.sendRawToTarget(
		context.Background(),
		"tcp4",
		addr,
		"localhost",
		"mx.example.com",
		"no-reply@example.com",
		"user@example.com",
		[]byte("From: no-reply@example.com\r\nTo: user@example.com\r\nSubject: direct\r\n\r\nbody\r\n"),
	)

	require.NoError(t, err)
	require.Equal(t, []string{"tcp4"}, dialNetworks)
	raw := <-dataCh
	assert.Contains(t, raw, "Subject: direct")
	assert.Contains(t, raw, "body")
	assert.Equal(t, []string{
		"EHLO mx.example.com",
		"MAIL FROM:<no-reply@example.com>",
		"RCPT TO:<user@example.com>",
		"DATA",
		"QUIT",
	}, commands())
}

func TestDirectSMTPDeliveryUpgradesWhenSTARTTLSAdvertised(t *testing.T) {
	cert, roots := newTestServerTLS(t)
	addr, dataCh, commands, stop := startDirectCapturingSMTPServer(t, true, cert)
	defer stop()

	sender := NewDirectSMTPDelivery(DirectSMTPConfig{
		From:       "no-reply@example.com",
		Domain:     "example.com",
		HELODomain: "mx.example.com",
	})
	sender.tlsConfig = func(serverName string) *tls.Config {
		return &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: serverName,
			RootCAs:    roots,
		}
	}

	err := sender.sendRawToTarget(
		context.Background(),
		"tcp4",
		addr,
		"localhost",
		"mx.example.com",
		"no-reply@example.com",
		"user@example.com",
		[]byte("From: no-reply@example.com\r\nTo: user@example.com\r\nSubject: tls\r\n\r\nbody\r\n"),
	)

	require.NoError(t, err)
	raw := <-dataCh
	assert.Contains(t, raw, "Subject: tls")
	assert.Contains(t, raw, "body")

	commandList := commands()
	firstEHLO := requireCommandIndex(t, commandList, "EHLO mx.example.com")
	startTLS := requireCommandIndex(t, commandList, "STARTTLS")
	secondEHLO := requireCommandLastIndex(t, commandList, "EHLO mx.example.com")
	mailFrom := requireCommandIndex(t, commandList, "MAIL FROM:<no-reply@example.com>")
	require.Less(t, firstEHLO, startTLS)
	require.Less(t, startTLS, secondEHLO)
	require.Less(t, secondEHLO, mailFrom)
}

func TestDirectSMTPTargetsRejectUnsafeAddresses(t *testing.T) {
	targets, err := lookupSMTPHostTargets(
		context.Background(),
		"mx.example.com",
		func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{
				netip.MustParseAddr("0.0.0.0"),
				netip.MustParseAddr("127.0.0.1"),
				netip.MustParseAddr("10.0.0.1"),
				netip.MustParseAddr("100.64.0.1"),
				netip.MustParseAddr("169.254.0.1"),
				netip.MustParseAddr("224.0.0.1"),
				netip.MustParseAddr("::"),
				netip.MustParseAddr("::1"),
				netip.MustParseAddr("::ffff:192.0.2.1"),
				netip.MustParseAddr("fc00::1"),
				netip.MustParseAddr("fe80::1"),
				netip.MustParseAddr("ff02::1"),
			}, nil
		},
	)

	require.Error(t, err)
	assert.Empty(t, targets)
}

func TestDirectSMTPDeliveryDialsResolvedPublicIP(t *testing.T) {
	cert, roots := newTestServerTLS(t)
	localAddress, _, _, stop := startDirectCapturingSMTPServer(t, true, cert)
	defer stop()

	sender := NewDirectSMTPDelivery(DirectSMTPConfig{From: "no-reply@example.com"})
	sender.lookupMX = func(context.Context, string) ([]*net.MX, error) {
		return []*net.MX{
			{Host: "backup.example.com.", Pref: 20},
			{Host: "mx.example.com.", Pref: 10},
		}, nil
	}
	sender.lookupNetIP = func(_ context.Context, _ string, host string) ([]netip.Addr, error) {
		if host != "mx.example.com" {
			t.Fatalf("resolved backup MX before trying the usable primary: %s", host)
		}
		return []netip.Addr{
			netip.MustParseAddr("10.0.0.1"),
			netip.MustParseAddr("93.184.216.33"),
			netip.MustParseAddr("93.184.216.34"),
		}, nil
	}
	var network, tlsServerName string
	var addresses []string
	dialer := &net.Dialer{}
	sender.dialContext = func(ctx context.Context, gotNetwork, gotAddress string) (net.Conn, error) {
		network = gotNetwork
		addresses = append(addresses, gotAddress)
		if gotAddress == "93.184.216.33:25" {
			return nil, errors.New("primary address unavailable")
		}
		return dialer.DialContext(ctx, gotNetwork, localAddress)
	}
	sender.tlsConfig = func(serverName string) *tls.Config {
		tlsServerName = serverName
		return &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "localhost", RootCAs: roots}
	}

	err := sender.Send(context.Background(), mailapp.VerificationCodeMessage("user@example.com", "123456"))

	require.NoError(t, err)
	assert.Equal(t, "tcp4", network)
	assert.Equal(t, []string{"93.184.216.33:25", "93.184.216.34:25"}, addresses)
	assert.Equal(t, "mx.example.com", tlsServerName)
}

func TestDirectSMTPFailureClassificationKeepsAnyTemporarySMTPResultRetryable(t *testing.T) {
	err := classifyDirectSMTPFailures("direct smtp delivery failed", []error{
		fmt.Errorf("primary failed: %w", &textproto.Error{Code: 451, Msg: "try later"}),
		fmt.Errorf("backup failed: %w", &textproto.Error{Code: 550, Msg: "mailbox unavailable"}),
	})

	var failure *mailapp.OutboundSendFailure
	require.ErrorAs(t, err, &failure)
	assert.True(t, failure.Retryable)
}

func TestDirectSMTPFailureClassificationGivesInfrastructurePriority(t *testing.T) {
	err := classifyDirectSMTPFailures("direct smtp delivery failed", []error{
		errors.New("dial timeout"),
		fmt.Errorf("backup failed: %w", &textproto.Error{Code: 550, Msg: "mailbox unavailable"}),
	})

	var failure *mailapp.OutboundSendFailure
	assert.False(t, errors.As(err, &failure))
	assert.ErrorIs(t, err, domain.ErrDeliveryUnavailable)
}

func TestDirectSMTPFailureClassificationKeepsSTARTTLSTemporaryResponseRetryable(t *testing.T) {
	err := classifyDirectSMTPFailures("direct smtp delivery failed", []error{
		fmt.Errorf("direct smtp starttls failed: %w", &textproto.Error{Code: 454, Msg: "TLS unavailable"}),
		fmt.Errorf("backup failed: %w", &textproto.Error{Code: 550, Msg: "mailbox unavailable"}),
	})

	var failure *mailapp.OutboundSendFailure
	require.ErrorAs(t, err, &failure)
	assert.True(t, failure.Retryable)
}

func TestDirectSMTPNullMXIsPermanentBusinessFailure(t *testing.T) {
	sender := NewDirectSMTPDelivery(DirectSMTPConfig{From: "no-reply@example.com"})
	sender.lookupMX = func(context.Context, string) ([]*net.MX, error) {
		return []*net.MX{{Host: ".", Pref: 0}}, nil
	}
	sender.lookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		t.Fatal("must not resolve an address for a null MX")
		return nil, nil
	}

	err := sender.Send(context.Background(), mailapp.VerificationCodeMessage("user@example.com", "123456"))

	var failure *mailapp.OutboundSendFailure
	require.ErrorAs(t, err, &failure)
	assert.False(t, failure.Retryable)
	assert.Equal(t, "Recipient domain does not accept email.", failure.SafeMessage)
}

func TestDirectSMTPOnlyUnsafeTargetsIsPermanentBusinessFailure(t *testing.T) {
	sender := NewDirectSMTPDelivery(DirectSMTPConfig{From: "no-reply@example.com"})
	sender.lookupMX = func(context.Context, string) ([]*net.MX, error) {
		return []*net.MX{{Host: "mx.example.com."}}, nil
	}
	sender.lookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("fec0::1")}, nil
	}
	sender.dialContext = func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("must not dial a rejected address")
		return nil, nil
	}

	err := sender.Send(context.Background(), mailapp.VerificationCodeMessage("user@example.com", "123456"))

	var failure *mailapp.OutboundSendFailure
	require.ErrorAs(t, err, &failure)
	assert.False(t, failure.Retryable)
	assert.Equal(t, "Recipient mail server address is not allowed.", failure.SafeMessage)
}

func TestDirectSMTPMissingRecipientAddressIsPermanentBusinessFailure(t *testing.T) {
	sender := NewDirectSMTPDelivery(DirectSMTPConfig{From: "no-reply@example.com"})
	sender.lookupMX = func(context.Context, string) ([]*net.MX, error) {
		return nil, &net.DNSError{IsNotFound: true, Name: "example.com"}
	}
	sender.lookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return nil, &net.DNSError{IsNotFound: true, Name: "example.com"}
	}

	err := sender.Send(context.Background(), mailapp.VerificationCodeMessage("user@example.com", "123456"))

	var failure *mailapp.OutboundSendFailure
	require.ErrorAs(t, err, &failure)
	assert.False(t, failure.Retryable)
	assert.Equal(t, "Recipient domain has no usable mail server.", failure.SafeMessage)
}

func TestDirectSMTPFallbackRejectsPrivateAddress(t *testing.T) {
	hosts, err := lookupMXHosts(
		context.Background(),
		"example.com",
		func(context.Context, string) ([]*net.MX, error) {
			return nil, &net.DNSError{IsNotFound: true}
		},
	)
	require.NoError(t, err)
	require.Equal(t, []string{"example.com"}, hosts)

	targets, err := lookupSMTPHostTargets(
		context.Background(),
		hosts[0],
		func(_ context.Context, _ string, host string) ([]netip.Addr, error) {
			assert.Equal(t, "example.com", host)
			return []netip.Addr{netip.MustParseAddr("100.64.0.1")}, nil
		},
	)

	require.Error(t, err)
	assert.Empty(t, targets)
}

func TestDirectSMTPAddressFilterRejectsSpecialUseRanges(t *testing.T) {
	tests := map[string]bool{
		"93.184.216.34":   true,
		"2606:4700::1111": true,
		"0.0.0.1":         false,
		"100.64.0.1":      false,
		"192.0.2.1":       false,
		"198.18.0.1":      false,
		"240.0.0.1":       false,
		"64:ff9b::a00:1":  false,
		"2001:db8::1":     false,
		"2002:a00:1::":    false,
		"fec0::1":         false,
	}

	for raw, want := range tests {
		t.Run(raw, func(t *testing.T) {
			assert.Equal(t, want, isPublicSMTPAddress(netip.MustParseAddr(raw)))
		})
	}
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

func startDirectCapturingSMTPServer(t *testing.T, advertiseSTARTTLS bool, cert tls.Certificate) (string, <-chan string, func() []string, func()) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	dataCh := make(chan string, 1)
	done := make(chan struct{})
	var mu sync.Mutex
	var active net.Conn
	commands := make([]string, 0, 8)
	recordCommand := func(line string) {
		mu.Lock()
		defer mu.Unlock()
		commands = append(commands, normalizeSMTPCommand(line))
	}
	commandSnapshot := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), commands...)
	}

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
		tlsActive := false
		for {
			line, err := rw.ReadString('\n')
			if err != nil {
				return
			}
			recordCommand(line)
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"):
				writeSMTPLine(t, rw, "250-remail-test")
				if advertiseSTARTTLS && !tlsActive {
					writeSMTPLine(t, rw, "250-STARTTLS")
				}
				writeSMTPLine(t, rw, "250 OK")
			case strings.HasPrefix(cmd, "STARTTLS"):
				if !advertiseSTARTTLS {
					writeSMTPLine(t, rw, "454 TLS not available")
					continue
				}
				writeSMTPLine(t, rw, "220 Ready to start TLS")
				tlsConn := tls.Server(conn, &tls.Config{
					MinVersion:   tls.VersionTLS12,
					Certificates: []tls.Certificate{cert},
				})
				if err := tlsConn.Handshake(); err != nil {
					return
				}
				conn = tlsConn
				mu.Lock()
				active = conn
				mu.Unlock()
				rw = bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
				tlsActive = true
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

	return ln.Addr().String(), dataCh, commandSnapshot, func() {
		_ = ln.Close()
		mu.Lock()
		if active != nil {
			_ = active.Close()
		}
		mu.Unlock()
		<-done
	}
}

func normalizeSMTPCommand(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	parts := strings.SplitN(line, " ", 2)
	if len(parts) == 1 {
		return strings.ToUpper(parts[0])
	}
	return strings.ToUpper(parts[0]) + " " + parts[1]
}

func requireCommandIndex(t *testing.T, commands []string, want string) int {
	t.Helper()
	for i, command := range commands {
		if command == want {
			return i
		}
	}
	require.Failf(t, "smtp command not found", "want %q in %v", want, commands)
	return -1
}

func requireCommandLastIndex(t *testing.T, commands []string, want string) int {
	t.Helper()
	for i := len(commands) - 1; i >= 0; i-- {
		if commands[i] == want {
			return i
		}
	}
	require.Failf(t, "smtp command not found", "want %q in %v", want, commands)
	return -1
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

func newTestServerTLS(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)

	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(certPEM))
	return cert, roots
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
