package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/stretchr/testify/require"
)

type historicalImportRepoSpy struct {
	Repository
	events *[]string
	order  *domain.Order
}

func (r *historicalImportRepoSpy) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (r *historicalImportRepoSpy) CreateHistoricalOrder(context.Context, CreateHistoricalOrderCommand) error {
	*r.events = append(*r.events, "order")
	return nil
}

func (r *historicalImportRepoSpy) CreateHistoricalGmailOrder(context.Context, CreateHistoricalGmailOrderCommand) error {
	*r.events = append(*r.events, "gmail-order")
	return nil
}

func (r *historicalImportRepoSpy) FindOrder(context.Context, string) (*domain.Order, error) {
	*r.events = append(*r.events, "find-order")
	if r.order == nil {
		return nil, domain.ErrOrderNotFound
	}
	result := *r.order
	return &result, nil
}

type historicalImportWalletSpy struct {
	WalletPort
	events *[]string
}

func (w *historicalImportWalletSpy) RecordHistoricalZeroDebit(context.Context, WalletCommand) (*WalletTransaction, error) {
	*w.events = append(*w.events, "zero-debit")
	return &WalletTransaction{ID: 1}, nil
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

func (a *historicalImportAllocationSpy) ImportHistoricalGmailAllocation(context.Context, HistoricalGmailAllocationCommand) (*AllocationResult, error) {
	*a.events = append(*a.events, "allocation")
	return a.result, nil
}

func TestHistoricalImportRecordsZeroDebitWithoutLockingWallet(t *testing.T) {
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

	require.NoError(t, err)
	require.Equal(t, []string{"allocation", "zero-debit", "order"}, events)
}

func TestHistoricalImportNoopDoesNotWriteWalletOrOrder(t *testing.T) {
	events := []string{}
	repo := &historicalImportRepoSpy{events: &events}
	allocation := &historicalImportAllocationSpy{events: &events}
	uc := NewUseCase(repo, nil, &historicalImportWalletSpy{events: &events}, allocation, nil)

	err := uc.ImportHistoricalMicrosoftUsage(context.Background(), []HistoricalMicrosoftUsage{{
		ResourceID: 1, ProjectID: 2, ProductID: 3, Mailbox: "main", Email: "main@example.com",
		FirstMatchedAt: time.Now().Add(-time.Hour), LastMatchedAt: time.Now(), EvidenceCount: 1,
	}})

	require.NoError(t, err)
	require.Equal(t, []string{"allocation"}, events)
}

func TestHistoricalGmailImportReplaysExistingAndLegacyPlusWithoutWrites(t *testing.T) {
	events := []string{}
	allocationType := domain.AllocationTypeGmail
	debitID := uint(50)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	createdAt := now.Add(-time.Hour)
	expiredAt := now.Add(-time.Minute)
	orderNo := "HIST-GMAIL-existing"
	order := &domain.Order{
		OrderNo: orderNo, UserID: historicalGmailOwnerUserID, ProjectID: 10, ProjectProductID: 20,
		ProductType: domain.ProductTypeGmail, ServiceMode: domain.ServiceModePurchase,
		SupplyPolicy: domain.SupplyPolicyPublicOnly, Status: domain.OrderStatusCompleted,
		CodeWindowMinutes: 10, ActivationWindowMinutes: 20, WarrantyMinutes: 30,
		PayAmount: "0.00", RefundAmount: "0.00", DebitTxID: &debitID, AllocationType: &allocationType,
		DeliveryEmail: "user+legacy@gmail.com", ClientChannel: domain.ClientChannelConsole,
		ReceiveStartedAt: &createdAt, ReceiveUntil: &expiredAt, ActivatedAt: &createdAt, AfterSaleUntil: &expiredAt,
		IdempotencyKey:       "history:" + orderNo,
		RequestFingerprint:   fmt.Sprintf("%x", sha256.Sum256([]byte(orderNo))),
		ServiceCleanupStatus: "succeeded", CreatedAt: createdAt, Version: 1,
	}
	repo := &historicalImportRepoSpy{events: &events, order: order}
	allocation := &historicalImportAllocationSpy{events: &events, result: &AllocationResult{
		OrderNo: order.OrderNo, Type: domain.AllocationTypeGmail, ID: 40, ProductID: 20,
		Email: order.DeliveryEmail, SupplyScope: SupplyScopePublic, CreatedAt: createdAt, ReleasedAt: &expiredAt,
	}}
	uc := NewUseCase(repo, nil, &historicalImportWalletSpy{events: &events}, allocation, nil)
	uc.now = func() time.Time { return now }
	beforeOrder := *order
	beforeAllocation := *allocation.result
	base := HistoricalGmailUsage{
		ResourceID: 30, ProjectID: 10, ProductID: 20, ProductType: domain.ProductTypeGmail,
		Mailbox: "plus", Email: order.DeliveryEmail,
		CodeWindowMinutes: order.CodeWindowMinutes, ActivationWindowMinutes: order.ActivationWindowMinutes,
		WarrantyMinutes: order.WarrantyMinutes, FirstMatchedAt: createdAt,
		LastMatchedAt: expiredAt, EvidenceCount: 1,
	}

	require.NoError(t, uc.ImportHistoricalGmailUsage(context.Background(), []HistoricalGmailUsage{base}))
	variant := base
	variant.ProductID = 21
	variant.ProductType = domain.ProductTypeGmailVariant
	require.NoError(t, uc.ImportHistoricalGmailUsage(context.Background(), []HistoricalGmailUsage{variant}))

	require.Equal(t, []string{"allocation", "find-order", "allocation", "find-order"}, events)
	require.Equal(t, beforeOrder, *order)
	require.Equal(t, beforeAllocation, *allocation.result)

	events = events[:0]
	rescan := base
	rescan.CodeWindowMinutes++
	rescan.ActivationWindowMinutes++
	rescan.WarrantyMinutes++
	rescan.FirstMatchedAt = createdAt.Add(-time.Minute)
	rescan.LastMatchedAt = expiredAt.Add(time.Second)
	require.NoError(t, uc.ImportHistoricalGmailUsage(context.Background(), []HistoricalGmailUsage{rescan}))
	require.Equal(t, []string{"allocation", "find-order"}, events)

	events = events[:0]
	wrongTarget := variant
	wrongTarget.ProductType = domain.ProductTypeGmail
	require.ErrorIs(t, uc.ImportHistoricalGmailUsage(context.Background(), []HistoricalGmailUsage{wrongTarget}), domain.ErrIdempotencyConflict)
	require.Equal(t, []string{"allocation", "find-order"}, events)

	events = events[:0]
	order.RequestFingerprint = "inconsistent"
	require.ErrorIs(t, uc.ImportHistoricalGmailUsage(context.Background(), []HistoricalGmailUsage{base}), domain.ErrIdempotencyConflict)
	require.Equal(t, []string{"allocation", "find-order"}, events)
	order.RequestFingerprint = beforeOrder.RequestFingerprint

	events = events[:0]
	repo.order = nil
	allocation.result.Created = false
	require.ErrorIs(t, uc.ImportHistoricalGmailUsage(context.Background(), []HistoricalGmailUsage{base}), domain.ErrIdempotencyConflict)
	require.Equal(t, []string{"allocation", "find-order"}, events)

	events = events[:0]
	repo.order = order
	allocation.result.Created = true
	require.ErrorIs(t, uc.ImportHistoricalGmailUsage(context.Background(), []HistoricalGmailUsage{base}), domain.ErrIdempotencyConflict)
	require.Equal(t, []string{"allocation", "find-order"}, events)

	events = events[:0]
	repo.order = nil
	require.NoError(t, uc.ImportHistoricalGmailUsage(context.Background(), []HistoricalGmailUsage{base}))
	require.Equal(t, []string{"allocation", "find-order", "zero-debit", "gmail-order"}, events)
}
