package app

import (
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/stretchr/testify/require"
)

func TestMatchAndExtractAnyRecipientUsesAliasCandidate(t *testing.T) {
	scope := OrderScope{
		Recipient:     "alias+login@example.com",
		RecipientKind: "plus",
		LooseMatch:    true,
		Rules: []MailRule{
			{Type: MailRuleRecipient, Pattern: "plus", Enabled: true},
		},
	}
	message := FetchedMessage{
		Recipient:  "main@example.com",
		Recipients: []string{"main@example.com", "alias+login@example.com"},
		Body:       "Your code is 123456.",
	}

	matched, code, diagnostic := matchAndExtractAnyRecipient(message, scope)

	require.True(t, matched)
	require.Equal(t, "123456", code)
	require.Empty(t, diagnostic)
}

func TestRecipientBuiltInStrategyMustMatchAllocationKind(t *testing.T) {
	message := FetchedMessage{
		Recipient: "name.tag@example.com",
		Body:      "Code: 654321",
	}
	scope := OrderScope{
		Recipient:     "name.tag@example.com",
		RecipientKind: "exact",
		LooseMatch:    true,
		Rules: []MailRule{
			{Type: MailRuleRecipient, Pattern: "dot", Enabled: true},
		},
	}

	matched, _, _ := matchAndExtractAnyRecipient(message, scope)
	require.False(t, matched)

	scope.RecipientKind = "dot"
	matched, code, _ := matchAndExtractAnyRecipient(message, scope)
	require.True(t, matched)
	require.Equal(t, "654321", code)
}

func TestStrictBodyRuleExtractsCaptureGroup(t *testing.T) {
	message := FetchedMessage{
		Recipient: "user@example.com",
		Sender:    "notify@example.net",
		Subject:   "Login verification",
		Body:      "Use token ABC-135790 to continue.",
	}
	scope := OrderScope{
		Recipient:     "user@example.com",
		RecipientKind: "exact",
		LooseMatch:    false,
		Rules: []MailRule{
			{Type: MailRuleRecipient, Pattern: "exact", Enabled: true},
			{Type: MailRuleSender, Pattern: `notify@example\.net`, Enabled: true},
			{Type: MailRuleSubject, Pattern: `Login`, Enabled: true},
			{Type: MailRuleBody, Pattern: `token\s+([A-Z]+-\d{6})`, Enabled: true},
		},
	}

	matched, code, diagnostic := matchAndExtractAnyRecipient(message, scope)

	require.True(t, matched)
	require.Equal(t, "ABC-135790", code)
	require.Empty(t, diagnostic)
}

func TestLooseModeUsesGenericNumericExtraction(t *testing.T) {
	message := FetchedMessage{
		Recipient: "user@example.com",
		Body:      "OTP: 87654321",
	}
	scope := OrderScope{
		Recipient:     "user@example.com",
		RecipientKind: "exact",
		LooseMatch:    true,
		Rules: []MailRule{
			{Type: MailRuleRecipient, Pattern: "exact", Enabled: true},
			{Type: MailRuleBody, Pattern: `never-match-(\d+)`, Enabled: true},
		},
	}

	matched, code, diagnostic := matchAndExtractAnyRecipient(message, scope)

	require.True(t, matched)
	require.Equal(t, "87654321", code)
	require.Empty(t, diagnostic)
}

func TestScopeReadableKeepsPurchaseServiceAfterWarranty(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	receiveUntil := now.Add(time.Hour)
	afterSaleUntil := now.Add(24 * time.Hour)
	activatedAt := now.Add(5 * time.Minute)

	preActivation := OrderScope{
		OrderNo:          "OR_PURCHASE_PRE",
		EmailResourceID:  1,
		Recipient:        "user@example.com",
		ServiceMode:      "purchase",
		OrderStatus:      "active",
		ReceiveUntil:     &receiveUntil,
		AfterSaleUntil:   nil,
		AllocationID:     1,
		AllocationType:   domain.ResourceTypeMicrosoft,
		ReceiveStartedAt: &now,
	}
	require.True(t, scopeReadable(preActivation, func() time.Time { return now }))
	require.True(t, scopeReadable(preActivation, func() time.Time { return receiveUntil.Add(time.Second) }))

	activated := preActivation
	activated.OrderNo = "OR_PURCHASE_ACTIVE"
	activated.ActivatedAt = &activatedAt
	activated.AfterSaleUntil = &afterSaleUntil
	require.True(t, scopeReadable(activated, func() time.Time { return receiveUntil.Add(time.Second) }))
	activated.OrderStatus = "completed"
	require.True(t, scopeReadable(activated, func() time.Time { return afterSaleUntil.Add(time.Second) }))
}

