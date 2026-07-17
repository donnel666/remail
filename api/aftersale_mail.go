package api

import (
	"context"

	aftersaleapi "github.com/donnel666/remail/internal/aftersale/api"
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
	return a.delivery.Send(ctx, mailtransportdomain.OutboundMessage{
		IdempotencyKey: mail.IdempotencyKey,
		Purpose:        mailtransportdomain.PurposeSystemNotice,
		From:           a.from,
		To:             mail.To,
		ReplyTo:        mail.ReplyTo,
		Subject:        mail.Subject,
		TextBody:       mail.TextBody,
		HTMLBody:       mail.HTMLBody,
	})
}

// ticketInboundRouter sends plus-addressed ticket replies to the aftersale
// consumer and everything else to the existing mailmatch consumer, so a single
// inbound SMTP server can serve both.
type ticketInboundRouter struct {
	ticket   *aftersaleapi.InboundConsumer
	fallback mailapp.InboundConsumerPort
}

func (r ticketInboundRouter) IngestInboundMail(ctx context.Context, req mailapp.InboundConsumeRequest) error {
	if r.ticket != nil && r.ticket.Handles(req.Recipient) {
		return r.ticket.IngestInboundMail(ctx, req)
	}
	if r.fallback != nil {
		return r.fallback.IngestInboundMail(ctx, req)
	}
	return nil
}
