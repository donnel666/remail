package app

import (
	"testing"

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

func TestFilterMessagesForScopeKeepsOriginalRecipient(t *testing.T) {
	scope := OrderScope{
		Recipient:     "alias+login@example.com",
		RecipientKind: "plus",
		ServiceMode:   "code",
		LooseMatch:    true,
		Rules: []MailRule{
			{Type: MailRuleRecipient, Pattern: "plus", Enabled: true},
		},
	}
	messages := []domain.Message{{
		EmailResourceID: 1,
		ResourceType:    domain.ResourceTypeMicrosoft,
		Recipient:       "main@example.com",
		Recipients:      []string{"main@example.com", "alias+login@example.com"},
		RawBody:         "One time code 112233.",
	}}

	filtered := filterMessagesForScope(messages, scope, 1)

	require.Len(t, filtered, 1)
	require.Equal(t, "main@example.com", filtered[0].Recipient)
	require.Equal(t, "112233", filtered[0].VerificationCode)
}
