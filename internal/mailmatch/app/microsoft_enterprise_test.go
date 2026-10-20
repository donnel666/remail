package app

import (
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestMicrosoftEnterpriseHistoryMatchesOnlyMainMailbox(t *testing.T) {
	scope := HistoricalProjectScope{ProjectID: 10, ProductID: 20, LooseMatch: true, Rules: []MailRule{{Type: MailRuleSender, Pattern: ".*", Enabled: true}, {Type: MailRuleRecipient, Pattern: "exact", Enabled: true}}}
	request := HistoricalProjectMatchRequest{ResourceID: 1, EmailAddress: "u@tenant.onmicrosoft.com"}
	for _, recipient := range []string{"u@tenant.onmicrosoft.com", "u+alias@tenant.onmicrosoft.com", "other@tenant.onmicrosoft.com", "u@outlook.com"} {
		request.Messages = []HistoricalProjectMessage{{Recipients: []string{recipient}, Sender: "sender@example.com", ReceivedAt: time.Now()}}
		matches := historicalProjectMatches(request, []HistoricalProjectScope{scope}, time.Now())
		if recipient == request.EmailAddress {
			require.Len(t, matches, 1)
			require.Equal(t, HistoricalMailboxMain, matches[0].MailboxType)
		} else {
			require.Empty(t, matches)
		}
	}
}
