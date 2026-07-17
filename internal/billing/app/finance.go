package app

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/billing/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/shopspring/decimal"
)

const (
	maxCardRedemptionList = 500
	maxSearchUserResolve  = 500
	defaultSummaryDays    = 30
	maxTrendBuckets       = 2000
)

// SetUserDirectory injects the IAM-backed identity source used to enrich finance
// read models and drive the balances list. Wired after construction.
func (uc *WalletUseCase) SetUserDirectory(d UserDirectory) { uc.users = d }

// ---- Read models --------------------------------------------------------

type AdminCard struct {
	Card  domain.CardKey
	Owner *UserDirectoryEntry
}

type GroupFacet struct {
	ID    uint
	Name  string
	Count int
}

type CardRoleFacet struct {
	All        int
	User       int
	Supplier   int
	Admin      int
	SuperAdmin int
}

type CardStatusFacet struct {
	All      int
	Enabled  int
	Disabled int
}

type CardFacets struct {
	Role   CardRoleFacet
	Groups []GroupFacet
	Status CardStatusFacet
}

type AdminCardFilter struct {
	Search       string
	Status       domain.CardKeyStatus
	OwnerRole    string
	OwnerGroupID uint
}

type AdminCardListResult struct {
	Items  []AdminCard
	Total  int
	Offset int
	Limit  int
	Facets CardFacets
}

type CardBulkFilter struct {
	Search       string
	Status       domain.CardKeyStatus
	OwnerRole    string
	OwnerGroupID uint
}

type CardBulkSelection struct {
	Mode     string
	CardKeys []string
	Filter   *CardBulkFilter
}

type AdminBulkResult struct {
	Requested int
	Affected  int
	Skipped   int
}

type AdminCardRedemption struct {
	Redemption domain.CardRedemption
	Amount     string
	User       *UserDirectoryEntry
}

type AdminTransaction struct {
	Transaction  domain.Transaction
	Reversed     bool
	ReversedByNo *string
	User         *UserDirectoryEntry
}

type AdminTransactionFilter struct {
	Search        string
	SearchUserIDs []uint
	SearchUserID  uint
	Type          domain.TransactionType
	Direction     domain.TransactionDirection
	CreatedFrom   *time.Time
	CreatedTo     *time.Time
}

type AdminTransactionListResult struct {
	Items  []AdminTransaction
	Total  int64
	Offset int
	Limit  int
}

type ReverseTransactionRequest struct {
	TransactionID  uint
	IdempotencyKey string
	RequestID      string
	OperationLog   *governancedomain.OperationLog
}

type ReverseTransactionCommand struct {
	Original           domain.Transaction
	IdempotencyKey     string
	RequestFingerprint string
	RequestID          string
	Now                time.Time
	OperationLog       *governancedomain.OperationLog
}

type ReverseTransactionResult struct {
	Original AdminTransaction
	Reversal AdminTransaction
}

type WithdrawSupplierRequest struct {
	UserID         uint
	Amount         string
	Note           string
	IdempotencyKey string
	RequestID      string
	OperationLog   *governancedomain.OperationLog
}

type WithdrawSupplierCommand struct {
	UserID             uint
	Amount             string
	BizID              string
	IdempotencyKey     string
	RequestFingerprint string
	RequestID          string
	Now                time.Time
	OperationLog       *governancedomain.OperationLog
}

type AdminWallet struct {
	Entry  UserDirectoryEntry
	Wallet domain.Wallet
}

type AdminWalletListResult struct {
	Items  []AdminWallet
	Total  int
	Offset int
	Limit  int
}

// LedgerBucketRow is one time bucket of ledger aggregates (money as strings).
type LedgerBucketRow struct {
	Bucket             string
	Recharge           string
	Spend              string
	Withdraw           string
	Refund             string
	SupplierSettlement string
	AccountRevenue     string
}

type HotItem struct {
	Name   string
	Amount string
	Count  int64
}

type TrendPoint struct {
	Label           string
	Recharge        float64
	Spend           float64
	Withdraw        float64
	Refund          float64
	PlatformRevenue float64
	AccountRevenue  float64
}

