package app

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/trade/domain"
)

type botSummaryRepo struct {
	Repository // Unimplemented console/facet methods fail if the summary invokes them.
	owner      uint
	reads      int
}

func (r *botSummaryRepo) ListOrders(_ context.Context, _ OrderListFilter, _ int, _ uint, _ int) ([]domain.Order, *uint, error) {
	r.reads++
	return []domain.Order{{UserID: r.owner, ProjectID: 7}}, nil, nil
}

func (r *botSummaryRepo) CountOrders(_ context.Context, _ OrderListFilter) (int64, error) {
	r.reads++
	return 1, nil
}

func TestBotSummaryDoesNotLoadConsoleFacetsAndRejectsOtherOwners(t *testing.T) {
	repo := &botSummaryRepo{owner: 9}
	uc := &UseCase{repo: repo}
	filter := OrderListFilter{UserID: 9, Scope: "mine"}
	result, err := uc.ListBotOrders(context.Background(), filter, 0, 0, 100)
	if err != nil || result == nil || result.Total != 1 || len(result.Items) != 1 || repo.reads != 2 {
		t.Fatalf("invalid owner summary: result=%+v reads=%d err=%v", result, repo.reads, err)
	}
	for _, denied := range []OrderListFilter{{Scope: "mine"}, {UserID: 9, Scope: "all"}, {UserID: 9, Scope: "mine", IsAdmin: true}} {
		before := repo.reads
		if _, err := uc.ListBotOrders(context.Background(), denied, 0, 0, 100); err == nil || repo.reads != before {
			t.Fatal("invalid owner scope reached the repository")
		}
	}
	repo.owner = 10
	if _, err := uc.ListBotOrders(context.Background(), filter, 0, 0, 100); err == nil {
		t.Fatal("foreign owner's row was accepted")
	}
}
