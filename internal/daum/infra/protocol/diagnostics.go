package protocol

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
)

func failureAt(err error, stage string) error {
	var failure *Failure
	if !errors.As(err, &failure) {
		return err
	}
	if failure.Stage != "" {
		return err
	}
	copyFailure := *failure
	copyFailure.Stage = stage
	copyFailure.Cause = err
	return &copyFailure
}

// Diagnostic codes never include provider responses, account names or proxy URLs.
func FailureDiagnostics(err error) (stage, reason string) {
	stage, reason = "unknown", "unknown"
	if err == nil {
		return stage, reason
	}
	var failure *Failure
	if errors.As(err, &failure) {
		switch failure.Stage {
		case "credentials", "connect_tls", "greeting", "authenticate", "mailbox_access":
			stage = failure.Stage
		}
	}
	var dns *net.DNSError
	var network net.Error
	var certificate *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var record tls.RecordHeaderError
	switch {
	case errors.As(err, &dns):
		reason = "dns"
	case errors.Is(err, context.Canceled):
		reason = "canceled"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &network) && network.Timeout():
		reason = "timeout"
	case errors.Is(err, syscall.ECONNREFUSED):
		reason = "connection_refused"
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH):
		reason = "network_unreachable"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE), errors.Is(err, net.ErrClosed):
		reason = "connection_closed"
	case errors.As(err, &certificate), errors.As(err, &unknownAuthority):
		reason = "tls_certificate"
	case errors.As(err, &record):
		reason = "tls_protocol"
	case failure != nil && stage == "credentials":
		reason = "invalid_input"
	case failure != nil && failure.Category != "transport":
		reason = "provider_rejection"
	}
	if reason == "unknown" {
		// SOCKS5 reply errors are plain strings rather than syscall errors.
		for cause := err; cause != nil; cause = errors.Unwrap(cause) {
			text := strings.ToLower(cause.Error())
			switch {
			case strings.Contains(text, "network unreachable"), strings.Contains(text, "network is unreachable"), strings.Contains(text, "host unreachable"), strings.Contains(text, "no route to host"):
				reason = "network_unreachable"
			case strings.Contains(text, "connection refused"), strings.Contains(text, "connection not allowed"), strings.Contains(text, "target refused"):
				reason = "connection_refused"
			case text == "proxy connect failed":
				reason = "proxy_connect"
			case strings.HasPrefix(text, "tls:"), strings.HasPrefix(text, "remote error: tls:"):
				reason = "tls_handshake"
			}
			if reason != "unknown" {
				break
			}
		}
	}
	return stage, reason
}
