package api

import (
	"context"
	"strings"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
)

type matchResultAdapter struct {
	trade *tradeapp.UseCase
}

func (a *matchResultAdapter) NotifyMatchedCode(ctx context.Context, result mailmatchapp.MatchResult) error {
	if result.ResourceType == domain.ResourceTypeGmail && result.ServiceMode == "code" && strings.TrimSpace(result.VerificationCode) == "" {
		return nil
	}
	if a.trade == nil {
		return nil
	}
	return a.trade.NotifyMatchedCode(ctx, tradeapp.MatchCodeResultRequest{
		OrderNo:   result.OrderNo,
		MatchedAt: result.MatchedAt,
	})
}
