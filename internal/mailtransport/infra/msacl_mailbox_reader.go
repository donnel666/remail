package infra

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	stdmail "net/mail"
	"regexp"
	"strings"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	"github.com/donnel666/remail/internal/mailbox"
	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"gorm.io/gorm"
)

const msaclContentSearchWindow = 10 * time.Minute

type MSACLMailboxReader struct {
	db                  *gorm.DB
	files               governanceapp.FilePort
	contentSearchWindow time.Duration
}

func NewMSACLMailboxReader(db *gorm.DB, files governanceapp.FilePort) *MSACLMailboxReader {
	return NewMSACLMailboxReaderWithContentWindow(db, files, msaclContentSearchWindow)
}

// NewMSACLMailboxReaderWithContentWindow creates a mailbox reader whose exact
// recipient lookup may inspect older inbound evidence. Masked OTP lookups pass
// their own per-attempt window.
func NewMSACLMailboxReaderWithContentWindow(db *gorm.DB, files governanceapp.FilePort, window time.Duration) *MSACLMailboxReader {
	if window <= 0 {
		window = msaclContentSearchWindow
	}
	return &MSACLMailboxReader{db: db, files: files, contentSearchWindow: window}
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

	query := r.db.WithContext(ctx).Model(&InboundMailModel{}).
		Where("status IN ?", msaclReadableInboundStatuses()).
		Where("created_at >= ?", r.lookupSince())
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

func (r *MSACLMailboxReader) ListMasked(ctx context.Context, maskedMailbox string, query msacl.MaskedMailboxQuery) ([]msacl.EmailObj, error) {
	local, domainName, ok := strings.Cut(strings.ToLower(strings.TrimSpace(maskedMailbox)), "@")
	firstStar := strings.Index(local, "*")
	lastStar := strings.LastIndex(local, "*")
	if !ok || firstStar <= 0 || domainName == "" {
		return nil, nil
	}
	if query.Since.IsZero() {
		return nil, fmt.Errorf("list masked inbound mailbox: since is required")
	}
	if query.Limit <= 0 || query.Limit > 50 {
		query.Limit = 50
	}
	pattern := escapeMSACLLike(local[:firstStar]) + "%" + escapeMSACLLike(local[lastStar+1:]+"@"+domainName)
	columns := "*"
	if !query.LoadBody {
		columns = "id, recipient, received_at, status, created_at"
	}
	statement := "SELECT " + columns + `
FROM inbound_mails FORCE INDEX (idx_inbound_mails_status_created)
WHERE status IN ?
  AND created_at >= ?
  AND recipient LIKE ? ESCAPE '!'`
	args := []any{msaclReadableInboundStatuses(), query.Since.UTC(), pattern}
	if query.AfterID > 0 {
		statement += "\n  AND id > ?"
		args = append(args, query.AfterID)
	}
	statement += "\nORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, query.Limit)

	var rows []InboundMailModel
	if err := r.db.WithContext(ctx).Raw(statement, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list masked inbound mailbox: %w", err)
	}
	if !query.LoadBody {
		emails := make([]msacl.EmailObj, 0, len(rows))
		for _, row := range rows {
			emails = append(emails, newMSACLInboundEmail(row))
		}
		return emails, nil
	}
	return r.rowsToEmailObjects(ctx, rows)
}

func (r *MSACLMailboxReader) lookupSince() time.Time {
	window := r.contentSearchWindow
	if window <= 0 {
		window = msaclContentSearchWindow
	}
	return time.Now().UTC().Add(-window)
}

func escapeMSACLLike(value string) string {
	replacer := strings.NewReplacer(
		`!`, `!!`,
		`%`, `!%`,
		`_`, `!_`,
	)
	return replacer.Replace(value)
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
		if row.ParsedAt != nil {
			emails = append(emails, newMSACLInboundEmail(row))
			continue
		}
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
	receivedAt := row.CreatedAt
	if row.ReceivedAt != nil && !row.ReceivedAt.IsZero() {
		receivedAt = *row.ReceivedAt
	}
	return msacl.EmailObj{
		ID:               row.ID,
		ReceivedAt:       receivedAt.UTC().Format(time.RFC3339),
		Subject:          row.Subject,
		Preview:          row.BodyPreview,
		VerificationCode: row.VerificationCode,
		To:               row.Recipient,
		From:             row.HeaderFrom,
		Raw: map[string]any{
			"status": row.Status,
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
	} else if email.From == "" {
		email.From = row.EnvelopeFrom
	}
	body, isHTML, _, _ := readMIMEBodyPart(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Body)
	if isHTML {
		body = stripHTMLForMSACL(body)
	}
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

func readMIMEBody(contentType, transferEncoding string, body io.Reader) (string, error) {
	return mailbox.ReadMIMEBody(contentType, transferEncoding, body)
}

func readMIMEBodyPart(contentType, transferEncoding string, body io.Reader) (string, bool, bool, error) {
	return mailbox.ReadMIMEBodyPart(contentType, transferEncoding, body)
}

var (
	msaclHTMLScriptRe = regexp.MustCompile(`(?is)<script\b.*?</script>`)
	msaclHTMLStyleRe  = regexp.MustCompile(`(?is)<style\b.*?</style>`)
	msaclHTMLTagRe    = regexp.MustCompile(`(?s)<[^>]+>`)
)

func stripHTMLForMSACL(value string) string {
	value = msaclHTMLScriptRe.ReplaceAllString(value, " ")
	value = msaclHTMLStyleRe.ReplaceAllString(value, " ")
	value = msaclHTMLTagRe.ReplaceAllString(value, " ")
	return strings.Join(strings.Fields(value), " ")
}
