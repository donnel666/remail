package api

import (
	"context"
	"errors"
	"testing"
	"time"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

type microsoftMessageFetchClientStub struct {
	requests []mailinfra.MicrosoftMailFetchRequest
	results  []mailinfra.MicrosoftMailFetchResult
	errs     []error
}

type microsoftFetchProxyProviderStub struct {
	requests  []proxyapp.AcquireProxyRequest
	successes []uint
	failures  []uint
}

func (s *microsoftFetchProxyProviderStub) Acquire(_ context.Context, req proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error) {
	s.requests = append(s.requests, req)
	serverID := uint(len(s.requests) * 10)
	return &proxyapp.ProxyConfig{ID: serverID + 1, ProxyServerID: serverID, URL: "socks5://proxy.invalid:1080"}, nil
}

func (s *microsoftFetchProxyProviderStub) ReportSuccess(_ context.Context, proxyID uint) error {
	s.successes = append(s.successes, proxyID)
	return nil
}
func (s *microsoftFetchProxyProviderStub) ReportFailure(_ context.Context, proxyID uint, _ string) error {
	s.failures = append(s.failures, proxyID)
	return nil
}

type permanentFetchFailurePortStub struct {
	failures    []mailmatchapp.PermanentMicrosoftFetchFailure
	err         error
	hasDeadline bool
}

func (s *permanentFetchFailurePortStub) HandlePermanentMicrosoftFetchFailure(ctx context.Context, failure mailmatchapp.PermanentMicrosoftFetchFailure) error {
	s.failures = append(s.failures, failure)
	_, s.hasDeadline = ctx.Deadline()
	return s.err
}

func TestMicrosoftFetchAdapterRealtimeUsesPurchaseReadLimitWithinRequestedWindow(t *testing.T) {
	defer runtimeconfig.Delete("purchase_read_limit")
	runtimeconfig.Set("purchase_read_limit", "47")
	client := &microsoftMessageFetchClientStub{results: []mailinfra.MicrosoftMailFetchResult{{Valid: true}}}
	adapter := &MicrosoftFetchAdapter{client: client}
	sinceAt := time.Now().Add(-time.Hour)
	untilAt := time.Now()

	_, err := adapter.FetchMicrosoftMessages(context.Background(), mailmatchapp.FetchMessagesRequest{
		Scope: mailmatchapp.OrderScope{
			MicrosoftEmail: "owner@example.test", MicrosoftClientID: "client-id", MicrosoftRT: "refresh-token",
		},
		SinceAt: sinceAt, UntilAt: untilAt, Realtime: true,
		KnownMessageIDs: []string{"internet:cached@example.com"},
	})

	require.NoError(t, err)
	require.Len(t, client.requests, 1)
	require.Equal(t, 47, client.requests[0].MaxMessages)
	require.True(t, client.requests[0].StopAfterLimit)
	require.Equal(t, sinceAt, client.requests[0].SinceAt)
	require.Equal(t, untilAt, client.requests[0].UntilAt)
	require.Equal(t, []string{"internet:cached@example.com"}, client.requests[0].KnownMessageIDs)
}

func (s *microsoftMessageFetchClientStub) FetchAll(_ context.Context, req mailinfra.MicrosoftMailFetchRequest) (mailinfra.MicrosoftMailFetchResult, error) {
	s.requests = append(s.requests, req)
	index := len(s.requests) - 1
	var result mailinfra.MicrosoftMailFetchResult
	if index < len(s.results) {
		result = s.results[index]
	}
	var err error
	if index < len(s.errs) {
		err = s.errs[index]
	}
	return result, err
}

func TestMicrosoftFetchAdapterRetriesWithLatestRotatedRefreshToken(t *testing.T) {
	client := &microsoftMessageFetchClientStub{results: []mailinfra.MicrosoftMailFetchResult{
		{
			Category:     "request",
			SafeMessage:  "Microsoft mail service is temporarily unavailable.",
			ProxyFailure: true,
			RefreshToken: "rotated-after-first-attempt",
		},
		{
			Valid:        true,
			Protocol:     "graph",
			RefreshToken: "rotated-after-second-attempt",
		},
	}}
	adapter := &MicrosoftFetchAdapter{client: client}

	result, err := adapter.FetchMicrosoftMessages(context.Background(), mailmatchapp.FetchMessagesRequest{
		Scope: mailmatchapp.OrderScope{
			MicrosoftEmail:    "owner@example.test",
			MicrosoftClientID: "client-id",
			MicrosoftRT:       "original-refresh-token",
		},
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "rotated-after-second-attempt", result.RefreshToken)
	require.Len(t, client.requests, 2)
	require.Equal(t, "original-refresh-token", client.requests[0].RefreshToken)
	require.Equal(t, "rotated-after-first-attempt", client.requests[1].RefreshToken)
}

func TestMicrosoftFetchAdapterAvoidsFailedProxyServerOnRetry(t *testing.T) {
	client := &microsoftMessageFetchClientStub{results: []mailinfra.MicrosoftMailFetchResult{
		{Category: "request", ProxyFailure: true},
		{Valid: true},
	}}
	proxies := &microsoftFetchProxyProviderStub{}
	adapter := &MicrosoftFetchAdapter{client: client, proxies: proxies}

	_, err := adapter.FetchMicrosoftMessages(context.Background(), mailmatchapp.FetchMessagesRequest{
		RequestID: "fetch-request",
		Scope: mailmatchapp.OrderScope{
			MicrosoftEmail: "owner@example.test", MicrosoftClientID: "client-id", MicrosoftRT: "refresh-token",
		},
	})

	require.NoError(t, err)
	require.Len(t, proxies.requests, 2)
	require.Empty(t, proxies.requests[0].AvoidProxyServerIDs)
	require.Equal(t, []uint{10}, proxies.requests[1].AvoidProxyServerIDs)
}

func TestMicrosoftFetchAdapterDoesNotFailProxyForIMAPAuthenticationFailure(t *testing.T) {
	client := &microsoftMessageFetchClientStub{
		results: []mailinfra.MicrosoftMailFetchResult{{
			Category:    "imap_auth_failed",
			SafeMessage: "Microsoft IMAP authentication failed.",
		}},
		errs: []error{errors.New("imap authentication failed")},
	}
	proxies := &microsoftFetchProxyProviderStub{}
	adapter := &MicrosoftFetchAdapter{client: client, proxies: proxies}

	_, err := adapter.FetchMicrosoftMessages(context.Background(), mailmatchapp.FetchMessagesRequest{
		Scope: mailmatchapp.OrderScope{
			MicrosoftEmail: "owner@example.test", MicrosoftClientID: "client-id", MicrosoftRT: "refresh-token",
		},
	})

	var failure *mailmatchapp.MailFetchFailure
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "imap_auth_failed", failure.Category)
	require.Len(t, client.requests, 1)
	require.Empty(t, proxies.failures)
	require.Equal(t, []uint{11}, proxies.successes)
}

func TestMicrosoftFetchAdapterLeavesProxyNeutralForTaskError(t *testing.T) {
	taskErr := errors.New("local task failure")
	client := &microsoftMessageFetchClientStub{errs: []error{taskErr}}
	proxies := &microsoftFetchProxyProviderStub{}
	adapter := &MicrosoftFetchAdapter{client: client, proxies: proxies}

	_, err := adapter.FetchMicrosoftMessages(context.Background(), mailmatchapp.FetchMessagesRequest{
		Scope: mailmatchapp.OrderScope{
			MicrosoftEmail: "owner@example.test", MicrosoftClientID: "client-id", MicrosoftRT: "refresh-token",
		},
	})

	var failure *mailmatchapp.MailFetchFailure
	require.ErrorAs(t, err, &failure)
	require.ErrorIs(t, err, taskErr)
	require.True(t, failure.Retryable)
	require.Len(t, client.requests, 1)
	require.Empty(t, proxies.failures)
	require.Empty(t, proxies.successes)
}

func TestMicrosoftFetchAdapterLeavesProxyNeutralForCancellation(t *testing.T) {
	client := &microsoftMessageFetchClientStub{errs: []error{context.Canceled}}
	proxies := &microsoftFetchProxyProviderStub{}
	adapter := &MicrosoftFetchAdapter{client: client, proxies: proxies}

	_, err := adapter.FetchMicrosoftMessages(context.Background(), mailmatchapp.FetchMessagesRequest{
		Scope: mailmatchapp.OrderScope{
			MicrosoftEmail: "owner@example.test", MicrosoftClientID: "client-id", MicrosoftRT: "refresh-token",
		},
	})

	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, client.requests, 1)
	require.Empty(t, proxies.failures)
	require.Empty(t, proxies.successes)
}

