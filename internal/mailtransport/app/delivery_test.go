package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type memoryOutboundMailStore struct {
	mu    sync.Mutex
	mails map[string]*domain.OutboundMail
}

func newMemoryOutboundMailStore() *memoryOutboundMailStore {
	return &memoryOutboundMailStore{mails: make(map[string]*domain.OutboundMail)}
}

func (s *memoryOutboundMailStore) Reserve(_ context.Context, mail *domain.OutboundMail, _ time.Duration) (*domain.OutboundMail, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.mails[mail.IdempotencyKey]; ok {
		return cloneOutboundMail(existing), false, nil
	}
	s.mails[mail.IdempotencyKey] = cloneOutboundMail(mail)
	return cloneOutboundMail(mail), true, nil
}

func (s *memoryOutboundMailStore) Update(_ context.Context, mail *domain.OutboundMail, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.mails[mail.IdempotencyKey] = cloneOutboundMail(mail)
	return nil
}

func (s *memoryOutboundMailStore) get(idempotencyKey string) *domain.OutboundMail {
	s.mu.Lock()
	defer s.mu.Unlock()

	return cloneOutboundMail(s.mails[idempotencyKey])
}

type senderStub struct {
	err   error
	calls int
}

func (s *senderStub) Send(_ context.Context, _ domain.OutboundMessage) error {
	s.calls++
	return s.err
}

func TestDeliveryServiceMarksSentAndSkipsDeliveredDuplicate(t *testing.T) {
	store := newMemoryOutboundMailStore()
	sender := &senderStub{}
	service := NewDeliveryService(store, sender)
	service.now = fixedClock(time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC))

	msg := VerificationCodeMessage("user@example.com", "123456")
	require.NoError(t, service.Send(context.Background(), msg))

	record := store.get(msg.IdempotencyKey)
	require.NotNil(t, record)
	assert.Equal(t, domain.OutboundStatusSent, record.Status)
	assert.Equal(t, 1, record.Retries)
	assert.NotNil(t, record.SentAt)
	assert.Equal(t, 1, sender.calls)

	require.NoError(t, service.Send(context.Background(), msg))
	assert.Equal(t, 1, sender.calls)
}

func TestDeliveryServiceMarksFailedWithSafeDiagnostic(t *testing.T) {
	store := newMemoryOutboundMailStore()
	sender := &senderStub{err: errors.New("smtp auth failed\r\nsecret trailer")}
	service := NewDeliveryService(store, sender)
	service.now = fixedClock(time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC))

	msg := VerificationCodeMessage("user@example.com", "123456")
	err := service.Send(context.Background(), msg)
	require.Error(t, err)

	record := store.get(msg.IdempotencyKey)
	require.NotNil(t, record)
	assert.Equal(t, domain.OutboundStatusFailed, record.Status)
	assert.Equal(t, "smtp auth failedsecret trailer", record.FailureReason)
	assert.Equal(t, 1, record.Retries)
}

func TestDeliveryServiceDoesNotDuplicateInFlightMail(t *testing.T) {
	store := newMemoryOutboundMailStore()
	sender := &senderStub{}
	service := NewDeliveryService(store, sender)
	service.now = fixedClock(time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC))

	msg := VerificationCodeMessage("user@example.com", "123456")
	existing := domain.NewOutboundMail(msg, service.now())
	existing.MarkSending(service.now())
	require.NoError(t, store.Update(context.Background(), existing, outboundMailTTL))

	require.NoError(t, service.Send(context.Background(), msg))
	assert.Equal(t, 0, sender.calls)
}

func TestDeliveryServiceRetriesExpiredInFlightMail(t *testing.T) {
	store := newMemoryOutboundMailStore()
	sender := &senderStub{}
	service := NewDeliveryService(store, sender)
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	service.now = fixedClock(now)

	msg := VerificationCodeMessage("user@example.com", "123456")
	staleAt := now.Add(-outboundMailClaimTimeout - time.Second)
	existing := domain.NewOutboundMail(msg, staleAt)
	existing.MarkSending(staleAt)
	require.NoError(t, store.Update(context.Background(), existing, outboundMailTTL))

	require.NoError(t, service.Send(context.Background(), msg))
	record := store.get(msg.IdempotencyKey)
	require.NotNil(t, record)
	assert.Equal(t, domain.OutboundStatusSent, record.Status)
	assert.Equal(t, 2, record.Retries)
	assert.Equal(t, 1, sender.calls)
}

func TestDeliveryServiceRejectsUnknownStatus(t *testing.T) {
	store := newMemoryOutboundMailStore()
	sender := &senderStub{}
	service := NewDeliveryService(store, sender)
	service.now = fixedClock(time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC))

	msg := VerificationCodeMessage("user@example.com", "123456")
	existing := domain.NewOutboundMail(msg, service.now())
	existing.Status = domain.OutboundStatus("unknown")
	require.NoError(t, store.Update(context.Background(), existing, outboundMailTTL))

	err := service.Send(context.Background(), msg)
	require.ErrorIs(t, err, domain.ErrDeliveryUnavailable)
	assert.Equal(t, 0, sender.calls)
}

func cloneOutboundMail(mail *domain.OutboundMail) *domain.OutboundMail {
	if mail == nil {
		return nil
	}
	clone := *mail
	if mail.SentAt != nil {
		sentAt := *mail.SentAt
		clone.SentAt = &sentAt
	}
	return &clone
}

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}
