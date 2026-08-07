package gmail

import (
	"context"
	"testing"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocdomain "github.com/donnel666/remail/internal/alloc/domain"
	allocinfra "github.com/donnel666/remail/internal/alloc/infra"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"gorm.io/gorm"
)

type localAllocationGuardModel = allocinfra.OrderGuardModel

type gmailTradeSpy struct {
	activations   []tradeapp.ActivateGmailOrderRequest
	completed     []string
	failed        []string
	history       []tradeapp.HistoricalGmailUsage
	release       func(context.Context, string) error
	importHistory func(context.Context, []tradeapp.HistoricalGmailUsage) error
}

func (s *gmailTradeSpy) ActivateGmailOrder(_ context.Context, req tradeapp.ActivateGmailOrderRequest) error {
	s.activations = append(s.activations, req)
	return nil
}

func (s *gmailTradeSpy) CompleteGmailOrder(ctx context.Context, orderNo, _ string) error {
	s.completed = append(s.completed, orderNo)
	if s.release != nil {
		return s.release(ctx, orderNo)
	}
	return nil
}

func (s *gmailTradeSpy) FailGmailOrder(ctx context.Context, orderNo, _ string) error {
	s.failed = append(s.failed, orderNo)
	if s.release != nil {
		return s.release(ctx, orderNo)
	}
	return nil
}

func (s *gmailTradeSpy) ImportHistoricalGmailUsage(ctx context.Context, history []tradeapp.HistoricalGmailUsage) error {
	if s.importHistory != nil {
		if err := s.importHistory(ctx, history); err != nil {
			return err
		}
	}
	s.history = append(s.history, history...)
	return nil
}

func newGmailHistoryTradeSpy(db *gorm.DB) *gmailTradeSpy {
	allocator := allocapp.NewUseCase(allocinfra.NewRepo(db))
	trade := &gmailTradeSpy{}
	trade.importHistory = func(ctx context.Context, history []tradeapp.HistoricalGmailUsage) error {
		for _, item := range history {
			if _, err := allocator.ImportHistoricalGmailAllocation(ctx, allocapp.HistoricalGmailAllocationCommand{
				ProjectID: item.ProjectID, ProductID: item.ProductID, ResourceID: item.ResourceID,
				Mailbox: allocdomain.GmailMailbox(item.Mailbox), Email: item.Email,
				CreatedAt: item.FirstMatchedAt, ReleasedAt: item.LastMatchedAt,
			}); err != nil {
				return err
			}
		}
		return nil
	}
	return trade
}

func setGmailRuntime(t *testing.T, values map[string]string) {
	t.Helper()
	previous := runtimeconfig.Snapshot()
	for key, value := range values {
		runtimeconfig.Set(key, value)
		key := key
		t.Cleanup(func() {
			if value, ok := previous[key]; ok {
				runtimeconfig.Set(key, value)
			} else {
				runtimeconfig.Delete(key)
			}
		})
	}
}
