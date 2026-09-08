package app

import (
	"context"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/stretchr/testify/require"
)

type protoFetchFunc func(context.Context, FetchMessagesRequest) (*FetchMessagesResult, error)

type protoPermanentFailureFunc func(context.Context, PermanentProtoFetchFailure) error

func (f protoPermanentFailureFunc) HandlePermanentProtoFetchFailure(ctx context.Context, failure PermanentProtoFetchFailure) error {
	return f(ctx, failure)
}

func (f protoFetchFunc) FetchProtoMessages(ctx context.Context, req FetchMessagesRequest) (*FetchMessagesResult, error) {
	return f(ctx, req)
}

func TestProtoFetchIsIsolatedAndFailsClosed(t *testing.T) {
	legacy := &incrementalFetchTransportStub{}
	uc := NewUseCase(nil, nil, legacy, nil)
	scope := OrderScope{AllocationType: domain.ResourceTypeProto, EmailResourceID: 91,
		CredentialRevision: 7, Recipient: "one@example.com", OrderNo: "PROTO-1"}
	_, err := uc.fetchMessages(context.Background(), scope, domain.FetchJob{}, nil)
	require.ErrorIs(t, err, domain.ErrMailServiceUnavailable)
	calls := 0
	uc.SetProtoMailFetchPort(protoFetchFunc(func(_ context.Context, req FetchMessagesRequest) (*FetchMessagesResult, error) {
		calls++
		require.Equal(t, uint(91), req.Scope.EmailResourceID)
		require.Equal(t, uint64(7), req.Scope.CredentialRevision)
		return &FetchMessagesResult{Messages: []FetchedMessage{{
			EmailResourceID: 91, ResourceType: domain.ResourceTypeProto, Recipient: "one@example.com",
		}}}, nil
	}))
	result, err := uc.fetchMessages(context.Background(), scope, domain.FetchJob{}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, uint64(7), result.Messages[0].CredentialRevision)
	require.Empty(t, legacy.request.Scope.OrderNo)
	uc.SetProtoMailFetchPort(protoFetchFunc(func(context.Context, FetchMessagesRequest) (*FetchMessagesResult, error) {
		return &FetchMessagesResult{Messages: []FetchedMessage{{EmailResourceID: 92, ResourceType: domain.ResourceTypeProto}}}, nil
	}))
	_, err = uc.fetchMessages(context.Background(), scope, domain.FetchJob{}, nil)
	require.ErrorIs(t, err, domain.ErrInvalidRequest)
	uc.SetProtoMailFetchPort(protoFetchFunc(func(context.Context, FetchMessagesRequest) (*FetchMessagesResult, error) { return nil, nil }))
	_, err = uc.fetchMessages(context.Background(), scope, domain.FetchJob{}, nil)
	require.ErrorIs(t, err, domain.ErrMailServiceUnavailable)
}

func TestProtoCacheRejectsOldCredentialsAndDerivedAddresses(t *testing.T) {
	now := time.Now().UTC()
	start, end := now.Add(-time.Minute), now.Add(time.Minute)
	scope := OrderScope{OrderNo: "PROTO-1", AllocationType: domain.ResourceTypeProto, EmailResourceID: 91,
		CredentialRevision: 7, Recipient: "one@example.com", RecipientKind: "exact", ServiceMode: "code",
		ReceiveStartedAt: &start, ReceiveUntil: &end, Rules: []MailRule{
			{Type: MailRuleRecipient, Pattern: "exact", Enabled: true},
			{Type: MailRuleRecipient, Pattern: "plus", Enabled: true},
			{Type: MailRuleSender, Pattern: "service", Enabled: true},
			{Type: MailRuleSubject, Pattern: "code", Enabled: true},
			{Type: MailRuleBody, Pattern: "([0-9]{6})", Enabled: true},
		}}
	message := FetchedMessage{EmailResourceID: 91, ResourceType: domain.ResourceTypeProto,
		CredentialRevision: 6, Recipient: "one@example.com", Sender: "service@example.com",
		Subject: "code", Body: "123456", ReceivedAt: now}
	messages, matched := cachedMessagesMatchingScopes([]FetchedMessage{message}, 91, []OrderScope{scope})
	require.Empty(t, messages)
	require.False(t, matched)
	message.CredentialRevision = 7
	message.Recipient = "one+tag@example.com"
	messages, matched = cachedMessagesMatchingScopes([]FetchedMessage{message}, 91, []OrderScope{scope})
	require.Empty(t, messages)
	require.False(t, matched)
	message.Recipient = scope.Recipient
	messages, matched = cachedMessagesMatchingScopes([]FetchedMessage{message}, 91, []OrderScope{scope})
	require.Len(t, messages, 1)
	require.True(t, matched)
}

func TestProtoAdminFetchWithoutTransportDoesNotCreateTask(t *testing.T) {
	repo := &resourceFetchSubmitRepoStub{}
	uc := NewAdminResourceFetchUseCase(repo, nil, nil, NewUseCase(nil, nil, nil, nil), nil)
	_, err := uc.Submit(context.Background(), AdminResourceFetchSubmitCommand{
		ResourceType: domain.ResourceTypeProto, ResourceID: 91, OperatorUserID: 1, IdempotencyKey: "proto-fetch",
	})
	require.ErrorIs(t, err, domain.ErrMailServiceUnavailable)
	require.Zero(t, repo.job.ResourceID)
}

func TestProtoOnlyPermanentCredentialsTriggerOrderCompensation(t *testing.T) {
	for _, tc := range []struct {
		category string
		want     int
	}{
		{"session_revoked", 0}, {"invalid_credentials", 1}, {"identity_mismatch", 1},
		{"action_required", 0}, {"decryption", 0}, {"protocol", 0}, {"session_persistence", 0},
	} {
		t.Run(tc.category, func(t *testing.T) {
			uc := NewUseCase(nil, nil, nil, nil)
			failure := &MailFetchFailure{Category: tc.category, SafeMessage: "Safe Proto failure."}
			uc.SetProtoMailFetchPort(protoFetchFunc(func(context.Context, FetchMessagesRequest) (*FetchMessagesResult, error) { return nil, failure }))
			calls := 0
			uc.SetPermanentProtoFetchFailurePort(protoPermanentFailureFunc(func(_ context.Context, failure PermanentProtoFetchFailure) error {
				calls++
				require.Equal(t, uint(91), failure.ResourceID)
				require.Equal(t, uint64(7), failure.CredentialRevision)
				return nil
			}))
			_, err := uc.fetchProtoMessages(context.Background(), FetchMessagesRequest{Scope: OrderScope{AllocationType: domain.ResourceTypeProto, EmailResourceID: 91, CredentialRevision: 7}})
			require.ErrorIs(t, err, failure)
			require.Equal(t, tc.want, calls)
		})
	}
}
