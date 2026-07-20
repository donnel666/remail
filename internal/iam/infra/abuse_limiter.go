package infra

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
)

const (
	turnstileLimit      = 30
	turnstileWindow     = 60
	loginEmailLimit     = 10
	loginIPLimit        = 60
	loginWindow         = 15 * 60
	emailCodeEmailLimit = 5
	emailCodeIPLimit    = 30
	emailCodeWindow     = 10 * 60
	abuseKeyPrefix      = "iam_abuse:"
)

var abuseHitScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('EXPIRE', KEYS[1], ARGV[2])
end
if count <= tonumber(ARGV[1]) then
  return 0
end
local ttl = redis.call('TTL', KEYS[1])
if ttl < 1 then return 1 end
return ttl
`)

var abuseTakeScript = redis.NewScript(`
local retry = 0
for i, key in ipairs(KEYS) do
  local count = tonumber(redis.call('GET', key) or '0')
  if count >= tonumber(ARGV[i]) then
    local ttl = redis.call('TTL', key)
    if ttl < 1 then ttl = 1 end
    if ttl > retry then retry = ttl end
  end
end
if retry > 0 then return retry end
for _, key in ipairs(KEYS) do
  local count = redis.call('INCR', key)
  if count == 1 then
	redis.call('EXPIRE', key, ARGV[#ARGV])
  end
end
return 0
`)

var abuseCancelScript = redis.NewScript(`
for _, key in ipairs(KEYS) do
  local count = tonumber(redis.call('GET', key) or '0')
  if count <= 1 then
	redis.call('DEL', key)
  else
	redis.call('DECR', key)
  end
end
return 1
`)

var abuseCompleteScript = redis.NewScript(`
redis.call('DEL', KEYS[1])
local count = tonumber(redis.call('GET', KEYS[2]) or '0')
if count <= 1 then
  redis.call('DEL', KEYS[2])
else
  redis.call('DECR', KEYS[2])
end
return 1
`)

// AbuseLimiter stores fixed-window authentication failure counters in Redis.
type AbuseLimiter struct {
	rdb redis.UniversalClient
}

func NewAbuseLimiter(rdb redis.UniversalClient) *AbuseLimiter {
	return &AbuseLimiter{rdb: rdb}
}

// HitTurnstile limits outbound Siteverify calls by client IP.
func (l *AbuseLimiter) HitTurnstile(ctx context.Context, ip string) (int, error) {
	retry, err := abuseHitScript.Run(ctx, l.rdb, []string{abuseIPKey("turnstile", ip)}, turnstileLimit, turnstileWindow).Int()
	if err != nil {
		return 0, fmt.Errorf("redis turnstile abuse limit: %w", err)
	}
	return retry, nil
}

func (l *AbuseLimiter) TakeLogin(ctx context.Context, email, ip string) (int, error) {
	return l.take(ctx,
		[]string{abuseEmailKey("login_email", email), abuseIPKey("login_ip", ip)},
		[]any{loginEmailLimit, loginIPLimit, loginWindow},
		"login",
	)
}

func (l *AbuseLimiter) CancelLogin(ctx context.Context, email, ip string) error {
	return l.cancel(ctx, []string{abuseEmailKey("login_email", email), abuseIPKey("login_ip", ip)}, "login")
}

func (l *AbuseLimiter) CompleteLogin(ctx context.Context, email, ip string) error {
	return l.complete(ctx, abuseEmailKey("login_email", email), abuseIPKey("login_ip", ip), "login")
}

func (l *AbuseLimiter) TakePasswordReset(ctx context.Context, email, ip string) (int, error) {
	return l.take(ctx,
		[]string{abuseEmailKey("email_code_email", email), abuseIPKey("email_code_ip", ip)},
		[]any{emailCodeEmailLimit, emailCodeIPLimit, emailCodeWindow},
		"password reset",
	)
}

func (l *AbuseLimiter) TakeRegistration(ctx context.Context, email, ip string) (int, error) {
	return l.take(ctx,
		[]string{abuseEmailKey("email_code_email", email), abuseIPKey("email_code_ip", ip)},
		[]any{emailCodeEmailLimit, emailCodeIPLimit, emailCodeWindow},
		"registration",
	)
}

func (l *AbuseLimiter) CancelRegistration(ctx context.Context, email, ip string) error {
	return l.cancel(ctx, []string{abuseEmailKey("email_code_email", email), abuseIPKey("email_code_ip", ip)}, "registration")
}

func (l *AbuseLimiter) CompleteRegistration(ctx context.Context, email, ip string) error {
	return l.complete(ctx, abuseEmailKey("email_code_email", email), abuseIPKey("email_code_ip", ip), "registration")
}

func (l *AbuseLimiter) CancelPasswordReset(ctx context.Context, email, ip string) error {
	return l.cancel(ctx, []string{abuseEmailKey("email_code_email", email), abuseIPKey("email_code_ip", ip)}, "password reset")
}

func (l *AbuseLimiter) CompletePasswordReset(ctx context.Context, email, ip string) error {
	return l.complete(ctx, abuseEmailKey("email_code_email", email), abuseIPKey("email_code_ip", ip), "password reset")
}

func (l *AbuseLimiter) ClearEmailCodeFailures(ctx context.Context, email string) error {
	if err := l.rdb.Del(ctx, abuseEmailKey("email_code_email", email)).Err(); err != nil {
		return fmt.Errorf("redis email code abuse clear email: %w", err)
	}
	return nil
}

func (l *AbuseLimiter) take(ctx context.Context, keys []string, args []any, operation string) (int, error) {
	retry, err := abuseTakeScript.Run(ctx, l.rdb, keys, args...).Int()
	if err != nil {
		return 0, fmt.Errorf("redis %s abuse take: %w", operation, err)
	}
	return retry, nil
}

func (l *AbuseLimiter) cancel(ctx context.Context, keys []string, operation string) error {
	if err := abuseCancelScript.Run(ctx, l.rdb, keys).Err(); err != nil {
		return fmt.Errorf("redis %s abuse cancel: %w", operation, err)
	}
	return nil
}

func (l *AbuseLimiter) complete(ctx context.Context, emailKey, ipKey, operation string) error {
	if err := abuseCompleteScript.Run(ctx, l.rdb, []string{emailKey, ipKey}).Err(); err != nil {
		return fmt.Errorf("redis %s abuse complete: %w", operation, err)
	}
	return nil
}

func abuseEmailKey(scope, email string) string {
	return abuseKey(scope, strings.ToLower(strings.TrimSpace(email)))
}

func abuseIPKey(scope, ip string) string {
	return abuseKey(scope, strings.TrimSpace(ip))
}

func abuseKey(scope, value string) string {
	sum := sha256.Sum256([]byte(value))
	return abuseKeyPrefix + scope + ":" + hex.EncodeToString(sum[:])
}
