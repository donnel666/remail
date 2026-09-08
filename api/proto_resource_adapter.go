package api

import (
	"context"
	"errors"
	"net/mail"
	"strings"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchdomain "github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/donnel666/remail/internal/platform"
	protodomain "github.com/donnel666/remail/internal/proto/domain"
	protoinfra "github.com/donnel666/remail/internal/proto/infra"
	"github.com/donnel666/remail/internal/proto/infra/proton"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
)

type protoMailboxReader interface {
	FetchMailbox(context.Context, uint, uint64, proton.FetchRequest) (proton.FetchResult, error)
}

type protoMailFetchAdapter struct{ resources protoMailboxReader }

func (a protoMailFetchAdapter) FetchProtoMessages(ctx context.Context, request mailmatchapp.FetchMessagesRequest) (*mailmatchapp.FetchMessagesResult, error) {
	if a.resources == nil {
		return nil, mailmatchdomain.ErrMailServiceUnavailable
	}
	if request.Scope.AllocationType != mailmatchdomain.ResourceTypeProto || request.Scope.EmailResourceID == 0 || request.Scope.CredentialRevision == 0 || strings.TrimSpace(request.Scope.Recipient) == "" {
		return nil, mailmatchdomain.ErrInvalidRequest
	}
	if request.RequestID != "" {
		ctx = context.WithValue(ctx, platform.RequestIDKey, request.RequestID)
	}
	fetch := proton.FetchRequest{
		SinceAt: request.SinceAt, UntilAt: request.UntilAt, FullHistory: request.FullHistory,
		MaxMessages: request.MaxMessages, KnownMessageIDs: request.KnownMessageIDs,
	}
	if request.OnMessages != nil {
		fetch.OnMessages = func(messages []proton.Message) error {
			request.OnMessages(protoFetchedMessages(request, messages))
			return nil
		}
	}
	result, err := a.resources.FetchMailbox(ctx, request.Scope.EmailResourceID, request.Scope.CredentialRevision, fetch)
	if err != nil {
		if errors.Is(err, protodomain.ErrInvalidClaim) {
			return nil, mailmatchdomain.ErrResourceFetchCredentialChanged
		}
		if errors.Is(err, protodomain.ErrResourceMissing) {
			return nil, mailmatchdomain.ErrResourceFetchNotFound
		}
		var failure *proton.Failure
		if errors.As(err, &failure) {
			return nil, &mailmatchapp.MailFetchFailure{Category: failure.Category, SafeMessage: failure.SafeMessage, Retryable: failure.Retryable, Cause: err}
		}
		category, message := "request", "Proto mail service is temporarily unavailable."
		if errors.Is(err, protoinfra.ErrSessionUnavailable) {
			category, message = "session_unavailable", "Proto session is unavailable; mailbox validation is required."
		} else if errors.Is(err, protoinfra.ErrSessionBusy) {
			category, message = "session_busy", "Proto mailbox is processing another request; retry shortly."
		}
		return nil, &mailmatchapp.MailFetchFailure{Category: category, SafeMessage: message, Retryable: true, Cause: err}
	}
	if request.FullHistory && !result.Complete {
		return nil, &mailmatchapp.MailFetchFailure{Category: "incomplete_history", SafeMessage: "Proto mailbox history was not completely read.", Retryable: true}
	}
	return &mailmatchapp.FetchMessagesResult{Messages: protoFetchedMessages(request, result.Messages)}, nil
}

func protoFetchedMessages(request mailmatchapp.FetchMessagesRequest, messages []proton.Message) []mailmatchapp.FetchedMessage {
	out := make([]mailmatchapp.FetchedMessage, 0, len(messages))
	recipient := strings.ToLower(strings.TrimSpace(request.Scope.Recipient))
	for _, message := range messages {
		preview := []rune(strings.Join(strings.Fields(message.Body), " "))
		if len(preview) > 1000 {
			preview = preview[:1000]
		}
		item := mailmatchapp.FetchedMessage{
			EmailResourceID: request.Scope.EmailResourceID, ResourceType: mailmatchdomain.ResourceTypeProto,
			CredentialRevision: request.Scope.CredentialRevision, Recipient: recipient, Recipients: []string{recipient},
			Sender:  protoinfra.FormatSender(message.Sender),
			Subject: message.Subject, Body: message.Body, BodyPreview: string(preview),
			MessageIDHeader:   strings.Trim(strings.TrimSpace(message.ExternalID), "<>"),
			ProviderMessageID: message.ID, Protocol: "proton", Folder: message.Folder, ReceivedAt: message.ReceivedAt,
		}
		if message.OriginalToCount == 1 && len(message.ToList) == 1 {
			if address, err := mail.ParseAddress(message.ToList[0].Address); err == nil {
				item.ToRecipients = []string{strings.ToLower(address.Address)}
			}
		}
		out = append(out, item)
	}
	return out
}

func importProtoHistory(orders *tradeapp.UseCase) func(context.Context, []protoinfra.HistoricalUsage) error {
	return func(ctx context.Context, matches []protoinfra.HistoricalUsage) error {
		usage := make([]tradeapp.HistoricalProtoUsage, 0, len(matches))
		for _, match := range matches {
			usage = append(usage, tradeapp.HistoricalProtoUsage{
				ResourceID: match.ResourceID, ProjectID: match.ProjectID, ProductID: match.ProductID,
				ProductType: tradedomain.ProductTypeProto, Mailbox: "main", Email: match.Email,
				CodeWindowMinutes: match.CodeWindowMinutes, ActivationWindowMinutes: match.ActivationWindowMinutes,
				WarrantyMinutes: match.WarrantyMinutes, FirstMatchedAt: match.FirstMatchedAt,
				LastMatchedAt: match.LastMatchedAt, EvidenceCount: match.EvidenceCount,
			})
		}
		return orders.ImportHistoricalProtoUsage(ctx, usage)
	}
}

var _ mailmatchapp.ProtoMailFetchPort = protoMailFetchAdapter{}

type protoFetchFailureAdapter struct {
	resources *protoinfra.Service
	orders    *tradeapp.UseCase
}

func (a protoFetchFailureAdapter) HandlePermanentProtoFetchFailure(ctx context.Context, failure mailmatchapp.PermanentProtoFetchFailure) error {
	applied, err := a.resources.MarkPermanentFetchFailure(ctx, failure.ResourceID, failure.CredentialRevision, failure.SafeMessage)
	if err != nil || !applied {
		return err
	}
	_, err = a.orders.RefundUnavailableProtoOrders(ctx, failure.ResourceID, failure.RequestID)
	return err
}
