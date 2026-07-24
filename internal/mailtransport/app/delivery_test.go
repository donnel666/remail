package app

import (
	"context"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/mailtransport/domain"
)

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

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}
