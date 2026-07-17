package app

import (
	"context"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/billing/domain"
)

// financeStubRepo satisfies WalletRepository via the embedded (nil) interface;
// only the methods the finance use cases exercise are implemented.
type financeStubRepo struct {
	WalletRepository
	allCards    []domain.CardKey
	setStatus   func(keys []string, status domain.CardKeyStatus) (int, error)
	getTxn      *AdminTransaction
	getTxnErr   error
	reverse     func(ReverseTransactionCommand) (*ReverseTransactionResult, error)
	withdraw    func(WithdrawSupplierCommand) (*AdjustBalanceResult, error)
	walletsByID map[uint]domain.Wallet
}

func (s financeStubRepo) ListAllCards(context.Context, CardListFilter) ([]domain.CardKey, error) {
	return s.allCards, nil
}

func (s financeStubRepo) SetCardsStatus(_ context.Context, keys []string, status domain.CardKeyStatus) (int, error) {
	return s.setStatus(keys, status)
}

func (s financeStubRepo) GetAdminTransaction(context.Context, uint) (*AdminTransaction, error) {
	return s.getTxn, s.getTxnErr
}

func (s financeStubRepo) ReverseTransaction(_ context.Context, cmd ReverseTransactionCommand) (*ReverseTransactionResult, error) {
	return s.reverse(cmd)
}

func (s financeStubRepo) WithdrawSupplier(_ context.Context, cmd WithdrawSupplierCommand) (*AdjustBalanceResult, error) {
	return s.withdraw(cmd)
}

func (s financeStubRepo) GetWalletsByUserIDs(context.Context, []uint) (map[uint]domain.Wallet, error) {
	return s.walletsByID, nil
}

type stubDirectory struct {
	lookup func(ids []uint) (map[uint]UserDirectoryEntry, error)
	list   func(q UserDirectoryQuery) (UserDirectoryPage, error)
}

func (s stubDirectory) LookupUsers(_ context.Context, ids []uint) (map[uint]UserDirectoryEntry, error) {
	if s.lookup == nil {
		return map[uint]UserDirectoryEntry{}, nil
	}
	return s.lookup(ids)
}

func (s stubDirectory) ListUsers(_ context.Context, q UserDirectoryQuery) (UserDirectoryPage, error) {
	return s.list(q)
}

func ptrUint(v uint) *uint { return &v }

func TestBuildFinanceSummaryHourly(t *testing.T) {
	// Times are constructed in time.Local because the summary buckets in the
	// DB session zone (loc=Local); using time.Local keeps this test correct
	// under any TZ (the wall-clock hours are what the labels reflect).
	from := time.Date(2026, 3, 15, 9, 30, 0, 0, time.Local)
	to := time.Date(2026, 3, 15, 11, 15, 0, 0, time.Local)
	gran := financeGranularity(from, to)
	if gran != "hour" {
		t.Fatalf("same-day range should bucket hourly, got %q", gran)
	}
	rows := []LedgerBucketRow{{
		Bucket:             "2026-03-15 10:00:00",
		Recharge:           "50.00",
		Spend:              "100.00",
		Refund:             "20.00",
		SupplierSettlement: "30.00",
		AccountRevenue:     "5.00",
	}}
	result := buildFinanceSummary(from, to, gran, rows, nil, nil)

	if len(result.Trend) != 3 {
		t.Fatalf("want 3 hourly buckets (09:00..11:00), got %d", len(result.Trend))
	}
	if result.Trend[0].Label != "09:00" || result.Trend[1].Label != "10:00" || result.Trend[2].Label != "11:00" {
		t.Fatalf("unexpected hourly labels: %+v", []string{result.Trend[0].Label, result.Trend[1].Label, result.Trend[2].Label})
	}
	// platform revenue = spend - supplier settlement - refund = 100 - 30 - 20 = 50
	if result.Trend[1].Spend != 100 || result.Trend[1].PlatformRevenue != 50 {
		t.Fatalf("bucket math wrong: spend=%v platform=%v", result.Trend[1].Spend, result.Trend[1].PlatformRevenue)
	}
	if result.Trend[0].Spend != 0 {
		t.Fatalf("empty bucket should be zero, got %v", result.Trend[0].Spend)
	}
	if result.SpendAmount != "100.00" || result.PlatformRevenue != "50.00" || result.RefundAmount != "20.00" || result.RechargeAmount != "50.00" {
		t.Fatalf("totals wrong: %+v", result)
	}
}

