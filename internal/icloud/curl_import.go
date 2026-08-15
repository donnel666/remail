package icloud

import (
	"errors"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

var errICloudAppleAccountListCurl = errors.New("icloud: Apple Account private email list cURL required")

// iCloudImportChannel is the parsed, provider-specific part of one import
// line.  The raw cURL is never put on a task payload or returned by an API.
type iCloudImportChannel struct {
	Kind                  string
	Host                  string
	Cookie                string
	Origin                string
	Referer               string
	UserAgent             string
	FDClientInfo          string
	APIKey                string
	DSID                  string
	ClientID              string
	ClientBuildNumber     string
	ClientMasteringNumber string
	Scnt                  string
}

// splitICloudImportLineParts accepts exactly:
//
//	email----curl [----curl]
//
// A cURL can contain the separator in a quoted Cookie value.  A separator is
// therefore considered a channel boundary only when the following token is a
// new cURL command.
func splitICloudImportLineParts(raw string) (email string, curls []string, ok bool) {
	raw = strings.TrimSpace(raw)
	first := strings.Index(raw, "----")
	if first <= 0 {
		return "", nil, false
	}
	email = strings.TrimSpace(raw[:first])
	rest := strings.TrimSpace(raw[first+4:])
	if email == "" || rest == "" {
		return "", nil, false
	}

	boundaries := iCloudCurlChannelBoundaries(rest)
	if len(boundaries) > 1 {
		return "", nil, false
	}
	if len(boundaries) == 1 {
		left := strings.TrimSpace(rest[:boundaries[0]])
		right := strings.TrimSpace(rest[boundaries[0]+4:])
		if left == "" || right == "" {
			return "", nil, false
		}
		curls = []string{left, right}
	} else {
		curls = []string{rest}
	}
	if len(curls) < 1 || len(curls) > 2 {
		return "", nil, false
	}
	for _, command := range curls {
		if !strings.EqualFold(firstICloudCurlToken(command), "curl") {
			return "", nil, false
		}
	}
	return email, curls, true
}

func parseICloudCurlImportLine(lineNumber int, raw string) (*iCloudImportLine, *iCloudImportFailure) {
	email, curls, ok := splitICloudImportLineParts(raw)
	email = strings.ToLower(strings.TrimSpace(email))
	failure := func(message string) (*iCloudImportLine, *iCloudImportFailure) {
		return nil, &iCloudImportFailure{Line: lineNumber, Email: email, Category: "invalid_format", SafeMessage: message}
	}
	if !ok || !isICloudImportEmail(email) {
		return failure("Invalid iCloud import format.")
	}
	line := &iCloudImportLine{LineNumber: lineNumber, PrimaryEmail: email}
	seen := make(map[string]struct{}, len(curls))
	for _, command := range curls {
		channel, err := parseICloudCurlChannel(command)
		if err != nil {
			if errors.Is(err, errICloudAppleAccountListCurl) {
				return failure("Copy the Apple Account private email list request as cURL.")
			}
			return failure("Invalid iCloud cURL import format.")
		}
		if _, exists := seen[channel.Kind]; exists {
			return failure("Duplicate iCloud cURL channel.")
		}
		seen[channel.Kind] = struct{}{}
		line.Channels = append(line.Channels, *channel)
	}
	if len(line.Channels) == 0 || len(line.Channels) > 2 {
		return failure("Invalid iCloud cURL channel count.")
	}
	return line, nil
}

func iCloudCurlChannelBoundaries(value string) []int {
	boundaries := make([]int, 0, 1)
	var quote byte
	escaped := false
	for index := 0; index < len(value); index++ {
		current := value[index]
		if escaped {
			escaped = false
			continue
		}
		if current == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if current == quote {
				quote = 0
			}
			continue
		}
		if current == '\'' || current == '"' {
			quote = current
			continue
		}
		if index+4 <= len(value) && value[index:index+4] == "----" &&
			strings.EqualFold(firstICloudCurlToken(strings.TrimSpace(value[index+4:])), "curl") {
			boundaries = append(boundaries, index)
			index += 3
		}
	}
	return boundaries
}

