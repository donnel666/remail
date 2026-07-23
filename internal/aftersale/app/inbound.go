package app

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/donnel666/remail/internal/aftersale/domain"
)

// IngestInboundReply appends an authenticated requester or super-admin email
// reply and notifies the other side. Permanent problems return nil so the mail
// is not retried; only transient repository/directory errors propagate.
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
	if ticket.Status.IsTerminal() {
		slog.Info("aftersale inbound reply to closed ticket dropped", "ticketNo", ticketNo)
		return nil
	}
	content := stripQuotedReply(cmd.Body)
	if content == "" {
		return nil
	}

	view, err := uc.viewOf(ctx, ticket)
	if err != nil {
		return err
	}
	if view.Requester == nil {
		slog.Warn("aftersale inbound reply rejected: requester unavailable", "ticketNo", ticketNo)
		return nil
	}

	message := MessageInsert{Content: content}
	platformSender := false
	if replyTokenEqual(ticket.ReplyToken, token) {
		name := strings.TrimSpace(view.Requester.Nickname)
		if name == "" {
			name = strings.TrimSpace(view.Requester.Email)
		}
		message.SenderType = domain.SenderTypeUser
		message.SenderUserID = ticket.RequesterUserID
		message.SenderName = name
		message.SenderEmail = strings.TrimSpace(view.Requester.Email)
	} else {
		if uc.owners == nil {
			return nil
		}
		admins, lookupErr := uc.owners.ListActiveSuperAdmins(ctx)
		if lookupErr != nil {
			return lookupErr
		}
		var sender *RequesterSummary
		for i := range admins {
			expected := uc.mailConfig.platformReplyToken(ticket.TicketNo, ticket.ReplyToken, admins[i].ID)
			if admins[i].Enabled && replyTokenEqual(expected, token) {
				sender = &admins[i]
				break
			}
		}
		if sender == nil {
			slog.Warn("aftersale inbound reply rejected: token mismatch", "ticketNo", ticketNo)
			return nil
		}
		name := strings.TrimSpace(sender.Nickname)
		if name == "" {
			name = strings.TrimSpace(sender.Email)
		}
		message.SenderType = domain.SenderTypePlatform
		message.SenderUserID = sender.ID
		message.SenderName = name
		message.SenderEmail = strings.TrimSpace(sender.Email)
		platformSender = true
	}

	// ponytail: inbound tasks lack a stable source ID; add one plus a unique
	// constraint if crash-window duplicate replies become a real issue.
	updated, err := uc.repo.Reply(ctx, ReplyParams{
		TicketNo: ticketNo,
		Message:  message,
	})
	if err != nil {
		if errors.Is(err, domain.ErrTicketClosed) {
			return nil
		}
		return err
	}
	view.Ticket = updated
	if platformSender {
		uc.notifyRequester(ctx, view, ticketMailReplied)
	} else {
		uc.notifySuperAdmins(ctx, view, ticketMailReplied)
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
