package api

import (
	"context"
	"errors"
	"net/mail"
	"strings"

	daumdomain "github.com/donnel666/remail/internal/daum/domain"
	dauminfra "github.com/donnel666/remail/internal/daum/infra"
	"github.com/donnel666/remail/internal/daum/infra/protocol"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchdomain "github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/donnel666/remail/internal/platform"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
)

type daumMailboxReader interface {
	FetchMailbox(context.Context, uint, uint64, protocol.FetchRequest) (protocol.FetchResult, error)
}

type daumMailFetchAdapter struct{ resources daumMailboxReader }

func (a daumMailFetchAdapter) FetchDaumMessages(ctx context.Context, request mailmatchapp.FetchMessagesRequest) (*mailmatchapp.FetchMessagesResult, error) {
	if a.resources == nil {
		return nil, mailmatchdomain.ErrMailServiceUnavailable
	}
	if request.Scope.AllocationType != mailmatchdomain.ResourceTypeDaum || request.Scope.EmailResourceID == 0 || request.Scope.CredentialRevision == 0 || (!request.FullHistory && strings.TrimSpace(request.Scope.Recipient) == "") {
		return nil, mailmatchdomain.ErrInvalidRequest
	}
	if request.RequestID != "" {
		ctx = context.WithValue(ctx, platform.RequestIDKey, request.RequestID)
	}
	fetch := protocol.FetchRequest{
		// The fetch lease/cache covers the account; MailMatch isolates each exact recipient.
		SinceAt: request.SinceAt, UntilAt: request.UntilAt, FullHistory: request.FullHistory,
		MaxMessages: request.MaxMessages, KnownMessageIDs: request.KnownMessageIDs,
		OnReset: request.OnReset,
	}
	if request.OnMessages != nil {
		fetch.OnMessages = func(messages []protocol.Message) error {
			request.OnMessages(daumFetchedMessages(request, messages))
			return nil
		}
	}
	result, err := a.resources.FetchMailbox(ctx, request.Scope.EmailResourceID, request.Scope.CredentialRevision, fetch)
	if err != nil {
		if errors.Is(err, daumdomain.ErrInvalidClaim) {
			return nil, mailmatchdomain.ErrResourceFetchCredentialChanged
		}
		if errors.Is(err, daumdomain.ErrResourceMissing) {
			return nil, mailmatchdomain.ErrResourceFetchNotFound
		}
		var failure *protocol.Failure
		if errors.As(err, &failure) {
			return nil, &mailmatchapp.MailFetchFailure{Category: failure.Category, SafeMessage: failure.SafeMessage, Retryable: failure.Retryable, Cause: err}
		}
		return nil, &mailmatchapp.MailFetchFailure{Category: "request", SafeMessage: "Daum mail service is temporarily unavailable.", Retryable: true, Cause: err}
	}
	if request.FullHistory && !result.Complete {
		return nil, &mailmatchapp.MailFetchFailure{Category: "incomplete_history", SafeMessage: "Daum mailbox history was not completely read.", Retryable: true}
	}
	return &mailmatchapp.FetchMessagesResult{Messages: daumFetchedMessages(request, result.Messages)}, nil
}

func daumFetchedMessages(request mailmatchapp.FetchMessagesRequest, messages []protocol.Message) []mailmatchapp.FetchedMessage {
	out := make([]mailmatchapp.FetchedMessage, 0, len(messages))
	for _, message := range messages {
		preview := []rune(strings.Join(strings.Fields(message.Body), " "))
		if len(preview) > 1000 {
			preview = preview[:1000]
		}
		recipients := make([]string, 0, len(message.ToList)+len(message.CCList)+len(message.BCCList))
		seen := map[string]bool{}
		for _, list := range [][]protocol.Address{message.ToList, message.CCList, message.BCCList} {
			for _, candidate := range list {
				address, err := mail.ParseAddress(candidate.Address)
				if err != nil {
					continue
				}
				email := strings.ToLower(strings.TrimSpace(address.Address))
				if email != "" && !seen[email] {
					recipients = append(recipients, email)
					seen[email] = true
				}
			}
		}
		// An absent envelope recipient never becomes the requested order address.
		recipient := ""
		if len(recipients) > 0 {
			recipient = recipients[0]
		}
		transport, _, _ := strings.Cut(message.ID, ":")
		if transport != "imap" && transport != "pop3" {
			transport = "imap"
		}
		item := mailmatchapp.FetchedMessage{
			EmailResourceID: request.Scope.EmailResourceID, ResourceType: mailmatchdomain.ResourceTypeDaum,
			CredentialRevision: request.Scope.CredentialRevision, Recipient: recipient, Recipients: recipients,
			Sender: dauminfra.FormatSender(message.Sender), Subject: message.Subject, Body: message.Body, BodyPreview: string(preview),
			MessageIDHeader:   strings.Trim(strings.TrimSpace(message.ExternalID), "<>"),
			ProviderMessageID: message.ID, Protocol: transport, Folder: message.Folder, ReceivedAt: message.ReceivedAt,
		}
		if item.MessageIDHeader == "" && message.RawHash != "" {
			item.ProviderMessageID = "sha256:" + message.RawHash
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

func importDaumHistory(orders *tradeapp.UseCase) func(context.Context, []dauminfra.HistoricalUsage) error {
	return func(ctx context.Context, matches []dauminfra.HistoricalUsage) error {
		usage := make([]tradeapp.HistoricalDaumUsage, 0, len(matches))
		for _, match := range matches {
			mailbox := "alias"
			if strings.HasSuffix(strings.ToLower(match.Email), "@daum.net") {
				mailbox = "main"
			}
			usage = append(usage, tradeapp.HistoricalDaumUsage{
				ResourceID: match.ResourceID, ProjectID: match.ProjectID, ProductID: match.ProductID,
				ProductType: tradedomain.ProductTypeDaum, Mailbox: mailbox, Email: match.Email,
				CodeWindowMinutes: match.CodeWindowMinutes, ActivationWindowMinutes: match.ActivationWindowMinutes,
				WarrantyMinutes: match.WarrantyMinutes, FirstMatchedAt: match.FirstMatchedAt,
				LastMatchedAt: match.LastMatchedAt, EvidenceCount: match.EvidenceCount,
			})
		}
		return orders.ImportHistoricalDaumUsage(ctx, usage)
	}
}

var _ mailmatchapp.DaumMailFetchPort = daumMailFetchAdapter{}

type daumFetchFailureAdapter struct{ orders *tradeapp.UseCase }

func (a daumFetchFailureAdapter) HandlePermanentDaumFetchFailure(ctx context.Context, failure mailmatchapp.PermanentDaumFetchFailure) error {
	// FetchMailbox already fenced and committed the resource failure. Rewriting
	// it here could invalidate a newer successful validation; Trade selects only
	// orders whose resource is still abnormal.
	_, err := a.orders.RefundUnavailableDaumOrders(ctx, failure.ResourceID, failure.RequestID)
	return err
}
