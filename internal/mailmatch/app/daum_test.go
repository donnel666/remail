package app

import (
	"context"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/stretchr/testify/require"
)

func TestDaumSharedInboxKeepsEverySuffixIsolatedEvenWithLooseRules(t *testing.T) {
	for _, wanted := range []string{"one@daum.net", "one@hanmail.net", "one@hanmail2.net"} {
		for _, actual := range []string{"one@daum.net", "one@hanmail.net", "one@hanmail2.net", ""} {
			scope := OrderScope{AllocationType: domain.ResourceTypeDaum, EmailResourceID: 7, Recipient: wanted,
				LooseMatch: true, Rules: []MailRule{{Type: MailRuleRecipient, Pattern: "exact", Enabled: true}, {Type: MailRuleSender, Pattern: "service", Enabled: true}}}
			matched, _ := matchScopeFilters(FetchedMessage{ResourceType: domain.ResourceTypeDaum, Recipient: actual, Sender: "service@example.test"}, scope, enabledRules(scope.Rules))
			require.Equal(t, wanted == actual, matched, "wanted %s, actual %s", wanted, actual)
		}
	}
}

func TestDaumSameMailDeduplicatesAcrossIMAPAndPOP3(t *testing.T) {
	first := FetchedMessage{ResourceType: domain.ResourceTypeDaum, MessageIDHeader: "mail@example.test", ProviderMessageID: "imap:INBOX:2:4", Protocol: "imap"}
	second := first
	second.Protocol, second.ProviderMessageID = "pop3", "pop3:opaque"
	require.Equal(t, messageDedupeKey(first), messageDedupeKey(second))
	first.MessageIDHeader, second.MessageIDHeader = "", ""
	first.ProviderMessageID, second.ProviderMessageID = "sha256:same-source", "sha256:same-source"
	second.ReceivedAt = time.Now()
	require.Equal(t, messageDedupeKey(first), messageDedupeKey(second))
	second.ProviderMessageID = "sha256:different-source"
	require.NotEqual(t, messageDedupeKey(first), messageDedupeKey(second))
}

type daumFetchFunc func(context.Context, FetchMessagesRequest) (*FetchMessagesResult, error)

type daumPermanentFailureFunc func(context.Context, PermanentDaumFetchFailure) error

func (f daumPermanentFailureFunc) HandlePermanentDaumFetchFailure(ctx context.Context, failure PermanentDaumFetchFailure) error {
	return f(ctx, failure)
}

func (f daumFetchFunc) FetchDaumMessages(ctx context.Context, req FetchMessagesRequest) (*FetchMessagesResult, error) {
	return f(ctx, req)
}

func TestDaumFetchIsIsolatedAndFailsClosed(t *testing.T) {
	legacy := &incrementalFetchTransportStub{}
	uc := NewUseCase(nil, nil, legacy, nil)
	scope := OrderScope{AllocationType: domain.ResourceTypeDaum, EmailResourceID: 91,
		CredentialRevision: 7, Recipient: "one@daum.net", OrderNo: "DAUM-1"}
	_, err := uc.fetchMessages(context.Background(), scope, domain.FetchJob{}, nil)
	require.ErrorIs(t, err, domain.ErrMailServiceUnavailable)
	calls := 0
	uc.SetDaumMailFetchPort(daumFetchFunc(func(_ context.Context, req FetchMessagesRequest) (*FetchMessagesResult, error) {
		calls++
		require.Equal(t, uint(91), req.Scope.EmailResourceID)
		require.Equal(t, uint64(7), req.Scope.CredentialRevision)
		return &FetchMessagesResult{Messages: []FetchedMessage{{
			EmailResourceID: 91, ResourceType: domain.ResourceTypeDaum, Recipient: "one@daum.net",
		}}}, nil
	}))
	result, err := uc.fetchMessages(context.Background(), scope, domain.FetchJob{}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, uint64(7), result.Messages[0].CredentialRevision)
	require.Empty(t, legacy.request.Scope.OrderNo)
	uc.SetDaumMailFetchPort(daumFetchFunc(func(context.Context, FetchMessagesRequest) (*FetchMessagesResult, error) {
		return &FetchMessagesResult{Messages: []FetchedMessage{{EmailResourceID: 92, ResourceType: domain.ResourceTypeDaum}}}, nil
	}))
	_, err = uc.fetchMessages(context.Background(), scope, domain.FetchJob{}, nil)
	require.ErrorIs(t, err, domain.ErrInvalidRequest)
	uc.SetDaumMailFetchPort(daumFetchFunc(func(context.Context, FetchMessagesRequest) (*FetchMessagesResult, error) { return nil, nil }))
	_, err = uc.fetchMessages(context.Background(), scope, domain.FetchJob{}, nil)
	require.ErrorIs(t, err, domain.ErrMailServiceUnavailable)
}

