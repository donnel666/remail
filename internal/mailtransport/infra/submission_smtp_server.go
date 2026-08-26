package infra

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	stdmail "net/mail"
	"strings"
	"sync"
	"time"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/mailtransport/domain"
	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	messagetextproto "github.com/emersion/go-message/textproto"
	"github.com/emersion/go-sasl"
	smtpserver "github.com/emersion/go-smtp"
)

type SubmissionSMTPConfig struct {
	Enabled         bool
	Addr            string
	Domain          string
	MaxMessageBytes int64
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
}

type SubmissionTokenAuthenticator interface {
	AuthenticateSMTPSubmissionKey(ctx context.Context, plain string) (uint, error)
}

type SubmissionSMTPServer struct {
	server *smtpserver.Server
}

func NewSubmissionSMTPServer(cfg SubmissionSMTPConfig, authenticator SubmissionTokenAuthenticator, delivery mailapp.DeliveryPort) *SubmissionSMTPServer {
	maxMessageBytes := cfg.MaxMessageBytes
	if maxMessageBytes == 0 {
		maxMessageBytes = 10 << 20
	}
	backend := &submissionSMTPBackend{
		authenticator:   authenticator,
		delivery:        delivery,
		maxMessageBytes: maxMessageBytes,
	}
	server := smtpserver.NewServer(backend)
	server.Addr = firstNonEmpty(cfg.Addr, ":2587")
	server.Domain = firstNonEmpty(cfg.Domain, "localhost")
	server.MaxMessageBytes = maxMessageBytes
	server.MaxRecipients = 1
	server.ReadTimeout = cfg.ReadTimeout
	if server.ReadTimeout == 0 {
		server.ReadTimeout = 30 * time.Second
	}
	server.WriteTimeout = cfg.WriteTimeout
	if server.WriteTimeout == 0 {
		server.WriteTimeout = 30 * time.Second
	}
	// ponytail: plaintext AUTH is for trusted LANs; add TLS before exposing this port publicly.
	server.AllowInsecureAuth = true
	return &SubmissionSMTPServer{server: server}
}

func (s *SubmissionSMTPServer) ListenAndServe() error {
	if s == nil || s.server == nil {
		return nil
	}
	return s.server.ListenAndServe()
}

