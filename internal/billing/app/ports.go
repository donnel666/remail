package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/billing/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform"
)

type WalletRepository interface {
	GetOrCreateWalletSummary(ctx context.Context, userID uint) (*domain.WalletSummary, error)
	ListConsumerBalances(ctx context.Context, userIDs []uint) (map[uint]string, error)
	GetReferralSummary(ctx context.Context, userID uint) (*domain.ReferralSummary, error)
	TransferReferralRewards(ctx context.Context, req TransferReferralRewardsCommand) (*TransferReferralRewardsResult, error)
	ListTransactions(ctx context.Context, filter TransactionListFilter, afterID uint, limit int) ([]domain.Transaction, *uint, error)
	ListRecharges(ctx context.Context, filter RechargeListFilter, offset, limit int) ([]domain.Recharge, error)
	CountRecharges(ctx context.Context, filter RechargeListFilter) (int64, error)
	RedeemCard(ctx context.Context, req RedeemCardCommand) (*RedeemCardResult, error)
	AdjustConsumerBalance(ctx context.Context, req AdjustConsumerBalanceCommand) (*AdjustBalanceResult, error)
	ListCards(ctx context.Context, filter CardListFilter, offset, limit int) ([]domain.CardKey, error)
	CountCards(ctx context.Context, filter CardListFilter) (int64, error)
	CreateCards(ctx context.Context, req CreateCardsCommand) ([]domain.CardKey, error)
	UpdateCard(ctx context.Context, req UpdateCardCommand) (*domain.CardKey, error)
}

// UserSelectionResolver resolves a selection-based batch (mode "ids" or
// "filter") into the concrete user IDs an admin may adjust. It lives in the
// billing package but is satisfied structurally by the IAM package at wiring
// time so the two bounded contexts stay decoupled.
type UserSelectionResolver interface {
	// ResolveAdjustableUserIDs returns non-super-admin user IDs targeted by the
	// selection (capped at 1000). mode is "ids" or "filter".
	ResolveAdjustableUserIDs(ctx context.Context, mode string, userIDs []uint, search string, role string, enabled *bool, userGroupID uint, createdFrom *time.Time, createdTo *time.Time) ([]uint, error)
}

type TransactionListFilter struct {
	UserID uint
	Search string
}

type RechargeListFilter struct {
	UserID uint
	Search string
	Status domain.RechargeStatus
}

type CardListFilter struct {
	Search string
	Status domain.CardKeyStatus
}

type RedeemCardRequest struct {
	UserID         uint
	CardKey        string
	IdempotencyKey string
	RequestID      string
}

type TransferReferralRewardsRequest struct {
	UserID         uint
	IdempotencyKey string
	RequestID      string
}

type TransferReferralRewardsCommand struct {
	UserID             uint
	IdempotencyKey     string
	RequestFingerprint string
	RequestID          string
	Now                time.Time
}

type TransferReferralRewardsResult struct {
	Wallet            domain.Wallet
	Transaction       domain.Transaction
	TransferredAmount string
	TransferredCount  int
}

type RedeemCardCommand struct {
	UserID             uint
	CardKey            string
	IdempotencyKey     string
	RequestFingerprint string
	RequestID          string
	Now                time.Time
}

type RedeemCardResult struct {
	Wallet      domain.Wallet
	Transaction domain.Transaction
	Card        domain.CardKey
}

type AdjustConsumerBalanceRequest struct {
	UserID          uint
	Amount          string
	Reason          string
	TransactionType domain.TransactionType
	IdempotencyKey  string
	RequestID       string
	OperationLog    *governancedomain.OperationLog
}

type AdjustConsumerBalanceCommand struct {
	UserID             uint
	Amount             string
	Reason             string
	TransactionType    domain.TransactionType
	Direction          domain.TransactionDirection
	IdempotencyKey     string
	RequestFingerprint string
	RequestID          string
	Now                time.Time
	OperationLog       *governancedomain.OperationLog
}

type AdjustBalanceResult struct {
	Wallet      domain.Wallet
	Transaction domain.Transaction
}

type CreateCardsRequest struct {
	Amount          string
	Count           int
	MaxRedemptions  int
	ExpireAt        *time.Time
	CardKeys        []string
	CreatedByUserID uint
	IdempotencyKey  string
	OperationLog    *governancedomain.OperationLog
}

