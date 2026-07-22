package app

import (
	"context"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/stretchr/testify/require"
)

type historicalImportRepoSpy struct {
	Repository
	events  *[]string
	ownerID uint
}

func (r *historicalImportRepoSpy) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (r *historicalImportRepoSpy) FindHistoricalOrderOwner(context.Context) (uint, error) {
	*r.events = append(*r.events, "owner")
	return r.ownerID, nil
}

func (*historicalImportRepoSpy) CreateHistoricalOrder(context.Context, CreateHistoricalOrderCommand) error {
	return nil
}

type historicalImportWalletSpy struct {
	WalletPort
	events *[]string
}

func (w *historicalImportWalletSpy) LockConsumer(context.Context, uint) error {
	*w.events = append(*w.events, "wallet")
	return nil
}

type historicalImportAllocationSpy struct {
	AllocationPort
	events *[]string
	result *AllocationResult
}

func (a *historicalImportAllocationSpy) ImportHistoricalMicrosoftAllocation(context.Context, HistoricalMicrosoftAllocationCommand) (*AllocationResult, error) {
	*a.events = append(*a.events, "allocation")
	return a.result, nil
}

func TestHistoricalImportLocksWalletBeforeAllocationRoot(t *testing.T) {
	events := []string{}
	repo := &historicalImportRepoSpy{events: &events, ownerID: 7}
	allocation := &historicalImportAllocationSpy{events: &events}
	uc := NewUseCase(repo, nil, &historicalImportWalletSpy{events: &events}, allocation, nil)

	err := uc.ImportHistoricalMicrosoftUsage(context.Background(), []HistoricalMicrosoftUsage{{
		ResourceID: 1, ProjectID: 2, ProductID: 3, Mailbox: "main", Email: "main@example.com",
		FirstMatchedAt: time.Now().Add(-time.Hour), LastMatchedAt: time.Now(), EvidenceCount: 1,
	}})

	require.NoError(t, err)
	require.Equal(t, []string{"owner", "wallet", "allocation"}, events)
}

func TestHistoricalImportNoopDoesNotRequireOwnerOrWallet(t *testing.T) {
	events := []string{}
	repo := &historicalImportRepoSpy{events: &events}
	allocation := &historicalImportAllocationSpy{events: &events}
	uc := NewUseCase(repo, nil, &historicalImportWalletSpy{events: &events}, allocation, nil)

	err := uc.ImportHistoricalMicrosoftUsage(context.Background(), []HistoricalMicrosoftUsage{{
		ResourceID: 1, ProjectID: 2, ProductID: 3, Mailbox: "main", Email: "main@example.com",
		FirstMatchedAt: time.Now().Add(-time.Hour), LastMatchedAt: time.Now(), EvidenceCount: 1,
	}})

	require.NoError(t, err)
	require.Equal(t, []string{"owner", "allocation"}, events)
}

func TestHistoricalImportCannotCreateWithoutOwner(t *testing.T) {
	events := []string{}
	repo := &historicalImportRepoSpy{events: &events}
	allocation := &historicalImportAllocationSpy{events: &events, result: &AllocationResult{
		OrderNo: "history-order", Type: domain.AllocationTypeMicrosoft, ID: 1,
	}}
	uc := NewUseCase(repo, nil, &historicalImportWalletSpy{events: &events}, allocation, nil)

	err := uc.ImportHistoricalMicrosoftUsage(context.Background(), []HistoricalMicrosoftUsage{{
		ResourceID: 1, ProjectID: 2, ProductID: 3, Mailbox: "main", Email: "main@example.com",
		FirstMatchedAt: time.Now().Add(-time.Hour), LastMatchedAt: time.Now(), EvidenceCount: 1,
	}})

	require.ErrorIs(t, err, ErrHistoricalAllocationOwnerRequired)
	require.Equal(t, []string{"owner", "allocation"}, events)
}
