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
	testICloudNewCurl      = `curl --url 'https://appleid.apple.com/account/manage/gs/ws/token' -b 'myacinfo=secret' -H 'X-Apple-I-FD-Client-Info: ` + testICloudFDClientInfo + `'`
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
				if kind == iCloudChannelAppleAccount && line.Channels[index].FDClientInfo != testICloudFDClientInfo {
					t.Fatalf("Apple Account FD client info = %q", line.Channels[index].FDClientInfo)
				}
			}
		})
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