type CreateCardsCommand struct {
	Cards              []domain.CardKey
	OwnerUserID        uint
	IdempotencyKey     string
	RequestFingerprint string
	OperationLog       *governancedomain.OperationLog
}

type UpdateCardRequest struct {
	CardKey      string
	Status       *domain.CardKeyStatus
	ExpireAt     *time.Time
	ExpireAtSet  bool
	OperationLog *governancedomain.OperationLog
}

type UpdateCardCommand = UpdateCardRequest

type WalletUseCase struct {
	repo WalletRepository
	now  func() time.Time
}

const (
	defaultBillingListLimit = 20
	maxBillingListLimit     = 1000
	maxCardCreateCount      = 1000
)

func NewWalletUseCase(repo WalletRepository) *WalletUseCase {
	return &WalletUseCase{
		repo: repo,
		now:  func() time.Time { return time.Now().UTC() },
	}
}

func (uc *WalletUseCase) GetWallet(ctx context.Context, userID uint) (*domain.WalletSummary, error) {
	if userID == 0 {
		return nil, domain.ErrInvalidFilter
	}
	return uc.repo.GetOrCreateWalletSummary(ctx, userID)
}

// ListConsumerBalances returns consumer balances keyed by user id, used to
// populate the admin user list balance column. Missing wallets are omitted.
func (uc *WalletUseCase) ListConsumerBalances(ctx context.Context, userIDs []uint) (map[uint]string, error) {
	if len(userIDs) == 0 {
		return map[uint]string{}, nil
	}
	return uc.repo.ListConsumerBalances(ctx, userIDs)
}

func (uc *WalletUseCase) GetReferralSummary(ctx context.Context, userID uint) (*domain.ReferralSummary, error) {
	if userID == 0 {
		return nil, domain.ErrInvalidFilter
	}
	return uc.repo.GetReferralSummary(ctx, userID)
}

func (uc *WalletUseCase) TransferReferralRewards(ctx context.Context, req TransferReferralRewardsRequest) (*TransferReferralRewardsResult, error) {
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if req.UserID == 0 {
		return nil, domain.ErrInvalidFilter
	}
	if idempotencyKey == "" {
		return nil, domain.ErrIdempotencyRequired
	}
	fingerprint := fingerprint("referrals.transfer", req.UserID)
	return uc.repo.TransferReferralRewards(ctx, TransferReferralRewardsCommand{
		UserID:             req.UserID,
		IdempotencyKey:     idempotencyKey,
		RequestFingerprint: fingerprint,
		RequestID:          strings.TrimSpace(req.RequestID),
		Now:                uc.now(),
	})
}

func (uc *WalletUseCase) ListTransactions(ctx context.Context, filter TransactionListFilter, afterID uint, limit int) (*TransactionListResult, error) {
	_, limit = normalizePagination(0, limit)
	items, nextAfterID, err := uc.repo.ListTransactions(ctx, filter, afterID, limit)
	if err != nil {
		return nil, err
	}
	return &TransactionListResult{Items: items, NextAfterID: nextAfterID, HasNext: nextAfterID != nil, Limit: limit}, nil
}

func (uc *WalletUseCase) ListRecharges(ctx context.Context, filter RechargeListFilter, offset, limit int) (*RechargeListResult, error) {
	offset, limit = normalizePagination(offset, limit)
	items, err := uc.repo.ListRecharges(ctx, filter, offset, limit)
	if err != nil {
		return nil, err
	}
	total, err := uc.repo.CountRecharges(ctx, filter)
	if err != nil {
		return nil, err
	}
	return &RechargeListResult{Items: items, Total: total, Offset: offset, Limit: limit}, nil
}

func (uc *WalletUseCase) RedeemCard(ctx context.Context, req RedeemCardRequest) (*RedeemCardResult, error) {
	cardKey := strings.TrimSpace(req.CardKey)
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if req.UserID == 0 || cardKey == "" {
		return nil, domain.ErrInvalidCardKey
	}
	if idempotencyKey == "" {
		return nil, domain.ErrIdempotencyRequired
	}
	fingerprint := fingerprint("cards.redeem", req.UserID, cardKey)
	return uc.repo.RedeemCard(ctx, RedeemCardCommand{
		UserID:             req.UserID,
		CardKey:            cardKey,
		IdempotencyKey:     idempotencyKey,
		RequestFingerprint: fingerprint,
		RequestID:          strings.TrimSpace(req.RequestID),
		Now:                uc.now(),
	})
}

