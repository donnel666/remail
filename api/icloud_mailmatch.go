package api

import (
	"context"
	"time"

	icloudapi "github.com/donnel666/remail/internal/icloud"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchdomain "github.com/donnel666/remail/internal/mailmatch/domain"
)

type iCloudMailIngestAdapter struct {
	mailmatch *mailmatchapp.UseCase
}

func (a iCloudMailIngestAdapter) IngestICloudMail(
	ctx context.Context,
	resourceID uint,
	recipient string,
	envelopeFrom string,
	raw []byte,
	receivedAt time.Time,
	providerMessageID string,
) error {
	if a.mailmatch == nil {
		return nil
	}
	return a.mailmatch.IngestInboundMail(ctx, mailmatchapp.InboundMailRequest{
		EmailResourceID:   resourceID,
		ResourceType:      mailmatchdomain.ResourceTypeICloud,
		Recipient:         recipient,
		EnvelopeFrom:      envelopeFrom,
		Raw:               raw,
		ReceivedAt:        receivedAt,
		ProviderMessageID: providerMessageID,
		Protocol:          "smtp",
		Folder:            "inbound",
	})
}

func (a iCloudMailIngestAdapter) IngestICloudMailWithFence(
	ctx context.Context,
	resourceID uint,
	recipient string,
	envelopeFrom string,
	raw []byte,
	receivedAt time.Time,
	providerMessageID string,
	fence func(context.Context) error,
) (icloudapi.MailIngestResult, error) {
	if a.mailmatch == nil {
		return icloudapi.MailIngestResult{}, nil
	}
	stored, matched, err := a.mailmatch.IngestInboundMailWithFence(ctx, mailmatchapp.InboundMailRequest{
		EmailResourceID:   resourceID,
		ResourceType:      mailmatchdomain.ResourceTypeICloud,
		Recipient:         recipient,
		EnvelopeFrom:      envelopeFrom,
		Raw:               raw,
		ReceivedAt:        receivedAt,
		ProviderMessageID: providerMessageID,
		Protocol:          "smtp",
		Folder:            "inbound",
	}, fence)
	return icloudapi.MailIngestResult{Stored: stored, Matched: matched}, err
}

var _ icloudapi.MailIngestPort = iCloudMailIngestAdapter{}
var _ icloudapi.MailIngestWithFencePort = iCloudMailIngestAdapter{}
