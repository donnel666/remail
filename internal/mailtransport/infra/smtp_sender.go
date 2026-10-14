package infra

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	stdmail "net/mail"
	"net/textproto"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/mailtransport/domain"
	gomail "github.com/wneessen/go-mail"
	mailsmtp "github.com/wneessen/go-mail/smtp"
)

var diagnosticEmailPattern = regexp.MustCompile(`(?i)\b([a-z0-9._%+\-])[a-z0-9._%+\-]*@([a-z0-9.\-]+\.[a-z]{2,})\b`)

type SMTPConfig struct {
	Addr     string
	Username string
	Password string
	From     string
	DKIM     *DKIMSigner
}

type SMTPDelivery struct {
	cfg         SMTPConfig
	dialContext func(context.Context, string, string) (net.Conn, error)
	tlsConfig   func(string) *tls.Config
}

func NewSMTPDelivery(cfg SMTPConfig) *SMTPDelivery {
	return &SMTPDelivery{cfg: cfg, dialContext: (&net.Dialer{}).DialContext, tlsConfig: directSMTPTLSConfig}
}

func (s *SMTPDelivery) Send(ctx context.Context, message domain.OutboundMessage) error {
	addr := normalizeSMTPAddr(s.cfg.Addr)
	from := envelopeAddress(firstNonEmpty(message.From, s.cfg.From))
	if from == "" {
		from = envelopeAddress(s.cfg.Username)
	}
	to := envelopeAddress(message.To)
	if addr == "" || from == "" || to == "" {
		return permanentOutboundFailure("Outbound mail configuration is invalid.", deliveryError("smtp config incomplete", nil))
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return permanentOutboundFailure("Outbound mail configuration is invalid.", deliveryError("invalid smtp addr", err))
	}
	portNum, err := strconv.Atoi(port)
	if err != nil || host == "" || portNum < 1 || portNum > 65535 {
		return permanentOutboundFailure("Outbound mail configuration is invalid.", deliveryError("invalid smtp port", err))
	}
	rawMessage, err := newSignedSMTPMessage(from, to, message, s.cfg.DKIM)
	if err != nil {
		return permanentOutboundFailure("Outbound mail message is invalid.", deliveryError("smtp message failed", err))
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if notAccepted, err := s.sendRawMailWithAcceptedClose(ctx, host, port, from, to, rawMessage); err != nil {
		failure := classifySMTPFailure("smtp send failed", err)
		if notAccepted {
			return &mailapp.OutboundNotAcceptedError{Cause: failure}
		}
		return failure
	}
	return nil
}

func (s *SMTPDelivery) sendRawMailWithAcceptedClose(ctx context.Context, host, port, from, to string, rawMessage []byte) (notAccepted bool, err error) {
	rawConn, err := s.dialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return true, fmt.Errorf("dial failed: %w", err)
	}
	// Own the socket: go-mail's high-level dialer replaces the context deadline after connecting.
	stopClose := context.AfterFunc(ctx, func() { _ = rawConn.Close() })
	defer func() { stopClose(); _ = rawConn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		if err := rawConn.SetDeadline(deadline); err != nil {
			return true, fmt.Errorf("dial failed: %w", err)
		}
	}
	tlsConfig := s.tlsConfig(host)
	conn := rawConn
	if port == "465" {
		secure := tls.Client(rawConn, tlsConfig)
		if err := secure.HandshakeContext(ctx); err != nil {
			return true, fmt.Errorf("TLS handshake failed: %w", err)
		}
		conn = secure
	}
	smtpClient, err := mailsmtp.NewClient(conn, host)
	if err != nil {
		return true, fmt.Errorf("dial failed: %w", err)
	}
	defer smtpClient.Close()
	helo, err := os.Hostname()
	if err != nil {
		return true, fmt.Errorf("dial failed: %w", err)
	}
	if err := smtpClient.Hello(helo); err != nil {
		return true, fmt.Errorf("dial failed: %w", err)
	}
	if port != "465" {
		supported, _ := smtpClient.Extension("STARTTLS")
		if !supported && requiresSTARTTLS(port, s.cfg) {
			return true, errors.New("STARTTLS is required but the SMTP server does not support it")
		}
		if supported {
			if err := smtpClient.StartTLS(tlsConfig); err != nil {
				return true, fmt.Errorf("STARTTLS failed: %w", err)
			}
		}
	}
	if s.cfg.Username != "" || s.cfg.Password != "" {
		if err := authenticateSMTP(smtpClient, host, s.cfg.Username, s.cfg.Password); err != nil {
			return true, fmt.Errorf("SMTP AUTH failed: %w", err)
		}
	}
	if err := smtpClient.Mail(from); err != nil {
		return true, fmt.Errorf("mail from failed: %w", err)
	}
	if err := smtpClient.Rcpt(to); err != nil {
		return true, fmt.Errorf("rcpt to failed: %w", err)
	}
	writer, err := smtpClient.Data()
	if err != nil {
		return true, fmt.Errorf("data failed: %w", err)
	}
	if _, err := io.Copy(writer, bytes.NewReader(rawMessage)); err != nil {
		_ = writer.Close()
		return false, fmt.Errorf("write failed: %w", err)
	}
	if err := writer.Close(); err != nil {
		var rejection *textproto.Error
		return errors.As(err, &rejection) && rejection.Code >= 400 && rejection.Code < 600, fmt.Errorf("data close failed: %w", err)
	}
	_ = smtpClient.Quit()
	return false, nil
}