func (uc *WalletUseCase) CreditConsumer(ctx context.Context, req AdjustConsumerBalanceRequest) (*AdjustBalanceResult, error) {
	req.TransactionType = domain.TransactionTypeCredit
	return uc.adjustConsumer(ctx, req, domain.TransactionDirectionIn)
}

func (uc *WalletUseCase) DebitConsumer(ctx context.Context, req AdjustConsumerBalanceRequest) (*AdjustBalanceResult, error) {
	req.TransactionType = domain.TransactionTypeDebit
	return uc.adjustConsumer(ctx, req, domain.TransactionDirectionOut)
}

func (uc *WalletUseCase) RefundConsumer(ctx context.Context, req AdjustConsumerBalanceRequest) (*AdjustBalanceResult, error) {
	req.TransactionType = domain.TransactionTypeRefund
	return uc.adjustConsumer(ctx, req, domain.TransactionDirectionIn)
}

func (uc *WalletUseCase) adjustConsumer(ctx context.Context, req AdjustConsumerBalanceRequest, direction domain.TransactionDirection) (*AdjustBalanceResult, error) {
	amount, err := normalizeConsumerAdjustmentAmount(req.Amount, req.TransactionType)
	if err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(req.Reason)
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if req.UserID == 0 || reason == "" {
		return nil, domain.ErrInvalidRecharge
	}
	if idempotencyKey == "" {
		return nil, domain.ErrIdempotencyRequired
	}
	fingerprint := fingerprint("wallet.adjust", req.UserID, string(req.TransactionType), string(direction), amount, reason)
	return uc.repo.AdjustConsumerBalance(ctx, AdjustConsumerBalanceCommand{
		UserID:             req.UserID,
		Amount:             amount,
		Reason:             reason,
		TransactionType:    req.TransactionType,
		Direction:          direction,
		IdempotencyKey:     idempotencyKey,
		RequestFingerprint: fingerprint,
		RequestID:          strings.TrimSpace(req.RequestID),
		Now:                uc.now(),
		OperationLog:       req.OperationLog,
	})
}

// BulkAdjustConsumer applies a signed amount to each user's consumer balance:
// positive credits, negative debits (abs value). The amount is parsed once; a
// zero amount is rejected. Each user gets a unique idempotency key derived from
// the requestID so a retried bulk request dedupes per user. A per-user
// insufficient-balance failure is counted as skipped without aborting the
// batch; any other error is treated as unexpected and returned.
func (uc *WalletUseCase) BulkAdjustConsumer(ctx context.Context, userIDs []uint, amount string, reason string, idempotencyKey string, requestID string, operationLog *governancedomain.OperationLog) (affected int, skipped int, err error) {
	parsed, err := domain.ParseMoney(amount)
	if err != nil {
		return 0, 0, err
	}
	credit := parsed.IsPositive()
	if !credit && !parsed.IsNegative() {
		return 0, 0, domain.ErrInvalidAmount
	}
	absAmount := domain.MoneyString(parsed.Abs())
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	requestID = strings.TrimSpace(requestID)
	for _, userID := range userIDs {
		req := AdjustConsumerBalanceRequest{
			UserID:         userID,
			Amount:         absAmount,
			Reason:         reason,
			IdempotencyKey: fingerprint("wallet.bulk.adjust", idempotencyKey, userID),
			RequestID:      requestID,
			OperationLog:   operationLog,
		}
		if credit {
			_, err = uc.CreditConsumer(ctx, req)
		} else {
			_, err = uc.DebitConsumer(ctx, req)
		}
		if err != nil {
			if errors.Is(err, domain.ErrInsufficientBalance) {
				skipped++
				continue
			}
			return affected, skipped, err
		}
		affected++
	}
	return affected, skipped, nil
}

func normalizeConsumerAdjustmentAmount(value string, transactionType domain.TransactionType) (string, error) {
	if transactionType == domain.TransactionTypeDebit || transactionType == domain.TransactionTypeRefund {
		return domain.NormalizeNonNegativeMoney(value)
	}
	return domain.NormalizePositiveMoney(value)
}

