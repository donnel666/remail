package api

import (
	"context"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
)

type matchResultAdapter struct {
	trade *tradeapp.UseCase
}

func (a matchResultAdapter) NotifyMatchedCode(ctx context.Context, result mailmatchapp.MatchResult) error {
	if a.trade == nil {
		return nil
	}
	return a.trade.NotifyMatchedCode(ctx, tradeapp.MatchCodeResultRequest{
		OrderNo:   result.OrderNo,
		MatchedAt: result.MatchedAt,
	})
}