func TestBuildFinanceSummaryDaily(t *testing.T) {
	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.Local)
	to := time.Date(2026, 3, 3, 0, 0, 0, 0, time.Local)
	gran := financeGranularity(from, to)
	if gran != "day" {
		t.Fatalf("multi-day range should bucket daily, got %q", gran)
	}
	rows := []LedgerBucketRow{{Bucket: "2026-03-02", Withdraw: "15.00"}}
	result := buildFinanceSummary(from, to, gran, rows, nil, nil)

	if len(result.Trend) != 3 {
		t.Fatalf("want 3 daily buckets, got %d", len(result.Trend))
	}
	if result.Trend[0].Label != "3/1" || result.Trend[1].Label != "3/2" || result.Trend[2].Label != "3/3" {
		t.Fatalf("unexpected daily labels: %+v", result.Trend)
	}
	if result.Trend[1].Withdraw != 15 || result.WithdrawAmount != "15.00" {
		t.Fatalf("withdraw math wrong: %v / %s", result.Trend[1].Withdraw, result.WithdrawAmount)
	}
}

func TestBuildFinanceSummaryCrossYearLabels(t *testing.T) {
	from := time.Date(2025, 12, 31, 0, 0, 0, 0, time.Local)
	to := time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)
	result := buildFinanceSummary(from, to, financeGranularity(from, to), nil, nil, nil)
	if len(result.Trend) != 2 || result.Trend[0].Label != "2025/12/31" || result.Trend[1].Label != "2026/1/1" {
		t.Fatalf("cross-year labels wrong: %+v", result.Trend)
	}
}

func TestComputeCardFacets(t *testing.T) {
	cards := []domain.CardKey{
		{Key: "A", Status: domain.CardKeyStatusEnabled, CreatedByUserID: ptrUint(1)},
		{Key: "B", Status: domain.CardKeyStatusDisabled, CreatedByUserID: ptrUint(2)},
		{Key: "C", Status: domain.CardKeyStatusEnabled, CreatedByUserID: ptrUint(1)},
		{Key: "D", Status: domain.CardKeyStatusEnabled}, // no owner
	}
	dir := map[uint]UserDirectoryEntry{
		1: {UserID: 1, Role: "supplier", GroupID: 9, GroupName: "Gold"},
		2: {UserID: 2, Role: "admin", GroupID: 9, GroupName: "Gold"},
	}
	f := computeCardFacets(cards, dir)
	if f.Status.All != 4 || f.Status.Enabled != 3 || f.Status.Disabled != 1 {
		t.Fatalf("status facets wrong: %+v", f.Status)
	}
	if f.Role.All != 4 || f.Role.Supplier != 2 || f.Role.Admin != 1 || f.Role.User != 0 {
		t.Fatalf("role facets wrong: %+v", f.Role)
	}
	if len(f.Groups) != 1 || f.Groups[0].ID != 9 || f.Groups[0].Count != 3 || f.Groups[0].Name != "Gold" {
		t.Fatalf("group facets wrong: %+v", f.Groups)
	}
}

