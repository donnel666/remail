package app

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/donnel666/remail/internal/aftersale/domain"
)

// IngestInboundReply appends a customer's email reply as a user message. It
// validates the per-ticket token from the plus-addressed recipient so a leaked
// ticket number alone cannot forge messages. Permanent problems (unknown
// ticket, bad token, closed ticket, empty body) return nil so the mail is not
// retried; only transient repo errors propagate.
func (uc *UseCase) IngestInboundReply(ctx context.Context, cmd InboundReplyCommand) error {
	ticketNo, token, ok := uc.parseReplyRecipient(cmd.Recipient)
	if !ok {
		return nil
	}
	ticket, err := uc.repo.Get(ctx, ticketNo, false)
	if err != nil {
		if errors.Is(err, domain.ErrTicketNotFound) {
			return nil
		}
		return err
	}
	if stored := strings.TrimSpace(ticket.ReplyToken); stored == "" || !strings.EqualFold(stored, token) {
		slog.Warn("aftersale inbound reply rejected: token mismatch", "ticketNo", ticketNo)
		return nil
	}
	if ticket.Status.IsTerminal() {
		slog.Info("aftersale inbound reply to closed ticket dropped", "ticketNo", ticketNo)
		return nil
	}
	content := stripQuotedReply(cmd.Body)
	if content == "" {
		return nil
	}
	_, err = uc.repo.Reply(ctx, ReplyParams{
		TicketNo: ticketNo,
		Message: MessageInsert{
			SenderType:   domain.SenderTypeUser,
			SenderUserID: ticket.RequesterUserID,
			SenderName:   strings.TrimSpace(cmd.FromName),
			SenderEmail:  strings.TrimSpace(cmd.FromEmail),
			Content:      content,
		},
	})
	if err != nil {
		if errors.Is(err, domain.ErrTicketClosed) {
			return nil
		}
		return err
	}
	return nil
}

// parseReplyRecipient extracts the ticket number and token from a plus-address
// like "support+AS123-token@domain" (case-insensitive; the SMTP layer lowercased
// it). Ticket numbers and tokens are hyphen-free hex, so a single '-' separates
// them.
func (uc *UseCase) parseReplyRecipient(recipient string) (ticketNo, token string, ok bool) {
	at := strings.LastIndex(recipient, "@")
	if at <= 0 {
		return "", "", false
	}
	local := recipient[:at]
	plus := strings.IndexByte(local, '+')
	if plus < 0 {
		return "", "", false
	}
	prefix := strings.TrimSpace(local[:plus])
	if !strings.EqualFold(prefix, strings.TrimSpace(uc.mailConfig.ReplyLocalPart)) {
		return "", "", false
	}
	tag := local[plus+1:]
	dash := strings.IndexByte(tag, '-')
	if dash <= 0 || dash == len(tag)-1 {
		return "", "", false
	}
	ticketNo = strings.ToUpper(strings.TrimSpace(tag[:dash]))
	token = strings.ToLower(strings.TrimSpace(tag[dash+1:]))
	if ticketNo == "" || token == "" {
		return "", "", false
	}
	return ticketNo, token, true
}

// stripQuotedReply keeps only the customer's new text by cutting at the reply
// delimiter (inserted into every outbound email) with quote-header heuristics as
// a fallback, then trims trailing quoted/blank lines.
func stripQuotedReply(body string) string {
	normalized := strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.Contains(line, replyDelimiter) || isQuoteHeader(line) {
			break
		}
		kept = append(kept, line)
	}
	for len(kept) > 0 {
		trimmed := strings.TrimSpace(kept[len(kept)-1])
		if trimmed == "" || strings.HasPrefix(trimmed, ">") {
			kept = kept[:len(kept)-1]
			continue
		}
		break
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func isQuoteHeader(line string) bool {
	trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), ">"))
	switch {
	case trimmed == "":
		return false
	case strings.Contains(trimmed, "写道：") || strings.Contains(trimmed, "写道:"):
		return true
	case strings.HasPrefix(trimmed, "On ") && strings.HasSuffix(trimmed, "wrote:"):
		return true
	case strings.HasPrefix(trimmed, "-----Original Message-----"):
		return true
	case strings.HasPrefix(trimmed, "原始邮件"), strings.HasPrefix(trimmed, "发件人："), strings.HasPrefix(trimmed, "发件人:"):
		return true
	default:
		return false
	}
}
