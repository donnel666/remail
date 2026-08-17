package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAppleTransientError(t *testing.T) {
	require.True(t, isAppleTransientError(fmt.Errorf("request failed: %w", &appleTransientError{status: 503})))
	require.False(t, isAppleTransientError(errors.New("request failed")))
}

func TestCredentialMetadataAndOutputFormat(t *testing.T) {
	region, opened, twoFactor := parseProductMetadata("美国区微软后缀已开通iCloud【老号改了邮箱】 x 2")
	require.Equal(t, "美国区", region)
	require.Equal(t, "是", opened)
	require.False(t, twoFactor)
	_, _, twoFactor = parseProductMetadata("美国区谷歌后缀、未开通iCloud、已开通双重认证 x 1")
	require.True(t, twoFactor)

	account, err := parseCredential("owner@example.com----Secret123----qt11----qt22----qt33----2000/2/24")
	require.NoError(t, err)
	account.Region = region
	account.ICloudOpen = opened
	line, err := formatOutputLine(account, accountOutput{Password: "NewSecret1!", Birthday: "1990-01-02", Questions: [3]securityAnswer{
		{Question: "Question one?", Answer: "remail1"},
		{Question: "Question two?", Answer: "remail2"},
		{Question: "Question three?", Answer: "remail3"},
	}})
	require.NoError(t, err)
	require.Equal(t, "美国区----是----owner@example.com----NewSecret1!----Question one?(remail1)----Question two?(remail2)----Question three?(remail3)----1990-01-02", line)
	parsed, err := parseOutputAccountLine(line)
	require.NoError(t, err)
	require.Equal(t, "NewSecret1!", parsed.Password)
	require.Equal(t, "1990-01-02", parsed.BirthdayISO)
	require.Equal(t, securityAnswer{Question: "Question one?", Answer: "remail1"}, parsed.Current[0])

	textPath := filepath.Join(t.TempDir(), "apple.uk.txt")
	require.NoError(t, os.WriteFile(textPath, []byte("owner@example.com----Secret123----qt11----qt22----qt33----2000/2/24\n"), 0o600))
	accounts, _, _, _, err := loadAccounts([]string{textPath}, map[string]struct{}{})
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.Equal(t, "英国区", accounts[0].Region)
	require.Equal(t, "未知", accounts[0].ICloudOpen)
}

func TestGeneratedChangesAreDifferentAndRecoverable(t *testing.T) {
	now := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	path := filepath.Join(t.TempDir(), "apple_ok.txt.pending")
	accounts := []accountInput{
		{Email: "first@example.com", Password: "Original1!", BirthdayISO: "2000-02-24"},
		{Email: "second@example.com", Password: "Original2!", BirthdayISO: "1988-06-01"},
	}
	pending, err := prepareAccountChanges(accounts, path, now)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	require.NotEqual(t, accounts[0].Password, accounts[0].NewPassword)
	require.NotEqual(t, accounts[1].Password, accounts[1].NewPassword)
	require.NotEqual(t, accounts[0].NewPassword, accounts[1].NewPassword)
	require.True(t, validGeneratedPassword(accounts[0].NewPassword))
	require.True(t, validGeneratedPassword(accounts[1].NewPassword))
	require.NotEqual(t, accounts[0].BirthdayISO, accounts[0].NewBirthday)
	require.NotEqual(t, accounts[1].BirthdayISO, accounts[1].NewBirthday)
	require.True(t, birthdayWithinAgeRange(accounts[0].NewBirthday, now))
	require.True(t, birthdayWithinAgeRange(accounts[1].NewBirthday, now))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	recovery := []accountInput{
		{Email: accounts[0].Email, Password: accounts[0].Password, BirthdayISO: accounts[0].BirthdayISO},
		{Email: accounts[1].Email, Password: accounts[1].Password, BirthdayISO: accounts[1].BirthdayISO},
	}
	_, err = prepareAccountChanges(recovery, path, now)
	require.NoError(t, err)
	require.True(t, recovery[0].Recovering)
	require.True(t, recovery[1].Recovering)
	require.Equal(t, accounts[0].NewPassword, recovery[0].NewPassword)
	require.Equal(t, accounts[0].NewBirthday, recovery[0].NewBirthday)

	require.NoError(t, prunePendingChanges(path, pending, map[string]struct{}{accounts[0].Email: {}}))
	remaining, err := loadPendingChanges(path, now)
	require.NoError(t, err)
	require.NotContains(t, remaining, accounts[0].Email)
	require.Contains(t, remaining, accounts[1].Email)
}

func TestSecurityQuestionAnswerAttemptsSupportInterruptedRuns(t *testing.T) {
	questions := []map[string]any{
		{"id": "136", "question": "Question two?"},
		{"id": "130", "question": "Question one?"},
	}
	attempts, ok := securityQuestionAnswerAttempts(
		[3]securityAnswer{{Answer: "old1"}, {Answer: "old2"}, {Answer: "old3"}},
		[3]string{"remail1", "remail2", "remail3"},
		questions,
	)
	require.True(t, ok)
	require.Equal(t, [][]string{{"old2", "old1"}, {"remail2", "remail1"}}, attempts)
}

func TestSRPProofMatchesAppleScriptVector(t *testing.T) {
	privateA, ok := new(big.Int).SetString("12345678901234567890", 10)
	require.True(t, ok)
	serverPrivate, ok := new(big.Int).SetString("98765432109876543210", 10)
	require.True(t, ok)
	publicA := padN(new(big.Int).Exp(srpG, privateA, srpN).Bytes())
	serverB := padN(new(big.Int).Exp(srpG, serverPrivate, srpN).Bytes())
	m1, m2, err := srpProofs(
		"user@example.com",
		"Secret123!",
		mustDecodeHex(t, "00112233445566778899aabbccddeeff"),
		1000,
		"s2k",
		privateA,
		publicA,
		serverB,
	)
	require.NoError(t, err)
	require.Equal(t, "3cc5d8d3f6bb92afc0d02fccf96a5c709c5e1021caa9ae4a9c7d7d600dbc6e5d", hex.EncodeToString(m1))
	require.Equal(t, "cec3bb1e2e276ea6fec46f84ae72f114fa5e8d1fd5e29a8cf31dd7f4163d4c21", hex.EncodeToString(m2))

	encoded, err := encodeAppleCollector("TF1;020;")
	require.NoError(t, err)
	require.Equal(t, "s0a44j1dXjV.Duv", encoded)
	clientInfo, err := fdClientInfo(time.UnixMilli(1700000000123))
	require.NoError(t, err)
	require.Equal(t, `{"U":"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36","L":"zh-CN","Z":"GMT+08:00","V":"1.1","F":"cda44j1e3NlY5BNlY5BSmHACVZXnNA9hLfNH1ZrurJhBR.uMp4UdHz13NlgN.xLB.Tf1cK8D9Jsdj.z9_ye3NlY5BNp55BNlan0Os5Apw.2YL"}`, clientInfo)
}

func mustDecodeHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	require.NoError(t, err)
	return decoded
}
