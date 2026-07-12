package app

import (
	"bytes"
	"encoding/base64"
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
		maxInboundHeaderRunes,
	)
	result.Summary.MessageIDHeader = truncateInboundRunes(
		safeInboundSingleLine(strings.Trim(strings.TrimSpace(message.Header.Get("Message-Id")), "<>")),
		maxInboundHeaderRunes,
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
	result.Body = truncateInboundRunes(body, maxInboundBodyRunes)
	result.Summary.BodyPreview = truncateInboundRunes(
		strings.Join(strings.Fields(result.Body), " "),
		maxInboundPreviewRunes,
	)
	result.Summary.VerificationCode = truncateInboundRunes(
		extractInboundVerificationCode(result.Summary.Subject+" "+result.Body),
		64,
	)
	return result
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
	if depth >= maxInboundMIMEDepth {
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
	data, readErr := io.ReadAll(io.LimitReader(reader, maxInboundBodyBytes+1))
	truncated := len(data) > maxInboundBodyBytes
	if truncated {
		data = data[:maxInboundBodyBytes]
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

const inboundCodeKeywords = `安全代码|一次性代码|验证码|驗證碼|安全碼|verification code|security code|one-time code|single-use code|セキュリティ\s*コード|確認コード|보안\s*코드|확인\s*코드|Sicherheitscode|Bestätigungscode|code de sécurité|code de vérification|código de seguridad|código de segurança|код безопасности|код подтверждения|رمز الأمان|رمز التحقق|codice di sicurezza|beveiligingscode|güvenlik kodu|kod bezpieczeństwa|รหัสความปลอดภัย|mã bảo mật|kode keamanan`

var (
	inboundCodeContextRe = regexp.MustCompile(`(?is)(?:` + inboundCodeKeywords + `)[^\d]{0,30}(\d{4,8})`)
	inboundCodeKeywordRe = regexp.MustCompile(`(?is)` + inboundCodeKeywords)
	inboundSixDigitRe    = regexp.MustCompile(`(^|[^\d])(\d{6})([^\d]|$)`)
)

func extractInboundVerificationCode(value string) string {
	if match := inboundCodeContextRe.FindStringSubmatch(value); len(match) > 1 {
		return match[1]
	}
	if !inboundCodeKeywordRe.MatchString(value) {
		return ""
	}
	if match := inboundSixDigitRe.FindStringSubmatch(value); len(match) > 2 {
		return match[2]
	}
	return ""
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
