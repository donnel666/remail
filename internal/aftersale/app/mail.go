package app

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"strings"

	"github.com/donnel666/remail/internal/aftersale/domain"
	"github.com/donnel666/remail/internal/platform"
)

// replyDelimiter is inserted into every outbound ticket email. Inbound replies
// are stripped at this marker so only the customer's new text is ingested.
const replyDelimiter = "##- 请在此行以上回复 / Reply above this line -##"

type ticketMailKind int

const (
	ticketMailCreated ticketMailKind = iota
	ticketMailReplied
	ticketMailResolved
)

func newReplyToken() string {
	return strings.ToLower(platform.NewUUIDV4CompactUpper())[:16]
}

// notifyRequester emails the ticket requester about the latest platform activity.
func (uc *UseCase) notifyRequester(ctx context.Context, view *TicketView, kind ticketMailKind) {
	if uc.mail == nil || !uc.mailConfig.enabled() || view == nil || view.Ticket == nil {
		return
	}
	ticket := view.Ticket
	if view.Requester == nil {
		return
	}
	to := strings.TrimSpace(view.Requester.Email)
	if to == "" || len(ticket.Messages) == 0 || strings.TrimSpace(ticket.ReplyToken) == "" {
		return
	}
	uc.sendTicketMail(ctx, ticket, to, ticket.ReplyToken, 0, kind, false)
}

// notifySuperAdmins emails every active super-admin about requester activity.
// Recipient lookup and delivery are best-effort (INV-AS7).
func (uc *UseCase) notifySuperAdmins(ctx context.Context, view *TicketView, kind ticketMailKind) {
	if uc.mail == nil || !uc.mailConfig.enabled() || uc.owners == nil || view == nil || view.Ticket == nil {
		return
	}
	ticket := view.Ticket
	if len(ticket.Messages) == 0 || strings.TrimSpace(ticket.ReplyToken) == "" {
		return
	}
	admins, err := uc.owners.ListActiveSuperAdmins(ctx)
	if err != nil {
		slog.Warn("aftersale super-admin lookup failed", "ticketNo", ticket.TicketNo, "error", err)
		return
	}
	for _, admin := range admins {
		to := strings.TrimSpace(admin.Email)
		if admin.ID == 0 || !admin.Enabled || to == "" {
			continue
		}
		token := uc.mailConfig.platformReplyToken(ticket.TicketNo, ticket.ReplyToken, admin.ID)
		uc.sendTicketMail(ctx, ticket, to, token, admin.ID, kind, true)
	}
}

func (uc *UseCase) sendTicketMail(ctx context.Context, ticket *domain.Ticket, to, replyToken string, adminID uint, kind ticketMailKind, platformRecipient bool) {
	last := ticket.Messages[len(ticket.Messages)-1]

	subject, intro := ticketMailSubjectIntro(kind, ticket, platformRecipient)
	command := TicketMailCommand{
		IdempotencyKey: ticketMailIdempotencyKey(ticket.TicketNo, last.ID, adminID),
		To:             to,
		ReplyTo:        uc.mailConfig.replyAddress(ticket.TicketNo, replyToken),
		Subject:        subject,
		TextBody:       ticketMailText(ticket, intro, last.Content),
		HTMLBody:       ticketMailHTML(ticket, intro, last.Content),
	}
	if err := uc.mail.SendTicketMail(ctx, command); err != nil {
		slog.Warn("aftersale ticket email failed", "ticketNo", ticket.TicketNo, "error", err)
	}
}

func ticketMailSubjectIntro(kind ticketMailKind, ticket *domain.Ticket, platformRecipient bool) (subject, intro string) {
	base := fmt.Sprintf("【工单 %s】%s", ticket.TicketNo, ticket.Title)
	if platformRecipient {
		switch kind {
		case ticketMailCreated:
			return base, "用户创建了新的售后工单："
		case ticketMailReplied:
			return "Re: " + base, "用户回复了售后工单："
		default: // ticketMailResolved
			return "Re: " + base, "用户已关闭售后工单："
		}
	}
	switch kind {
	case ticketMailCreated:
		return base, "您的售后工单已创建，我们会尽快为您处理。您提交的内容："
	case ticketMailReplied:
		return "Re: " + base, "客服回复了您的工单："
	default: // ticketMailResolved
		return "Re: " + base, "您的工单已处理完成："
	}
}

func ticketMailText(ticket *domain.Ticket, intro, content string) string {
	var b strings.Builder
	b.WriteString("您好，\n\n")
	b.WriteString(intro)
	b.WriteString("\n\n")
	b.WriteString(strings.TrimSpace(content))
	b.WriteString("\n\n")
	b.WriteString(replyDelimiter)
	b.WriteString("\n")
	fmt.Fprintf(&b, "工单号：%s\n", ticket.TicketNo)
	b.WriteString("直接回复本邮件即可继续沟通，请勿修改邮件主题。\n")
	return b.String()
}

func ticketMailHTML(ticket *domain.Ticket, intro, content string) string {
	safeContent := strings.ReplaceAll(html.EscapeString(strings.TrimSpace(content)), "\n", "<br>")
	return fmt.Sprintf(
		`<div style="font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;color:#1f2329;line-height:1.6">`+
			`<p>您好，</p>`+
			`<p>%s</p>`+
			`<blockquote style="margin:12px 0;padding:8px 12px;border-left:3px solid #d0d3d6;color:#41464b">%s</blockquote>`+
			`<hr style="border:none;border-top:1px dashed #d0d3d6;margin:20px 0">`+
			`<p style="color:#8a9099;font-size:12px">%s<br>工单号：%s<br>直接回复本邮件即可继续沟通，请勿修改邮件主题。</p>`+
			`</div>`,
		html.EscapeString(intro), safeContent, html.EscapeString(replyDelimiter), html.EscapeString(ticket.TicketNo),
	)
}
