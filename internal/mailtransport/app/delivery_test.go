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
	mu     sync.Mutex
	nextID uint
	mails  map[string]*domain.OutboundMail
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

func (s *memoryOutboundMailStore) ClaimSending(_ context.Context, idempotencyKey string, staleBefore time.Time, now time.Time) (*domain.OutboundMail, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing := s.mails[idempotencyKey]
	if existing == nil {
		return nil, false, nil
	}
	canClaim := existing.Status == domain.OutboundStatusPending ||
		(existing.Status == domain.OutboundStatusSending && existing.UpdatedAt.Before(staleBefore))
	if !canClaim {
		return nil, false, nil
	}
	next := cloneOutboundMail(existing)
	next.MarkSending(now)
	s.mails[idempotencyKey] = next
	return cloneOutboundMail(next), true, nil
}

func (s *memoryOutboundMailStore) ClaimDispatchable(_ context.Context, limit int, staleBefore time.Time) ([]domain.OutboundMail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 {
		limit = 100
	}
	var mails []domain.OutboundMail
	for _, mail := range s.mails {
		if mail.Status == domain.OutboundStatusPending ||
			(mail.Status == domain.OutboundStatusSending && mail.UpdatedAt.Before(staleBefore)) {
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

func (s *memoryOutboundMailStore) MarkPending(_ context.Context, idempotencyKey string, reason string) error {
	return s.update(idempotencyKey, func(mail *domain.OutboundMail) {
		mail.MarkPending(time.Now().UTC(), reason)
	})
}

func (s *memoryOutboundMailStore) MarkSent(_ context.Context, idempotencyKey string, now time.Time) error {
	return s.update(idempotencyKey, func(mail *domain.OutboundMail) {
		mail.MarkSent(now)
	})
}

func (s *memoryOutboundMailStore) MarkFailed(_ context.Context, idempotencyKey string, reason string) error {
	return s.update(idempotencyKey, func(mail *domain.OutboundMail) {
		mail.MarkFailed(time.Now().UTC(), reason)
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

func (s *memoryOutboundMailStore) update(idempotencyKey string, apply func(*domain.OutboundMail)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	mail := s.mails[idempotencyKey]
	if mail == nil {
		return errors.New("not found")
	}
	next := cloneOutboundMail(mail)
	apply(next)
	s.mails[idempotencyKey] = next
	return nil
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
