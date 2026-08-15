package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	stdmail "net/mail"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

const (
	maxInboundHeaderRunes  = 500
	maxInboundPreviewRunes = 1000
	maxInboundBodyBytes    = 1024 * 1024
	maxInboundBodyRunes    = 200000
	maxInboundMIMEDepth    = 12
)

type parsedInboundMessage struct {
	Summary    domain.InboundMailSummary
	Body       string
	Diagnostic string
}

func parseInboundMessage(raw []byte, fallbackReceivedAt time.Time) parsedInboundMessage {
	now := time.Now().UTC()
	if fallbackReceivedAt.IsZero() {
		fallbackReceivedAt = now
	}
	result := parsedInboundMessage{
		Summary: domain.InboundMailSummary{
			ReceivedAt: fallbackReceivedAt.UTC(),
			ParsedAt:   now,
		},
	}
	message, err := stdmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		result.Diagnostic = "Message content could not be parsed."
		return result
	}

	decoder := new(mime.WordDecoder)
	result.Summary.HeaderFrom = safeInboundSender(decodeInboundMIMEHeader(decoder, message.Header.Get("From")))
	result.Summary.Subject = truncateInboundRunes(
		safeInboundSingleLine(decodeInboundMIMEHeader(decoder, message.Header.Get("Subject"))),
		runtimeconfig.Int("max_inbound_header_runes", maxInboundHeaderRunes, 1),
	)
	result.Summary.MessageIDHeader = truncateInboundRunes(
		safeInboundSingleLine(strings.Trim(strings.TrimSpace(message.Header.Get("Message-Id")), "<>")),
		runtimeconfig.Int("max_inbound_header_runes", maxInboundHeaderRunes, 1),
	)
	if receivedAt, dateErr := stdmail.ParseDate(message.Header.Get("Date")); dateErr == nil && !receivedAt.IsZero() {
		result.Summary.ReceivedAt = receivedAt.UTC()
	}

	body, truncated, bodyErr := readInboundMIMEBody(
		message.Header.Get("Content-Type"),
		message.Header.Get("Content-Transfer-Encoding"),
		message.Header.Get("Content-Disposition"),
		message.Body,
		0,
	)
	body = safeInboundBody(body)
	if bodyErr != nil && body == "" {
		result.Diagnostic = "Message content could not be parsed."
	}
	if truncated {
		result.Diagnostic = "Message body was truncated for safe display."
	}
	result.Body = truncateInboundRunes(body, runtimeconfig.Int("max_inbound_body_runes", maxInboundBodyRunes, 1))
	result.Summary.BodyPreview = truncateInboundRunes(
		strings.Join(strings.Fields(result.Body), " "),
		runtimeconfig.Int("max_inbound_preview_runes", maxInboundPreviewRunes, 1),
	)
	result.Summary.VerificationCode = truncateInboundRunes(
		extractInboundVerificationCode(result.Summary.Subject+" "+result.Body),
		64,
	)
	return result
}

// ParseInboundMessageSummary exposes the bounded parser to workflows that
// already own access to the private RFC822 object.
func ParseInboundMessageSummary(raw []byte, fallbackReceivedAt time.Time) domain.InboundMailSummary {
	return parseInboundMessage(raw, fallbackReceivedAt).Summary
}

func decodeInboundMIMEHeader(decoder *mime.WordDecoder, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	decoded, err := decoder.DecodeHeader(value)
	if err != nil {
		return value
	}
	return decoded
}

func safeInboundSender(value string) string {
	value = safeInboundSingleLine(value)
	if value == "" {
		return ""
	}
	if address, err := stdmail.ParseAddress(value); err == nil {
		return truncateInboundRunes(strings.ToLower(strings.TrimSpace(address.Address)), 320)
	}
	return truncateInboundRunes(value, 320)
}

func safeInboundSingleLine(value string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)), " ")
}

func safeInboundBody(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\t':
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	return strings.TrimSpace(value)
}

