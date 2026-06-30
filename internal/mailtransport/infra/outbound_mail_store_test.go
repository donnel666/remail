package infra

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutboundMailStoreReserveAndUpdate(t *testing.T) {
	store := newTestOutboundMailStore(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	mail := domain.NewOutboundMail(domain.OutboundMessage{
		IdempotencyKey: "mail-1",
		Purpose:        domain.PurposeVerificationCode,
		To:             "user@example.com",
		Subject:        "ReMail 邮箱验证码",
	}, now)

	reserved, created, err := store.Reserve(ctx, mail, time.Hour)
	require.NoError(t, err)
	require.True(t, created)
	assert.Equal(t, domain.OutboundStatusPending, reserved.Status)

	reserved.MarkSent(now.Add(time.Second))
	require.NoError(t, store.Update(ctx, reserved, time.Hour))

	duplicate, created, err := store.Reserve(ctx, mail, time.Hour)
	require.NoError(t, err)
	require.False(t, created)
	assert.Equal(t, domain.OutboundStatusSent, duplicate.Status)
	assert.NotNil(t, duplicate.SentAt)
}

func TestOutboundMailStoreReturnsRedisErrors(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{
		Addr:        server.Addr(),
		DialTimeout: 20 * time.Millisecond,
		ReadTimeout: 20 * time.Millisecond,
	})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })

	store := NewOutboundMailStore(rdb)
	server.Close()

	_, _, err := store.Reserve(context.Background(), domain.NewOutboundMail(domain.OutboundMessage{
		IdempotencyKey: "mail-1",
		Purpose:        domain.PurposeVerificationCode,
		To:             "user@example.com",
		Subject:        "ReMail 邮箱验证码",
	}, time.Now()), time.Hour)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "redis outbound mail reserve")
}

func newTestOutboundMailStore(t *testing.T) *OutboundMailStore {
	t.Helper()

	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	return NewOutboundMailStore(rdb)
}
