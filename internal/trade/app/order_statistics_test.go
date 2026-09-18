package app

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/trade/domain"
)

type cachedStatisticsRepo struct {
	Repository // Calling synchronous CountOrders or OrderFacets is a regression.
	items      []domain.Order
	next       *uint
}

func (r *cachedStatisticsRepo) ListOrders(context.Context, OrderListFilter, int, uint, int) ([]domain.Order, *uint, error) {
	return r.items, r.next, nil
}

func (*cachedStatisticsRepo) CachedOrderStats(context.Context, OrderListFilter) (int64, *OrderListFacets, error) {
	return 10, nil, nil // A cold/old snapshot must not hide the live page.
}

func TestListOrdersReturnsLatest500WithoutWaitingForStatistics(t *testing.T) {
	next := uint(501)
	repo := &cachedStatisticsRepo{items: make([]domain.Order, 500), next: &next}
	for i := range repo.items {
		repo.items[i] = domain.Order{ID: uint(1000 - i), UserID: 7}
	}
	uc := NewUseCase(repo, nil, nil, nil, nil)
	result, err := uc.ListOrders(context.Background(), OrderListFilter{UserID: 7}, 0, 0, 500)
	if err != nil || len(result.Items) != 500 || result.Total != 501 || result.NextAfterID == nil {
		t.Fatalf("latest orders hidden by stale statistics: %+v, %v", result, err)
	}
	repo.items, repo.next = nil, nil
	result, err = uc.ListOrders(context.Background(), OrderListFilter{UserID: 7}, 5000, 0, 500)
	if err != nil || result.Total != 10 {
		t.Fatalf("empty offset inflated the total: %+v, %v", result, err)
	}
}
