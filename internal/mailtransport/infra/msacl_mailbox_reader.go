package infra

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	stdmail "net/mail"
	"regexp"
	"strings"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"gorm.io/gorm"
)

const msaclContentSearchWindow = 10 * time.Minute

type MSACLMailboxReader struct {
	db    *gorm.DB
	files governanceapp.FilePort
}

func NewMSACLMailboxReader(db *gorm.DB, files governanceapp.FilePort) *MSACLMailboxReader {
	return &MSACLMailboxReader{db: db, files: files}
}

func (r *MSACLMailboxReader) List(ctx context.Context, mailbox string, limit int, fuzzy bool) ([]msacl.EmailObj, error) {
	mailbox = strings.ToLower(strings.TrimSpace(mailbox))
	if mailbox == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > 50 {
		limit = 50
	}

	query := r.db.WithContext(ctx).Model(&InboundMailModel{}).Where("status IN ?", msaclReadableInboundStatuses())
	if fuzzy && !strings.Contains(mailbox, "@") {
		query = query.Where("recipient LIKE ?", mailbox+"%")
	} else {
		query = query.Where("recipient = ?", mailbox)
	}

	var rows []InboundMailModel
	if err := query.Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list inbound mailbox: %w", err)
	}
	return r.rowsToEmailObjects(ctx, rows)
}

func (r *MSACLMailboxReader) SearchByContent(ctx context.Context, content string, limit int) ([]msacl.EmailObj, error) {
	content = strings.ToLower(strings.Trim(strings.TrimSpace(content), "%"))
	if content == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}

	var rows []InboundMailModel
	since := time.Now().UTC().Add(-msaclContentSearchWindow)
	if err := r.db.WithContext(ctx).
		Model(&InboundMailModel{}).
		Where("status IN ?", msaclReadableInboundStatuses()).
		Where("created_at >= ?", since).
		Order("created_at DESC, id DESC").
		Limit(limit * 4).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("search inbound mailbox: %w", err)
	}

	emails, err := r.rowsToEmailObjects(ctx, rows)
	if err != nil {
		return nil, err
	}
	filtered := make([]msacl.EmailObj, 0, limit)
	for _, email := range emails {
		haystack := strings.ToLower(email.Subject + " " + email.Preview + " " + email.To)
		if strings.Contains(haystack, content) {
			filtered = append(filtered, email)
			if len(filtered) >= limit {
				break
			}
		}
	}
	return filtered, nil
}

func msaclReadableInboundStatuses() []string {
	return []string{
		string(domain.InboundStatusPending),
		string(domain.InboundStatusProcessing),
		string(domain.InboundStatusStored),
	}
}

func (r *MSACLMailboxReader) rowsToEmailObjects(ctx context.Context, rows []InboundMailModel) ([]msacl.EmailObj, error) {
	emails := make([]msacl.EmailObj, 0, len(rows))
	for _, row := range rows {
		stored, err := r.files.ReadPrivate(ctx, row.SourceObjectKey)
		if err != nil {
			// Preserve the row identity in mailbox snapshots. If the object
			// becomes readable later, an old message must not look newly arrived.
			emails = append(emails, newMSACLInboundEmail(row))
			continue
		}
		email := parseMSACLInboundEmail(row, stored.ContentBytes)
		emails = append(emails, email)
	}
	return emails, nil
}

func newMSACLInboundEmail(row InboundMailModel) msacl.EmailObj {
	return msacl.EmailObj{
		ID:         row.ID,
		ReceivedAt: row.CreatedAt.UTC().Format(time.RFC3339),
		To:         row.Recipient,
		From:       row.EnvelopeFrom,
		Raw: map[string]any{
			"sourceObjectKey": row.SourceObjectKey,
			"status":          row.Status,
		},
	}
}

func parseMSACLInboundEmail(row InboundMailModel, raw []byte) msacl.EmailObj {
	email := newMSACLInboundEmail(row)

	msg, err := stdmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		email.Preview = string(raw)
		return email
	}

	decoder := new(mime.WordDecoder)
	email.Subject = decodeMIMEHeader(decoder, msg.Header.Get("Subject"))
	if from := decodeMIMEHeader(decoder, msg.Header.Get("From")); from != "" {
		email.From = from
	}
	if to := decodeMIMEHeader(decoder, msg.Header.Get("To")); to != "" {
		email.To = to
	}
	body, _ := readMIMEBody(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Body)
	email.Preview = strings.TrimSpace(body)
	return email
}

func decodeMIMEHeader(decoder *mime.WordDecoder, value string) string {
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

func readMIMEBody(contentType string, transferEncoding string, body io.Reader) (string, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = "text/plain"
	}
	if strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		mr := multipart.NewReader(body, params["boundary"])
		var htmlFallback string
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", err
			}
			partBody, err := readMIMEBody(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part)
			if err != nil {
				continue
			}
			partType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
			switch strings.ToLower(partType) {
			case "text/plain":
				if strings.TrimSpace(partBody) != "" {
					return partBody, nil
				}
			case "text/html":
				if htmlFallback == "" {
					htmlFallback = stripHTMLForMSACL(partBody)
				}
			}
		}
		return htmlFallback, nil
	}

	reader := decodeTransferReader(body, transferEncoding)
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	text := string(data)
	if strings.EqualFold(mediaType, "text/html") {
		text = stripHTMLForMSACL(text)
	}
	return text, nil
}

func decodeTransferReader(body io.Reader, transferEncoding string) io.Reader {
	switch strings.ToLower(strings.TrimSpace(transferEncoding)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, body)
	case "quoted-printable":
		return quotedprintable.NewReader(body)
	default:
		return body
	}
}

func stripHTMLForMSACL(value string) string {
	value = regexp.MustCompile(`(?is)<script\b.*?</script>`).ReplaceAllString(value, " ")
	value = regexp.MustCompile(`(?is)<style\b.*?</style>`).ReplaceAllString(value, " ")
	value = regexp.MustCompile(`(?s)<[^>]+>`).ReplaceAllString(value, " ")
	return strings.Join(strings.Fields(value), " ")
}
