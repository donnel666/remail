package infra

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/mailtransport/domain"
	smtpserver "github.com/emersion/go-smtp"
)

type InboundSMTPConfig struct {
	Enabled         bool
	Addr            string
	Domain          string
	MaxMessageBytes int64
	MaxRecipients   int
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
}

type InboundAccepter interface {
	ResolveRecipient(ctx context.Context, email string) (*domain.InboundRecipient, error)
	Accept(ctx context.Context, message mailapp.InboundRawMessage) ([]domain.InboundMail, error)
}

type InboundSMTPServer struct {
	server *smtpserver.Server
}

func NewInboundSMTPServer(cfg InboundSMTPConfig, accepter InboundAccepter) *InboundSMTPServer {
	backend := &inboundSMTPBackend{accepter: accepter}
	server := smtpserver.NewServer(backend)
	server.Addr = firstNonEmpty(cfg.Addr, ":2525")
	server.Domain = firstNonEmpty(cfg.Domain, "mx.aishop6.com")
	server.MaxMessageBytes = cfg.MaxMessageBytes
	if server.MaxMessageBytes == 0 {
		server.MaxMessageBytes = 10 << 20
	}
	server.MaxRecipients = cfg.MaxRecipients
	if server.MaxRecipients == 0 {
		server.MaxRecipients = 20
	}
	server.ReadTimeout = cfg.ReadTimeout
	if server.ReadTimeout == 0 {
		server.ReadTimeout = 30 * time.Second
	}
	server.WriteTimeout = cfg.WriteTimeout
	if server.WriteTimeout == 0 {
		server.WriteTimeout = 30 * time.Second
	}
	server.AllowInsecureAuth = false
	return &InboundSMTPServer{server: server}
}

func (s *InboundSMTPServer) ListenAndServe() error {
	if s == nil || s.server == nil {
		return nil
	}
	return s.server.ListenAndServe()
}

func (s *InboundSMTPServer) Shutdown(ctx context.Context) error {
	if s == nil || s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

type inboundSMTPBackend struct {
	accepter InboundAccepter
}

func (b *inboundSMTPBackend) NewSession(conn *smtpserver.Conn) (smtpserver.Session, error) {
	remoteAddr := ""
	if conn != nil && conn.Conn() != nil {
		remoteAddr = conn.Conn().RemoteAddr().String()
	}
	return &inboundSMTPSession{
		accepter:   b.accepter,
		remoteAddr: remoteAddr,
	}, nil
}

type inboundSMTPSession struct {
	accepter     InboundAccepter
	remoteAddr   string
	envelopeFrom string
	recipients   []domain.InboundRecipient
}

func (s *inboundSMTPSession) Reset() {
	s.envelopeFrom = ""
	s.recipients = nil
}

func (s *inboundSMTPSession) Logout() error {
	return nil
}

func (s *inboundSMTPSession) Mail(from string, _ *smtpserver.MailOptions) error {
	from = envelopeAddress(from)
	if from == "" {
		return smtpPermanent("sender rejected")
	}
	s.envelopeFrom = from
	s.recipients = nil
	return nil
}

func (s *inboundSMTPSession) Rcpt(to string, _ *smtpserver.RcptOptions) error {
	to = envelopeAddress(to)
	if to == "" {
		return smtpPermanent("recipient rejected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	recipient, err := s.accepter.ResolveRecipient(ctx, to)
	if err != nil {
		if isInboundRecipientRejected(err) {
			return smtpPermanent("recipient rejected")
		}
		slog.Warn("inbound smtp recipient resolver failed", "remote_addr", s.remoteAddr, "error", err)
		return smtpTemporary("recipient resolver unavailable")
	}
	s.recipients = append(s.recipients, *recipient)
	return nil
}

func (s *inboundSMTPSession) Data(r io.Reader) error {
	if s.envelopeFrom == "" || len(s.recipients) == 0 {
		return smtpPermanent("message envelope rejected")
	}
	content, err := io.ReadAll(r)
	if err != nil {
		return smtpTemporary("message read failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = s.accepter.Accept(ctx, mailapp.InboundRawMessage{
		EnvelopeFrom: s.envelopeFrom,
		Recipients:   append([]domain.InboundRecipient(nil), s.recipients...),
		RemoteAddr:   s.remoteAddr,
		ContentBytes: content,
	})
	if err != nil {
		if errors.Is(err, domain.ErrInboundRecipientRejected) {
			return smtpPermanent("recipient rejected")
		}
		slog.Warn("inbound smtp accept failed", "remote_addr", s.remoteAddr, "error", err)
		return smtpTemporary("message storage unavailable")
	}
	return nil
}

func smtpPermanent(message string) error {
	return &smtpserver.SMTPError{Code: 550, Message: message}
}

func smtpTemporary(message string) error {
	return &smtpserver.SMTPError{Code: 451, Message: message}
}
