package infra

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/donnel666/remail/internal/proto/domain"
	"github.com/donnel666/remail/internal/proto/infra/proton"
	"github.com/stretchr/testify/require"
)

func TestParseImportStrictTwoFields(t *testing.T) {
	rows, failures, err := ParseImport("a@proton.me----p@ss\nb@protonmail.com----x----extra\n", domain.ErrorStrategySkip)
	if err != nil || len(rows) != 1 || len(failures) != 1 {
		t.Fatalf("rows=%d failures=%d err=%v", len(rows), len(failures), err)
	}
	if rows[0].Password != "p@ss" {
		t.Fatal("password changed")
	}
}

func TestParseImportAcceptsOptionalPKLWithoutLeakingIt(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("\x80\x04native-pkl-secret"))
	rows, failures, err := ParseImport("\ufeffowner@proton.me----  password  ---- \t"+encoded+" \r\nlegacy@proton.me----old password\nOWNER@proton.me----duplicate", domain.ErrorStrategySkip)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Len(t, failures, 1)
	require.Equal(t, "  password  ", rows[0].Password)
	require.Equal(t, encoded, rows[0].PKLBase64)
	require.Empty(t, rows[1].PKLBase64)
	require.Equal(t, "duplicate", failures[0].Category)
	serialized, err := json.Marshal(rows)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), encoded)
	for _, input := range []string{"", "not-base64!", "YQ=", "YR==", "Y Q==", "YQ==\rYQ==", "-_==", encoded + "----extra",
		strings.Repeat("A", 1020) + "AA==AAAA", strings.Repeat("A", 1020) + "AAA=AAAA"} {
		_, _, err := ParseImport("owner@proton.me----secret----"+input, domain.ErrorStrategyAbort)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret")
		require.NotContains(t, err.Error(), encoded)
	}
	_, failures, err = ParseImport(encoded+"\nnot an email----secret----"+encoded, domain.ErrorStrategySkip)
	require.NoError(t, err)
	for _, failure := range failures {
		require.Empty(t, failure.Email)
	}
}

func TestParseImportKeepsBothSupportedDomainsAndDeduplicatesFullEmails(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("pkl-canary"))
	content := " User@PROTON.ME ---- password ----" + encoded + "\r\n user@PROTONMAIL.COM ---- other password \r\nUSER@proton.me----duplicate"
	rows, failures, err := ParseImport(content, domain.ErrorStrategySkip)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "user@proton.me", rows[0].Email)
	require.Equal(t, " password ", rows[0].Password)
	require.Equal(t, encoded, rows[0].PKLBase64)
	require.Equal(t, "user@protonmail.com", rows[1].Email)
	require.Equal(t, " other password ", rows[1].Password)
	require.Empty(t, rows[1].PKLBase64)
	require.Len(t, failures, 1)
	require.Equal(t, "duplicate", failures[0].Category)
	require.Equal(t, 3, failures[0].Line)
	_, _, err = ParseImport(content, domain.ErrorStrategyAbort)
	require.Error(t, err)
	for _, email := range []string{"user@proton.me", "user@protonmail.com"} {
		for _, pkl := range []string{"", "----" + encoded} {
			rows, failures, err := ParseImport(email+"---- \tpassword \t "+pkl+"\r\n", domain.ErrorStrategyAbort)
			require.NoError(t, err)
			require.Empty(t, failures)
			require.Equal(t, email, rows[0].Email)
			require.Equal(t, " \tpassword \t ", rows[0].Password)
			require.Equal(t, strings.TrimPrefix(pkl, "----"), rows[0].PKLBase64)
		}
	}
}

func TestParseImportRejectsBareNamesAndUnsupportedDomains(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("pkl-canary"))
	for _, account := range []string{"", "user", " User ", "with space", "user@", "@proton.me", "user@@proton.me", "<user>", "bad\x00name",
		"user@proto.me", "user@example.com", "user@gmail.com", "user@sub.proton.me", "user@proton.me.evil", "user@protonmail.com.evil", encoded} {
		for _, pkl := range []string{"", "----" + encoded} {
			input := account + "----password-canary" + pkl
			rows, _, err := ParseImport(input, domain.ErrorStrategyAbort)
			require.Error(t, err, "account=%q", account)
			require.Empty(t, rows)
			require.NotContains(t, err.Error(), "password-canary")
			require.NotContains(t, err.Error(), encoded)
			rows, failures, err := ParseImport(input+"\nvalid@protonmail.com----unchanged", domain.ErrorStrategySkip)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, "valid@protonmail.com", rows[0].Email)
			require.Len(t, failures, 1)
			require.Equal(t, "invalid_format", failures[0].Category)
			if !strings.Contains(account, "@") {
				require.Empty(t, failures[0].Email, "do not display a fabricated mailbox for a rejected username")
			}
			serialized, err := json.Marshal(failures)
			require.NoError(t, err)
			require.NotContains(t, string(serialized), "password-canary")
			require.NotContains(t, string(serialized), encoded)
		}
	}
}

func TestParseImportPKLDecodedSizeBoundary(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("k"), proton.MaxPKLBytes))
	rows, failures, err := ParseImport("owner@proton.me----secret----"+encoded, domain.ErrorStrategyAbort)
	require.NoError(t, err)
	require.Empty(t, failures)
	require.Equal(t, encoded, rows[0].PKLBase64)
	tooLarge := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("k"), proton.MaxPKLBytes+1))
	_, _, err = ParseImport("owner@proton.me----secret----"+tooLarge, domain.ErrorStrategyAbort)
	require.Error(t, err)
	require.NotContains(t, err.Error(), tooLarge)
	require.False(t, validImportPKL(strings.Repeat("A", MaxImportLineBytes)))
}

func TestParseImportAbort(t *testing.T) {
	if _, _, err := ParseImport("a@proton.me----x----y", domain.ErrorStrategyAbort); err == nil {
		t.Fatal("expected strict format error")
	}
}

func TestParseImportPreservesPasswordWhitespace(t *testing.T) {
	rows, failures, err := ParseImport("a@proton.me----  pass with spaces  \r\n", domain.ErrorStrategyAbort)
	if err != nil || len(failures) != 0 || len(rows) != 1 {
		t.Fatalf("rows=%d failures=%d err=%v", len(rows), len(failures), err)
	}
	if rows[0].Password != "  pass with spaces  " {
		t.Fatalf("password whitespace changed: %q", rows[0].Password)
	}
}
