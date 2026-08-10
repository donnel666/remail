package icloud

import (
	"errors"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	coreDomain "github.com/donnel666/remail/internal/core/domain"
)

// looksLikeICloudCurlImport recognizes the explicit import form
// "primaryEmail----curl ..." and routes a bare cURL to the safe missing-email
// error because Apple does not expose primaryEmail in the request context.
func looksLikeICloudCurlImport(content string) bool {
	for _, raw := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.EqualFold(firstICloudCurlToken(line), "curl") {
			return true
		}
		if _, _, ok := splitICloudCurlImportHeader(line); ok {
			return true
		}
		return false
	}
	return false
}

type iCloudCurlImportBlock struct {
	lineNumber int
	primary    string
	command    strings.Builder
}

func parseICloudCurlImport(content string, strategy coreDomain.ImportErrorStrategy) ([]iCloudImportLine, []iCloudImportFailure, *iCloudImportFailure) {
	normalizedStrategy, ok := coreDomain.NormalizeImportErrorStrategy(string(strategy))
	if !ok || !utf8.ValidString(content) {
		return nil, nil, &iCloudImportFailure{Category: "invalid_format", SafeMessage: "Invalid iCloud import format."}
	}
	strategy = normalizedStrategy

	var blocks []iCloudCurlImportBlock
	var current *iCloudCurlImportBlock
	finish := func() {
		if current != nil {
			blocks = append(blocks, *current)
			current = nil
		}
	}
	for lineNumber, raw := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			if current != nil {
				current.command.WriteByte('\n')
			}
			continue
		}
		primary, command, isHeader := splitICloudCurlImportHeader(line)
		if isHeader {
			finish()
			current = &iCloudCurlImportBlock{lineNumber: lineNumber + 1, primary: primary}
			current.command.WriteString(command)
			continue
		}
		if current == nil {
			// A bare cURL has no safe source for the Apple account email.
			failure := &iCloudImportFailure{Line: lineNumber + 1, Category: "missing_primary_email", SafeMessage: "Primary email is required before importing a cURL request."}
			if strategy == coreDomain.ImportErrorStrategyAbort {
				return nil, nil, failure
			}
			return nil, []iCloudImportFailure{*failure}, nil
		}
		current.command.WriteByte('\n')
		current.command.WriteString(raw)
	}
	finish()
	if len(blocks) == 0 {
		return nil, nil, &iCloudImportFailure{Category: "invalid_format", SafeMessage: "Invalid iCloud cURL import format."}
	}

	lines := make([]iCloudImportLine, 0, len(blocks))
	var failures []iCloudImportFailure
	for _, block := range blocks {
		line, failure := parseICloudCurlImportBlock(block.lineNumber, block.primary, block.command.String())
		if failure == nil {
			lines = append(lines, *line)
			continue
		}
		if strategy == coreDomain.ImportErrorStrategyAbort {
			return nil, nil, failure
		}
		failures = append(failures, *failure)
	}
	return lines, failures, nil
}

func splitICloudCurlImportHeader(line string) (primaryEmail, command string, ok bool) {
	separator := strings.Index(line, "----")
	if separator <= 0 {
		return "", "", false
	}
	primaryEmail = strings.TrimSpace(line[:separator])
	command = strings.TrimSpace(line[separator+len("----"):])
	if command == "" {
		return "", "", false
	}
	if !strings.EqualFold(firstICloudCurlToken(command), "curl") {
		return "", "", false
	}
	return primaryEmail, command, true
}

func firstICloudCurlToken(value string) string {
	value = strings.TrimLeftFunc(value, unicode.IsSpace)
	for index, runeValue := range value {
		if unicode.IsSpace(runeValue) || runeValue == '\\' {
			return value[:index]
		}
	}
	return value
}

func parseICloudCurlImportBlock(lineNumber int, primaryEmail, command string) (*iCloudImportLine, *iCloudImportFailure) {
	primaryEmail = strings.ToLower(strings.TrimSpace(primaryEmail))
	failure := func(category, message string) (*iCloudImportLine, *iCloudImportFailure) {
		return nil, &iCloudImportFailure{Line: lineNumber, Email: primaryEmail, Category: category, SafeMessage: message}
	}
	if !isICloudImportEmail(primaryEmail) {
		return failure("invalid_format", "Invalid iCloud import format.")
	}
	tokens, err := tokenizeICloudCurl(command)
	if err != nil || len(tokens) == 0 || !strings.EqualFold(tokens[0], "curl") {
		return failure("invalid_format", "Invalid iCloud cURL import format.")
	}
	requestURL, cookie, headers := extractICloudCurlArguments(tokens)
	parsed, err := parseICloudCurlHMEURL(requestURL)
	if err != nil {
		return failure("invalid_format", "Invalid iCloud cURL import URL.")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return failure("invalid_format", "Invalid iCloud cURL query parameters.")
	}
	dsid, ok := iCloudCurlQueryValue(query, "dsid")
	if !ok || !validICloudImportValue(dsid, iCloudImportDSIDMaxLength) {
		return failure("invalid_format", "Invalid iCloud cURL query parameters.")
	}
	clientID, ok := iCloudCurlQueryValue(query, "clientId")
	if !ok || !validICloudImportValue(clientID, iCloudImportClientMaxLength) {
		return failure("invalid_format", "Invalid iCloud cURL query parameters.")
	}
	build, ok := iCloudCurlQueryValue(query, "clientBuildNumber")
	if !ok || !validICloudImportValue(build, iCloudImportBuildMaxLength) {
		return failure("invalid_format", "Invalid iCloud cURL query parameters.")
	}
	mastering, ok := iCloudCurlQueryValue(query, "clientMasteringNumber")
	if !ok || !validICloudImportValue(mastering, iCloudImportBuildMaxLength) {
		return failure("invalid_format", "Invalid iCloud cURL query parameters.")
	}
	if !validICloudImportCookie(cookie) {
		return failure("invalid_format", "Invalid iCloud cURL Cookie.")
	}
	langCode, origin, referer := defaultICloudHMEContext(parsed.Hostname())
	if value := strings.TrimSpace(headers["origin"]); value != "" {
		if !validICloudCurlHeader(value) {
			return failure("invalid_format", "Invalid iCloud cURL Origin header.")
		}
		origin = value
	}
	if value := strings.TrimSpace(headers["referer"]); value != "" {
		if !validICloudCurlHeader(value) {
			return failure("invalid_format", "Invalid iCloud cURL Referer header.")
		}
		referer = value
	}
	userAgent := defaultICloudHMEUserAgent
	if value := strings.TrimSpace(headers["user-agent"]); value != "" {
		if !validICloudCurlHeader(value) {
			return failure("invalid_format", "Invalid iCloud cURL User-Agent header.")
		}
		userAgent = value
	}
	return &iCloudImportLine{
		LineNumber: lineNumber, PrimaryEmail: primaryEmail, Host: strings.ToLower(parsed.Hostname()), DSID: dsid,
		ClientID: clientID, ClientBuildNumber: build, ClientMasteringNumber: mastering, Cookie: cookie,
		LangCode: langCode, Origin: origin, Referer: referer, UserAgent: userAgent,
	}, nil
}

func parseICloudCurlHMEURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || !strings.EqualFold(parsed.Scheme, "https") || parsed.User != nil {
		return nil, errors.New("invalid HME URL")
	}
	if parsed.Hostname() == "" || !validICloudHMEHost(parsed.Hostname()) {
		return nil, errors.New("invalid HME host")
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return nil, errors.New("invalid HME port")
	}
	if !strings.HasPrefix(parsed.Path, "/v2/hme/list") && !strings.HasPrefix(parsed.Path, "/v1/hme/") {
		return nil, errors.New("invalid HME path")
	}
	return parsed, nil
}

func iCloudCurlQueryValue(query url.Values, key string) (string, bool) {
	values, ok := query[key]
	if !ok || len(values) != 1 {
		return "", false
	}
	value := strings.TrimSpace(values[0])
	return value, value != ""
}

func validICloudCurlHeader(value string) bool {
	return utf8.ValidString(value) && len(value) <= 2048 && !strings.ContainsAny(value, "\r\n")
}

func extractICloudCurlArguments(tokens []string) (requestURL, cookie string, headers map[string]string) {
	headers = make(map[string]string)
	for index := 1; index < len(tokens); index++ {
		token := tokens[index]
		value, matched, consumedNext := iCloudCurlOptionValue(tokens, index, token, "--url", "")
		if matched {
			if requestURL == "" {
				requestURL = value
			}
			if consumedNext {
				index++
			}
			continue
		}
		value, matched, consumedNext = iCloudCurlOptionValue(tokens, index, token, "--cookie", "-b")
		if matched {
			if cookie == "" {
				cookie = value
			}
			if consumedNext {
				index++
			}
			continue
		}
		value, matched, consumedNext = iCloudCurlOptionValue(tokens, index, token, "--header", "-H")
		if matched {
			name, headerValue, found := strings.Cut(value, ":")
			if found {
				headers[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(headerValue)
			}
			if consumedNext {
				index++
			}
			continue
		}
		if requestURL == "" && (strings.HasPrefix(strings.ToLower(token), "https://") || strings.HasPrefix(strings.ToLower(token), "http://")) {
			requestURL = token
		}
	}
	if cookie == "" {
		cookie = headers["cookie"]
	}
	return requestURL, cookie, headers
}

func iCloudCurlOptionValue(tokens []string, index int, token, longName, shortName string) (value string, matched, consumedNext bool) {
	if token == longName || (shortName != "" && token == shortName) {
		if index+1 >= len(tokens) {
			return "", true, false
		}
		return tokens[index+1], true, true
	}
	if strings.HasPrefix(token, longName+"=") {
		return strings.TrimPrefix(token, longName+"="), true, false
	}
	if shortName != "" && strings.HasPrefix(token, shortName) && len(token) > len(shortName) {
		return token[len(shortName):], true, false
	}
	return "", false, false
}

// tokenizeICloudCurl accepts the quoting emitted by browser "copy as cURL"
// output. It never executes shell syntax; it only removes quotes and line
// continuation backslashes.
func tokenizeICloudCurl(command string) ([]string, error) {
	var tokens []string
	var value strings.Builder
	var quote rune
	escaped := false
	started := false
	flush := func() {
		if started {
			tokens = append(tokens, value.String())
			value.Reset()
			started = false
		}
	}
	for _, runeValue := range command {
		if escaped {
			if runeValue != '\n' {
				value.WriteRune(runeValue)
				started = true
			}
			escaped = false
			continue
		}
		if quote == '\'' {
			if runeValue == quote {
				quote = 0
			} else {
				value.WriteRune(runeValue)
			}
			started = true
			continue
		}
		if quote == '"' {
			switch runeValue {
			case quote:
				quote = 0
			case '\\':
				escaped = true
			default:
				value.WriteRune(runeValue)
			}
			started = true
			continue
		}
		switch {
		case runeValue == '\\':
			escaped = true
		case runeValue == '\'' || runeValue == '"':
			quote = runeValue
			started = true
		case unicode.IsSpace(runeValue):
			flush()
		default:
			value.WriteRune(runeValue)
			started = true
		}
	}
	if escaped || quote != 0 {
		return nil, errors.New("unterminated cURL quoting")
	}
	flush()
	return tokens, nil
}
