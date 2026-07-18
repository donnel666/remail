package app

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/mailtransport/domain"
)

type memoryOutboundMailStore struct {
	mu           sync.Mutex
	nextID       uint
	mails        map[string]*domain.OutboundMail
	activateRace bool
	findErr      error
	recordErr    error
	markSentErr  error
}

func newMemoryOutboundMailStore() *memoryOutboundMailStore {
	return &memoryOutboundMailStore{nextID: 1, mails: make(map[string]*domain.OutboundMail)}
}

func (s *memoryOutboundMailStore) Reserve(_ context.Context, mail *domain.OutboundMail) (*domain.OutboundMail, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.mails[mail.IdempotencyKey]; ok {
		if existing.RequestHash != mail.RequestHash {
			return nil, false, domain.ErrOutboundIdempotencyConflict
		}
		return cloneOutboundMail(existing), false, nil
	}
	next := cloneOutboundMail(mail)
	next.ID = s.nextID
	s.nextID++
	s.mails[next.IdempotencyKey] = next
	return cloneOutboundMail(next), true, nil
}

func (s *memoryOutboundMailStore) FindByIdempotencyKey(_ context.Context, idempotencyKey string) (*domain.OutboundMail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findErr != nil {
		return nil, s.findErr
	}
	return cloneOutboundMail(s.mails[idempotencyKey]), nil
}

func (s *memoryOutboundMailStore) ListPending(_ context.Context, limit int) ([]domain.OutboundMail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 {
		limit = 100
	}
	var mails []domain.OutboundMail
	for _, mail := range s.mails {
		if mail.Status == domain.OutboundStatusPending {
			mails = append(mails, *cloneOutboundMail(mail))
		}
	}
	sort.SliceStable(mails, func(i, j int) bool {
		if mails[i].CreatedAt.Equal(mails[j].CreatedAt) {
			return mails[i].ID < mails[j].ID
		}
		return mails[i].CreatedAt.Before(mails[j].CreatedAt)
	})
	if len(mails) > limit {
		mails = mails[:limit]
	}
	return mails, nil
}

func (s *memoryOutboundMailStore) ActivateSending(_ context.Context, idempotencyKey string, generation uint64, now time.Time) (bool, error) {
	applied, err := s.updateIf(idempotencyKey, generation, domain.OutboundStatusPending, func(mail *domain.OutboundMail) {
		mail.MarkSending(now)
	})
	if applied && s.activateRace {
		s.activateRace = false
		return false, err
	}
	return applied, err
}

func (s *memoryOutboundMailStore) ReleasePending(_ context.Context, idempotencyKey string, generation uint64, reason string) (bool, error) {
	return s.updateIf(idempotencyKey, generation, domain.OutboundStatusSending, func(mail *domain.OutboundMail) {
		mail.MarkPending(time.Now().UTC(), reason)
	})
}

func (s *memoryOutboundMailStore) ResetPending(_ context.Context, idempotencyKey string, generation uint64, reason string) (bool, error) {
	return s.updateIf(idempotencyKey, generation, domain.OutboundStatusFailed, func(mail *domain.OutboundMail) {
		mail.ResetForRetry(time.Now().UTC(), reason)
	})
}

func (s *memoryOutboundMailStore) RecordSendFailure(_ context.Context, idempotencyKey string, generation uint64, reason string, retryable bool) (bool, bool, error) {
	if s.recordErr != nil {
		return false, false, s.recordErr
	}
	terminal := false
	applied, err := s.updateIf(idempotencyKey, generation, domain.OutboundStatusSending, func(mail *domain.OutboundMail) {
		terminal = mail.RecordSendFailure(time.Now().UTC(), reason, retryable)
	})
	return terminal, applied, err
}

func (s *memoryOutboundMailStore) MarkSent(_ context.Context, idempotencyKey string, generation uint64, now time.Time) (bool, error) {
	if s.markSentErr != nil {
		return false, s.markSentErr
	}
	return s.updateIf(idempotencyKey, generation, domain.OutboundStatusSending, func(mail *domain.OutboundMail) {
		mail.MarkSent(now)
	})
}

func (s *memoryOutboundMailStore) put(mail *domain.OutboundMail) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mail.ID == 0 {
		mail.ID = s.nextID
		s.nextID++
	}
	s.mails[mail.IdempotencyKey] = cloneOutboundMail(mail)
}

func (s *memoryOutboundMailStore) get(idempotencyKey string) *domain.OutboundMail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneOutboundMail(s.mails[idempotencyKey])
}

func (s *memoryOutboundMailStore) updateIf(idempotencyKey string, generation uint64, status domain.OutboundStatus, apply func(*domain.OutboundMail)) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	mail := s.mails[idempotencyKey]
	if mail == nil {
		return false, errors.New("not found")
	}
	if mail.SendGeneration != generation || mail.Status != status {
		return false, nil
	}
	next := cloneOutboundMail(mail)
	apply(next)
	s.mails[idempotencyKey] = next
	return true, nil
}

type senderStub struct {
	err      error
	calls    int
	messages []domain.OutboundMessage
}

func (s *senderStub) Send(_ context.Context, message domain.OutboundMessage) error {
	s.calls++
	s.messages = append(s.messages, message)
	return s.err
}

type systemLogStub struct {
	logs []governancedomain.SystemLog
}

func (s *systemLogStub) Create(_ context.Context, log *governancedomain.SystemLog) error {
	s.logs = append(s.logs, *log)
	return nil
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
