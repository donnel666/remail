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
	history             []tradeapp.HistoricalGmailUsage
	refundedResourceIDs []uint
	importHistory       func(context.Context, []tradeapp.HistoricalGmailUsage) error
}

func (s *gmailTradeSpy) RefundUnavailableGmailOrders(_ context.Context, resourceID uint, _ string) (int, error) {
	s.refundedResourceIDs = append(s.refundedResourceIDs, resourceID)
	return 1, nil
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