func authenticateSMTP(client *mailsmtp.Client, host, username, password string) error {
	supported, mechanisms := client.Extension("AUTH")
	if !supported {
		return errors.New("server does not support SMTP AUTH")
	}
	state, err := client.GetTLSConnectionState()
	if err != nil {
		return err
	}
	// Preserve go-mail's authentication preference order and mechanisms.
	for _, candidate := range []struct {
		name gomail.SMTPAuthType
		auth mailsmtp.Auth
	}{
		{gomail.SMTPAuthSCRAMSHA256PLUS, mailsmtp.ScramSHA256PlusAuth(username, password, state)},
		{gomail.SMTPAuthSCRAMSHA256, mailsmtp.ScramSHA256Auth(username, password)},
		{gomail.SMTPAuthSCRAMSHA1PLUS, mailsmtp.ScramSHA1PlusAuth(username, password, state)},
		{gomail.SMTPAuthSCRAMSHA1, mailsmtp.ScramSHA1Auth(username, password)},
		{gomail.SMTPAuthCramMD5, mailsmtp.CRAMMD5Auth(username, password)},
		{gomail.SMTPAuthPlain, mailsmtp.PlainAuth("", username, password, host, false)},
		{gomail.SMTPAuthLogin, mailsmtp.LoginAuth(username, password, host, false)},
	} {
		if slices.Contains(strings.Fields(mechanisms), string(candidate.name)) {
			return client.Auth(candidate.auth)
		}
	}
	return gomail.ErrNoSupportedAuthDiscovered
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
	if replyTo := strings.TrimSpace(message.ReplyTo); replyTo != "" {
		if err := msg.ReplyTo(replyTo); err != nil {
			return nil, err
		}
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
	for _, image := range message.InlineImages {
		contentType := strings.ToLower(strings.TrimSpace(image.ContentType))
		contentID := strings.Trim(strings.TrimSpace(firstLineValue(image.ContentID)), "<>")
		fileName := strings.TrimSpace(firstLineValue(image.FileName))
		if len(image.Content) == 0 || !strings.HasPrefix(contentType, "image/") || contentID == "" {
			continue
		}
		if fileName == "" {
			fileName = "image"
		}
		if err := msg.EmbedReader(
			fileName,
			bytes.NewReader(image.Content),
			gomail.WithFileContentType(gomail.ContentType(contentType)),
			gomail.WithFileContentID("<"+contentID+">"),
		); err != nil {
			return nil, err
		}
	}
	return msg, nil
}

func newSignedSMTPMessage(from string, to string, message domain.OutboundMessage, signer *DKIMSigner) ([]byte, error) {
	if len(message.RawMessage) > 0 {
		rawMessage := append([]byte(nil), message.RawMessage...)
		if signer == nil {
			return rawMessage, nil
		}
		return signer.Sign(rawMessage)
	}
	mailMessage, err := newSMTPMessage(from, to, message)
	if err != nil {
		return nil, err
	}
	rawMessage, err := renderSMTPMessageBytes(mailMessage)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return rawMessage, nil
	}
	return signer.Sign(rawMessage)
}

func renderSMTPMessageBytes(message *gomail.Msg) ([]byte, error) {
	var raw bytes.Buffer
	if _, err := message.WriteTo(&raw); err != nil {
		return nil, err
	}
	return raw.Bytes(), nil
}

func deliveryError(stage string, err error) error {
	if err == nil {
		return fmt.Errorf("%w: %s", domain.ErrDeliveryUnavailable, stage)
	}
	return fmt.Errorf("%w: %s: %s", domain.ErrDeliveryUnavailable, stage, safeDiagnostic(err.Error()))
}

func classifySMTPFailure(stage string, err error) error {
	var smtpError *textproto.Error
	if !errors.As(err, &smtpError) || smtpError == nil {
		return deliveryError(stage, err)
	}
	retryable := smtpError.Code >= 400 && smtpError.Code < 500
	message := "SMTP server rejected the message."
	if retryable {
		message = "SMTP server temporarily rejected the message."
	}
	contextMessage := strings.TrimSuffix(err.Error(), smtpError.Error())
	recipientStage := strings.Contains(contextMessage, "rcpt to failed:") || strings.Contains(contextMessage, "data failed:") || strings.Contains(contextMessage, "data close failed:")
	return &mailapp.OutboundSendFailure{
		SafeMessage: message,
		Retryable:   retryable,
		RecipientRejected: smtpError.Code >= 500 && smtpError.Code < 600 && recipientStage &&
			domain.IsRecipientRejectionStatus(smtpError.Msg),
		RecipientTemporary: retryable && recipientStage && domain.IsTemporaryRecipientStatus(smtpError.Msg),
		Cause:              deliveryError(stage, err),
	}
}

func permanentOutboundFailure(safeMessage string, cause error) error {
	return &mailapp.OutboundSendFailure{SafeMessage: safeMessage, Cause: cause}
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
