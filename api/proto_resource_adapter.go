package api

import (
	"context"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	protoinfra "github.com/donnel666/remail/internal/proto/infra"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
)

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