func TestMicrosoftFetchAdapterStopsAfterTwoInternalAttempts(t *testing.T) {
	client := &microsoftMessageFetchClientStub{results: []mailinfra.MicrosoftMailFetchResult{
		{Category: "request", ProxyFailure: true},
		{Category: "request", ProxyFailure: true},
		{Valid: true},
	}}
	adapter := &MicrosoftFetchAdapter{client: client}

	_, err := adapter.FetchMicrosoftMessages(context.Background(), mailmatchapp.FetchMessagesRequest{
		Scope: mailmatchapp.OrderScope{
			MicrosoftEmail: "owner@example.test", MicrosoftClientID: "client-id", MicrosoftRT: "refresh-token",
		},
	})

	require.Error(t, err)
	require.Len(t, client.requests, 2)
}

func TestMicrosoftFetchAdapterProxyAttemptsUpdateAtRuntime(t *testing.T) {
	defer runtimeconfig.Delete("max_proxy_attempts")
	runtimeconfig.Set("max_proxy_attempts", "3")
	client := &microsoftMessageFetchClientStub{results: []mailinfra.MicrosoftMailFetchResult{
		{Category: "request", ProxyFailure: true},
		{Category: "request", ProxyFailure: true},
		{Valid: true},
	}}
	adapter := &MicrosoftFetchAdapter{client: client}
	req := mailmatchapp.FetchMessagesRequest{Scope: mailmatchapp.OrderScope{
		MicrosoftEmail: "owner@example.test", MicrosoftClientID: "client-id", MicrosoftRT: "refresh-token",
	}}

	_, err := adapter.FetchMicrosoftMessages(context.Background(), req)

	require.NoError(t, err)
	require.Len(t, client.requests, 3)

	runtimeconfig.Set("max_proxy_attempts", "1")
	client.requests = nil
	client.results = []mailinfra.MicrosoftMailFetchResult{
		{Category: "request", ProxyFailure: true},
		{Valid: true},
	}

	_, err = adapter.FetchMicrosoftMessages(context.Background(), req)

	require.Error(t, err)
	require.Len(t, client.requests, 1)
}

