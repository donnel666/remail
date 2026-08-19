package icloud

import (
	"strings"
	"testing"

	coreDomain "github.com/donnel666/remail/internal/core/domain"
)

const (
	testICloudOldCookie       = "X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token; X-APPLE-DS-WEB-SESSION-TOKEN=session"
	testICloudNewCookie       = "myacinfo=secret"
	testICloudFDClientInfo    = `{"F":"test-fingerprint"}`
	testICloudLegacyUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36"
	testICloudOldCurl         = `curl --url 'https://p119-maildomainws.icloud.com.cn/v2/hme/list?clientBuildNumber=build&clientMasteringNumber=master&clientId=client&dsid=123' -b '` + testICloudOldCookie + `'`
)

var (
	testICloudLongScnt = strings.Repeat("s", 400)
	testICloudNewCurl  = `curl --url 'https://appleid.apple.com/account/manage/email/private' -b '` + testICloudNewCookie + `' -H 'X-Apple-Api-Key: api-key' -H 'X-Apple-I-FD-Client-Info: ` + testICloudFDClientInfo + `' -H 'scnt: ` + testICloudLongScnt + `'`
)

func TestParseICloudImportSupportsCompleteCredentialLines(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantKinds []string
	}{
		{name: "old channel", line: "owner@icloud.com----" + testICloudOldCurl, wantKinds: []string{iCloudChannelWeb}},
		{name: "new channel", line: "owner@icloud.com----" + testICloudNewCurl, wantKinds: []string{iCloudChannelAppleAccount}},
		{name: "both channels", line: "owner@icloud.com----" + testICloudNewCurl + "----" + testICloudOldCurl, wantKinds: []string{iCloudChannelAppleAccount, iCloudChannelWeb}},
		{name: "both channels reversed", line: "owner@icloud.com----" + testICloudOldCurl + "----" + testICloudNewCurl, wantKinds: []string{iCloudChannelWeb, iCloudChannelAppleAccount}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lines, failures, fatal := parseICloudImport(test.line, coreDomain.ImportErrorStrategyAbort)
			if fatal != nil || len(failures) != 0 || len(lines) != 1 {
				t.Fatalf("unexpected parse result: lines=%#v failures=%#v fatal=%#v", lines, failures, fatal)
			}
			line := lines[0]
			if line.PrimaryEmail != "owner@icloud.com" || len(line.Channels) != len(test.wantKinds) {
				t.Fatalf("unexpected credentials: %#v", line)
			}
			for index, kind := range test.wantKinds {
				if line.Channels[index].Kind != kind {
					t.Fatalf("channel %d kind = %q, want %q", index, line.Channels[index].Kind, kind)
				}
				if line.Channels[index].UserAgent != testICloudLegacyUserAgent {
					t.Fatalf("channel %d user agent = %q, want legacy Windows default", index, line.Channels[index].UserAgent)
				}
				if kind == iCloudChannelAppleAccount &&
					(line.Channels[index].APIKey != "api-key" || line.Channels[index].FDClientInfo != testICloudFDClientInfo || line.Channels[index].Scnt != testICloudLongScnt) {
					t.Fatalf("unexpected Apple Account request context")
				}
			}
		})
	}
}

func TestParseICloudImportAcceptsBrowserCopiedOpaqueValues(t *testing.T) {
	scnt := strings.Repeat("s", iCloudAppleAccountValueMaxLength)
	cookie := "opaque_a=1; opaque_b=" + strings.Repeat("a", 4096)
	command := `curl --url 'https://appleid.apple.com/account/manage/email/private' ` +
		`-H 'Accept: application/json, text/plain, */*' ` +
		`-H 'X-Ignored-Browser-Context: ` + strings.Repeat("x", 4096) + `' ` +
		`-H 'Cookie: ` + cookie + `' ` +
		`-H 'Origin: https://account.apple.com' ` +
		`-H 'X-Apple-Api-Key: api-key' ` +
		`-H 'X-Apple-I-FD-Client-Info: {"U":"browser","F":"fingerprint"}' ` +
		`-H 'scnt: ` + scnt + `'`
	line, failure := parseICloudImportLine(1, "owner@example.com----"+command)
	if failure != nil || len(line.Channels) != 1 || line.Channels[0].Cookie != cookie || line.Channels[0].Scnt != scnt {
		t.Fatalf("browser cURL parse failed: failure=%#v", failure)
	}

	tooLong := strings.Replace(command, scnt, scnt+"x", 1)
	if _, failure = parseICloudImportLine(1, "owner@example.com----"+tooLong); failure == nil {
		t.Fatal("Apple Account value above 1000 characters must be rejected")
	}
}

