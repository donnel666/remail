package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/donnel666/remail/internal/aftersale/domain"
)

const inboundReplyReadLimit = 30

func (uc *UseCase) syncInboundReplies(ctx context.Context, ticket *domain.Ticket) (bool, error) {
	if uc == nil || uc.inbound == nil || ticket == nil || ticket.Status.IsTerminal() {
		return false, nil
	}
	messages, err := uc.inbound.ListTicketReplies(ctx, ticket.TicketNo, ticket.CreatedAt, inboundReplyReadLimit)
	if err != nil {
		return false, err
	}
	changed := false
	for _, source := range messages {
		if source.ID == 0 {
			continue
		}
		if isBounceEnvelope(source.EnvelopeFrom) {
			if err := uc.repo.IgnoreInboundReply(ctx, source.ID, ticket.TicketNo); err != nil {
				return changed, err
			}
			continue
		}
		body, auto := parseInboundEmail(source.Raw)
		if auto || strings.TrimSpace(body) == "" {
			if err := uc.repo.IgnoreInboundReply(ctx, source.ID, ticket.TicketNo); err != nil {
				return changed, err
			}
			continue
		}
		created, err := uc.ingestInboundReply(ctx, InboundReplyCommand{
			Recipient: source.Recipient,
			Body:      body,
		}, source.ID, ticket.TicketNo)
		if err != nil {
			return changed, err
		}
		changed = changed || created
	}
	return changed, nil
}

// IngestInboundReply appends an authenticated requester or super-admin email
// reply and notifies the other side. Permanent problems return nil so the mail
// is not retried; only transient repository/directory errors propagate.
func (uc *UseCase) IngestInboundReply(ctx context.Context, cmd InboundReplyCommand) error {
	_, err := uc.ingestInboundReply(ctx, cmd, 0, "")
	return err
}

func (uc *UseCase) ingestInboundReply(ctx context.Context, cmd InboundReplyCommand, inboundMailID uint, expectedTicketNo string) (bool, error) {
	ticketNo, token, ok := uc.parseReplyRecipient(cmd.Recipient)
	if !ok {
		return false, uc.ignoreInboundReply(ctx, inboundMailID, expectedTicketNo)
	}
	ticket, err := uc.repo.Get(ctx, ticketNo, false)
	if err != nil {
		if errors.Is(err, domain.ErrTicketNotFound) {
			return false, uc.ignoreInboundReply(ctx, inboundMailID, ticketNo)
		}
		return false, err
	}
	if ticket.Status.IsTerminal() {
		slog.Info("aftersale inbound reply to closed ticket dropped", "ticketNo", ticketNo)
		return false, uc.ignoreInboundReply(ctx, inboundMailID, ticketNo)
	}
	content := stripQuotedReply(cmd.Body)
	if content == "" {
		return false, uc.ignoreInboundReply(ctx, inboundMailID, ticketNo)
	}

	view, err := uc.viewOf(ctx, ticket)
	if err != nil {
		return false, err
	}
	if view.Requester == nil {
		slog.Warn("aftersale inbound reply rejected: requester unavailable", "ticketNo", ticketNo)
		return false, uc.ignoreInboundReply(ctx, inboundMailID, ticketNo)
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
			return false, uc.ignoreInboundReply(ctx, inboundMailID, ticketNo)
		}
		admins, lookupErr := uc.owners.ListActiveSuperAdmins(ctx)
		if lookupErr != nil {
			return false, lookupErr
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
			return false, uc.ignoreInboundReply(ctx, inboundMailID, ticketNo)
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

	params := ReplyParams{
		TicketNo: ticketNo,
		Message:  message,
	}
	var updated *domain.Ticket
	created := true
	if inboundMailID > 0 {
		updated, created, err = uc.repo.ReplyInbound(ctx, inboundMailID, params)
	} else {
		updated, err = uc.repo.Reply(ctx, params)
	}
	if err != nil {
		if errors.Is(err, domain.ErrTicketClosed) {
			return false, uc.ignoreInboundReply(ctx, inboundMailID, ticketNo)
		}
		return false, err
	}
	if !created {
		return false, nil
	}
	view.Ticket = updated
	if platformSender {
		uc.notifyRequester(ctx, view, ticketMailReplied)
	} else {
		uc.notifySuperAdmins(ctx, view, ticketMailReplied)
	}
	return true, nil
}

func (uc *UseCase) ignoreInboundReply(ctx context.Context, inboundMailID uint, ticketNo string) error {
	if inboundMailID == 0 {
		return nil
	}
	if err := uc.repo.IgnoreInboundReply(ctx, inboundMailID, strings.ToUpper(strings.TrimSpace(ticketNo))); err != nil {
		return fmt.Errorf("ignore aftersale inbound reply: %w", err)
	}
	return nil
}

// parseReplyRecipient extracts the ticket number and token from a plus-address
// like "support+AS123-token@domain". Matching is case-insensitive while the
// SMTP envelope recipient remains stored in its original form.
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
	normalized := strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(normalized, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.Contains(line, replyDelimiter) || isQuoteHeader(line) || isReplyFooter(line) {
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

func isReplyFooter(line string) bool {
	if strings.TrimLeft(line, " \t") == "-- " {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "sent from my iphone", "sent from my ipad", "get outlook for ios", "get outlook for android",
		"发自我的 iphone", "从我的 iphone 发送":
		return true
	default:
		return false
	}
}

func isQuoteHeader(line string) bool {
	trimmed := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), ">"))
	marker := strings.Trim(trimmed, "-—–－_=* ")
	switch {
	case trimmed == "":
		return false
	case strings.Contains(trimmed, "写道：") || strings.Contains(trimmed, "写道:"):
		return true
	case strings.HasPrefix(trimmed, "On ") && strings.HasSuffix(trimmed, "wrote:"):
		return true
	case strings.EqualFold(marker, "Original Message"), marker == "原始邮件":
		return true
	case strings.HasPrefix(trimmed, "原始邮件"), strings.HasPrefix(trimmed, "发件人："), strings.HasPrefix(trimmed, "发件人:"):
		return true
	default:
		return false
	}
}