func TestMicrosoftFetchAdapterFullHistoryPreservesWindowWithoutMessageLimit(t *testing.T) {
	client := &microsoftMessageFetchClientStub{results: []mailinfra.MicrosoftMailFetchResult{{Valid: true}}}
	adapter := &MicrosoftFetchAdapter{client: client}
	sinceAt := time.Now().Add(-90 * 24 * time.Hour)
	untilAt := time.Now()

	_, err := adapter.FetchMicrosoftMessages(context.Background(), mailmatchapp.FetchMessagesRequest{
		Scope: mailmatchapp.OrderScope{
			MicrosoftEmail: "owner@example.test", MicrosoftClientID: "client-id", MicrosoftRT: "refresh-token",
		},
		SinceAt: sinceAt, UntilAt: untilAt, FullHistory: true,
	})

	require.NoError(t, err)
	require.Len(t, client.requests, 1)
	require.Zero(t, client.requests[0].MaxMessages)
	require.Equal(t, sinceAt, client.requests[0].SinceAt)
	require.Equal(t, untilAt, client.requests[0].UntilAt)
	require.False(t, client.requests[0].StopAfterLimit)
}

func TestMicrosoftFetchAdapterReturnsRotatedTokenOnFetchFailure(t *testing.T) {
	client := &microsoftMessageFetchClientStub{results: []mailinfra.MicrosoftMailFetchResult{{
		Category: "graph_forbidden", SafeMessage: "Mailbox permission is unavailable.", RefreshToken: "rotated-refresh-token",
	}}}
	adapter := &MicrosoftFetchAdapter{client: client}

	_, err := adapter.FetchMicrosoftMessages(context.Background(), mailmatchapp.FetchMessagesRequest{
		Scope: mailmatchapp.OrderScope{
			MicrosoftEmail: "owner@example.test", MicrosoftClientID: "client-id", MicrosoftRT: "original-refresh-token",
		},
	})

	var failure *mailmatchapp.MailFetchFailure
	require.True(t, errors.As(err, &failure))
	require.Equal(t, "rotated-refresh-token", failure.RefreshToken)
}