func TestShouldScheduleReadFetchOnlySuppressesDeliveredCodeOrders(t *testing.T) {
	require.False(t, shouldScheduleReadFetch(OrderScope{ServiceMode: "code"}, true))
	require.True(t, shouldScheduleReadFetch(OrderScope{ServiceMode: "code"}, false))
	require.True(t, shouldScheduleReadFetch(OrderScope{ServiceMode: "purchase"}, true))
	require.True(t, shouldScheduleReadFetch(OrderScope{ServiceMode: "purchase"}, false))
}

func TestFetchDueUsesServerCooldown(t *testing.T) {
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	future := now.Add(time.Second)
	past := now.Add(-time.Second)
	require.False(t, fetchDue(&domain.FetchState{CooldownUntil: &future}, now))
	require.True(t, fetchDue(&domain.FetchState{CooldownUntil: &past}, now))
	require.True(t, fetchDue(nil, now))
}

func TestMessageDedupeKeyMatchesAcrossProvidersWithoutMessageID(t *testing.T) {
	receivedAt := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	graph := FetchedMessage{
		Recipient:         "user@example.com",
		Sender:            "noreply@example.net",
		Subject:           "Verification",
		Body:              "<p>Your code is <b>123456</b></p>",
		ProviderMessageID: "graph-id",
		Protocol:          "graph",
		ReceivedAt:        receivedAt,
	}
	imap := graph
	imap.ProviderMessageID = "imap-uid"
	imap.Protocol = "imap"

	require.Equal(t, messageDedupeKey(graph), messageDedupeKey(imap))
}

func TestHistoricalMessageMatchesProjectWithoutVerificationCode(t *testing.T) {
	scope := HistoricalProjectScope{
		ProjectID:  10,
		LooseMatch: true,
		Rules: []MailRule{
			{Type: MailRuleRecipient, Pattern: "exact", Enabled: true},
			{Type: MailRuleSender, Pattern: `noreply@github\.com`, Enabled: true},
		},
	}
	message := HistoricalProjectMessage{
		Recipients: []string{"main@example.com"},
		Sender:     "noreply@github.com",
		Subject:    "Welcome to GitHub",
		Body:       "Your account is ready.",
		ReceivedAt: time.Now().UTC(),
	}

	require.True(t, historicalMessageMatchesProject(message, "main@example.com", scope))
	require.Equal(t, "plus", historicalRecipientKind("main@example.com", "main+github@example.com"))
	require.Equal(t, "dot", historicalRecipientKind("firstname@example.com", "first.name@example.com"))
}

func TestLatestOrderDeliveriesKeepsNewestMessagePerOrder(t *testing.T) {
	base := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	deliveries := latestOrderDeliveries([]matchedDelivery{
		{scope: OrderScope{OrderID: 1}, message: domain.Message{DedupeKey: "older", ReceivedAt: base}},
		{scope: OrderScope{OrderID: 2}, message: domain.Message{DedupeKey: "other", ReceivedAt: base}},
		{scope: OrderScope{OrderID: 1}, message: domain.Message{DedupeKey: "newer", ReceivedAt: base.Add(time.Minute)}},
	})
	require.Len(t, deliveries, 2)
	byOrder := make(map[uint]matchedDelivery, len(deliveries))
	for _, delivery := range deliveries {
		byOrder[delivery.scope.OrderID] = delivery
	}
	require.Equal(t, "newer", byOrder[1].message.DedupeKey)
	require.Equal(t, "other", byOrder[2].message.DedupeKey)
}
