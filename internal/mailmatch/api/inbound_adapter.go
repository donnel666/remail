package api

import (
	"context"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	mailtransportapp "github.com/donnel666/remail/internal/mailtransport/app"
)

type InboundConsumerAdapter struct {
	useCase *mailmatchapp.UseCase
}

func NewInboundConsumerAdapter(useCase *mailmatchapp.UseCase) *InboundConsumerAdapter {
	return &InboundConsumerAdapter{useCase: useCase}
}

func (a *InboundConsumerAdapter) IngestInboundMail(ctx context.Context, req mailtransportapp.InboundConsumeRequest) error {
	if a == nil || a.useCase == nil {
		return nil
	}
	return a.useCase.IngestInboundMail(ctx, mailmatchapp.InboundMailRequest{
		EmailResourceID: req.EmailResourceID,
		ResourceType:    domain.ResourceType(req.ResourceType),
		Recipient:       req.Recipient,
		EnvelopeFrom:    req.EnvelopeFrom,
		Raw:             req.Raw,
		ReceivedAt:      req.ReceivedAt,
	})
}