type FinanceSummaryResult struct {
	RechargeAmount  string
	SpendAmount     string
	WithdrawAmount  string
	RefundAmount    string
	PlatformRevenue string
	AccountRevenue  string
	Trend           []TrendPoint
	HotProjects     []HotItem
	HotProducts     []HotItem
}

// ---- Cards --------------------------------------------------------------

// ListAdminCards loads every card once (facets are computed over all cards),
// enriches owners via the user directory, then filters + paginates in memory.
// ponytail: full card scan + in-memory owner filter; add a denormalized
// owner_role/owner_group column and SQL paging if card volume outgrows this.
func (uc *WalletUseCase) ListAdminCards(ctx context.Context, filter AdminCardFilter, offset, limit int) (*AdminCardListResult, error) {
	offset, limit = normalizePagination(offset, limit)
	all, err := uc.repo.ListAllCards(ctx, CardListFilter{})
	if err != nil {
		return nil, err
	}
	dir := uc.lookupUsers(ctx, cardOwnerIDs(all))
	facets := computeCardFacets(all, dir)

	filtered := make([]domain.CardKey, 0, len(all))
	for _, card := range all {
		if cardMatchesAdminFilter(card, filter, dir) {
			filtered = append(filtered, card)
		}
	}
	total := len(filtered)
	page := filtered
	if offset >= len(page) {
		page = nil
	} else {
		page = page[offset:]
	}
	if len(page) > limit {
		page = page[:limit]
	}
	items := make([]AdminCard, len(page))
	for i, card := range page {
		items[i] = AdminCard{Card: card, Owner: ownerEntry(dir, card.CreatedByUserID)}
	}
	return &AdminCardListResult{Items: items, Total: total, Offset: offset, Limit: limit, Facets: facets}, nil
}

func (uc *WalletUseCase) BulkSetCardStatus(ctx context.Context, selection CardBulkSelection, status domain.CardKeyStatus) (*AdminBulkResult, error) {
	if !domain.IsValidCardStatus(status) {
		return nil, domain.ErrInvalidCardStatus
	}
	var keys []string
	switch selection.Mode {
	case "ids":
		keys = normalizeCardKeys(selection.CardKeys)
	case "filter":
		f := CardBulkFilter{}
		if selection.Filter != nil {
			f = *selection.Filter
		}
		all, err := uc.repo.ListAllCards(ctx, CardListFilter{Search: f.Search, Status: f.Status})
		if err != nil {
			return nil, err
		}
		dir := uc.lookupUsers(ctx, cardOwnerIDs(all))
		af := AdminCardFilter{Search: f.Search, Status: f.Status, OwnerRole: f.OwnerRole, OwnerGroupID: f.OwnerGroupID}
		for _, card := range all {
			if cardMatchesAdminFilter(card, af, dir) {
				keys = append(keys, card.Key)
			}
		}
	default:
		return nil, domain.ErrInvalidFilter
	}
	requested := len(keys)
	if requested == 0 {
		return &AdminBulkResult{}, nil
	}
	// SetCardsStatus flips only rows not already in the target status, so
	// RowsAffected == number changed; the rest are skipped (already in state
	// or nonexistent keys).
	affected, err := uc.repo.SetCardsStatus(ctx, keys, status)
	if err != nil {
		return nil, err
	}
	return &AdminBulkResult{Requested: requested, Affected: affected, Skipped: requested - affected}, nil
}

func (uc *WalletUseCase) ListCardRedemptions(ctx context.Context, cardKey string) ([]AdminCardRedemption, error) {
	cardKey = strings.TrimSpace(cardKey)
	if cardKey == "" {
		return nil, domain.ErrInvalidCardKey
	}
	redemptions, amount, err := uc.repo.ListCardRedemptions(ctx, cardKey, maxCardRedemptionList)
	if err != nil {
		return nil, err
	}
	ids := make([]uint, 0, len(redemptions))
	for _, r := range redemptions {
		ids = append(ids, r.UserID)
	}
	dir := uc.lookupUsers(ctx, ids)
	items := make([]AdminCardRedemption, len(redemptions))
	for i, r := range redemptions {
		items[i] = AdminCardRedemption{Redemption: r, Amount: amount, User: lookupEntry(dir, r.UserID)}
	}
	return items, nil
}

