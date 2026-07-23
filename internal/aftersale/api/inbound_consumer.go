package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"

	aftersaleapp "github.com/donnel666/remail/internal/aftersale/app"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
)

const maxInboundBodyBytes = 256 << 10 // 256 KiB of text is plenty for one reply

// InboundConsumer ingests requester and super-admin email replies into tickets. It implements
// mailtransport's InboundConsumerPort and parses the raw MIME itself, since the
// transport only hands over the raw bytes.
type InboundConsumer struct {
	useCase   *aftersaleapp.UseCase
	localPart string
}

func NewInboundConsumer(useCase *aftersaleapp.UseCase, localPart string) *InboundConsumer {
	return &InboundConsumer{useCase: useCase, localPart: strings.ToLower(strings.TrimSpace(localPart))}
}

// Handles reports whether an inbound recipient is a ticket reply plus-address,
// so the composition-root router can send only those to this consumer.
func (c *InboundConsumer) Handles(recipient string) bool {
	if c == nil || c.localPart == "" {
		return false
	}
	at := strings.LastIndex(recipient, "@")
	if at <= 0 {
		return false
	}
	local := strings.ToLower(recipient[:at])
	return strings.HasPrefix(local, c.localPart+"+")
}

func (c *InboundConsumer) IngestInboundMail(ctx context.Context, req mailapp.InboundConsumeRequest) error {
	if c == nil || c.useCase == nil {
		return nil
	}
	if isBounceEnvelope(req.EnvelopeFrom) {
		return nil
	}
	_, _, body, auto := parseInboundEmail(req.Raw)
	if auto || strings.TrimSpace(body) == "" {
		return nil
	}
	return c.useCase.IngestInboundReply(ctx, aftersaleapp.InboundReplyCommand{
		Recipient: req.Recipient,
		Body:      body,
	})
}

func isBounceEnvelope(envelopeFrom string) bool {
	trimmed := strings.TrimSpace(envelopeFrom)
	return trimmed == "" || trimmed == "<>"
}

func parseInboundEmail(raw []byte) (fromEmail, fromName, body string, auto bool) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", "", "", false
	}
	if addr, err := mail.ParseAddress(msg.Header.Get("From")); err == nil {
		fromEmail = addr.Address
		fromName = addr.Name
	} else {
		fromEmail = strings.TrimSpace(msg.Header.Get("From"))
	}
	auto = isAutoResponse(msg.Header, fromEmail)
	body = readTextBody(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Body)
	return fromEmail, fromName, body, auto
}

func isAutoResponse(header mail.Header, fromEmail string) bool {
	if v := strings.ToLower(strings.TrimSpace(header.Get("Auto-Submitted"))); v != "" && v != "no" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(header.Get("Precedence"))) {
	case "bulk", "list", "junk", "auto_reply":
		return true
	}
	lower := strings.ToLower(fromEmail)
	return strings.Contains(lower, "mailer-daemon") || strings.HasPrefix(lower, "postmaster@")
}

func readTextBody(contentType, cte string, r io.Reader) string {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = "text/plain"
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		return readMultipart(params["boundary"], r, 0)
	}
	raw, _ := io.ReadAll(io.LimitReader(r, maxInboundBodyBytes))
	decoded := decodeCTE(cte, raw)
	if mediaType == "text/html" {
		return htmlToText(string(decoded))
	}
	return string(decoded)
}

// readMultipart prefers the text/plain part, falling back to a stripped
// text/html part; attachments are skipped.
func readMultipart(boundary string, r io.Reader, depth int) string {
	if boundary == "" || depth > 5 {
		return ""
	}
	reader := multipart.NewReader(r, boundary)
	htmlFallback := ""
	for {
		part, err := reader.NextPart()
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
		return raw
	case "quoted-printable":
		if decoded, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(raw))); err == nil {
			return decoded
		}
		return raw
	default:
		return raw
	}
}

var (
	htmlBlockPattern    = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	htmlLineBreakRegexp = regexp.MustCompile(`(?i)<br\s*/?>|</p>|</div>|</tr>|</li>`)
	htmlTagPattern      = regexp.MustCompile(`(?s)<[^>]*>`)
)

// htmlToText makes an HTML body linewise-strippable: block tags become newlines
// so the reply-delimiter/quote heuristics keep working, then tags are removed
// and entities decoded.
func htmlToText(input string) string {
	input = htmlBlockPattern.ReplaceAllString(input, " ")
	input = htmlLineBreakRegexp.ReplaceAllString(input, "\n")
	text := htmlTagPattern.ReplaceAllString(input, "")
	text = html.UnescapeString(text)
	return strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
}