func (s *SubmissionSMTPServer) Shutdown(ctx context.Context) error {
	if s == nil || s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

type submissionSMTPBackend struct {
	authenticator   SubmissionTokenAuthenticator
	delivery        mailapp.DeliveryPort
	maxMessageBytes int64
	connMu          sync.Mutex
	activeConns     int
}

func (b *submissionSMTPBackend) NewSession(conn *smtpserver.Conn) (smtpserver.Session, error) {
	b.connMu.Lock()
	if b.activeConns >= runtimeconfig.Int("default_inbound_smtp_max_connections", defaultInboundSMTPMaxConnections, 1) {
		b.connMu.Unlock()
		return nil, smtpTemporary("too many connections")
	}
	b.activeConns++
	b.connMu.Unlock()

	remoteAddr := ""
	if conn != nil && conn.Conn() != nil {
		remoteAddr = conn.Conn().RemoteAddr().String()
	}
	return &submissionSMTPSession{
		authenticator:   b.authenticator,
		delivery:        b.delivery,
		remoteAddr:      remoteAddr,
		maxMessageBytes: b.maxMessageBytes,
		releaseConn: func() {
			b.connMu.Lock()
			if b.activeConns > 0 {
				b.activeConns--
			}
			b.connMu.Unlock()
		},
	}, nil
}

type submissionSMTPSession struct {
	authenticator   SubmissionTokenAuthenticator
	delivery        mailapp.DeliveryPort
	remoteAddr      string
	maxMessageBytes int64
	releaseConn     func()
	releaseOnce     sync.Once
	authenticated   bool
	envelopeFrom    string
	recipients      []string
}

func (s *submissionSMTPSession) AuthMechanisms() []string {
	return []string{sasl.Plain, sasl.Login}
}

func (s *submissionSMTPSession) Auth(mechanism string) (sasl.Server, error) {
	switch {
	case strings.EqualFold(mechanism, sasl.Plain):
		return sasl.NewPlainServer(func(_, _, password string) error {
			return s.authenticateToken(password)
		}), nil
	case strings.EqualFold(mechanism, sasl.Login):
		return &submissionLoginServer{authenticate: s.authenticateToken}, nil
	default:
		return nil, smtpserver.ErrAuthUnknownMechanism
	}
}

func (s *submissionSMTPSession) authenticateToken(password string) error {
	if s.authenticator == nil {
		return smtpTemporary("authentication unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := s.authenticator.AuthenticateSMTPSubmissionKey(ctx, password); err != nil {
		if !isInvalidSystemKeyError(err) {
			slog.Warn("smtp submission authentication lookup failed", "remote_addr", s.remoteAddr, "error", err)
			return smtpTemporary("authentication unavailable")
		}
		return smtpserver.ErrAuthFailed
	}
	s.authenticated = true
	return nil
}

type submissionLoginServer struct {
	authenticate func(string) error
	step         uint8
}

func (s *submissionLoginServer) Next(response []byte) ([]byte, bool, error) {
	switch s.step {
	case 0:
		if response == nil {
			s.step = 1
			return []byte("Username:"), false, nil
		}
		s.step = 2
		return []byte("Password:"), false, nil
	case 1:
		s.step = 2
		return []byte("Password:"), false, nil
	default:
		return nil, true, s.authenticate(string(response))
	}
}

func isInvalidSystemKeyError(err error) bool {
	return errors.Is(err, settingsdomain.ErrInvalidSystemKey) || errors.Is(err, settingsdomain.ErrSystemKeyNotFound)
}

func (s *submissionSMTPSession) Reset() {
	s.envelopeFrom = ""
	s.recipients = nil
}

func (s *submissionSMTPSession) Logout() error {
	s.releaseOnce.Do(func() {
		if s.releaseConn != nil {
			s.releaseConn()
		}
	})
	return nil
}

func (s *submissionSMTPSession) Mail(from string, _ *smtpserver.MailOptions) error {
	if !s.authenticated {
		return smtpserver.ErrAuthRequired
	}
	from = validSubmissionAddress(from)
	if from == "" {
		return smtpPermanent("sender rejected")
	}
	s.envelopeFrom = from
	s.recipients = nil
	return nil
}

func (s *submissionSMTPSession) Rcpt(to string, _ *smtpserver.RcptOptions) error {
	if !s.authenticated {
		return smtpserver.ErrAuthRequired
	}
	to = validSubmissionAddress(to)
	if to == "" {
		return smtpPermanent("recipient rejected")
	}
	s.recipients = append(s.recipients, to)
	return nil
}

func (s *submissionSMTPSession) Data(r io.Reader) error {
	if !s.authenticated {
		return smtpserver.ErrAuthRequired
	}
	if s.envelopeFrom == "" || len(s.recipients) != 1 || s.delivery == nil {
		return smtpPermanent("message envelope rejected")
	}
	limit := s.maxMessageBytes
	if limit <= 0 {
		limit = 10 << 20
	}
	rawMessage, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		if errors.Is(err, smtpserver.ErrDataTooLarge) {
			return smtpserver.ErrDataTooLarge
		}
		return smtpTemporary("message read failed")
	}
	if int64(len(rawMessage)) > limit {
		return smtpMessageTooLarge("message too large")
	}
	rawMessage, err = sanitizeSubmissionMessage(rawMessage)
	if err != nil {
		return smtpPermanent("message content rejected")
	}
	messageKey, err := newSubmissionMessageKey()
	if err != nil {
		slog.Warn("smtp submission identity generation failed", "remote_addr", s.remoteAddr, "error", err)
		return smtpTemporary("message queue unavailable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.delivery.Send(ctx, domain.OutboundMessage{
		IdempotencyKey: messageKey,
		Purpose:        domain.PurposeSMTPSubmission,
		From:           s.envelopeFrom,
		To:             s.recipients[0],
		RawMessage:     rawMessage,
	}); err != nil {
		slog.Warn("smtp submission enqueue failed", "remote_addr", s.remoteAddr, "error", err)
		return smtpTemporary("message queue unavailable")
	}
	slog.Info("smtp submission accepted", "remote_addr", s.remoteAddr, "recipients", 1, "bytes", len(rawMessage))
	return nil
}

func sanitizeSubmissionMessage(rawMessage []byte) ([]byte, error) {
	reader := bufio.NewReader(bytes.NewReader(rawMessage))
	header, err := messagetextproto.ReadHeader(reader)
	if err != nil || strings.TrimSpace(header.Get("From")) == "" {
		return nil, errors.New("invalid message header")
	}
	for _, name := range []string{"Bcc", "Resent-Bcc", "Return-Path"} {
		header.Del(name)
	}
	var sanitized bytes.Buffer
	if err := messagetextproto.WriteHeader(&sanitized, header); err != nil {
		return nil, err
	}
	if _, err := io.Copy(&sanitized, reader); err != nil {
		return nil, err
	}
	return sanitized.Bytes(), nil
}

func validSubmissionAddress(value string) string {
	address, err := stdmail.ParseAddress(firstLineValue(value))
	if err != nil || !strings.Contains(address.Address, "@") {
		return ""
	}
	return address.Address
}

func newSubmissionMessageKey() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "smtp-submission-" + hex.EncodeToString(random[:]), nil
}
