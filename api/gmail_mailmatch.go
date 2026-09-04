package api

import (
	"context"
	"errors"
	"time"

	gmailapi "github.com/donnel666/remail/internal/gmail"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchdomain "github.com/donnel666/remail/internal/mailmatch/domain"
)

type gmailResourceFetchAdapter struct {
	service *gmailapi.Service
}

func (a gmailResourceFetchAdapter) FetchLocalResourceMailWithFence(ctx context.Context, resourceID uint, credentialRevision uint64, fence func(context.Context) error) (int, int, int, error) {
	if a.service == nil {
		return 0, 0, 0, mailmatchdomain.ErrMailServiceUnavailable
	}
	fetched, stored, matched, err := a.service.FetchLocalResourceMailWithFence(ctx, resourceID, credentialRevision, fence)
	switch {
	case errors.Is(err, gmailapi.ErrLocalResourceMissing):
		return 0, 0, 0, mailmatchdomain.ErrResourceFetchNotFound
	case errors.Is(err, gmailapi.ErrLocalValidationConflict):
		return 0, 0, 0, mailmatchdomain.ErrResourceFetchCredentialChanged
	case errors.Is(err, gmailapi.ErrInvalidLocalResource):
		return 0, 0, 0, mailmatchdomain.ErrResourceFetchCredentialsMissing
	default:
		return fetched, stored, matched, err
	}
}

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
	fence func(context.Context) error,
) (int, int, error) {
	return a.ingest(ctx, mailmatchdomain.ResourceTypeGmail, resourceID, recipient, raw, receivedAt, providerMessageID, folder, fence)
}

func (a gmailMailIngestAdapter) ingest(
	ctx context.Context,
	resourceType mailmatchdomain.ResourceType,
	resourceID uint,
	recipient string,
	raw []byte,
	receivedAt time.Time,
	providerMessageID, folder string,
	fence func(context.Context) error,
) (int, int, error) {
	if a.mailmatch == nil {
		return 0, 0, nil
	}
	return a.mailmatch.IngestInboundMailWithFence(ctx, mailmatchapp.InboundMailRequest{
		EmailResourceID: resourceID, ResourceType: resourceType,
		Recipient: recipient, Raw: raw, ReceivedAt: receivedAt,
		ProviderMessageID: providerMessageID, Protocol: "imap", Folder: folder,
	}, fence)
}

var _ gmailapi.MailIngestPort = gmailMailIngestAdapter{}