// ---- Transactions -------------------------------------------------------

func (uc *WalletUseCase) ListAdminTransactions(ctx context.Context, filter AdminTransactionFilter, offset, limit int) (*AdminTransactionListResult, error) {
	offset, limit = normalizePagination(offset, limit)
	if search := strings.TrimSpace(filter.Search); search != "" {
		if id, err := strconv.ParseUint(search, 10, 64); err == nil {
			filter.SearchUserID = uint(id)
		}
		if uc.users != nil {
			if page, err := uc.users.ListUsers(ctx, UserDirectoryQuery{Search: search, Limit: maxSearchUserResolve}); err == nil {
				for _, e := range page.Entries {
					filter.SearchUserIDs = append(filter.SearchUserIDs, e.UserID)
				}
			}
		}
	}
	items, total, err := uc.repo.ListAdminTransactions(ctx, filter, offset, limit)
	if err != nil {
		return nil, err
	}
	ids := make([]uint, 0, len(items))
	for i := range items {
		ids = append(ids, items[i].Transaction.UserID)
	}
	dir := uc.lookupUsers(ctx, ids)
	for i := range items {
		items[i].User = lookupEntry(dir, items[i].Transaction.UserID)
	}
	return &AdminTransactionListResult{Items: items, Total: total, Offset: offset, Limit: limit}, nil
}

func (uc *WalletUseCase) ReverseTransaction(ctx context.Context, req ReverseTransactionRequest) (*ReverseTransactionResult, error) {
	if req.TransactionID == 0 {
		return nil, domain.ErrTransactionNotFound
	}
	key := strings.TrimSpace(req.IdempotencyKey)
	if key == "" {
		return nil, domain.ErrIdempotencyRequired
	}
	original, err := uc.repo.GetAdminTransaction(ctx, req.TransactionID)
	if err != nil {
		return nil, err
	}
	if original.Transaction.ReversalOfNo != nil {
		return nil, domain.ErrTransactionNotReversible
	}
	if original.Reversed {
		return nil, domain.ErrTransactionAlreadyReversed
	}
	result, err := uc.repo.ReverseTransaction(ctx, ReverseTransactionCommand{
		Original:           original.Transaction,
		IdempotencyKey:     key,
		RequestFingerprint: fingerprint("transactions.reverse", original.Transaction.TransactionNo),
		RequestID:          strings.TrimSpace(req.RequestID),
		Now:                uc.now(),
		OperationLog:       req.OperationLog,
	})
	if err != nil {
		return nil, err
	}
	dir := uc.lookupUsers(ctx, []uint{result.Original.Transaction.UserID})
	result.Original.User = lookupEntry(dir, result.Original.Transaction.UserID)
	result.Reversal.User = lookupEntry(dir, result.Reversal.Transaction.UserID)
	return result, nil
}

// ---- Wallets ------------------------------------------------------------

func (uc *WalletUseCase) ListAdminWallets(ctx context.Context, search string, offset, limit int) (*AdminWalletListResult, error) {
	offset, limit = normalizePagination(offset, limit)
	if uc.users == nil {
		return nil, domain.ErrInvalidFilter
	}
	page, err := uc.users.ListUsers(ctx, UserDirectoryQuery{Search: strings.TrimSpace(search), Offset: offset, Limit: limit})
	if err != nil {
		return nil, err
	}
	ids := make([]uint, 0, len(page.Entries))
	for _, e := range page.Entries {
		ids = append(ids, e.UserID)
	}
	wallets, err := uc.repo.GetWalletsByUserIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	items := make([]AdminWallet, len(page.Entries))
	for i, e := range page.Entries {
		items[i] = AdminWallet{Entry: e, Wallet: walletOrZero(wallets, e.UserID)}
	}
	return &AdminWalletListResult{Items: items, Total: page.Total, Offset: offset, Limit: limit}, nil
}