func TestDaumCacheRejectsOldCredentialsAndDerivedAddresses(t *testing.T) {
	now := time.Now().UTC()
	start, end := now.Add(-time.Minute), now.Add(time.Minute)
	scope := OrderScope{OrderNo: "DAUM-1", AllocationType: domain.ResourceTypeDaum, EmailResourceID: 91,
		CredentialRevision: 7, Recipient: "one@daum.net", RecipientKind: "exact", ServiceMode: "code",
		ReceiveStartedAt: &start, ReceiveUntil: &end, Rules: []MailRule{
			{Type: MailRuleRecipient, Pattern: "exact", Enabled: true},
			{Type: MailRuleRecipient, Pattern: "plus", Enabled: true},
			{Type: MailRuleSender, Pattern: "service", Enabled: true},
			{Type: MailRuleSubject, Pattern: "code", Enabled: true},
			{Type: MailRuleBody, Pattern: "([0-9]{6})", Enabled: true},
		}}
	message := FetchedMessage{EmailResourceID: 91, ResourceType: domain.ResourceTypeDaum,
		CredentialRevision: 6, Recipient: "one@daum.net", Sender: "service@example.com",
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

func TestDaumAdminFetchWithoutTransportDoesNotCreateTask(t *testing.T) {
	repo := &resourceFetchSubmitRepoStub{}
	uc := NewAdminResourceFetchUseCase(repo, nil, nil, NewUseCase(nil, nil, nil, nil), nil)
	_, err := uc.Submit(context.Background(), AdminResourceFetchSubmitCommand{
		ResourceType: domain.ResourceTypeDaum, ResourceID: 91, OperatorUserID: 1, IdempotencyKey: "daum-fetch",
	})
	require.ErrorIs(t, err, domain.ErrMailServiceUnavailable)
	require.Zero(t, repo.job.ResourceID)
}

func TestDaumTerminalFetchFailuresRequestAuthoritativeCompensationCheck(t *testing.T) {
	for _, tc := range []struct {
		category  string
		retryable bool
		want      int
	}{
		{"invalid_credentials", false, 1}, {"identity_mismatch", false, 1}, {"account_disabled", false, 1}, {"protocol_disabled", false, 1},
		{"transport", true, 0}, {"protocol", true, 0}, {"provider_unavailable", true, 0},
	} {
		t.Run(tc.category, func(t *testing.T) {
			uc := NewUseCase(nil, nil, nil, nil)
			failure := &MailFetchFailure{Category: tc.category, SafeMessage: "Safe Daum failure.", Retryable: tc.retryable}
			uc.SetDaumMailFetchPort(daumFetchFunc(func(context.Context, FetchMessagesRequest) (*FetchMessagesResult, error) { return nil, failure }))
			calls := 0
			uc.SetPermanentDaumFetchFailurePort(daumPermanentFailureFunc(func(_ context.Context, failure PermanentDaumFetchFailure) error {
				calls++
				require.Equal(t, uint(91), failure.ResourceID)
				require.Equal(t, uint64(7), failure.CredentialRevision)
				return nil
			}))
			_, err := uc.fetchDaumMessages(context.Background(), FetchMessagesRequest{Scope: OrderScope{AllocationType: domain.ResourceTypeDaum, EmailResourceID: 91, CredentialRevision: 7}})
			require.ErrorIs(t, err, failure)
			require.Equal(t, tc.want, calls)
		})
	}
}

func TestDaumStaleAndUncommittedFailuresNeverTriggerCompensation(t *testing.T) {
	for _, cause := range []error{domain.ErrResourceFetchCredentialChanged, &MailFetchFailure{Category: "request", Retryable: true}} {
		uc := NewUseCase(nil, nil, nil, nil)
		uc.SetDaumMailFetchPort(daumFetchFunc(func(context.Context, FetchMessagesRequest) (*FetchMessagesResult, error) { return nil, cause }))
		uc.SetPermanentDaumFetchFailurePort(daumPermanentFailureFunc(func(context.Context, PermanentDaumFetchFailure) error {
			t.Fatal("a stale or uncommitted failure must not request a refund")
			return nil
		}))
		_, err := uc.fetchDaumMessages(context.Background(), FetchMessagesRequest{Scope: OrderScope{AllocationType: domain.ResourceTypeDaum, EmailResourceID: 91, CredentialRevision: 7}})
		require.ErrorIs(t, err, cause)
	}
}
