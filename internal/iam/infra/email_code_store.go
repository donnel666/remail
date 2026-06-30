package infra

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/redis/go-redis/v9"
)

const emailCodeKeyPrefix = "email_code:"

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
	return fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: ReMail verification code\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nYour ReMail verification code is: %s\r\nIt expires in 10 minutes.\r\n",
		smtpHeaderValue(from),
		smtpHeaderValue(to),
		code,
	)
}

func smtpHeaderValue(value string) string {
	value = strings.ReplaceAll(value, "\r", "")
	return strings.ReplaceAll(value, "\n", "")
}
