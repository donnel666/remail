package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/donnel666/remail/internal/iam/domain"
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

type emailCodeSenderStub struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (s *emailCodeSenderStub) SendEmailCode(_ context.Context, _, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.err
}

func (s *emailCodeSenderStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestEmailCodeUseCaseSendDoesNotResendExistingCode(t *testing.T) {
	store := newEmailCodeStoreStub()
	sender := &emailCodeSenderStub{}
	uc := NewEmailCodeUseCase(store, sender)

	require.NoError(t, uc.Send(context.Background(), "User@Test.COM"))
	require.NoError(t, uc.Send(context.Background(), "user@test.com"))

	require.Equal(t, 1, sender.callCount())
}

func TestEmailCodeUseCaseSendDeletesCodeWhenDeliveryFails(t *testing.T) {
	store := newEmailCodeStoreStub()
	sender := &emailCodeSenderStub{err: domain.ErrMailServiceUnavailable}
	uc := NewEmailCodeUseCase(store, sender)

	err := uc.Send(context.Background(), "user@test.com")
	require.Error(t, err)
	require.True(t, errors.Is(err, domain.ErrMailServiceUnavailable))

	code, getErr := store.Get(context.Background(), emailCodeKey("user@test.com"))
	require.NoError(t, getErr)
	require.Empty(t, code)
}