func (uc *WalletUseCase) WithdrawSupplier(ctx context.Context, req WithdrawSupplierRequest) (*AdjustBalanceResult, error) {
	amount, err := domain.NormalizePositiveMoney(req.Amount)
	if err != nil {
		return nil, err
	}
	if req.UserID == 0 {
		return nil, domain.ErrInvalidFilter
	}
	key := strings.TrimSpace(req.IdempotencyKey)
	if key == "" {
		return nil, domain.ErrIdempotencyRequired
	}
	note := strings.TrimSpace(req.Note)
	bizID := note
	if bizID == "" {
		bizID = "withdrawal"
	}
	return uc.repo.WithdrawSupplier(ctx, WithdrawSupplierCommand{
		UserID:             req.UserID,
		Amount:             amount,
		BizID:              bizID,
		IdempotencyKey:     key,
		RequestFingerprint: fingerprint("wallets.withdraw", req.UserID, amount, note),
		RequestID:          strings.TrimSpace(req.RequestID),
		Now:                uc.now(),
		OperationLog:       req.OperationLog,
	})
}

// ---- Finance summary ----------------------------------------------------

func (uc *WalletUseCase) FinanceSummary(ctx context.Context, from, to *time.Time) (*FinanceSummaryResult, error) {
	fromT, toT := resolveFinanceRange(from, to, uc.now())
	gran := financeGranularity(fromT, toT)
	rows, err := uc.repo.FinanceLedgerBuckets(ctx, gran, fromT, toT)
	if err != nil {
		return nil, err
	}
	hotProjects, err := uc.repo.HotOrderItems(ctx, "project", fromT, toT, 10)
	if err != nil {
		return nil, err
	}
	hotProducts, err := uc.repo.HotOrderItems(ctx, "product", fromT, toT, 10)
	if err != nil {
		return nil, err
	}
	result := buildFinanceSummary(fromT, toT, gran, rows, hotProjects, hotProducts)
	return &result, nil
}

// buildFinanceSummary assembles a continuous trend series and totals from the
// per-bucket ledger aggregates. Empty buckets are filled with zeros so the
// chart is evenly spaced.
// ponytail: platform/account revenue is a ledger-derived estimate
// (platform = spend - supplier settlement credited - refund; account =
// referral credit rewards); upgrade to a real settlement-margin/commission
// source when available.
func buildFinanceSummary(from, to time.Time, gran string, rows []LedgerBucketRow, hotProjects, hotProducts []HotItem) FinanceSummaryResult {
	keyed := make(map[string]LedgerBucketRow, len(rows))
	for _, r := range rows {
		keyed[r.Bucket] = r
	}
	layout := bucketLayout(gran)
	sameYear := from.In(time.Local).Year() == to.In(time.Local).Year()

	var trend []TrendPoint
	rechargeT, spendT, withdrawT, refundT := decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero
	platformT, accountT := decimal.Zero, decimal.Zero

	for t := bucketStart(from, gran); !t.After(bucketStart(to, gran)) && len(trend) < maxTrendBuckets; t = nextBucket(t, gran) {
		row := keyed[t.Format(layout)]
		recharge := summaryMoney(row.Recharge)
		spend := summaryMoney(row.Spend)
		withdraw := summaryMoney(row.Withdraw)
		refund := summaryMoney(row.Refund)
		settlement := summaryMoney(row.SupplierSettlement)
		account := summaryMoney(row.AccountRevenue)
		// ponytail: platform revenue is a ledger-derived estimate and is floored
		// at zero for display (a refund-heavy bucket must not show negative
		// "income"); revisit if a signed net-margin metric is ever wanted.
		platform := spend.Sub(settlement).Sub(refund)
		if platform.IsNegative() {
			platform = decimal.Zero
		}

		trend = append(trend, TrendPoint{
			Label:           trendLabel(t, gran, sameYear),
			Recharge:        recharge.InexactFloat64(),
			Spend:           spend.InexactFloat64(),
			Withdraw:        withdraw.InexactFloat64(),
			Refund:          refund.InexactFloat64(),
			PlatformRevenue: platform.InexactFloat64(),
			AccountRevenue:  account.InexactFloat64(),
		})
		rechargeT = rechargeT.Add(recharge)
		spendT = spendT.Add(spend)
		withdrawT = withdrawT.Add(withdraw)
		refundT = refundT.Add(refund)
		platformT = platformT.Add(platform)
		accountT = accountT.Add(account)
	}
	if trend == nil {
		trend = []TrendPoint{}
	}
	return FinanceSummaryResult{
		RechargeAmount:  domain.MoneyString(rechargeT),
		SpendAmount:     domain.MoneyString(spendT),
		WithdrawAmount:  domain.MoneyString(withdrawT),
		RefundAmount:    domain.MoneyString(refundT),
		PlatformRevenue: domain.MoneyString(platformT),
		AccountRevenue:  domain.MoneyString(accountT),
		Trend:           trend,
		HotProjects:     hotProjects,
		HotProducts:     hotProducts,
	}
}

