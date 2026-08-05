package app

import (
	"bytes"
	"encoding/base64"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"
)

const maxInboundBodyBytes = 256 << 10

func isBounceEnvelope(envelopeFrom string) bool {
	trimmed := strings.TrimSpace(envelopeFrom)
	return trimmed == "" || trimmed == "<>"
}

func parseInboundEmail(raw []byte) (body string, auto bool) {
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", false
	}
	from := strings.TrimSpace(message.Header.Get("From"))
	if address, err := mail.ParseAddress(from); err == nil {
		from = address.Address
	}
	auto = isAutoResponse(message.Header, from)
	body = readTextBody(message.Header.Get("Content-Type"), message.Header.Get("Content-Transfer-Encoding"), message.Body)
	return body, auto
}

func isAutoResponse(header mail.Header, fromEmail string) bool {
	if value := strings.ToLower(strings.TrimSpace(header.Get("Auto-Submitted"))); value != "" && value != "no" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(header.Get("Precedence"))) {
	case "bulk", "list", "junk", "auto_reply":
		return true
	}
	lower := strings.ToLower(fromEmail)
	return strings.Contains(lower, "mailer-daemon") || strings.HasPrefix(lower, "postmaster@")
}

func readTextBody(contentType, cte string, reader io.Reader) string {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = "text/plain"
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		return readMultipart(params["boundary"], reader, 0)
	}
	raw, _ := io.ReadAll(io.LimitReader(reader, maxInboundBodyBytes))
	decoded := decodeCTE(cte, raw)
	if mediaType == "text/html" {
		return htmlToText(string(decoded))
	}
	return string(decoded)
}

func readMultipart(boundary string, reader io.Reader, depth int) string {
	if boundary == "" || depth > 5 {
		return ""
	}
	parts := multipart.NewReader(reader, boundary)
	htmlFallback := ""
	for {
		part, err := parts.NextPart()
		if err != nil {
			break
		}
		mediaType, params, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		cte := part.Header.Get("Content-Transfer-Encoding")
		disposition := strings.ToLower(part.Header.Get("Content-Disposition"))
		if strings.HasPrefix(mediaType, "multipart/") {
			nested := readMultipart(params["boundary"], part, depth+1)
			_ = part.Close()
			if strings.TrimSpace(nested) != "" {
				return nested
			}
			continue
		}
		if strings.HasPrefix(disposition, "attachment") {
			_ = part.Close()
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(part, maxInboundBodyBytes))
		_ = part.Close()
		decoded := decodeCTE(cte, raw)
		if mediaType == "text/plain" {
			return string(decoded)
		}
		if mediaType == "text/html" && htmlFallback == "" {
			htmlFallback = htmlToText(string(decoded))
		}
	}
	return htmlFallback
}

func decodeCTE(cte string, raw []byte) []byte {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "base64":
		cleaned := strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, string(raw))
		if decoded, err := base64.StdEncoding.DecodeString(cleaned); err == nil {
			return decoded
		}
	case "quoted-printable":
		if decoded, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(raw))); err == nil {
			return decoded
		}
	}
	return raw
}

var (
	htmlBlockPattern    = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	htmlLineBreakRegexp = regexp.MustCompile(`(?i)<br\s*/?>|</p>|</div>|</tr>|</li>`)
	htmlTagPattern      = regexp.MustCompile(`(?s)<[^>]*>`)
)

func htmlToText(input string) string {
	input = htmlBlockPattern.ReplaceAllString(input, " ")
	input = htmlLineBreakRegexp.ReplaceAllString(input, "\n")
	input = htmlTagPattern.ReplaceAllString(input, "")
	return strings.TrimSpace(strings.ReplaceAll(html.UnescapeString(input), "\r\n", "\n"))
}
