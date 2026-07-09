package api

import (
	"context"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	mailtransportapp "github.com/donnel666/remail/internal/mailtransport/app"
	mailtransportdomain "github.com/donnel666/remail/internal/mailtransport/domain"
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
	resourceType := domain.ResourceTypeDomain
	if req.ResourceType == mailtransportdomain.InboundResourceMicrosoft {
		resourceType = domain.ResourceTypeMicrosoft
	}
	return a.useCase.IngestInboundMail(ctx, mailmatchapp.InboundMailRequest{
		EmailResourceID: req.EmailResourceID,
		ResourceType:    resourceType,
		Recipient:       req.Recipient,
		EnvelopeFrom:    req.EnvelopeFrom,
		Raw:             req.Raw,
		ReceivedAt:      req.ReceivedAt,
	})
}
