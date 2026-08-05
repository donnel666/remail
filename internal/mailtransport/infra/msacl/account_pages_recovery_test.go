package msacl

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type lookupHistoryReader struct {
	calls int
}

func (*lookupHistoryReader) List(context.Context, string, int, bool) ([]EmailObj, error) {
	return nil, nil
}

func (r *lookupHistoryReader) ListMasked(context.Context, string, MaskedMailboxQuery) ([]EmailObj, error) {
	r.calls++
	return nil, nil
}

func TestLookupRealMailboxDoesNotReadStaleMailboxHistory(t *testing.T) {
	previousReader := activeMailboxReader()
	previousDomains := activeAuxiliaryDomains()
	defer SetMailboxReader(previousReader)
	defer SetAuxiliaryDomains(previousDomains)
	reader := &lookupHistoryReader{}
	SetMailboxReader(reader)
	SetAuxiliaryDomains([]string{"recovery.test"})

	resolved := lookupRealMailbox(context.Background(), "q*****9@recovery.test", "owner@example.test", "", "")

	require.Empty(t, resolved)
	require.Zero(t, reader.calls)
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
	reader := &lookupHistoryReader{}
	SetMailboxReader(reader)

	resolved := lookupRealMailbox(
		context.Background(),
		local[:2]+"*****@recovery.test",
		"owner@example.test",
		"",
		"",
	)
	require.Equal(t, generated, resolved)
	require.NotEqual(t, historical, resolved)
	require.Zero(t, reader.calls)
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
	SetMailboxReader(&lookupHistoryReader{})
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
	SetMailboxReader(&lookupHistoryReader{})
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
