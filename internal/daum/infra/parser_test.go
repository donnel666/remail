package infra

import (
	"strings"
	"testing"

	"github.com/donnel666/remail/internal/daum/domain"
	"github.com/stretchr/testify/require"
)

func TestCanonicalEmailRequiresAllFixedAddressesToFitStoredLength(t *testing.T) {
	for _, suffix := range []string{"daum.net", "hanmail.net", "hanmail2.net"} {
		t.Run(suffix, func(t *testing.T) {
			local := strings.Repeat("a", 242)
			email, ok := CanonicalEmail(local + "@" + suffix)
			require.True(t, ok, "the longest fixed alias fits exactly in 255 characters")
			require.Equal(t, local+"@daum.net", email)
			for _, length := range []int{243, 246} {
				_, ok := CanonicalEmail(strings.Repeat("a", length) + "@" + suffix)
				require.False(t, ok, "a shorter input suffix must not hide an oversized generated alias")
			}
		})
	}
}

func TestParseMixedImportPreservesCredentialsAndLineNumbers(t *testing.T) {
	rows, failures, err := ParseImport("\ufefffirst@daum.net---- app  \r\n\r\nsecond@hanmail.net---- login ---- APP \r\nthird@hanmail2.net----login----app----https://remail.test/v1/pickup?email=helper%40outlook.com&token=synthetic-secret----third@kakao.com\n", "skip")
	require.NoError(t, err)
	require.Empty(t, failures)
	require.Len(t, rows, 3)
	require.Equal(t, []int{1, 3, 4}, []int{rows[0].LineNumber, rows[1].LineNumber, rows[2].LineNumber})
	require.Empty(t, rows[0].Password)
	require.Equal(t, " app  ", rows[0].AppPassword)
	require.Equal(t, " login ", rows[1].Password)
	require.Equal(t, " APP ", rows[1].AppPassword)
	require.Equal(t, "second@daum.net", rows[1].Email)
	require.Equal(t, "third@daum.net", rows[2].Email)
	require.Equal(t, "helper@outlook.com", rows[2].BindingAddress)
	require.Equal(t, "third@kakao.com", rows[2].KakaoEmail)
}
func TestImportRejectsAmbiguousFieldsAndDuplicateCanonicalAccount(t *testing.T) {
	for _, line := range []string{"one@daum.net", "one@daum.net----login----app----url", "one@daum.net----login----app----url----aux@example.test----extra", "one@outlook.com----app", "one@daum.net----", "one@daum.net----login----app----javascript:alert(1)----aux@example.test"} {
		_, failures, err := ParseImport(line, "skip")
		require.NoError(t, err)
		require.Len(t, failures, 1)
		_, _, err = ParseImport(line, "abort")
		require.ErrorIs(t, err, domain.ErrInvalidImportFormat)
	}
	rows, failures, err := ParseImport("One@daum.net----first\none@hanmail.net----second\none@hanmail2.net----third", "skip")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Len(t, failures, 2)
	require.Equal(t, "duplicate", failures[0].Category)
}
func TestPickupURLIsParsedWithoutAcceptingAmbiguousCapabilities(t *testing.T) {
	for _, value := range []string{"https://host/pickup?email=a%40b.test&token=x&token=y", "https://user:pass@host/pickup?email=a%40b.test&token=x", "file:///pickup?email=a%40b.test&token=x", "https://host/pickup?email=a%40b.test&token=x#secret", "https://host/other?email=a%40b.test&token=x", "https://host/pickup?email=a%40b.test&token=%00"} {
		_, _, err := ParsePickupURL(value)
		require.Error(t, err)
	}
}
