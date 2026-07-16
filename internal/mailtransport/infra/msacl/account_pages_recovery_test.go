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

type accountLocalEvidenceReader struct {
	exact   []EmailObj
	content []EmailObj
}

func (r accountLocalEvidenceReader) List(context.Context, string, int, bool) ([]EmailObj, error) {
	return append([]EmailObj(nil), r.exact...), nil
}

func (r accountLocalEvidenceReader) SearchByContent(context.Context, string, int) ([]EmailObj, error) {
	return append([]EmailObj(nil), r.content...), nil
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

func TestLookupRealMailboxDeduplicatesRepeatedHistoricalAddress(t *testing.T) {
	previousReader := activeMailboxReader()
	defer SetMailboxReader(previousReader)
	defer SetAuxiliaryDomains([]string{"aishop6.com"})
	SetAuxiliaryDomains([]string{"recovery.test"})
	SetMailboxReader(recoveryEvidenceReader{mailboxes: []string{
		"qalpha01@recovery.test",
		"QALPHA01@RECOVERY.TEST",
		" qalpha01@recovery.test ",
	}})

	resolved := lookupRealMailbox(
		context.Background(),
		"qa*****@recovery.test",
		"owner@example.test",
		"",
		"",
	)
	require.Equal(t, "qalpha01@recovery.test", resolved)
}

func TestLookupRealMailboxPrefersAccountLocalMailboxWithExactAccountEvidence(t *testing.T) {
	previousReader := activeMailboxReader()
	defer SetMailboxReader(previousReader)
	defer SetAuxiliaryDomains([]string{"aishop6.com"})
	SetAuxiliaryDomains([]string{"recovery.test"})
	SetMailboxReader(accountLocalEvidenceReader{
		exact: []EmailObj{{
			To:      "brittanycoleman1901@recovery.test",
			Preview: "Security code for br*****1@outlook.com",
		}},
		content: []EmailObj{
			{To: "brandonking4691@recovery.test"},
			{To: "brittanycoleman1901@recovery.test"},
		},
	})

	resolved := lookupRealMailbox(
		context.Background(),
		"br*****@recovery.test",
		"brittanycoleman1901@outlook.com",
		"",
		"",
	)
	require.Equal(t, "brittanycoleman1901@recovery.test", resolved)
}

func TestLookupRealMailboxUsesAccountLocalRuleWithoutHistoricalEvidence(t *testing.T) {
	previousReader := activeMailboxReader()
	defer SetMailboxReader(previousReader)
	defer SetAuxiliaryDomains([]string{"aishop6.com"})
	SetAuxiliaryDomains([]string{"recovery.test"})
	SetMailboxReader(accountLocalEvidenceReader{
		exact: []EmailObj{{
			To:      "brittanycoleman1901@recovery.test",
			Preview: "Security code for another account",
		}},
		content: []EmailObj{
			{To: "brandonking4691@recovery.test"},
			{To: "brittanycoleman1901@recovery.test"},
		},
	})

	resolved := lookupRealMailbox(
		context.Background(),
		"br*****@recovery.test",
		"brittanycoleman1901@outlook.com",
		"",
		"",
	)
	require.Equal(t, "brittanycoleman1901@recovery.test", resolved)
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

func TestLookupRealMailboxPrefersInferenceOverHistoricalEvidence(t *testing.T) {
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
	require.Equal(t, generated, resolved)
	require.NotEqual(t, historical, resolved)
}

func TestInferBindingAddressChecksDeterministicThenResourcePrefix(t *testing.T) {
	previousDomains := activeAuxiliaryDomains()
	defer SetAuxiliaryDomains(previousDomains)
	SetAuxiliaryDomains([]string{"recovery.test"})

	generated, err := deterministicAuxiliaryAddressForDomain("owner@example.test", "recovery.test")
	require.NoError(t, err)
	require.Equal(t, generated, InferBindingAddress("owner@example.test", maskForTest(generated)))
	require.Equal(t, "owner@recovery.test", InferBindingAddress("owner@example.test", "o*****r@recovery.test"))
	require.Empty(t, InferBindingAddress("owner@example.test", "o*****r@external.test"))
	require.Empty(t, InferBindingAddress("owner@example.test", "q*****@recovery.test"))
}

func TestLookupRealMailboxDoesNotTreatMaskedPreferredAddressAsConcrete(t *testing.T) {
	previousReader := activeMailboxReader()
	previousDomains := activeAuxiliaryDomains()
	defer SetMailboxReader(previousReader)
	defer SetAuxiliaryDomains(previousDomains)
	SetMailboxReader(recoveryEvidenceReader{})
	SetAuxiliaryDomains([]string{"recovery.test"})

	resolved := lookupRealMailbox(
		context.Background(),
		"q*****9@recovery.test",
		"owner@example.test",
		"",
		"q*****9@recovery.test",
	)
	require.Empty(t, resolved)

	created, err := createTempMailbox(context.Background(), "owner@example.test", "q*****9@recovery.test")
	require.NoError(t, err)
	require.NotEqual(t, "q*****9@recovery.test", created)
	require.NotContains(t, created, "*")
}

func TestLookupRealMailboxRejectsExternalPreferredAddress(t *testing.T) {
	previousReader := activeMailboxReader()
	previousDomains := activeAuxiliaryDomains()
	defer SetMailboxReader(previousReader)
	defer SetAuxiliaryDomains(previousDomains)
	SetMailboxReader(recoveryEvidenceReader{})
	SetAuxiliaryDomains([]string{"recovery.test"})

	resolved := lookupRealMailbox(
		context.Background(),
		"o*****r@external.test",
		"owner@example.test",
		"",
		"owner@external.test",
	)
	require.Empty(t, resolved, "external proof must fail before Microsoft sends an unreadable OTP")
}

func TestMailboxMatchesMaskedRejectsMissingAddresses(t *testing.T) {
	require.False(t, mailboxMatchesMasked("", "owner@recovery.test"))
	require.False(t, mailboxMatchesMasked("o*****r@recovery.test", ""))
}

func maskForTest(address string) string {
	local, domain, ok := strings.Cut(address, "@")
	if !ok || len(local) < 2 {
		return address
	}
	return local[:1] + "*****" + local[len(local)-1:] + "@" + domain
}
