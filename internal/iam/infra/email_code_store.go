package infra

import (
	"context"
	"crypto/tls"
	"fmt"
	"html"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/redis/go-redis/v9"
)

const (
	emailCodeKeyPrefix = "email_code:"
	emailCodeBoundary  = "remail-email-code-boundary"
)

// EmailCodeStore stores email verification codes in Redis.
type EmailCodeStore struct {
	rdb redis.UniversalClient
}

// NewEmailCodeStore creates a Redis-backed email code store.
func NewEmailCodeStore(rdb redis.UniversalClient) *EmailCodeStore {
	return &EmailCodeStore{rdb: rdb}
}

func emailCodeRedisKey(key string) string {
	return emailCodeKeyPrefix + key
}

func (s *EmailCodeStore) CreateIfAbsent(ctx context.Context, key, code string, ttlSeconds int) (string, bool, error) {
	redisKey := emailCodeRedisKey(key)
	created, err := s.rdb.SetNX(ctx, redisKey, code, time.Duration(ttlSeconds)*time.Second).Result()
	if err != nil {
		return "", false, fmt.Errorf("redis email code setnx: %w", err)
	}
	if created {
		return code, false, nil
	}

	existing, err := s.Get(ctx, key)
	if err != nil {
		return "", false, err
	}
	if existing == "" {
		return "", false, fmt.Errorf("email code disappeared during idempotent send")
	}
	return existing, true, nil
}

func (s *EmailCodeStore) Get(ctx context.Context, key string) (string, error) {
	val, err := s.rdb.Get(ctx, emailCodeRedisKey(key)).Result()
	if err != nil {
		if err == redis.Nil {
			return "", nil
		}
		return "", fmt.Errorf("redis email code get: %w", err)
	}
	return val, nil
}

func (s *EmailCodeStore) Delete(ctx context.Context, key string) error {
	return s.rdb.Del(ctx, emailCodeRedisKey(key)).Err()
}

// EmailCodeSenderConfig holds SMTP settings for verification-code delivery.
type EmailCodeSenderConfig struct {
	Addr     string
	Username string
	Password string
	From     string
}

// EmailCodeSender sends verification codes through SMTP. It never logs or
// returns verification codes; delivery failures are surfaced as typed errors.
type EmailCodeSender struct {
	cfg EmailCodeSenderConfig
}

// NewEmailCodeSender creates an SMTP-backed email code sender.
func NewEmailCodeSender(cfg EmailCodeSenderConfig) *EmailCodeSender {
	return &EmailCodeSender{cfg: cfg}
}

func (s *EmailCodeSender) SendEmailCode(ctx context.Context, email, code string) error {
	from := strings.TrimSpace(s.cfg.From)
	if from == "" {
		from = strings.TrimSpace(s.cfg.Username)
	}
	if strings.TrimSpace(s.cfg.Addr) == "" || from == "" {
		return domain.ErrMailServiceUnavailable
	}

	host, _, err := net.SplitHostPort(s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("%w: invalid smtp addr", domain.ErrMailServiceUnavailable)
	}

	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("%w: smtp dial failed", domain.ErrMailServiceUnavailable)
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("%w: smtp client failed", domain.ErrMailServiceUnavailable)
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}); err != nil {
			return fmt.Errorf("%w: smtp starttls failed", domain.ErrMailServiceUnavailable)
		}
	}

	if s.cfg.Username != "" || s.cfg.Password != "" {
		auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("%w: smtp auth failed", domain.ErrMailServiceUnavailable)
		}
	}

	if err := client.Mail(from); err != nil {
		return fmt.Errorf("%w: smtp from rejected", domain.ErrMailServiceUnavailable)
	}
	if err := client.Rcpt(email); err != nil {
		return fmt.Errorf("%w: smtp recipient rejected", domain.ErrMailServiceUnavailable)
	}

	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("%w: smtp data failed", domain.ErrMailServiceUnavailable)
	}
	if _, err := writer.Write([]byte(emailCodeMessage(from, email, code))); err != nil {
		_ = writer.Close()
		return fmt.Errorf("%w: smtp write failed", domain.ErrMailServiceUnavailable)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("%w: smtp close data failed", domain.ErrMailServiceUnavailable)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("%w: smtp quit failed", domain.ErrMailServiceUnavailable)
	}
	return nil
}

func emailCodeMessage(from, to, code string) string {
	code = emailBodyValue(code)
	plainBody := emailCodePlainText(code)
	htmlBody := emailCodeHTML(code)

	return fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: ReMail verification code\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=%q\r\n\r\n--%s\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 7bit\r\n\r\n%s\r\n--%s\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: 7bit\r\n\r\n%s\r\n--%s--\r\n",
		smtpHeaderValue(from),
		smtpHeaderValue(to),
		emailCodeBoundary,
		emailCodeBoundary,
		plainBody,
		emailCodeBoundary,
		htmlBody,
		emailCodeBoundary,
	)
}

func emailCodePlainText(code string) string {
	return fmt.Sprintf("Your ReMail verification code is: %s\r\nIt expires in 10 minutes.\r\nIf you did not request this code, you can ignore this email.\r\n", code)
}

func emailCodeHTML(code string) string {
	code = html.EscapeString(code)
	return fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>ReMail verification code</title>
</head>
<body style="margin:0;background:#f6f7f9;color:#111827;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Arial,sans-serif;">
  <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background:#f6f7f9;margin:0;padding:32px 16px;">
    <tr>
      <td align="center">
        <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="width:100%%;max-width:520px;background:#ffffff;border:1px solid #e5e7eb;border-radius:8px;overflow:hidden;">
          <tr>
            <td style="height:4px;background:#ff6a3d;"></td>
          </tr>
          <tr>
            <td style="padding:32px 32px 28px;">
              <div style="font-size:14px;line-height:20px;font-weight:700;color:#ff6a3d;margin:0 0 18px;">ReMail</div>
              <h1 style="font-size:22px;line-height:30px;font-weight:700;color:#111827;margin:0 0 10px;">Verification code</h1>
              <p style="font-size:15px;line-height:24px;color:#4b5563;margin:0 0 24px;">Use this code to continue signing in to ReMail.</p>
              <div style="font-size:32px;line-height:40px;font-weight:700;color:#111827;background:#f9fafb;border:1px solid #e5e7eb;border-radius:8px;padding:18px 20px;text-align:center;margin:0 0 20px;">%s</div>
              <p style="font-size:14px;line-height:22px;color:#6b7280;margin:0;">This code expires in 10 minutes. If you did not request it, you can ignore this email.</p>
            </td>
          </tr>
        </table>
      </td>
    </tr>
  </table>
</body>
</html>`, code)
}

func smtpHeaderValue(value string) string {
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "")
	return strings.TrimSpace(value)
}

func emailBodyValue(value string) string {
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "")
	return strings.TrimSpace(value)
}