// ---- helpers ------------------------------------------------------------

func (uc *WalletUseCase) lookupUsers(ctx context.Context, ids []uint) map[uint]UserDirectoryEntry {
	if uc.users == nil || len(ids) == 0 {
		return map[uint]UserDirectoryEntry{}
	}
	m, err := uc.users.LookupUsers(ctx, ids)
	if err != nil || m == nil {
		return map[uint]UserDirectoryEntry{}
	}
	return m
}

func cardOwnerIDs(cards []domain.CardKey) []uint {
	seen := make(map[uint]struct{}, len(cards))
	ids := make([]uint, 0, len(cards))
	for _, c := range cards {
		if c.CreatedByUserID == nil || *c.CreatedByUserID == 0 {
			continue
		}
		if _, ok := seen[*c.CreatedByUserID]; ok {
			continue
		}
		seen[*c.CreatedByUserID] = struct{}{}
		ids = append(ids, *c.CreatedByUserID)
	}
	return ids
}

func ownerEntry(dir map[uint]UserDirectoryEntry, id *uint) *UserDirectoryEntry {
	if id == nil {
		return nil
	}
	return lookupEntry(dir, *id)
}

func lookupEntry(dir map[uint]UserDirectoryEntry, id uint) *UserDirectoryEntry {
	if id == 0 {
		return nil
	}
	if e, ok := dir[id]; ok {
		return &e
	}
	return nil
}

func cardMatchesAdminFilter(card domain.CardKey, filter AdminCardFilter, dir map[uint]UserDirectoryEntry) bool {
	if filter.Status != "" && card.Status != filter.Status {
		return false
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		if !strings.Contains(strings.ToLower(card.Key), strings.ToLower(search)) {
			return false
		}
	}
	if filter.OwnerRole == "" && filter.OwnerGroupID == 0 {
		return true
	}
	owner := ownerEntry(dir, card.CreatedByUserID)
	if owner == nil {
		return false
	}
	if filter.OwnerRole != "" && owner.Role != filter.OwnerRole {
		return false
	}
	if filter.OwnerGroupID != 0 && owner.GroupID != filter.OwnerGroupID {
		return false
	}
	return true
}

