package infra

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestAbuseLimiterThresholdsAndClear(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	limiter := NewAbuseLimiter(client)
	ctx := context.Background()

	for range captchaLimit {
		retry, err := limiter.HitCaptcha(ctx, "203.0.113.7")
		require.NoError(t, err)
		require.Zero(t, retry)
	}
	retry, err := limiter.HitCaptcha(ctx, "203.0.113.7")
	require.NoError(t, err)
	require.Equal(t, captchaWindow, retry)

	for range loginEmailLimit {
		retry, err = limiter.TakeLogin(ctx, " User@Test.COM ", "203.0.113.8")
		require.NoError(t, err)
		require.Zero(t, retry)
	}
	retry, err = limiter.TakeLogin(ctx, "user@test.com", "203.0.113.8")
	require.NoError(t, err)
	require.Equal(t, loginWindow, retry)
	require.NoError(t, limiter.CompleteLogin(ctx, "USER@TEST.COM", "203.0.113.8"))
	retry, err = limiter.TakeLogin(ctx, "user@test.com", "203.0.113.8")
	require.NoError(t, err)
	require.Zero(t, retry)
	require.NoError(t, limiter.CancelLogin(ctx, "user@test.com", "203.0.113.8"))

	for range emailCodeEmailLimit {
		retry, err = limiter.TakePasswordReset(ctx, "reset@test.com", "203.0.113.9")
		require.NoError(t, err)
		require.Zero(t, retry)
	}
	retry, err = limiter.TakePasswordReset(ctx, "RESET@test.com", "203.0.113.9")
	require.NoError(t, err)
	require.Equal(t, emailCodeWindow, retry)
	require.NoError(t, limiter.CompletePasswordReset(ctx, "reset@test.com", "203.0.113.9"))
	retry, err = limiter.TakePasswordReset(ctx, "reset@test.com", "203.0.113.9")
	require.NoError(t, err)
	require.Zero(t, retry)
	require.True(t, server.Exists(abuseEmailKey("email_code_email", "reset@test.com")))
	require.NoError(t, limiter.ClearEmailCodeFailures(ctx, "RESET@test.com"))
	require.False(t, server.Exists(abuseEmailKey("email_code_email", "reset@test.com")))

	for _, key := range server.Keys() {
		require.False(t, strings.Contains(key, "test.com"), key)
		require.False(t, strings.Contains(key, "203.0.113"), key)
	}
}

func TestAbuseLimiterPasswordResetLimitIsAtomic(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	limiter := NewAbuseLimiter(client)
	ctx := context.Background()

	const requests = 50
	results := make(chan int, requests)
	errs := make(chan error, requests)
	var wg sync.WaitGroup
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			retry, err := limiter.TakePasswordReset(ctx, "user@test.com", "203.0.113.10")
			errs <- err
			results <- retry
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}

	allowed := 0
	for retry := range results {
		if retry == 0 {
			allowed++
		}
	}
	require.Equal(t, emailCodeEmailLimit, allowed)
}

func TestAbuseLimiterRegistrationLimitIsAtomic(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	limiter := NewAbuseLimiter(client)
	ctx := context.Background()

	const requests = 50
	results := make(chan int, requests)
	errs := make(chan error, requests)
	var wg sync.WaitGroup
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			retry, err := limiter.TakeRegistration(ctx, "user@test.com", "203.0.113.11")
			errs <- err
			results <- retry
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	allowed := 0
	for retry := range results {
		if retry == 0 {
			allowed++
		}
	}
	require.Equal(t, emailCodeEmailLimit, allowed)
}

func TestAbuseLimiterSharesVerificationBudgetAcrossRegistrationAndReset(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	limiter := NewAbuseLimiter(client)
	ctx := context.Background()

	for i := range emailCodeEmailLimit {
		var retry int
		var err error
		if i%2 == 0 {
			retry, err = limiter.TakeRegistration(ctx, "victim@test.com", "203.0.113.12")
		} else {
			retry, err = limiter.TakePasswordReset(ctx, "victim@test.com", "203.0.113.12")
		}
		require.NoError(t, err)
		require.Zero(t, retry)
	}
	retry, err := limiter.TakeRegistration(ctx, "victim@test.com", "203.0.113.12")
	require.NoError(t, err)
	require.Equal(t, emailCodeWindow, retry)
}
