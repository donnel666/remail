package api

import (
	"context"
	"testing"
	"time"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/stretchr/testify/require"
)

type gmailMatchStub struct {
	orderNo    string
	code       string
	receivedAt time.Time
	calls      int
}

func (s *gmailMatchStub) RecordMatchedCode(_ context.Context, orderNo, code string, receivedAt time.Time) error {
	s.calls++
	s.orderNo = orderNo
	s.code = code
	s.receivedAt = receivedAt
	return nil
}

func TestMatchResultAdapterRoutesGmailCode(t *testing.T) {
	matchedAt := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	adapter := &matchResultAdapter{}
	port := &gmailMatchStub{}
	module := &Module{matchResults: adapter}
	module.SetGmailMatchPort(port)

	err := adapter.NotifyMatchedCode(context.Background(), mailmatchapp.MatchResult{
		OrderNo: "OR_GMAIL_EMPTY", ResourceType: domain.ResourceTypeGmail,
		ServiceMode: "code", VerificationCode: "  ", MatchedAt: matchedAt,
	})
	require.NoError(t, err)
	require.Zero(t, port.calls, "mail without an extracted code must not block the Gmail polling cursor")

	err = adapter.NotifyMatchedCode(context.Background(), mailmatchapp.MatchResult{
		OrderNo:          "OR_GMAIL_MATCH",
		ResourceType:     domain.ResourceTypeGmail,
		ServiceMode:      "code",
		VerificationCode: "123456",
		MatchedAt:        matchedAt,
	})

	require.NoError(t, err)
	require.Equal(t, 1, port.calls)
	require.Equal(t, "OR_GMAIL_MATCH", port.orderNo)
	require.Equal(t, "123456", port.code)
	require.Equal(t, matchedAt, port.receivedAt)

	err = adapter.NotifyMatchedCode(context.Background(), mailmatchapp.MatchResult{
		OrderNo: "OR_GMAIL_PURCHASE", ResourceType: domain.ResourceTypeGmail,
		ServiceMode: "purchase", VerificationCode: "654321", MatchedAt: matchedAt,
	})
	require.NoError(t, err)
	require.Equal(t, 1, port.calls, "purchase mail must not call the code-session callback")
}