func computeCardFacets(cards []domain.CardKey, dir map[uint]UserDirectoryEntry) CardFacets {
	facets := CardFacets{}
	facets.Role.All = len(cards)
	facets.Status.All = len(cards)
	groups := map[uint]*GroupFacet{}
	for _, c := range cards {
		switch c.Status {
		case domain.CardKeyStatusEnabled:
			facets.Status.Enabled++
		case domain.CardKeyStatusDisabled:
			facets.Status.Disabled++
		}
		owner := ownerEntry(dir, c.CreatedByUserID)
		if owner == nil {
			continue
		}
		switch owner.Role {
		case "user":
			facets.Role.User++
		case "supplier":
			facets.Role.Supplier++
		case "admin":
			facets.Role.Admin++
		case "super_admin":
			facets.Role.SuperAdmin++
		}
		if owner.GroupID != 0 {
			g, ok := groups[owner.GroupID]
			if !ok {
				g = &GroupFacet{ID: owner.GroupID, Name: owner.GroupName}
				groups[owner.GroupID] = g
			}
			g.Count++
		}
	}
	facets.Groups = make([]GroupFacet, 0, len(groups))
	for _, g := range groups {
		facets.Groups = append(facets.Groups, *g)
	}
	sort.Slice(facets.Groups, func(i, j int) bool {
		if facets.Groups[i].Count != facets.Groups[j].Count {
			return facets.Groups[i].Count > facets.Groups[j].Count
		}
		return facets.Groups[i].ID < facets.Groups[j].ID
	})
	return facets
}

func walletOrZero(wallets map[uint]domain.Wallet, id uint) domain.Wallet {
	if w, ok := wallets[id]; ok {
		return w
	}
	return domain.Wallet{UserID: id, ConsumerBalance: "0.00", SupplierAvailable: "0.00", SupplierFrozen: "0.00"}
}

func resolveFinanceRange(from, to *time.Time, now time.Time) (time.Time, time.Time) {
	toT := now.UTC()
	if to != nil {
		toT = to.UTC()
	}
	var fromT time.Time
	if from != nil {
		fromT = from.UTC()
	} else {
		fromT = toT.AddDate(0, 0, -defaultSummaryDays)
	}
	if fromT.After(toT) {
		fromT = toT
	}
	// Clamp the span so daily buckets never exceed maxTrendBuckets; otherwise the
	// trend loop caps mid-range while the SQL aggregates the whole span, which
	// would under-report the header totals. Keeps the most recent window.
	if maxSpan := time.Duration(maxTrendBuckets) * 24 * time.Hour; toT.Sub(fromT) > maxSpan {
		fromT = toT.Add(-maxSpan)
	}
	return fromT, toT
}

// financeGranularity buckets by hour for a single calendar day, else by day.
// The calendar day is evaluated in time.Local because the ledger SQL groups by
// DATE_FORMAT(created_at) in the DB session's zone, which the DSN pins to Local
// (loc=Local); bucketing here in any other zone would desync the keys.
func financeGranularity(from, to time.Time) string {
	fl, tl := from.In(time.Local), to.In(time.Local)
	if fl.Year() == tl.Year() && fl.YearDay() == tl.YearDay() {
		return "hour"
	}
	return "day"
}

func bucketLayout(gran string) string {
	if gran == "hour" {
		return "2006-01-02 15:00:00"
	}
	return "2006-01-02"
}

// bucketStart truncates to the hour/day boundary in time.Local so the formatted
// key matches the SQL DATE_FORMAT bucket (see financeGranularity).
func bucketStart(t time.Time, gran string) time.Time {
	t = t.In(time.Local)
	if gran == "hour" {
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.Local)
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}

func nextBucket(t time.Time, gran string) time.Time {
	if gran == "hour" {
		return t.Add(time.Hour)
	}
	return t.AddDate(0, 0, 1)
}

func trendLabel(t time.Time, gran string, sameYear bool) string {
	if gran == "hour" {
		return pad2(t.Hour()) + ":00"
	}
	if sameYear {
		return strconv.Itoa(int(t.Month())) + "/" + strconv.Itoa(t.Day())
	}
	return strconv.Itoa(t.Year()) + "/" + strconv.Itoa(int(t.Month())) + "/" + strconv.Itoa(t.Day())
}

func pad2(v int) string {
	s := strconv.Itoa(v)
	if len(s) < 2 {
		return "0" + s
	}
	return s
}

func summaryMoney(value string) decimal.Decimal {
	if strings.TrimSpace(value) == "" {
		return decimal.Zero
	}
	amount, err := domain.ParseMoney(value)
	if err != nil {
		return decimal.Zero
	}
	return amount
}
