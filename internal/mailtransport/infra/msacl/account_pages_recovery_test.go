package msacl

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type recoveryEvidenceReader struct {
	mailboxes []string
}

func (r recoveryEvidenceReader) List(context.Context, string, int, bool) ([]EmailObj, error) {
	return recoveryEvidenceEmails(r.mailboxes), nil
}

func (r recoveryEvidenceReader) SearchByContent(context.Context, string, int) ([]EmailObj, error) {
	return recoveryEvidenceEmails(r.mailboxes), nil
}

func recoveryEvidenceEmails(mailboxes []string) []EmailObj {
	items := make([]EmailObj, 0, len(mailboxes))
	for index, mailbox := range mailboxes {
		items = append(items, EmailObj{ID: index + 1, To: mailbox})
	}
	return items
}

func TestLookupRealMailboxRequiresUniqueHistoricalEvidence(t *testing.T) {
	previousReader := activeMailboxReader()
	defer SetMailboxReader(previousReader)
	defer SetAuxiliaryDomains([]string{"aishop6.com"})
	SetAuxiliaryDomains([]string{"recovery.test"})

	SetMailboxReader(recoveryEvidenceReader{mailboxes: []string{
		"qalpha01@recovery.test",
		"qanother@recovery.test",
	}})
	resolved := lookupRealMailbox(
		context.Background(),
		"qa*****@recovery.test",
		"owner@example.test",
		"",
		"",
	)
	require.Empty(t, resolved)

	SetMailboxReader(recoveryEvidenceReader{mailboxes: []string{"qalpha01@recovery.test"}})
	resolved = lookupRealMailbox(
		context.Background(),
		"qa*****@recovery.test",
		"owner@example.test",
		"",
		"",
	)
	require.Equal(t, "qalpha01@recovery.test", resolved)
}

func TestLookupRealMailboxCountsOnlyFullMaskMatches(t *testing.T) {
	previousReader := activeMailboxReader()
	defer SetMailboxReader(previousReader)
	defer SetAuxiliaryDomains([]string{"aishop6.com"})
	SetAuxiliaryDomains([]string{"recovery.test"})
	SetMailboxReader(recoveryEvidenceReader{mailboxes: []string{
		"qalpha01@recovery.test",
		"qanother@recovery.test",
	}})

	resolved := lookupRealMailbox(
		context.Background(),
		"qa*****1@recovery.test",
		"owner@example.test",
		"",
		"",
	)
	require.Equal(t, "qalpha01@recovery.test", resolved)
}

func TestLookupRealMailboxPrefersUniqueHistoricalEvidenceOverDeterministicGuess(t *testing.T) {
	previousReader := activeMailboxReader()
	defer SetMailboxReader(previousReader)
	defer SetAuxiliaryDomains([]string{"aishop6.com"})
	SetAuxiliaryDomains([]string{"recovery.test"})

	generated, err := deterministicAuxiliaryAddressForDomain("owner@example.test", "recovery.test")
	require.NoError(t, err)
	local, _, ok := strings.Cut(generated, "@")
	require.True(t, ok)
	require.GreaterOrEqual(t, len(local), 2)
	historical := local[:2] + "historical@recovery.test"
	SetMailboxReader(recoveryEvidenceReader{mailboxes: []string{historical}})

	resolved := lookupRealMailbox(
		context.Background(),
		local[:2]+"*****@recovery.test",
		"owner@example.test",
		"",
		"",
	)
	require.Equal(t, historical, resolved)
	require.NotEqual(t, generated, resolved)
}