func (uc *WalletUseCase) ListCards(ctx context.Context, filter CardListFilter, offset, limit int) (*CardListResult, error) {
	offset, limit = normalizePagination(offset, limit)
	items, err := uc.repo.ListCards(ctx, filter, offset, limit)
	if err != nil {
		return nil, err
	}
	total, err := uc.repo.CountCards(ctx, filter)
	if err != nil {
		return nil, err
	}
	return &CardListResult{Items: items, Total: total, Offset: offset, Limit: limit}, nil
}

func (uc *WalletUseCase) CreateCards(ctx context.Context, req CreateCardsRequest) (*CreateCardsResult, error) {
	amount, err := domain.NormalizePositiveMoney(req.Amount)
	if err != nil {
		return nil, err
	}
	maxRedemptions := req.MaxRedemptions
	if maxRedemptions <= 0 {
		maxRedemptions = 1
	}

	cardKeys := normalizeCardKeys(req.CardKeys)
	providedCardKeys := len(cardKeys) > 0
	count := req.Count
	if providedCardKeys {
		count = len(cardKeys)
	}
	if count <= 0 {
		count = 1
	}
	if count > maxCardCreateCount {
		return nil, domain.ErrInvalidCardKey
	}
	if len(cardKeys) == 0 {
		cardKeys = generateCardKeys(count)
	}
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if req.CreatedByUserID == 0 || idempotencyKey == "" {
		return nil, domain.ErrIdempotencyRequired
	}
	expireAt := ""
	if req.ExpireAt != nil {
		expireAt = req.ExpireAt.UTC().Format(time.RFC3339Nano)
	}
	cardKeyFingerprint := ""
	if providedCardKeys {
		cardKeyFingerprint = strings.Join(cardKeys, "\n")
	}
	fingerprint := fingerprint("cards.create", req.CreatedByUserID, amount, count, maxRedemptions, expireAt, cardKeyFingerprint)
	cards := make([]domain.CardKey, 0, len(cardKeys))
	for _, key := range cardKeys {
		cards = append(cards, domain.CardKey{
			Key:             key,
			Amount:          amount,
			Status:          domain.CardKeyStatusEnabled,
			MaxRedemptions:  maxRedemptions,
			ExpireAt:        req.ExpireAt,
			CreatedByUserID: &req.CreatedByUserID,
		})
	}
	created, err := uc.repo.CreateCards(ctx, CreateCardsCommand{
		Cards:              cards,
		OwnerUserID:        req.CreatedByUserID,
		IdempotencyKey:     idempotencyKey,
		RequestFingerprint: fingerprint,
		OperationLog:       req.OperationLog,
	})
	if err != nil {
		return nil, err
	}
	return &CreateCardsResult{Items: created, Created: len(created)}, nil
}

func (uc *WalletUseCase) UpdateCard(ctx context.Context, req UpdateCardRequest) (*domain.CardKey, error) {
	req.CardKey = strings.TrimSpace(req.CardKey)
	if req.CardKey == "" {
		return nil, domain.ErrInvalidCardKey
	}
	if req.Status != nil && !domain.IsValidCardStatus(*req.Status) {
		return nil, domain.ErrInvalidCardStatus
	}
	return uc.repo.UpdateCard(ctx, req)
}

type TransactionListResult struct {
	Items       []domain.Transaction
	NextAfterID *uint
	HasNext     bool
	Limit       int
}

type RechargeListResult struct {
	Items  []domain.Recharge
	Total  int64
	Offset int
	Limit  int
}

type CardListResult struct {
	Items  []domain.CardKey
	Total  int64
	Offset int
	Limit  int
}

type CreateCardsResult struct {
	Items   []domain.CardKey
	Created int
}

func normalizePagination(offset, limit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = defaultBillingListLimit
	}
	if limit > maxBillingListLimit {
		limit = maxBillingListLimit
	}
	return offset, limit
}

func normalizeCardKeys(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	keys := make([]string, 0, len(values))
	for _, value := range values {
		key := strings.TrimSpace(value)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

func generateCardKeys(count int) []string {
	keys := make([]string, 0, count)
	for len(keys) < count {
		keys = append(keys, "RM-"+platform.NewUUIDV4CompactUpper())
	}
	return keys
}

func fingerprint(parts ...any) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprint(hash, part)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
