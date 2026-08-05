package api

import (
	"context"

	aftersaleapp "github.com/donnel666/remail/internal/aftersale/app"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	mailtransportdomain "github.com/donnel666/remail/internal/mailtransport/domain"
)

// aftersaleMailAdapter adapts the mailtransport async delivery port to the
// aftersale MailPort, tagging every ticket email as a system notification and
// stamping the configured From. Reply-To is set per message by the use case.
type aftersaleMailAdapter struct {
	delivery mailapp.DeliveryPort
	from     string
}

func (a aftersaleMailAdapter) SendTicketMail(ctx context.Context, mail aftersaleapp.TicketMailCommand) error {
	message := mailtransportdomain.OutboundMessage{
		IdempotencyKey: mail.IdempotencyKey,
		Purpose:        mailtransportdomain.PurposeSystemNotice,
		From:           a.from,
		To:             mail.To,
		ReplyTo:        mail.ReplyTo,
		Subject:        mail.Subject,
		TextBody:       mail.TextBody,
		HTMLBody:       mail.HTMLBody,
	}
	for _, image := range mail.InlineImages {
		message.InlineImages = append(message.InlineImages, mailtransportdomain.OutboundInlineImage{
			ObjectKey:   image.ObjectKey,
			FileName:    image.FileName,
			ContentType: image.ContentType,
			ContentID:   image.ContentID,
		})
	}
	return a.delivery.Send(ctx, message)
}