func TestBulkSetCardStatusIDs(t *testing.T) {
	var gotKeys []string
	var gotStatus domain.CardKeyStatus
	repo := financeStubRepo{setStatus: func(keys []string, status domain.CardKeyStatus) (int, error) {
		gotKeys = keys
		gotStatus = status
		return 2, nil // 2 flipped, 1 already in state
	}}
	uc := NewWalletUseCase(repo)
	res, err := uc.BulkSetCardStatus(context.Background(), CardBulkSelection{Mode: "ids", CardKeys: []string{"A", "B", "C"}}, domain.CardKeyStatusDisabled)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStatus != domain.CardKeyStatusDisabled || len(gotKeys) != 3 {
		t.Fatalf("repo got status=%q keys=%v", gotStatus, gotKeys)
	}
	if res.Requested != 3 || res.Affected != 2 || res.Skipped != 1 {
		t.Fatalf("bulk result wrong: %+v", res)
	}
}

func TestBulkSetCardStatusFilterByOwnerRole(t *testing.T) {
	repo := financeStubRepo{
		allCards: []domain.CardKey{
			{Key: "A", Status: domain.CardKeyStatusEnabled, CreatedByUserID: ptrUint(1)},
			{Key: "B", Status: domain.CardKeyStatusEnabled, CreatedByUserID: ptrUint(2)},
		},
		setStatus: func(keys []string, _ domain.CardKeyStatus) (int, error) { return len(keys), nil },
	}
	uc := NewWalletUseCase(repo)
	uc.SetUserDirectory(stubDirectory{lookup: func(_ []uint) (map[uint]UserDirectoryEntry, error) {
		return map[uint]UserDirectoryEntry{1: {UserID: 1, Role: "supplier"}, 2: {UserID: 2, Role: "admin"}}, nil
	}})
	res, err := uc.BulkSetCardStatus(context.Background(), CardBulkSelection{
		Mode:   "filter",
		Filter: &CardBulkFilter{OwnerRole: "supplier"},
	}, domain.CardKeyStatusDisabled)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// only card A (owner role supplier) matches
	if res.Requested != 1 || res.Affected != 1 {
		t.Fatalf("owner-role filter wrong: %+v", res)
	}
}

func TestReverseTransactionRejectsReversalEntry(t *testing.T) {
	ofNo := "TX-ORIGINAL"
	repo := financeStubRepo{getTxn: &AdminTransaction{
		Transaction: domain.Transaction{ID: 5, TransactionNo: "TX-REV", UserID: 7, ReversalOfNo: &ofNo},
	}}
	uc := NewWalletUseCase(repo)
	if _, err := uc.ReverseTransaction(context.Background(), ReverseTransactionRequest{TransactionID: 5, IdempotencyKey: "k"}); err != domain.ErrTransactionNotReversible {
		t.Fatalf("want ErrTransactionNotReversible, got %v", err)
	}
}

func TestReverseTransactionRejectsAlreadyReversed(t *testing.T) {
	repo := financeStubRepo{getTxn: &AdminTransaction{
		Transaction: domain.Transaction{ID: 5, TransactionNo: "TX1", UserID: 7},
		Reversed:    true,
	}}
	uc := NewWalletUseCase(repo)
	if _, err := uc.ReverseTransaction(context.Background(), ReverseTransactionRequest{TransactionID: 5, IdempotencyKey: "k"}); err != domain.ErrTransactionAlreadyReversed {
		t.Fatalf("want ErrTransactionAlreadyReversed, got %v", err)
	}
}

func TestReverseTransactionRequiresIdempotencyKey(t *testing.T) {
	uc := NewWalletUseCase(financeStubRepo{})
	if _, err := uc.ReverseTransaction(context.Background(), ReverseTransactionRequest{TransactionID: 5}); err != domain.ErrIdempotencyRequired {
		t.Fatalf("want ErrIdempotencyRequired, got %v", err)
	}
}