func TestParseICloudImportRequiresAppleAccountPrivateEmailListCurl(t *testing.T) {
	commands := []string{
		`curl --url 'https://appleid.apple.com/account/manage/gs/ws/token' -b 'myacinfo=secret' -H 'X-Apple-Api-Key: api-key' -H 'scnt: value'`,
		`curl --url 'https://appleid.apple.com/account/manage/email/private' -b 'myacinfo=secret' -H 'X-Apple-Api-Key: api-key'`,
		`curl --url 'https://appleid.apple.com/account/manage/email/private' -b 'myacinfo=secret' -H 'scnt: value'`,
	}
	for _, command := range commands {
		line, failure := parseICloudImportLine(1, "owner@example.com----"+command)
		if line != nil || failure == nil || failure.SafeMessage != "Copy the Apple Account private email list request as cURL." {
			t.Fatalf("incomplete Apple Account request accepted: line=%#v failure=%#v", line, failure)
		}
	}
}

func TestParseICloudImportRejectsDuplicateOrUnsafeChannels(t *testing.T) {
	tests := []string{
		"owner@icloud.com----" + testICloudOldCurl + "----" + testICloudOldCurl,
		"owner@icloud.com----curl 'https://p119-maildomainws.icloud.com.evil.example/v2/hme/list?clientBuildNumber=build&clientMasteringNumber=master&clientId=client&dsid=123' -H 'Cookie: " + testICloudOldCookie + "'",
		"owner@icloud.com----" + strings.Replace(testICloudNewCurl, testICloudFDClientInfo, strings.Repeat("x", 2049), 1),
		"owner@icloud.com",
	}
	for _, content := range tests {
		lines, failures, fatal := parseICloudImport(content, coreDomain.ImportErrorStrategySkip)
		if fatal != nil || len(lines) != 0 || len(failures) != 1 || failures[0].Category != "invalid_format" {
			t.Fatalf("unexpected invalid result: lines=%#v failures=%#v fatal=%#v", lines, failures, fatal)
		}
		if strings.Contains(failures[0].SafeMessage, "evil.example") || strings.Contains(failures[0].SafeMessage, "secret") {
			t.Fatalf("credential leaked into safe error: %q", failures[0].SafeMessage)
		}
	}
}

func TestParseICloudImportKeepsSeparatorsInsideCookie(t *testing.T) {
	content := strings.Replace(testICloudOldCurl, "session", "session----curl-inside", 1)
	line, failure := parseICloudImportLine(7, "Owner@icloud.com----"+content)
	if failure != nil {
		t.Fatalf("parse failure: %#v", failure)
	}
	if line.PrimaryEmail != "owner@icloud.com" || line.Channels[0].Cookie != strings.Replace(testICloudOldCookie, "session", "session----curl-inside", 1) {
		t.Fatalf("unexpected parsed line: %#v", line)
	}
}

func TestParseICloudImportJoinsBrowserCurlContinuations(t *testing.T) {
	content := "owner@icloud.com----curl --url 'https://appleid.apple.com/account/manage/email/private' \\\r\n" +
		"  -b '" + testICloudNewCookie + "' \\\r\n" +
		"  -H 'X-Apple-Api-Key: api-key' \\\r\n" +
		"  -H 'scnt: value'\nsecond@icloud.com----" + testICloudOldCurl
	lines, failures, fatal := parseICloudImport(content, coreDomain.ImportErrorStrategyAbort)
	if fatal != nil || len(failures) != 0 || len(lines) != 2 || lines[0].LineNumber != 1 || lines[1].LineNumber != 2 {
		t.Fatalf("unexpected multiline parse: lines=%#v failures=%#v fatal=%#v", lines, failures, fatal)
	}
}

func TestTokenizeICloudCurlDoesNotExecuteShellSyntax(t *testing.T) {
	tokens, err := tokenizeICloudCurl(`curl --url 'https://appleid.apple.com/account/manage/' -H 'X-Test: $(touch /tmp/should-not-exist)' -b 'a=b'`)
	if err != nil {
		t.Fatalf("tokenize cURL: %v", err)
	}
	if len(tokens) != 7 || tokens[0] != "curl" || tokens[1] != "--url" || tokens[5] != "-b" || tokens[6] != "a=b" {
		t.Fatalf("unexpected shell tokens: %#v", tokens)
	}
}
