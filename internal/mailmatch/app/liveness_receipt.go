package app

import (
	"context"
	"strings"

	"github.com/donnel666/remail/internal/mailmatch/domain"
)

// StoreLivenessReceipt publishes a verified probe in the resource inbox without fulfilling an order.
func (uc *UseCase) StoreLivenessReceipt(ctx context.Context, req InboundMailRequest, fence func(context.Context) error) error {
	if uc == nil || uc.repo == nil {
		return domain.ErrMailServiceUnavailable
	}
	if req.ResourceType != domain.ResourceTypeICloud || req.EmailResourceID == 0 || strings.TrimSpace(req.Recipient) == "" ||
		len(req.Raw) == 0 || strings.TrimSpace(req.ProviderMessageID) == "" || req.ReceivedAt.IsZero() || fence == nil {
		return domain.ErrInvalidRequest
	}
	repo, ok := uc.repo.(MessageAppendRepository)
	if !ok {
		return domain.ErrMailServiceUnavailable
	}
	message := baseMessageFromFetched(inboundFetchedMessage(req))
	return uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		if err := fence(txCtx); err != nil {
			return err
		}
		stored, _, err := repo.AppendMessages(txCtx, []domain.Message{message})
		if err != nil {
			return err
		}
		if len(stored) != 1 {
			return domain.ErrMailServiceUnavailable
		}
		stored[0].Status = domain.MessageStatusIgnored
		stored[0].MatchDiagnostic = "iCloud liveness test email; no order matching required."
		stored[0].MatchedOrderID = nil
		stored[0].VerificationCode = ""
		_, _, err = repo.InsertMessageProjections(txCtx, stored)
		return err
	})
}