func TestMicrosoftFetchFailureKeepsTerminalClassification(t *testing.T) {
	for _, category := range []string{
		"oauth_invalid_grant", "oauth_client", "oauth_permission", "mfa", "passkey", "phone", "password",
		"unknown_mailbox", "locked", "graph_unauthorized", "graph_forbidden", "imap_auth_failed", "identity_mismatch", "missing_token",
	} {
		failure := microsoftFetchFailure(category, "safe", false)
		require.Equal(t, category, failure.Category)
		require.False(t, failure.Retryable, category)
	}
	for _, category := range []string{"request", "auth_timeout", "rate_limited", "unknown", "protocol_changed"} {
		require.True(t, microsoftFetchFailure(category, "safe", false).Retryable, category)
	}
	require.True(t, microsoftFetchFailure("oauth_invalid_grant", "safe", true).Retryable)
}

func TestMicrosoftFetchAdapterHandlesPermanentFailureForEveryCaller(t *testing.T) {
	client := &microsoftMessageFetchClientStub{results: []mailinfra.MicrosoftMailFetchResult{{
		Category: "oauth_invalid_grant", SafeMessage: "Microsoft refresh token is invalid or expired.", RefreshToken: "rotated-refresh-token",
	}}}
	failures := &permanentFetchFailurePortStub{}
	adapter := &MicrosoftFetchAdapter{client: client, fetchFailures: failures}

	_, err := adapter.FetchMicrosoftMessages(context.Background(), mailmatchapp.FetchMessagesRequest{
		Scope: mailmatchapp.OrderScope{
			OrderNo: "OR019F97158E9A713AA3A82FEB49DE0486", EmailResourceID: 30043, CredentialRevision: 7,
			MicrosoftEmail: "owner@example.test", MicrosoftClientID: "client-id", MicrosoftRT: "refresh-token",
		},
		RequestID: "019f972e-26d0-76c4-8d55-942e685db7d7",
	})

	require.Error(t, err)
	require.Equal(t, "Microsoft refresh token is invalid or expired.", err.Error())
	var fetchFailure *mailmatchapp.MailFetchFailure
	require.True(t, errors.As(err, &fetchFailure))
	require.Equal(t, "oauth_invalid_grant", fetchFailure.Category)
	require.False(t, fetchFailure.Retryable)
	require.Equal(t, []mailmatchapp.PermanentMicrosoftFetchFailure{{
		ResourceID: 30043, CredentialRevision: 7,
		RefreshToken: "rotated-refresh-token",
		OrderNo:      "OR019F97158E9A713AA3A82FEB49DE0486", RequestID: "019f972e-26d0-76c4-8d55-942e685db7d7",
		Category: "oauth_invalid_grant", SafeMessage: "Microsoft refresh token is invalid or expired.",
	}}, failures.failures)
	require.True(t, failures.hasDeadline)
}

