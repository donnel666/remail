package api

import (
	"context"

	icloudapi "github.com/donnel666/remail/internal/icloud"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchdomain "github.com/donnel666/remail/internal/mailmatch/domain"
)

type iCloudMailFetchAdapter struct {
	service *icloudapi.Service
}

func (a iCloudMailFetchAdapter) FetchICloudMessages(ctx context.Context, req mailmatchapp.FetchMessagesRequest) (*mailmatchapp.FetchMessagesResult, error) {
	if a.service == nil {
		return nil, mailmatchdomain.ErrMailServiceUnavailable
	}
	result, err := a.service.FetchMail(ctx, icloudapi.MailFetchRequest{
		ResourceID:  req.Scope.EmailResourceID,
		SinceAt:     req.SinceAt,
		UntilAt:     req.UntilAt,
		MaxMessages: req.MaxMessages,
		FullHistory: req.FullHistory,
	})
	if err != nil {
		return nil, err
	}
	out := &mailmatchapp.FetchMessagesResult{Messages: make([]mailmatchapp.FetchedMessage, 0, len(result.Messages))}
	for _, message := range result.Messages {
		out.Messages = append(out.Messages, mailmatchapp.ParseInboundFetchedMessage(mailmatchapp.InboundMailRequest{
			EmailResourceID:   req.Scope.EmailResourceID,
			ResourceType:      mailmatchdomain.ResourceTypeICloud,
			Recipient:         message.Recipient,
			EnvelopeFrom:      message.Sender,
			Raw:               message.Raw,
			ReceivedAt:        message.ReceivedAt,
			ProviderMessageID: message.ProviderMessageID,
			Protocol:          "imap",
			Folder:            "INBOX",
		}))
	}
	if result.Cursor != nil {
		cursor := *result.Cursor
		out.CommitCursor = func(commitCtx context.Context, fence func(context.Context) error) error {
			return a.service.CommitMailCursor(commitCtx, cursor, fence)
		}
	}
	return out, nil
}

var _ mailmatchapp.ICloudMailFetchPort = iCloudMailFetchAdapter{}