func TestReverseTransactionHappyPath(t *testing.T) {
	var gotCmd ReverseTransactionCommand
	repo := financeStubRepo{
		getTxn: &AdminTransaction{Transaction: domain.Transaction{ID: 5, TransactionNo: "TX1", UserID: 7}},
		reverse: func(cmd ReverseTransactionCommand) (*ReverseTransactionResult, error) {
			gotCmd = cmd
			return &ReverseTransactionResult{
				Original: AdminTransaction{Transaction: domain.Transaction{ID: 5, TransactionNo: "TX1", UserID: 7}},
				Reversal: AdminTransaction{Transaction: domain.Transaction{ID: 6, TransactionNo: "TX2", UserID: 7}},
			}, nil
		},
	}
	uc := NewWalletUseCase(repo)
	res, err := uc.ReverseTransaction(context.Background(), ReverseTransactionRequest{TransactionID: 5, IdempotencyKey: "k"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotCmd.Original.TransactionNo != "TX1" || gotCmd.RequestFingerprint == "" {
		t.Fatalf("reverse command wrong: %+v", gotCmd)
	}
	if res.Reversal.Transaction.TransactionNo != "TX2" {
		t.Fatalf("reversal result wrong: %+v", res.Reversal)
	}
}

func TestWithdrawSupplierValidation(t *testing.T) {
	uc := NewWalletUseCase(financeStubRepo{})
	if _, err := uc.WithdrawSupplier(context.Background(), WithdrawSupplierRequest{UserID: 1, Amount: "-5", IdempotencyKey: "k"}); err != domain.ErrInvalidAmount {
		t.Fatalf("want ErrInvalidAmount, got %v", err)
	}
	if _, err := uc.WithdrawSupplier(context.Background(), WithdrawSupplierRequest{UserID: 1, Amount: "5.00"}); err != domain.ErrIdempotencyRequired {
		t.Fatalf("want ErrIdempotencyRequired, got %v", err)
	}
}

func TestWithdrawSupplierDefaultsBizID(t *testing.T) {
	var gotCmd WithdrawSupplierCommand
	repo := financeStubRepo{withdraw: func(cmd WithdrawSupplierCommand) (*AdjustBalanceResult, error) {
		gotCmd = cmd
		return &AdjustBalanceResult{}, nil
	}}
	uc := NewWalletUseCase(repo)
	if _, err := uc.WithdrawSupplier(context.Background(), WithdrawSupplierRequest{UserID: 1, Amount: "5", IdempotencyKey: "k"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotCmd.BizID != "withdrawal" || gotCmd.Amount != "5.00" {
		t.Fatalf("withdraw command wrong: %+v", gotCmd)
	}
}

func TestListAdminWalletsZeroesMissing(t *testing.T) {
	repo := financeStubRepo{walletsByID: map[uint]domain.Wallet{
		1: {UserID: 1, ConsumerBalance: "12.00", SupplierAvailable: "3.00", SupplierFrozen: "0.00"},
	}}
	uc := NewWalletUseCase(repo)
	uc.SetUserDirectory(stubDirectory{list: func(UserDirectoryQuery) (UserDirectoryPage, error) {
		return UserDirectoryPage{
			Entries: []UserDirectoryEntry{
				{UserID: 1, Email: "a@x.com"},
				{UserID: 2, Email: "b@x.com"},
			},
			Total: 5,
		}, nil
	}})
	res, err := uc.ListAdminWallets(context.Background(), "", 0, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Total != 5 || len(res.Items) != 2 {
		t.Fatalf("wallets list shape wrong: total=%d items=%d", res.Total, len(res.Items))
	}
	if res.Items[0].Wallet.ConsumerBalance != "12.00" {
		t.Fatalf("wallet 1 balance wrong: %+v", res.Items[0].Wallet)
	}
	if res.Items[1].Wallet.ConsumerBalance != "0.00" || res.Items[1].Wallet.SupplierAvailable != "0.00" {
		t.Fatalf("wallet 2 should be zeroed: %+v", res.Items[1].Wallet)
	}
	if res.Items[1].Entry.Email != "b@x.com" {
		t.Fatalf("wallet 2 identity lost: %+v", res.Items[1].Entry)
	}
}

func TestListAdminWalletsRequiresDirectory(t *testing.T) {
	uc := NewWalletUseCase(financeStubRepo{})
	if _, err := uc.ListAdminWallets(context.Background(), "", 0, 20); err == nil {
		t.Fatalf("expected error when user directory is not wired")
	}
}
