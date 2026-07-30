package app

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"strings"

	"github.com/donnel666/remail/internal/aftersale/domain"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/platform"
)

// replyDelimiter is inserted into every outbound ticket email. Inbound replies
// are stripped at this marker so only the customer's new text is ingested.
const (
	replyDelimiter            = "##- 请在此行以上回复 / Reply above this line -##"
	maxInlineTicketMailImages = 2
)

var ticketMailContentTemplate = template.Must(template.New("ticket-mail-content").Funcs(template.FuncMap{"lines": ticketMailTextLines}).Parse(`{{if .Content}}<div style="font-size:15px;line-height:24px;color:#374151;background:#f9fafb;border:1px solid #e5e7eb;border-radius:8px;padding:18px 20px;margin:0 0 20px;white-space:pre-wrap;mso-spacerun:yes;word-break:break-word;">{{range $index, $line := lines .Content}}{{if $index}}<br>{{end}}{{$line}}{{end}}</div>{{end}}{{if .Images}}<div style="margin:0 0 20px;">{{range .Images}}<img src="{{.}}" alt="工单回复图片" style="display:block;max-width:100%;height:auto;border:0;border-radius:8px;margin:0 0 12px;">{{end}}</div>{{end}}<table aria-label="通知详情" width="100%" cellpadding="0" cellspacing="0" style="width:100%;border-collapse:separate;border-spacing:0;border:1px solid #e5e7eb;border-bottom:0;border-radius:8px;overflow:hidden;margin:0 0 20px;">{{range .Details}}<tr><th scope="row" align="left" valign="top" width="34%" style="border-bottom:1px solid #e5e7eb;background:#f9fafb;color:#4b5563;font-size:14px;line-height:22px;font-weight:600;padding:13px 16px;word-break:break-word;">{{.Label}}</th><td align="left" valign="top" style="border-bottom:1px solid #e5e7eb;color:#111827;font-size:15px;line-height:22px;padding:13px 16px;white-space:pre-wrap;mso-spacerun:yes;word-break:break-word;">{{range $index, $line := lines .Value}}{{if $index}}<br>{{end}}{{$line}}{{end}}</td></tr>{{end}}</table>`))

type ticketMailDetail struct {
	Label string
	Value string
}

type ticketMailHTMLData struct {
	Details []ticketMailDetail
	Content string
	Images  []template.URL
}

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
	if view == nil || view.Ticket == nil {
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
	uc.sendTicketMail(ctx, view, to, ticket.ReplyToken, 0, kind, false)
}

// notifySuperAdmins emails every active super-admin about requester activity.
// Recipient lookup and delivery are best-effort (INV-AS7).
func (uc *UseCase) notifySuperAdmins(ctx context.Context, view *TicketView, kind ticketMailKind) {
	if uc.owners == nil || view == nil || view.Ticket == nil {
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
		uc.sendTicketMail(ctx, view, to, token, admin.ID, kind, true)
	}
}

func ticketMailField(value string) string { return strings.Join(strings.Fields(value), " ") }

func ticketMailTextLines(value string) []string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.Split(strings.ReplaceAll(value, "\r", "\n"), "\n")
}

func (uc *UseCase) sendTicketMail(ctx context.Context, view *TicketView, to, replyToken string, adminID uint, kind ticketMailKind, platformRecipient bool) {
	ticket := view.Ticket
	last := ticket.Messages[len(ticket.Messages)-1]
	inlineImages := make([]TicketMailInlineImage, 0, len(last.Attachments))
	htmlImages := make([]template.URL, 0, len(last.Attachments))
	if len(last.Attachments) <= maxInlineTicketMailImages {
		for _, attachment := range last.Attachments {
			mime := normalizeTicketImageMime(attachment.Mime)
			if mime == "" || strings.TrimSpace(attachment.ObjectKey) == "" || strings.TrimSpace(attachment.AttachmentNo) == "" {
				continue
			}
			contentID := strings.ToLower(attachment.AttachmentNo) + "@remail"
			inlineImages = append(inlineImages, TicketMailInlineImage{
				ObjectKey:   attachment.ObjectKey,
				FileName:    attachment.AttachmentNo + extForMime(mime),
				ContentType: mime,
				ContentID:   contentID,
			})
			htmlImages = append(htmlImages, template.URL("cid:"+contentID))
		}
	}
	content := strings.TrimSpace(last.Content)
	imageOnly := content == "[图片]" || strings.EqualFold(content, "[Image]")
	if (len(inlineImages) > 0 || len(last.Attachments) > maxInlineTicketMailImages) && imageOnly {
		content = ""
	}
	if len(last.Attachments) > maxInlineTicketMailImages {
		notice := fmt.Sprintf("（本次回复包含 %d 张图片，请登录工单查看）", len(last.Attachments))
		if content == "" {
			content = notice
		} else {
			content += "\n\n" + notice
		}
	}
	textContent := content
	if len(inlineImages) > 0 {
		notice := fmt.Sprintf("（含 %d 张图片）", len(inlineImages))
		if textContent == "" {
			textContent = notice
		} else {
			textContent += "\n\n" + notice
		}
	}
	details := []ticketMailDetail{{Label: "工单号", Value: ticket.TicketNo}, {Label: "工单标题", Value: ticket.Title}}
	if platformRecipient {
		summary := RequesterSummary{ID: ticket.RequesterUserID}
		if view.Requester != nil {
			summary = *view.Requester
		}
		requesterDetails := []ticketMailDetail{
			{Label: "用户 ID", Value: fmt.Sprint(ticket.RequesterUserID)},
			{Label: "昵称", Value: ticketMailField(summary.Nickname)},
			{Label: "邮箱", Value: ticketMailField(summary.Email)},
			{Label: "分组", Value: ticketMailField(summary.GroupName)},
			{Label: "角色", Value: ticketMailField(summary.Role)},
		}
		details = append(details, requesterDetails...)
		textContent += "\n\n提交用户信息\n" + ticketMailDetailsText(requesterDetails)
	}
	subject, intro := ticketMailSubjectIntro(kind, ticket, platformRecipient)
	htmlBody, err := ticketMailHTML(ticket, subject, intro, content, details, htmlImages)
	if err != nil {
		slog.Warn("aftersale ticket email HTML render failed", "ticketNo", ticket.TicketNo, "error", err)
	}
	command := TicketMailCommand{
		IdempotencyKey: ticketMailIdempotencyKey(ticket.TicketNo, last.ID, adminID),
		To:             to,
		ReplyTo:        uc.mailConfig.replyAddress(ticket.TicketNo, replyToken),
		Subject:        subject,
		TextBody:       ticketMailText(ticket, intro, textContent),
		HTMLBody:       htmlBody,
		InlineImages:   inlineImages,
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

func ticketMailDetailsText(details []ticketMailDetail) string {
	lines := make([]string, len(details))
	for i, detail := range details {
		lines[i] = detail.Label + "：" + detail.Value
	}
	return strings.Join(lines, "\n")
}

func ticketMailHTML(ticket *domain.Ticket, subject, intro, content string, details []ticketMailDetail, images []template.URL) (string, error) {
	return mailapp.BrandedHTML(
		subject,
		"工单通知",
		intro,
		ticketMailContentTemplate,
		ticketMailHTMLData{Details: details, Content: content, Images: images},
		fmt.Sprintf("%s 工单号：%s。直接回复本邮件即可继续沟通，请勿修改邮件主题。", replyDelimiter, ticket.TicketNo),
	)
}