func parseICloudCurlChannel(command string) (*iCloudImportChannel, error) {
	tokens, err := tokenizeICloudCurl(command)
	if err != nil || len(tokens) == 0 || !strings.EqualFold(tokens[0], "curl") {
		return nil, errors.New("invalid cURL")
	}
	requestURL, cookie, headers, invalidHeader := extractICloudCurlArguments(tokens)
	if invalidHeader {
		return nil, errors.New("invalid header")
	}
	parsed, err := url.Parse(strings.TrimSpace(requestURL))
	if err != nil || parsed.Scheme == "" || !strings.EqualFold(parsed.Scheme, "https") || parsed.User != nil || parsed.Hostname() == "" {
		return nil, errors.New("invalid URL")
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return nil, errors.New("invalid port")
	}
	host := strings.ToLower(parsed.Hostname())
	path := parsed.EscapedPath()
	channel := &iCloudImportChannel{Host: host, Cookie: strings.TrimSpace(cookie)}
	channel.Origin = strings.TrimSpace(headers["origin"])
	channel.Referer = strings.TrimSpace(headers["referer"])
	channel.UserAgent = strings.TrimSpace(headers["user-agent"])
	channel.FDClientInfo = strings.TrimSpace(headers["x-apple-i-fd-client-info"])
	if channel.Cookie == "" {
		return nil, errors.New("missing cookie")
	}
	if !validICloudImportCookie(channel.Cookie) && !validAppleAccountCookie(channel.Cookie) {
		return nil, errors.New("invalid cookie")
	}
	if strings.EqualFold(host, "appleid.apple.com") || strings.EqualFold(host, "appleid.apple.com.cn") {
		if path != appleAccountPrivateEmailPath {
			return nil, errICloudAppleAccountListCurl
		}
		channel.Kind = iCloudChannelAppleAccount
		channel.Scnt = strings.TrimSpace(headers["scnt"])
		if channel.Scnt == "" {
			channel.Scnt = strings.TrimSpace(headers["x-apple-scnt"])
		}
		channel.APIKey = strings.TrimSpace(headers["x-apple-api-key"])
		if !validICloudImportValue(channel.Scnt, iCloudAppleAccountValueMaxLength) ||
			!validICloudImportValue(channel.APIKey, iCloudAppleAccountValueMaxLength) {
			return nil, errICloudAppleAccountListCurl
		}
		if channel.Origin == "" {
			channel.Origin = defaultAppleAccountOrigin(host)
		}
		if channel.Referer == "" {
			channel.Referer = strings.TrimRight(channel.Origin, "/") + "/"
		}
		if channel.UserAgent == "" {
			channel.UserAgent = defaultICloudHMEUserAgent
		}
		return channel, nil
	}
	if !validICloudHMEHost(host) || (!strings.HasPrefix(path, "/v2/hme/list") && !strings.HasPrefix(path, "/v1/hme/")) {
		return nil, errors.New("invalid web HME path")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return nil, errors.New("invalid query")
	}
	channel.Kind = iCloudChannelWeb
	var found bool
	if channel.DSID, found = iCloudCurlQueryValue(query, "dsid"); !found || !validICloudImportValue(channel.DSID, iCloudImportDSIDMaxLength) {
		return nil, errors.New("missing dsid")
	}
	if channel.ClientID, found = iCloudCurlQueryValue(query, "clientId"); !found || !validICloudImportValue(channel.ClientID, iCloudImportClientMaxLength) {
		return nil, errors.New("missing clientId")
	}
	if channel.ClientBuildNumber, found = iCloudCurlQueryValue(query, "clientBuildNumber"); !found || !validICloudImportValue(channel.ClientBuildNumber, iCloudImportBuildMaxLength) {
		return nil, errors.New("missing build")
	}
	if channel.ClientMasteringNumber, found = iCloudCurlQueryValue(query, "clientMasteringNumber"); !found || !validICloudImportValue(channel.ClientMasteringNumber, iCloudImportBuildMaxLength) {
		return nil, errors.New("missing mastering")
	}
	lang, origin, referer := defaultICloudHMEContext(host)
	if channel.Origin == "" {
		channel.Origin = origin
	}
	if channel.Referer == "" {
		channel.Referer = referer
	}
	if channel.UserAgent == "" {
		channel.UserAgent = defaultICloudHMEUserAgent
	}
	_ = lang
	return channel, nil
}

func defaultAppleAccountOrigin(host string) string {
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(host)), ".cn") {
		return "https://account.apple.com.cn"
	}
	return "https://account.apple.com"
}

func validAppleAccountCookie(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && utf8.ValidString(value) && len(value) <= iCloudImportCookieMaxBytes && !strings.ContainsAny(value, "\r\n")
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

func extractICloudCurlArguments(tokens []string) (requestURL, cookie string, headers map[string]string, invalidHeader bool) {
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
			name = strings.ToLower(strings.TrimSpace(name))
			headerValue = strings.TrimSpace(headerValue)
			if !found || name == "" {
				invalidHeader = true
			} else {
				switch name {
				case "cookie":
					if validAppleAccountCookie(headerValue) {
						headers[name] = headerValue
					} else {
						invalidHeader = true
					}
				case "origin", "referer", "user-agent", "x-apple-api-key", "x-apple-i-fd-client-info", "scnt", "x-apple-scnt":
					if validICloudCurlHeader(headerValue) {
						headers[name] = headerValue
					} else {
						invalidHeader = true
					}
				}
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
	return requestURL, cookie, headers, invalidHeader
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

func tokenizeICloudCurl(command string) ([]string, error) {
	var tokens []string
	var value strings.Builder
	var quote rune
	escaped, started := false, false
	flush := func() {
		if started {
			tokens = append(tokens, value.String())
			value.Reset()
			started = false
		}
	}
	for _, r := range command {
		if escaped {
			if r != '\n' {
				value.WriteRune(r)
				started = true
			}
			escaped = false
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else if quote == '"' && r == '\\' {
				escaped = true
			} else {
				value.WriteRune(r)
			}
			started = true
			continue
		}
		switch {
		case r == '\\':
			escaped = true
		case r == '\'' || r == '"':
			quote = r
			started = true
		case unicode.IsSpace(r):
			flush()
		default:
			value.WriteRune(r)
			started = true
		}
	}
	if escaped || quote != 0 {
		return nil, errors.New("unterminated cURL quoting")
	}
	flush()
	return tokens, nil
}
