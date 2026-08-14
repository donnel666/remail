package icloud

import (
	"strings"
	"testing"

	coreDomain "github.com/donnel666/remail/internal/core/domain"
)

const (
	testICloudOldCookie    = "X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token; X-APPLE-DS-WEB-SESSION-TOKEN=session"
	testICloudFDClientInfo = `{"F":"test-fingerprint"}`
	testICloudOldCurl      = `curl --url 'https://p119-maildomainws.icloud.com.cn/v2/hme/list?clientBuildNumber=build&clientMasteringNumber=master&clientId=client&dsid=123' -b '` + testICloudOldCookie + `'`
)

var (
	testICloudLongScnt = strings.Repeat("s", 400)
	testICloudNewCurl  = `curl --url 'https://appleid.apple.com/account/manage/gs/ws/token' -b 'myacinfo=secret' -H 'X-Apple-I-FD-Client-Info: ` + testICloudFDClientInfo + `' -H 'scnt: ` + testICloudLongScnt + `'`
)

func TestParseICloudImportSupportsCompleteCredentialLines(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantKinds []string
	}{
		{name: "old channel", line: "owner@icloud.com----app-password----" + testICloudOldCurl, wantKinds: []string{iCloudChannelWeb}},
		{name: "new channel", line: "owner@icloud.com----app-password----" + testICloudNewCurl, wantKinds: []string{iCloudChannelAppleAccount}},
		{name: "both channels", line: "owner@icloud.com----app-password----" + testICloudNewCurl + "----" + testICloudOldCurl, wantKinds: []string{iCloudChannelAppleAccount, iCloudChannelWeb}},
		{name: "both channels reversed", line: "owner@icloud.com----app-password----" + testICloudOldCurl + "----" + testICloudNewCurl, wantKinds: []string{iCloudChannelWeb, iCloudChannelAppleAccount}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lines, failures, fatal := parseICloudImport(test.line, coreDomain.ImportErrorStrategyAbort)
			if fatal != nil || len(failures) != 0 || len(lines) != 1 {
				t.Fatalf("unexpected parse result: lines=%#v failures=%#v fatal=%#v", lines, failures, fatal)
			}
			line := lines[0]
			if line.PrimaryEmail != "owner@icloud.com" || line.AppPassword != "app-password" || len(line.Channels) != len(test.wantKinds) {
				t.Fatalf("unexpected credentials: %#v", line)
			}
			for index, kind := range test.wantKinds {
				if line.Channels[index].Kind != kind {
					t.Fatalf("channel %d kind = %q, want %q", index, line.Channels[index].Kind, kind)
				}
				if kind == iCloudChannelAppleAccount &&
					(line.Channels[index].FDClientInfo != testICloudFDClientInfo || line.Channels[index].Scnt != testICloudLongScnt) {
					t.Fatalf("unexpected Apple Account request context")
				}
			}
		})
	}
}

func TestParseICloudImportAcceptsBrowserCopiedOpaqueValues(t *testing.T) {
	scnt := strings.Repeat("s", iCloudAppleAccountValueMaxLength)
	cookie := "POD=cn~zh; myacinfo=" + strings.Repeat("a", 4096)
	command := `curl --url 'https://appleid.apple.com/account/manage/gs/ws/token' ` +
		`-H 'Accept: application/json, text/plain, */*' ` +
		`-H 'X-Ignored-Browser-Context: ` + strings.Repeat("x", 4096) + `' ` +
		`-H 'Cookie: ` + cookie + `' ` +
		`-H 'Origin: https://account.apple.com' ` +
		`-H 'X-Apple-I-FD-Client-Info: {"U":"browser","F":"fingerprint"}' ` +
		`-H 'scnt: ` + scnt + `'`
	line, failure := parseICloudImportLine(1, "owner@example.com----app-password----"+command)
	if failure != nil || len(line.Channels) != 1 || line.Channels[0].Cookie != cookie || line.Channels[0].Scnt != scnt {
		t.Fatalf("browser cURL parse failed: failure=%#v", failure)
	}

	tooLong := strings.Replace(command, scnt, scnt+"x", 1)
	if _, failure = parseICloudImportLine(1, "owner@example.com----app-password----"+tooLong); failure == nil {
		t.Fatal("Apple Account value above 1000 characters must be rejected")
	}
}

func TestParseICloudImportRejectsDuplicateOrUnsafeChannels(t *testing.T) {
	tests := []string{
		"owner@icloud.com----app-password----" + testICloudOldCurl + "----" + testICloudOldCurl,
		"owner@icloud.com----app-password----curl 'https://p119-maildomainws.icloud.com.evil.example/v2/hme/list?clientBuildNumber=build&clientMasteringNumber=master&clientId=client&dsid=123' -H 'Cookie: " + testICloudOldCookie + "'",
		"owner@icloud.com----app-password----" + strings.Replace(testICloudNewCurl, testICloudFDClientInfo, strings.Repeat("x", 2049), 1),
		"owner@icloud.com----app-password",
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
	content := strings.Replace(testICloudOldCurl, "session", "session----inside", 1)
	line, failure := parseICloudImportLine(7, "Owner@icloud.com----app-password----"+content)
	if failure != nil {
		t.Fatalf("parse failure: %#v", failure)
	}
	if line.PrimaryEmail != "owner@icloud.com" || line.Channels[0].Cookie != strings.Replace(testICloudOldCookie, "session", "session----inside", 1) {
		t.Fatalf("unexpected parsed line: %#v", line)
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
