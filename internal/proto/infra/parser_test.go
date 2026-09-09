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
	rows, failures, err := ParseImport("a@example.com----p@ss\nb@example.com----x----extra\n", domain.ErrorStrategySkip)
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

func TestParseImportCompletesUsernamesBeforeDeduplication(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("pkl-canary"))
	content := " User ---- password ----" + encoded + "\nlegacy----old\nUSER@proton.me----duplicate\nuser@protonmail.com----other-domain"
	rows, failures, err := ParseImport(content, domain.ErrorStrategySkip)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	require.Equal(t, "user@proton.me", rows[0].Email)
	require.Equal(t, " password ", rows[0].Password)
	require.Equal(t, encoded, rows[0].PKLBase64)
	require.Equal(t, "legacy@proton.me", rows[1].Email)
	require.Equal(t, "user@protonmail.com", rows[2].Email)
	require.Len(t, failures, 1)
	require.Equal(t, "duplicate", failures[0].Category)
	_, _, err = ParseImport(content, domain.ErrorStrategyAbort)
	require.Error(t, err)
	for _, account := range []string{"", "with space", "user@", "@proton.me", "user@@proton.me", "<user>", "bad\x00name"} {
		_, _, err := ParseImport(account+"----secret", domain.ErrorStrategyAbort)
		require.Error(t, err)
	}
	_, failures, err = ParseImport(encoded+"----secret----bad-base64!", domain.ErrorStrategySkip)
	require.NoError(t, err)
	require.Len(t, failures, 1)
	require.Empty(t, failures[0].Email, "do not turn a malformed username field into a displayed mailbox")
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
	if _, _, err := ParseImport("a@example.com----x----y", domain.ErrorStrategyAbort); err == nil {
		t.Fatal("expected strict format error")
	}
}

func TestParseImportPreservesPasswordWhitespace(t *testing.T) {
	rows, failures, err := ParseImport("a@example.com----  pass with spaces  \r\n", domain.ErrorStrategyAbort)
	if err != nil || len(failures) != 0 || len(rows) != 1 {
		t.Fatalf("rows=%d failures=%d err=%v", len(rows), len(failures), err)
	}
	if rows[0].Password != "  pass with spaces  " {
		t.Fatalf("password whitespace changed: %q", rows[0].Password)
	}
}