func TestMicrosoftFetchAdapterRetriesWhenPermanentFailureHandlingFails(t *testing.T) {
	client := &microsoftMessageFetchClientStub{results: []mailinfra.MicrosoftMailFetchResult{{Category: "oauth_invalid_grant"}}}
	adapter := &MicrosoftFetchAdapter{
		client: client,
		fetchFailures: &permanentFetchFailurePortStub{
			err: errors.New("database unavailable"),
		},
	}

	_, err := adapter.FetchMicrosoftMessages(context.Background(), mailmatchapp.FetchMessagesRequest{Scope: mailmatchapp.OrderScope{
		EmailResourceID: 30043, CredentialRevision: 7,
		MicrosoftEmail: "owner@example.test", MicrosoftClientID: "client-id", MicrosoftRT: "refresh-token",
	}})

	require.ErrorIs(t, err, mailmatchapp.ErrPermanentMicrosoftFetchFailureHandling)
	var fetchFailure *mailmatchapp.MailFetchFailure
	require.False(t, errors.As(err, &fetchFailure))
}

func TestMicrosoftMessagesToMailmatchPreservesCompleteProviderContent(t *testing.T) {
	rawSource := "  MIME-Version: 1.0\r\n\r\nbody\r\n"
	providerPayload := "\n{\"id\":\"message-id\",\"body\":\"full\"}\n"
	body := " \n<a href=\"https://example.com/verify?token=abc\">Verify</a>\n"

	messages := microsoftMessagesToMailmatch(mailmatchapp.OrderScope{EmailResourceID: 42}, []mailinfra.MicrosoftFetchedMessage{{
		ID: "message-id", To: "user@example.com", Body: body, Preview: "preview must stay separate", RawSource: rawSource, ProviderPayload: providerPayload,
	}, {
		ID: "empty-body", To: "user@example.com", Preview: "preview must not become body",
	}})

	require.Len(t, messages, 2)
	require.Equal(t, body, messages[0].Body)
	require.Equal(t, rawSource, messages[0].RawSource)
	require.Equal(t, providerPayload, messages[0].ProviderPayload)
	require.Empty(t, messages[1].Body)
}

func TestMicrosoftMessagesToMailmatchRequiresExactlyOneOriginalToRecipient(t *testing.T) {
	messages := microsoftMessagesToMailmatch(mailmatchapp.OrderScope{
		EmailResourceID: 42,
		Recipient:       "requesting-alias@example.com",
	}, []mailinfra.MicrosoftFetchedMessage{
		{ID: "without-recipient"},
		{ID: "cc-recipient", Cc: "Alias <cc@example.com>", Bcc: "bcc@example.com"},
		{ID: "to-recipients", To: "First <first@example.com>, second@example.com", ToRecipientCount: 2, Cc: "ignored@example.com"},
		{ID: "single-to", To: "Only <only@hotmail.com>", ToRecipientCount: 1},
		{ID: "duplicate-to", To: "same@hotmail.com, same@hotmail.com", ToRecipientCount: 2},
		{ID: "malformed-second", To: "valid@outlook.com, malformed-recipient", ToRecipientCount: 2},
	})

	require.Len(t, messages, 6)
	require.Equal(t, "without-recipient", messages[0].ProviderMessageID)
	require.Empty(t, messages[0].Recipient)
	require.Empty(t, messages[0].Recipients)
	require.NotNil(t, messages[0].ToRecipients)
	require.Empty(t, messages[0].ToRecipients)
	require.Equal(t, "cc@example.com", messages[1].Recipient)
	require.Equal(t, []string{"cc@example.com", "bcc@example.com"}, messages[1].Recipients)
	require.NotNil(t, messages[1].ToRecipients)
	require.Empty(t, messages[1].ToRecipients)
	require.Equal(t, "first@example.com", messages[2].Recipient)
	require.Equal(t, []string{"first@example.com", "second@example.com", "ignored@example.com"}, messages[2].Recipients)
	require.Empty(t, messages[2].ToRecipients)
	require.Equal(t, []string{"only@hotmail.com"}, messages[3].ToRecipients)
	require.Equal(t, []string{"same@hotmail.com"}, messages[4].Recipients)
	require.Empty(t, messages[4].ToRecipients)
	require.Equal(t, []string{"valid@outlook.com"}, messages[5].Recipients)
	require.Empty(t, messages[5].ToRecipients)
}
