package icloud

import (
	"strings"
	"testing"

	coreDomain "github.com/donnel666/remail/internal/core/domain"
)

func TestParseICloudImportCurlExtractsRuntimeContext(t *testing.T) {
	const cookie = `X-APPLE-WEBAUTH-USER="v=1:s=0:d=16583180622"; X-APPLE-WEBAUTH-TOKEN="token"; X-APPLE-DS-WEB-SESSION-TOKEN="session----inside"`
	content := "main@icloud.com----curl --url 'https://p217-maildomainws.icloud.com.cn/v2/hme/list?clientBuildNumber=2628Build19&clientMasteringNumber=2628Build19&clientId=client-1&dsid=16583180622' \\\n  -H 'Origin: https://www.icloud.com.cn' \\\n  -H 'Referer: https://www.icloud.com.cn/' \\\n  -H 'User-Agent: Mozilla/5.0 Test Browser' \\\n  -b '" + cookie + "'"

	lines, failures, fatal := parseICloudImport(content, coreDomain.ImportErrorStrategyAbort)
	if fatal != nil || len(failures) != 0 || len(lines) != 1 {
		t.Fatalf("unexpected cURL parse result: lines=%#v failures=%#v fatal=%#v", lines, failures, fatal)
	}
	line := lines[0]
	if line.PrimaryEmail != "main@icloud.com" || line.Host != "p217-maildomainws.icloud.com.cn" ||
		line.DSID != "16583180622" || line.ClientID != "client-1" || line.ClientBuildNumber != "2628Build19" ||
		line.ClientMasteringNumber != "2628Build19" || line.Cookie != cookie {
		t.Fatalf("unexpected parsed credentials: %#v", line)
	}
	if line.LangCode != "zh-cn" || line.Origin != "https://www.icloud.com.cn" || line.Referer != "https://www.icloud.com.cn/" || line.UserAgent != "Mozilla/5.0 Test Browser" {
		t.Fatalf("unexpected parsed request context: %#v", line)
	}
}

func TestParseICloudImportCurlSupportsHeaderCookieAndBareURL(t *testing.T) {
	content := `main@icloud.com----curl https://p119-maildomainws.icloud.com/v2/hme/list?clientBuildNumber=build&clientMasteringNumber=master&clientId=client&dsid=123 -H "Cookie: X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token; X-APPLE-DS-WEB-SESSION-TOKEN=session"`
	lines, failures, fatal := parseICloudImport(content, coreDomain.ImportErrorStrategyAbort)
	if fatal != nil || len(failures) != 0 || len(lines) != 1 {
		t.Fatalf("unexpected header-cookie parse result: lines=%#v failures=%#v fatal=%#v", lines, failures, fatal)
	}
	if lines[0].Host != "p119-maildomainws.icloud.com" || lines[0].Cookie != "X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token; X-APPLE-DS-WEB-SESSION-TOKEN=session" {
		t.Fatalf("unexpected parsed header-cookie context: %#v", lines[0])
	}
}

func TestICloudCurlDetectionDoesNotCaptureOrdinaryEmailStartingWithCurl(t *testing.T) {
	content := "curl-user@icloud.com----p119-maildomainws.icloud.com----123----client----build----master----X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token; X-APPLE-DS-WEB-SESSION-TOKEN=session"
	lines, failures, fatal := parseICloudImport(content, coreDomain.ImportErrorStrategyAbort)
	if fatal != nil || len(failures) != 0 || len(lines) != 1 || lines[0].PrimaryEmail != "curl-user@icloud.com" {
		t.Fatalf("ordinary import was misclassified as cURL: lines=%#v failures=%#v fatal=%#v", lines, failures, fatal)
	}
}

func TestParseICloudImportCurlRequiresPrimaryEmailAndSafeHMEURL(t *testing.T) {
	unsafe := `curl 'https://p119-maildomainws.icloud.com.evil.example/v2/hme/list?clientBuildNumber=build&clientMasteringNumber=master&clientId=client&dsid=123' -b 'X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token; X-APPLE-DS-WEB-SESSION-TOKEN=session'`
	_, failures, fatal := parseICloudImport(unsafe, coreDomain.ImportErrorStrategySkip)
	if fatal != nil || len(failures) != 1 || failures[0].Category != "missing_primary_email" {
		t.Fatalf("unexpected bare/unsafe cURL result: failures=%#v fatal=%#v", failures, fatal)
	}

	wrapped := "main@icloud.com----" + unsafe
	_, failures, fatal = parseICloudImport(wrapped, coreDomain.ImportErrorStrategySkip)
	if fatal != nil || len(failures) != 1 || failures[0].Category != "invalid_format" {
		t.Fatalf("unexpected unsafe URL result: failures=%#v fatal=%#v", failures, fatal)
	}
	if strings.Contains(failures[0].SafeMessage, "evil.example") {
		t.Fatalf("unsafe URL leaked into safe error: %q", failures[0].SafeMessage)
	}
}

func TestTokenizeICloudCurlDoesNotExecuteShellSyntax(t *testing.T) {
	tokens, err := tokenizeICloudCurl(`curl --url 'https://p119-maildomainws.icloud.com/v2/hme/list?a=1' -H 'X-Test: $(touch /tmp/should-not-exist)' \` + "\n" + `  -b 'a=b'`)
	if err != nil {
		t.Fatalf("tokenize cURL: %v", err)
	}
	if len(tokens) != 7 || tokens[0] != "curl" || tokens[1] != "--url" || tokens[5] != "-b" || tokens[6] != "a=b" {
		t.Fatalf("unexpected shell tokens: %#v", tokens)
	}
}
