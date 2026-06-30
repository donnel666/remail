package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/stretchr/testify/require"
)

type emailCodeStoreStub struct {
	mu    sync.Mutex
	codes map[string]string
}

func newEmailCodeStoreStub() *emailCodeStoreStub {
	return &emailCodeStoreStub{codes: make(map[string]string)}
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

func TestEmailCodeUseCaseSendDoesNotResendExistingCode(t *testing.T) {
	store := newEmailCodeStoreStub()
	sender := &mailDeliveryStub{}
	uc := NewEmailCodeUseCase(store, sender, nil)

	require.NoError(t, uc.Send(context.Background(), "User@Test.COM"))
	require.NoError(t, uc.Send(context.Background(), "user@test.com"))

	require.Equal(t, 1, sender.callCount())
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
}
