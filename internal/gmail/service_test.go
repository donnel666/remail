package gmail

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
)

type gmailTradeSpy struct {
	activations []tradeapp.ActivateGmailOrderRequest
	completed   []string
	failed      []string
	history     []tradeapp.HistoricalGmailUsage
}

func (s *gmailTradeSpy) ActivateGmailOrder(_ context.Context, req tradeapp.ActivateGmailOrderRequest) error {
	s.activations = append(s.activations, req)
	return nil
}

func (s *gmailTradeSpy) CompleteGmailOrder(_ context.Context, orderNo, _ string) error {
	s.completed = append(s.completed, orderNo)
	return nil
}

func (s *gmailTradeSpy) FailGmailOrder(_ context.Context, orderNo, _ string) error {
	s.failed = append(s.failed, orderNo)
	return nil
}

func (s *gmailTradeSpy) ImportHistoricalGmailUsage(_ context.Context, history []tradeapp.HistoricalGmailUsage) error {
	s.history = append(s.history, history...)
	return nil
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
