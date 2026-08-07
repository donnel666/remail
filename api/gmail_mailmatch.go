package api

import (
	"context"
	"time"

	gmailapi "github.com/donnel666/remail/internal/gmail"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchdomain "github.com/donnel666/remail/internal/mailmatch/domain"
)

type gmailMailIngestAdapter struct {
	mailmatch *mailmatchapp.UseCase
}

func (a gmailMailIngestAdapter) IngestGmailMail(
	ctx context.Context,
	resourceID uint,
	recipient string,
	raw []byte,
	receivedAt time.Time,
	providerMessageID, folder string,
) error {
	return a.ingest(ctx, mailmatchdomain.ResourceTypeGmail, resourceID, recipient, raw, receivedAt, providerMessageID, folder)
}

func (a gmailMailIngestAdapter) IngestICloudMail(
	ctx context.Context,
	resourceID uint,
	recipient string,
	raw []byte,
	receivedAt time.Time,
	providerMessageID, folder string,
) error {
	return a.ingest(ctx, mailmatchdomain.ResourceTypeICloud, resourceID, recipient, raw, receivedAt, providerMessageID, folder)
}

func (a gmailMailIngestAdapter) ingest(
	ctx context.Context,
	resourceType mailmatchdomain.ResourceType,
	resourceID uint,
	recipient string,
	raw []byte,
	receivedAt time.Time,
	providerMessageID, folder string,
) error {
	if a.mailmatch == nil {
		return nil
	}
	return a.mailmatch.IngestInboundMail(ctx, mailmatchapp.InboundMailRequest{
		EmailResourceID: resourceID, ResourceType: resourceType,
		Recipient: recipient, Raw: raw, ReceivedAt: receivedAt,
		ProviderMessageID: providerMessageID, Protocol: "imap", Folder: folder,
	})
}

var _ gmailapi.MailIngestPort = gmailMailIngestAdapter{}
