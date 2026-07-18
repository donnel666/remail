package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/donnel666/remail/internal/iam/domain"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/stretchr/testify/require"
)

type emailCodeStoreStub struct {
	mu        sync.Mutex
	codes     map[string]string
	claims    map[string]string
	cooldowns map[string]bool
}

func newEmailCodeStoreStub() *emailCodeStoreStub {
	return &emailCodeStoreStub{codes: make(map[string]string), claims: make(map[string]string), cooldowns: make(map[string]bool)}
}

func (s *emailCodeStoreStub) StartCooldown(_ context.Context, key string, seconds int) (bool, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cooldowns[key] {
		return false, seconds, nil
	}
	s.cooldowns[key] = true
	return true, 0, nil
}

func (s *emailCodeStoreStub) ClearCooldown(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cooldowns, key)
	return nil
}

func (s *emailCodeStoreStub) CreateIfAbsent(_ context.Context, key, code string, _ int) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.codes[key]; ok {
		return existing, true, nil
	}
	s.codes[key] = code
	return code, false, nil
}

func (s *emailCodeStoreStub) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.codes[key], nil
}

func (s *emailCodeStoreStub) Claim(_ context.Context, key, expected, claimToken string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.codes[key] != expected {
		return false, nil
	}
	delete(s.codes, key)
	s.claims[key] = claimToken
	return true, nil
}

func (s *emailCodeStoreStub) Commit(_ context.Context, key, claimToken string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claims[key] != claimToken {
		return false, nil
	}
	delete(s.claims, key)
	return true, nil
}

func (s *emailCodeStoreStub) Restore(_ context.Context, key, claimToken, code string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claims[key] != claimToken {
		return false, nil
	}
	delete(s.claims, key)
	s.codes[key] = code
	return true, nil
}

func (s *emailCodeStoreStub) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.codes, key)
	return nil
}

type mailDeliveryStub struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (s *mailDeliveryStub) Send(_ context.Context, _ maildomain.OutboundMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.err
}

func (s *mailDeliveryStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestEmailCodeUseCaseSendThrottlesWithinCooldown(t *testing.T) {
	store := newEmailCodeStoreStub()
	sender := &mailDeliveryStub{}
	uc := NewEmailCodeUseCase(store, sender, nil)

	require.NoError(t, uc.Send(context.Background(), "User@Test.COM"))

	// A second send during the cooldown is rejected, not silently dropped, and
	// carries the remaining cooldown for the Retry-After header.
	err := uc.Send(context.Background(), "user@test.com")
	require.ErrorIs(t, err, domain.ErrEmailCodeThrottled)
	var throttled *domain.EmailCodeThrottledError
	require.True(t, errors.As(err, &throttled))
	require.Equal(t, emailCodeResendGap, throttled.RetryAfterSeconds)
	require.Equal(t, 1, sender.callCount())
}

func TestEmailCodeUseCaseResendsSameCodeAfterCooldown(t *testing.T) {
	store := newEmailCodeStoreStub()
	sender := &mailDeliveryStub{}
	uc := NewEmailCodeUseCase(store, sender, nil)

	require.NoError(t, uc.Send(context.Background(), "user@test.com"))
	first, err := store.Get(context.Background(), emailCodeKey("user@test.com"))
	require.NoError(t, err)
	require.NotEmpty(t, first)

	// Simulate cooldown expiry (the stub ignores TTLs).
	delete(store.cooldowns, emailCodeKey("user@test.com"))

	require.NoError(t, uc.Send(context.Background(), "user@test.com"))
	require.Equal(t, 2, sender.callCount())

	second, err := store.Get(context.Background(), emailCodeKey("user@test.com"))
	require.NoError(t, err)
	require.Equal(t, first, second) // the still-valid code is re-delivered
}

func TestEmailCodeUseCaseSendDeletesCodeWhenDeliveryFails(t *testing.T) {
	store := newEmailCodeStoreStub()
	sender := &mailDeliveryStub{err: maildomain.ErrDeliveryUnavailable}
	uc := NewEmailCodeUseCase(store, sender, nil)

	err := uc.Send(context.Background(), "user@test.com")
	require.Error(t, err)
	require.True(t, errors.Is(err, maildomain.ErrDeliveryUnavailable))

	code, getErr := store.Get(context.Background(), emailCodeKey("user@test.com"))
	require.NoError(t, getErr)
	require.Empty(t, code)

	// The cooldown is released so the user can retry immediately after a failure.
	require.False(t, store.cooldowns[emailCodeKey("user@test.com")])
}

func TestEmailCodeUseCaseFailedResendKeepsExistingCode(t *testing.T) {
	store := newEmailCodeStoreStub()
	sender := &mailDeliveryStub{}
	uc := NewEmailCodeUseCase(store, sender, nil)
	require.NoError(t, uc.Send(context.Background(), "user@test.com"))
	code, err := store.Get(context.Background(), emailCodeKey("user@test.com"))
	require.NoError(t, err)
	require.NotEmpty(t, code)

	delete(store.cooldowns, emailCodeKey("user@test.com"))
	sender.err = maildomain.ErrDeliveryUnavailable
	require.Error(t, uc.Send(context.Background(), "user@test.com"))

	stored, err := store.Get(context.Background(), emailCodeKey("user@test.com"))
	require.NoError(t, err)
	require.Equal(t, code, stored)
}