func readInboundMIMEBody(contentType, transferEncoding, disposition string, body io.Reader, depth int) (string, bool, error) {
	if depth >= runtimeconfig.Int("max_inbound_mime_depth", maxInboundMIMEDepth, 1) {
		return "", false, io.ErrUnexpectedEOF
	}
	if mediaType, _, err := mime.ParseMediaType(disposition); err == nil && strings.EqualFold(mediaType, "attachment") {
		return "", false, nil
	}

	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = "text/plain"
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := strings.TrimSpace(params["boundary"])
		if boundary == "" {
			return "", false, io.ErrUnexpectedEOF
		}
		reader := multipart.NewReader(body, boundary)
		var htmlFallback string
		var htmlTruncated bool
		for {
			part, partErr := reader.NextPart()
			if partErr == io.EOF {
				break
			}
			if partErr != nil {
				return htmlFallback, htmlTruncated, partErr
			}
			partBody, partTruncated, readErr := readInboundMIMEBody(
				part.Header.Get("Content-Type"),
				part.Header.Get("Content-Transfer-Encoding"),
				part.Header.Get("Content-Disposition"),
				part,
				depth+1,
			)
			if readErr != nil || strings.TrimSpace(partBody) == "" {
				continue
			}
			partType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
			switch strings.ToLower(strings.TrimSpace(partType)) {
			case "text/plain":
				return partBody, partTruncated, nil
			case "text/html":
				if htmlFallback == "" {
					htmlFallback = stripInboundHTML(partBody)
					htmlTruncated = partTruncated
				}
			default:
				if strings.HasPrefix(strings.ToLower(partType), "multipart/") && htmlFallback == "" {
					htmlFallback = partBody
					htmlTruncated = partTruncated
				}
			}
		}
		return htmlFallback, htmlTruncated, nil
	}
	if mediaType != "text/plain" && mediaType != "text/html" && mediaType != "" {
		return "", false, nil
	}

	reader := decodeInboundTransferReader(body, transferEncoding)
	maxBodyBytes := runtimeconfig.Int("max_inbound_body_bytes", maxInboundBodyBytes, 1)
	data, readErr := io.ReadAll(io.LimitReader(reader, int64(maxBodyBytes)+1))
	truncated := len(data) > maxBodyBytes
	if truncated {
		data = data[:maxBodyBytes]
	}
	text := string(data)
	if mediaType == "text/html" {
		text = stripInboundHTML(text)
	}
	return text, truncated, readErr
}

func decodeInboundTransferReader(body io.Reader, transferEncoding string) io.Reader {
	switch strings.ToLower(strings.TrimSpace(transferEncoding)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, body)
	case "quoted-printable":
		return quotedprintable.NewReader(body)
	default:
		return body
	}
}

var (
	inboundHTMLScriptRe = regexp.MustCompile(`(?is)<script\b.*?</script>`)
	inboundHTMLStyleRe  = regexp.MustCompile(`(?is)<style\b.*?</style>`)
	inboundHTMLTagRe    = regexp.MustCompile(`(?s)<[^>]+>`)
)

func stripInboundHTML(value string) string {
	value = inboundHTMLScriptRe.ReplaceAllString(value, " ")
	value = inboundHTMLStyleRe.ReplaceAllString(value, " ")
	value = inboundHTMLTagRe.ReplaceAllString(value, " ")
	return strings.Join(strings.Fields(html.UnescapeString(value)), " ")
}

const inboundVerificationCodePattern = `(?:^|[^\d])(\d{6,8})(?:[^\d]|$)`

func extractInboundVerificationCode(value string) string {
	for _, pattern := range inboundVerificationPatterns() {
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		matches := re.FindStringSubmatch(value)
		if len(matches) == 0 {
			continue
		}
		if len(matches) == 1 {
			return strings.TrimSpace(matches[0])
		}
		for _, match := range matches[1:] {
			if match = strings.TrimSpace(match); match != "" {
				return match
			}
		}
	}
	return ""
}

func inboundVerificationPatterns() []string {
	raw := strings.TrimSpace(runtimeconfig.String("verification_code_pattern", inboundVerificationCodePattern))
	var patterns []string
	if json.Unmarshal([]byte(raw), &patterns) != nil {
		patterns = []string{raw}
	}
	if len(patterns) == 0 {
		return []string{inboundVerificationCodePattern}
	}
	for index, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			return []string{inboundVerificationCodePattern}
		}
		patterns[index] = pattern
	}
	return patterns
}

func truncateInboundRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
