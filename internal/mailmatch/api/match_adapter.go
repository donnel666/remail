package api

import (
	"context"
	"strings"
	"time"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
)

type GmailMatchPort interface {
	RecordMatchedCode(ctx context.Context, orderNo, code string, receivedAt time.Time) error
}

type matchResultAdapter struct {
	trade *tradeapp.UseCase
	gmail GmailMatchPort
}

func (a *matchResultAdapter) NotifyMatchedCode(ctx context.Context, result mailmatchapp.MatchResult) error {
	if result.ResourceType == domain.ResourceTypeGmail {
		if result.ServiceMode != "code" || strings.TrimSpace(result.VerificationCode) == "" || a.gmail == nil {
			return nil
		}
		return a.gmail.RecordMatchedCode(ctx, result.OrderNo, result.VerificationCode, result.MatchedAt)
	}
	if a.trade == nil {
		return nil
	}
	return a.trade.NotifyMatchedCode(ctx, tradeapp.MatchCodeResultRequest{
		OrderNo:   result.OrderNo,
		MatchedAt: result.MatchedAt,
	})
}
