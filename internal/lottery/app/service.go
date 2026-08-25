package app

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"sort"
	"strings"
	"time"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	lotterydomain "github.com/donnel666/remail/internal/lottery/domain"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/money"
	"github.com/donnel666/remail/internal/platform"
	"github.com/shopspring/decimal"
)

const (
	// v3 uses history-weighted fixed prize counts and allocates the complete pool.
	algorithmVersion               = "fixed-tier-v3"
	legacyAlgorithmVersionV2       = "bounded-tier-v2"
	legacyAlgorithmVersionV1       = "bounded-tier-v1"
	winnerInitialScore       int64 = 1000
	winnerLuckyPenalty       int64 = 500
	winnerNormalPenalty      int64 = 100
	winnerConsolationBonus   int64 = 50
	// ponytail: keep one settlement below the current Billing task ceiling;
	// raise this only with a chunked Billing contract.
	maxLotteryEntries = 5000
)

func (s *Service) Create(ctx context.Context, req CreateRequest) (*CreateResult, error) {
	now := s.now().UTC()
	key := strings.TrimSpace(req.IdempotencyKey)
	if req.CreatedByUserID == 0 || key == "" || len(key) > 128 {
		return nil, lotterydomain.ErrLotteryInvalidRules
	}
	req.LotteryType = normalizeLotteryType(req.LotteryType)
	poolIncrement, _, incrementErr := normalizedPoolIncrement(req)
	if incrementErr != nil {
		return nil, incrementErr
	}
	req.PoolIncrementAmount = poolIncrement
	total, minPayout, maxPayout, maxParticipants, err := validateRules(req)
	if err != nil {
		return nil, err
	}
	fingerprint := lotteryRequestFingerprint(req, total, minPayout, maxPayout)
	if existing, lookupErr := s.repo.FindByIdempotency(ctx, req.CreatedByUserID, key); lookupErr == nil && existing != nil {
		storedFingerprint := existing.RequestFingerprint
		if storedFingerprint == "" {
			storedFingerprint = lotteryFingerprintFromDomain(*existing)
		}
		if storedFingerprint != fingerprint {
			return nil, lotterydomain.ErrLotteryIdempotencyConflict
		}
		return &CreateResult{Lottery: existing, Replayed: true}, nil
	} else if lookupErr != nil && !errors.Is(lookupErr, lotterydomain.ErrLotteryNotFound) {
		return nil, lookupErr
	}
	if req.DrawAt != nil && !req.DrawAt.UTC().After(now) {
		return nil, lotterydomain.ErrLotteryInvalidRules
	}
	target := copyInt(req.ParticipantTarget)
	lottery := &lotterydomain.Lottery{
		PublicToken:         platform.NewUUIDV4String(),
		CreatedByUserID:     req.CreatedByUserID,
		Title:               truncateTitle(req.Title),
		LotteryType:         req.LotteryType,
		StartingAmount:      total,
		TotalAmount:         total,
		PoolIncrementAmount: poolIncrement,
		MinPayout:           minPayout,
		MaxPayout:           maxPayout,
		TierWeights:         req.TierWeights,
		MinAccountAgeDays:   req.MinAccountAgeDays,
		DrawAt:              copyTime(req.DrawAt),
		ParticipantTarget:   target,
		MaxParticipants:     maxParticipants,
		Status:              lotterydomain.StatusOpen,
		AlgorithmVersion:    algorithmVersionForWeights(req.TierWeights),
		UnusedAmount:        "0.00",
		IdempotencyKey:      key,
		RequestFingerprint:  fingerprint,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if err := s.repo.Create(ctx, lottery); err != nil {
		if existing, lookupErr := s.repo.FindByIdempotency(ctx, req.CreatedByUserID, lottery.IdempotencyKey); lookupErr == nil && existing != nil {
			storedFingerprint := existing.RequestFingerprint
			if storedFingerprint == "" {
				storedFingerprint = lotteryFingerprintFromDomain(*existing)
			}
			if storedFingerprint != fingerprint {
				return nil, lotterydomain.ErrLotteryIdempotencyConflict
			}
			return &CreateResult{Lottery: existing, Replayed: true}, nil
		}
		return nil, err
	}
	if s.queue != nil && lottery.DrawAt != nil {
		_ = s.queue.EnqueueDraw(ctx, lottery.ID, lottery.DrawAt)
	}
	return &CreateResult{Lottery: lottery}, nil
}

func (s *Service) Enter(ctx context.Context, token string, userID uint) (*EntryResult, error) {
	if userID == 0 {
		return nil, lotterydomain.ErrLotteryNotEligible
	}
	lottery, err := s.repo.GetByToken(ctx, strings.TrimSpace(token))
	if err != nil {
		return nil, err
	}
	if lottery.CreatedByUserID == userID {
		return nil, &lotterydomain.EntryRejectedError{
			Code: lotterydomain.EntryRejectedCreator, Cause: lotterydomain.ErrLotteryClosed,
		}
	}
	now := s.now().UTC()
	if lottery.Status != lotterydomain.StatusOpen ||
		(lottery.DrawAt != nil && !lottery.DrawAt.After(now)) ||
		(lottery.ParticipantTarget != nil && lottery.ParticipantCount >= *lottery.ParticipantTarget) {
		return nil, &lotterydomain.EntryRejectedError{
			Code: lotterydomain.EntryRejectedClosed, Cause: lotterydomain.ErrLotteryClosed,
		}
	}
	if lottery.MaxParticipants > 0 && lottery.ParticipantCount >= lottery.MaxParticipants {
		return nil, &lotterydomain.EntryRejectedError{
			Code: lotterydomain.EntryRejectedFull, Cause: lotterydomain.ErrLotteryClosed,
		}
	}
	if s.users == nil {
		return nil, &lotterydomain.EntryRejectedError{
			Code: lotterydomain.EntryRejectedInactive, Cause: lotterydomain.ErrLotteryNotEligible,
		}
	}
	user, err := s.users.FindLotteryUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user == nil || strings.ToLower(strings.TrimSpace(user.Status)) != "active" {
		return nil, &lotterydomain.EntryRejectedError{
			Code: lotterydomain.EntryRejectedInactive, Cause: lotterydomain.ErrLotteryNotEligible,
		}
	}
	now = s.now().UTC()
	if lottery.MinAccountAgeDays > 0 && user.CreatedAt.After(now.AddDate(0, 0, -lottery.MinAccountAgeDays)) {
		return nil, &lotterydomain.EntryRejectedError{
			Code: lotterydomain.EntryRejectedAge, RequiredDays: lottery.MinAccountAgeDays,
			Cause: lotterydomain.ErrLotteryNotEligible,
		}
	}
	result, err := s.repo.AddEntry(ctx, lottery.ID, userID, user.CreatedAt, s.now)
	if err != nil {
		if errors.Is(err, lotterydomain.ErrLotteryClosed) {
			return nil, &lotterydomain.EntryRejectedError{
				Code: lotterydomain.EntryRejectedClosed, Cause: lotterydomain.ErrLotteryClosed,
			}
		}
		return nil, err
	}
	if !result.AlreadyExists && result.Lottery != nil && result.Lottery.ParticipantTarget != nil && result.Lottery.ParticipantCount >= *result.Lottery.ParticipantTarget && s.queue != nil {
		_ = s.queue.EnqueueDraw(ctx, lottery.ID, nil)
	}
	return result, nil
}

func (s *Service) Draw(ctx context.Context, lotteryID uint) error {
	if lotteryID == 0 {
		return lotterydomain.ErrLotteryNotFound
	}
	lottery, err := s.repo.GetByID(ctx, lotteryID)
	if err != nil {
		return err
	}
	if lottery.Status == lotterydomain.StatusCompleted || lottery.Status == lotterydomain.StatusCancelled {
		return nil
	}
	if lottery.Status == lotterydomain.StatusOpen {
		lottery, err = s.repo.ClaimSettlement(ctx, lotteryID, s.now().UTC())
		if errors.Is(err, lotterydomain.ErrLotteryNotReady) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	if lottery.Status != lotterydomain.StatusSettling {
		return nil
	}
	if txRepo, ok := s.repo.(DrawTransactionRepository); ok {
		var settledLottery *lotterydomain.Lottery
		var payouts []lotterydomain.Payout
		err := txRepo.WithDrawTransaction(ctx, lotteryID, func(txCtx context.Context, locked *lotterydomain.Lottery) error {
			if locked.Status != lotterydomain.StatusSettling {
				return nil
			}
			settledLottery, payouts, err = s.drawSettling(txCtx, locked)
			return err
		})
		if err != nil {
			return err
		}
		if settledLottery != nil && len(payouts) > 0 {
			s.sendWinnerEmails(ctx, *settledLottery, payouts)
		}
		return nil
	}
	settledLottery, payouts, err := s.drawSettling(ctx, lottery)
	if err != nil {
		return err
	}
	if settledLottery != nil && len(payouts) > 0 {
		s.sendWinnerEmails(ctx, *settledLottery, payouts)
	}
	return nil
}

// drawSettling performs one complete settlement attempt. The caller may wrap
// it in a repository transaction; emails are intentionally sent afterwards.
func (s *Service) drawSettling(ctx context.Context, lottery *lotterydomain.Lottery) (*lotterydomain.Lottery, []lotterydomain.Payout, error) {
	entries, err := s.repo.ListAllEntries(ctx, lottery.ID)
	if err != nil {
		return nil, nil, err
	}
	existing, err := s.repo.GetPayouts(ctx, lottery.ID)
	if err != nil {
		return nil, nil, err
	}
	var payouts []lotterydomain.Payout
	var unusedAmount string
	if len(existing) > 0 {
		payouts = existing
		unusedAmount, err = remainingUnused(lottery.TotalAmount, payouts)
		if err != nil {
			return nil, nil, err
		}
	} else if len(entries) > 0 {
		if lockRepo, ok := s.repo.(WinnerUserLockRepository); ok {
			if err := lockRepo.LockWinnerUsers(ctx, entryUserIDs(entries)); err != nil {
				return nil, nil, err
			}
		}
		switch lottery.AlgorithmVersion {
		case algorithmVersion:
			stats, statsErr := s.repo.LookupWinnerStats(ctx, entryUserIDs(entries))
			if statsErr != nil {
				return nil, nil, statsErr
			}
			if err := rankEntriesByHistory(entries, stats); err != nil {
				return nil, nil, err
			}
			weights := lottery.TierWeights
			if lottery.TriggeredBy == lotterydomain.TriggerTime &&
				(len(entries) < weights.Normal+weights.Lucky) {
				weights = fitEarlyDrawCounts(len(entries), lottery.TotalAmount, lottery.MinPayout, lottery.MaxPayout, weights)
			}
			payouts, unusedAmount, err = allocateFixedCountsRanked(entries, lottery.TotalAmount, lottery.MinPayout, lottery.MaxPayout, weights)
		case legacyAlgorithmVersionV2:
			payouts, unusedAmount, err = allocateLegacy(entries, lottery.TotalAmount, lottery.MinPayout, lottery.MaxPayout, lottery.TierWeights, true)
		case legacyAlgorithmVersionV1:
			payouts, unusedAmount, err = allocateLegacy(entries, lottery.TotalAmount, lottery.MinPayout, lottery.MaxPayout, lottery.TierWeights, false)
		default:
			payouts, unusedAmount, err = allocateLegacy(entries, lottery.TotalAmount, lottery.MinPayout, lottery.MaxPayout, lottery.TierWeights, false)
		}
		if err != nil {
			if !isLotteryAllocationFallbackError(err) {
				return nil, nil, err
			}
			allocationErr := err
			payouts, unusedAmount, err = fallbackLotteryAllocation(entries, *lottery)
			if err != nil {
				return nil, nil, fmt.Errorf("fallback allocation for lottery %d: %w", lottery.ID, err)
			}
			slog.Warn("lottery allocation fallback used", "lottery_id", lottery.ID, "entries", len(entries), "error", allocationErr)
		} else if lotteryAllocationHasUnused(unusedAmount) {
			originalUnused := unusedAmount
			payouts, unusedAmount, err = fallbackLotteryAllocation(entries, *lottery)
			if err != nil {
				return nil, nil, fmt.Errorf("fallback allocation for lottery %d: %w", lottery.ID, err)
			}
			slog.Warn("lottery allocation fallback replaced unused budget", "lottery_id", lottery.ID, "entries", len(entries), "unused_amount", originalUnused)
		}
		if err := s.repo.SavePayouts(ctx, lottery.ID, payouts); err != nil {
			return nil, nil, err
		}
		// Another worker may have won the insert race. Always settle using the
		// persisted payout rows, never a discarded in-memory allocation.
		payouts, err = s.repo.GetPayouts(ctx, lottery.ID)
		if err != nil {
			return nil, nil, err
		}
	} else {
		unusedAmount = lottery.TotalAmount
	}
	if len(payouts) == 0 {
		// No participant wallet is credited. The unused system budget is only
		// recorded for audit.
		if err := s.repo.RecordBillingTransactions(ctx, lottery.ID, nil, unusedAmount); err != nil {
			return nil, nil, err
		}
		if err := s.repo.Complete(ctx, lottery.ID, lotterydomain.StatusCancelled, unusedAmount, s.now().UTC()); err != nil {
			return nil, nil, err
		}
		return nil, nil, nil
	}
	awards := make([]billingapp.LotteryAward, len(payouts))
	for i := range payouts {
		awards[i] = billingapp.LotteryAward{UserID: payouts[i].UserID, Amount: payouts[i].Amount}
	}
	if s.billing == nil {
		return nil, nil, lotterydomain.ErrLotterySettlement
	}
	settlement, err := s.billing.SettleLotteryPool(ctx, billingapp.LotterySettlementRequest{
		LotteryID: lottery.ID, TotalAmount: lottery.TotalAmount,
		Awards: awards, UnusedAmount: unusedAmount,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("settle lottery %d: %w", lottery.ID, err)
	}
	if settlement == nil {
		return nil, nil, fmt.Errorf("settle lottery %d: nil billing result: %w", lottery.ID, lotterydomain.ErrLotterySettlement)
	}
	if err := validateSettlementAwards(payouts, settlement.Awards); err != nil {
		return nil, nil, fmt.Errorf("settle lottery %d: %w", lottery.ID, err)
	}
	transactions := make(map[uint]string, len(settlement.Awards))
	for _, award := range settlement.Awards {
		transactions[award.UserID] = strings.TrimSpace(award.Transaction.TransactionNo)
	}
	if err := s.repo.RecordBillingTransactions(ctx, lottery.ID, transactions, unusedAmount); err != nil {
		return nil, nil, err
	}
	if err := s.repo.Complete(ctx, lottery.ID, lotterydomain.StatusCompleted, unusedAmount, s.now().UTC()); err != nil {
		return nil, nil, err
	}
	return lottery, payouts, nil
}

func (s *Service) sendWinnerEmails(ctx context.Context, lottery lotterydomain.Lottery, payouts []lotterydomain.Payout) {
	if s.delivery == nil || s.users == nil || len(payouts) == 0 {
		return
	}
	ids := make([]uint, len(payouts))
	for i := range payouts {
		ids[i] = payouts[i].UserID
	}
	users, err := s.users.LookupLotteryUsers(ctx, ids)
	if err != nil {
		return
	}
	for _, payout := range payouts {
		user, ok := users[payout.UserID]
		if !ok || strings.TrimSpace(user.Email) == "" {
			continue
		}
		_ = s.delivery.Send(ctx, mailapp.LotteryWinnerMessage(user.Email, lottery.ID, lottery.Title, payout.Amount))
	}
}

func algorithmVersionForWeights(weights lotterydomain.TierWeights) string {
	if weights.ValidLegacyPercentages() {
		// Keep old percentage clients on the v2 whole-point allocator. New
		// requests use consolation=0 and are unambiguously v3 fixed counts.
		return legacyAlgorithmVersionV2
	}
	return algorithmVersion
}

func validateRules(req CreateRequest) (total, minPayout, maxPayout string, maxParticipants int, err error) {
	req.LotteryType = normalizeLotteryType(req.LotteryType)
	_, _, incrementErr := normalizedPoolIncrement(req)
	if incrementErr != nil {
		err = incrementErr
		return
	}
	totalDec, parseErr := money.Parse(req.TotalAmount)
	if parseErr != nil || !totalDec.IsPositive() {
		err = lotterydomain.ErrLotteryInvalidRules
		return
	}
	minDec, parseErr := money.Parse(req.MinPayout)
	if parseErr != nil || !minDec.IsPositive() {
		err = lotterydomain.ErrLotteryInvalidRules
		return
	}
	maxDec, parseErr := money.Parse(req.MaxPayout)
	if parseErr != nil || !maxDec.IsPositive() || !minDec.LessThan(maxDec) {
		err = lotterydomain.ErrLotteryInvalidRules
		return
	}
	legacyWeights := req.TierWeights.ValidLegacyPercentages()
	fixedWeights := req.TierWeights.ValidFixedCounts()
	if req.MinAccountAgeDays < 0 || (!legacyWeights && !fixedWeights) ||
		(req.LotteryType == lotterydomain.LotteryTypeGrowing && !fixedWeights) ||
		(fixedWeights && (req.TierWeights.Normal > maxLotteryEntries || req.TierWeights.Lucky > maxLotteryEntries-req.TierWeights.Normal)) ||
		(req.DrawAt == nil && req.ParticipantTarget == nil) {
		err = lotterydomain.ErrLotteryInvalidRules
		return
	}
	if req.ParticipantTarget != nil && (*req.ParticipantTarget <= 0 || *req.ParticipantTarget > maxLotteryEntries) {
		err = lotterydomain.ErrLotteryInvalidRules
		return
	}
	totalUnits, totalErr := wholePointUnits(totalDec)
	minUnits, minErr := wholePointUnits(minDec)
	maxUnits, maxErr := wholePointUnits(maxDec)
	if totalErr != nil || minErr != nil || maxErr != nil || minUnits <= 0 || maxUnits <= minUnits {
		err = lotterydomain.ErrLotteryInvalidRules
		return
	}
	maxParticipants = maxLotteryEntries
	if req.ParticipantTarget != nil {
		target := int64(*req.ParticipantTarget)
		if target > math.MaxInt64/minUnits || target*minUnits > totalUnits ||
			target > math.MaxInt64/maxUnits || target*maxUnits < totalUnits {
			// The target draw must be able to distribute the complete pool within
			// the configured per-participant range.
			err = lotterydomain.ErrLotteryInvalidRules
			return
		}
		if fixedWeights && !fixedCountsFitPool(target, totalUnits, minUnits, maxUnits, req.TierWeights) {
			err = lotterydomain.ErrLotteryInvalidRules
			return
		}
		maxParticipants = int(target)
	} else {
		if totalUnits < minUnits {
			err = lotterydomain.ErrLotteryInvalidRules
			return
		}
		if req.LotteryType == lotterydomain.LotteryTypeGrowing {
			// The starting pool is used for creation validation, but growing
			// lotteries must keep accepting entries until their real draw condition.
			return money.Format(totalDec), money.Format(minDec), money.Format(maxDec), maxParticipants, nil
		}
		// A time-only activity has no user-supplied target, so cap entries at a
		// count that can receive at least the configured minimum.
		fundable := totalUnits / minUnits
		if fundable < int64(maxParticipants) {
			maxParticipants = int(fundable)
		}
		if fixedWeights {
			if int64(req.TierWeights.Normal+req.TierWeights.Lucky) > int64(maxParticipants) ||
				!fixedCountsCanFitAnyPool(int64(maxParticipants), totalUnits, minUnits, maxUnits, req.TierWeights) {
				err = lotterydomain.ErrLotteryInvalidRules
				return
			}
		}
	}
	return money.Format(totalDec), money.Format(minDec), money.Format(maxDec), maxParticipants, nil
}

func normalizeLotteryType(value lotterydomain.LotteryType) lotterydomain.LotteryType {
	if value == "" {
		return lotterydomain.LotteryTypeFixed
	}
	return value
}

func normalizedPoolIncrement(req CreateRequest) (string, decimal.Decimal, error) {
	value := strings.TrimSpace(req.PoolIncrementAmount)
	if value == "" {
		value = "0"
	}
	increment, err := money.Parse(value)
	if err != nil || !increment.IsInteger() || increment.IsNegative() {
		return "", decimal.Zero, lotterydomain.ErrLotteryInvalidRules
	}
	typeValue := normalizeLotteryType(req.LotteryType)
	if !typeValue.Valid() ||
		(typeValue == lotterydomain.LotteryTypeFixed && !increment.IsZero()) ||
		(typeValue == lotterydomain.LotteryTypeGrowing && !increment.IsPositive()) {
		return "", decimal.Zero, lotterydomain.ErrLotteryInvalidRules
	}
	return money.Format(increment), increment, nil
}

func fixedCountsFitPool(n, total, minValue, maxValue int64, counts lotterydomain.TierWeights) bool {
	if n <= 0 || minValue <= 0 || maxValue <= minValue || !counts.ValidFixedCounts() {
		return false
	}
	lucky, normal := int64(counts.Lucky), int64(counts.Normal)
	if lucky < 0 || normal < 0 || lucky > n-normal {
		return false
	}
	capacity := maxValue - minValue
	if n > math.MaxInt64/minValue || n > math.MaxInt64/maxValue || lucky > math.MaxInt64/capacity {
		return false
	}
	base := n * minValue
	if lucky*capacity > math.MaxInt64-base {
		return false
	}
	base += lucky * capacity
	if total < base {
		return false
	}
	// Normal prizes absorb the pool first; any remaining amount is spread
	// evenly across consolation prizes. Every participant still respects the
	// configured maximum, so the whole pool must fit within n*maxValue.
	return total <= n*maxValue
}

// fitEarlyDrawCounts keeps a time-triggered fixed-count lottery settleable
// when the clock wins before its participant target. Lucky seats are downgraded to
// normal seats only as needed; the requested special-seat total is preserved
// up to the number of entries actually present.
func fitEarlyDrawCounts(n int, totalAmount, minPayout, maxPayout string, counts lotterydomain.TierWeights) lotterydomain.TierWeights {
	if n <= 0 {
		return counts
	}
	totalDec, totalErr := money.Parse(totalAmount)
	minDec, minErr := money.Parse(minPayout)
	maxDec, maxErr := money.Parse(maxPayout)
	total, totalUnitsErr := wholePointUnits(totalDec)
	minValue, minUnitsErr := wholePointUnits(minDec)
	maxValue, maxUnitsErr := wholePointUnits(maxDec)
	if totalErr != nil || minErr != nil || maxErr != nil || totalUnitsErr != nil || minUnitsErr != nil || maxUnitsErr != nil {
		return counts
	}
	lucky := int64(counts.Lucky)
	normal := int64(counts.Normal)
	if lucky < 0 || normal < 0 {
		return counts
	}
	if lucky > int64(n) {
		lucky = int64(n)
	}
	special := lucky
	if normal > int64(n)-special {
		special = int64(n)
	} else {
		special += normal
	}
	for {
		normal = special - lucky
		candidate := lotterydomain.TierWeights{Normal: int(normal), Lucky: int(lucky)}
		if fixedCountsFitPool(int64(n), total, minValue, maxValue, candidate) || lucky == 0 {
			return candidate
		}
		lucky--
	}
}

func fixedCountsCanFitAnyPool(maxParticipants, total, minValue, maxValue int64, counts lotterydomain.TierWeights) bool {
	minimumParticipants := int64(counts.Normal + counts.Lucky)
	if minimumParticipants < 1 {
		minimumParticipants = 1
	}
	if minimumParticipants > maxParticipants {
		return false
	}
	for n := minimumParticipants; n <= maxParticipants; n++ {
		if fixedCountsFitPool(n, total, minValue, maxValue, counts) {
			return true
		}
	}
	return false
}

func Allocate(entries []lotterydomain.Entry, totalAmount, minPayout, maxPayout string, weights lotterydomain.TierWeights) ([]lotterydomain.Payout, string, error) {
	if weights.ValidLegacyPercentages() {
		return allocateLegacy(entries, totalAmount, minPayout, maxPayout, weights, true)
	}
	return allocateFixedCounts(entries, totalAmount, minPayout, maxPayout, weights)
}

func allocate(entries []lotterydomain.Entry, totalAmount, minPayout, maxPayout string, weights lotterydomain.TierWeights, fixedCounts bool) ([]lotterydomain.Payout, string, error) {
	if fixedCounts {
		return allocateFixedCounts(entries, totalAmount, minPayout, maxPayout, weights)
	}
	return allocateLegacy(entries, totalAmount, minPayout, maxPayout, weights, false)
}

func allocateFixedCountsRanked(entries []lotterydomain.Entry, totalAmount, minPayout, maxPayout string, counts lotterydomain.TierWeights) ([]lotterydomain.Payout, string, error) {
	return allocateFixedCountsWithShuffle(entries, totalAmount, minPayout, maxPayout, counts, false)
}

// allocateFixedCounts gives lucky entries the configured maximum. Normal
// entries absorb the remaining pool first; any surplus is split evenly across
// consolation entries, so the settlement sum is exact.
func allocateFixedCounts(entries []lotterydomain.Entry, totalAmount, minPayout, maxPayout string, counts lotterydomain.TierWeights) ([]lotterydomain.Payout, string, error) {
	return allocateFixedCountsWithShuffle(entries, totalAmount, minPayout, maxPayout, counts, true)
}

func allocateFixedCountsWithShuffle(entries []lotterydomain.Entry, totalAmount, minPayout, maxPayout string, counts lotterydomain.TierWeights, shuffleEntries bool) ([]lotterydomain.Payout, string, error) {
	if len(entries) == 0 || !counts.ValidFixedCounts() {
		return nil, "0.00", lotterydomain.ErrLotteryInvalidRules
	}
	totalDec, err := money.Parse(totalAmount)
	if err != nil {
		return nil, "0.00", err
	}
	minDec, err := money.Parse(minPayout)
	if err != nil {
		return nil, "0.00", err
	}
	maxDec, err := money.Parse(maxPayout)
	if err != nil {
		return nil, "0.00", err
	}
	total, totalErr := wholePointUnits(totalDec)
	minValue, minErr := wholePointUnits(minDec)
	maxValue, maxErr := wholePointUnits(maxDec)
	if totalErr != nil || minErr != nil || maxErr != nil || total <= 0 {
		return nil, "0.00", lotterydomain.ErrLotteryInvalidRules
	}
	n := int64(len(entries))
	if n > maxLotteryEntries || minValue <= 0 || maxValue <= minValue {
		return nil, "0.00", lotterydomain.ErrLotteryInvalidRules
	}
	if n > math.MaxInt64/minValue || n > math.MaxInt64/maxValue {
		return nil, "0.00", lotterydomain.ErrLotteryInvalidRules
	}
	minTotal := n * minValue
	if total < minTotal || total > n*maxValue {
		// The complete pool must fit between the per-entry minimum and maximum.
		return nil, "0.00", lotterydomain.ErrLotteryInsufficientParticipants
	}

	capacity := maxValue - minValue
	luckyCount := int64(counts.Lucky)
	normalCount := int64(counts.Normal)
	if luckyCount < 0 || normalCount < 0 || luckyCount > n-normalCount ||
		!fixedCountsFitPool(n, total, minValue, maxValue, counts) {
		return nil, "0.00", lotterydomain.ErrLotteryInsufficientParticipants
	}
	normalCapacity := capacity
	if normalCapacity <= 0 || normalCapacity > math.MaxInt64-minValue {
		return nil, "0.00", lotterydomain.ErrLotteryInsufficientParticipants
	}
	normalMax := minValue + normalCapacity

	if shuffleEntries {
		if err := randomShuffle(entries); err != nil {
			return nil, "0.00", err
		}
	}
	luckyCountInt, normalCountInt := int(luckyCount), int(normalCount)
	tiers := make([]lotterydomain.Tier, len(entries))
	amounts := make([]int64, len(entries))
	for i := range entries {
		tiers[i] = lotterydomain.TierConsolation
		amounts[i] = minValue
	}
	for i := 0; i < luckyCountInt; i++ {
		tiers[i] = lotterydomain.TierLucky
		amounts[i] = maxValue
	}
	for i := luckyCountInt; i < luckyCountInt+normalCountInt; i++ {
		tiers[i] = lotterydomain.TierNormal
	}

	// Lucky awards already contribute their full max-minus-min bonus. Normal
	// awards absorb the remaining pool first; once they reach max, the rest is
	// split as evenly as possible across consolation awards.
	bonus := total - minTotal - luckyCount*capacity
	if normalCount > 0 {
		normalAmounts := make([]int64, normalCountInt)
		normalWeights := make([]int64, normalCountInt)
		for i := range normalAmounts {
			normalAmounts[i] = minValue
			normalWeights[i], err = tierRandomWeight(lotterydomain.TierNormal)
			if err != nil {
				return nil, "0.00", err
			}
		}
		normalBonusCapacity := normalCount * normalCapacity
		normalBonus := bonus
		if normalBonus > normalBonusCapacity {
			normalBonus = normalBonusCapacity
		}
		if err := distributeBonus(normalAmounts, normalWeights, normalBonus, normalCapacity); err != nil {
			return nil, "0.00", err
		}
		bonus -= normalBonus
		if normalBonus > 0 && len(normalAmounts) > 1 {
			ensureVariation(normalAmounts, normalWeights, minValue, normalMax)
		}
		for i, amount := range normalAmounts {
			amounts[luckyCountInt+i] = amount
		}
	}
	if bonus > 0 {
		consolationStart := luckyCountInt + normalCountInt
		if consolationStart >= len(amounts) {
			return nil, "0.00", lotterydomain.ErrLotteryInsufficientParticipants
		}
		if err := distributeEvenly(amounts[consolationStart:], bonus, capacity); err != nil {
			return nil, "0.00", err
		}
	}

	payouts := make([]lotterydomain.Payout, len(entries))
	now := time.Now().UTC()
	for i, entry := range entries {
		payouts[i] = lotterydomain.Payout{
			LotteryID: entry.LotteryID,
			UserID:    entry.UserID,
			Tier:      tiers[i],
			Amount:    wholePointsAmount(amounts[i]),
			CreatedAt: now,
		}
	}
	return payouts, "0.00", nil
}

// allocateLegacy keeps the percentage-based ledger-unit path available for
// campaigns persisted before fixed-count rules were introduced.
func allocateLegacy(entries []lotterydomain.Entry, totalAmount, minPayout, maxPayout string, weights lotterydomain.TierWeights, wholePoints bool) ([]lotterydomain.Payout, string, error) {
	if len(entries) == 0 || !weights.ValidLegacyPercentages() {
		return nil, "0.00", lotterydomain.ErrLotteryInvalidRules
	}
	totalDec, err := money.Parse(totalAmount)
	if err != nil {
		return nil, "0.00", err
	}
	minDec, err := money.Parse(minPayout)
	if err != nil {
		return nil, "0.00", err
	}
	maxDec, err := money.Parse(maxPayout)
	if err != nil {
		return nil, "0.00", err
	}
	unitAmount := amountUnits
	formatAmount := unitsAmount
	if wholePoints {
		unitAmount = wholePointUnits
		formatAmount = wholePointsAmount
	}
	total, totalUnitsErr := unitAmount(totalDec)
	minValue, minUnitsErr := unitAmount(minDec)
	maxValue, maxUnitsErr := unitAmount(maxDec)
	if totalUnitsErr != nil || minUnitsErr != nil || maxUnitsErr != nil || total <= 0 {
		return nil, "0.00", lotterydomain.ErrLotteryInvalidRules
	}
	n := int64(len(entries))
	if n <= 0 || n > maxLotteryEntries || minValue <= 0 || maxValue < minValue {
		return nil, "0.00", lotterydomain.ErrLotteryInvalidRules
	}
	if n > math.MaxInt64/minValue {
		return nil, "0.00", lotterydomain.ErrLotteryInvalidRules
	}
	// The configured maximum is still an upper bound, but a draw also caps one
	// account at three times the nominal average so a single lucky result cannot
	// consume a disproportionate share of a small pool.
	effectiveMax := maxValue
	if averageCap := (total / n) * 3; averageCap < effectiveMax {
		effectiveMax = averageCap
	}
	if effectiveMax <= minValue {
		return nil, "0.00", lotterydomain.ErrLotteryInvalidRules
	}
	if n > math.MaxInt64/effectiveMax {
		return nil, "0.00", lotterydomain.ErrLotteryInvalidRules
	}
	budget := total
	maxBudget := n * effectiveMax
	if n > 1 {
		variableCapacity := n * (effectiveMax - minValue)
		headroom := variableCapacity / 10
		if headroom < 1 {
			headroom = 1
		}
		maxBudget = n*minValue + variableCapacity - headroom
	}
	if budget > maxBudget {
		budget = maxBudget
	}
	if budget < n*minValue {
		return nil, "0.00", lotterydomain.ErrLotteryInvalidRules
	}
	if err := randomShuffle(entries); err != nil {
		return nil, "0.00", err
	}
	tierCounts := tierCounts(len(entries), weights)
	tiers := make([]lotterydomain.Tier, 0, len(entries))
	for _, item := range []struct {
		tier  lotterydomain.Tier
		count int
	}{{lotterydomain.TierConsolation, tierCounts[0]}, {lotterydomain.TierNormal, tierCounts[1]}, {lotterydomain.TierLucky, tierCounts[2]}} {
		for i := 0; i < item.count; i++ {
			tiers = append(tiers, item.tier)
		}
	}
	if len(tiers) > len(entries) {
		tiers = tiers[:len(entries)]
	}
	for len(tiers) < len(entries) {
		tiers = append(tiers, lotterydomain.TierConsolation)
	}
	amounts := make([]int64, len(entries))
	weightsByEntry := make([]int64, len(entries))
	for i, tier := range tiers {
		amounts[i] = minValue
		weightsByEntry[i], err = tierRandomWeight(tier)
		if err != nil {
			return nil, "0.00", err
		}
	}
	bonus := budget - n*minValue
	if err := distributeBonus(amounts, weightsByEntry, bonus, effectiveMax-minValue); err != nil {
		return nil, "0.00", err
	}
	if bonus > 0 && len(amounts) > 1 {
		ensureVariation(amounts, weightsByEntry, minValue, effectiveMax)
	}
	payouts := make([]lotterydomain.Payout, len(entries))
	now := time.Now().UTC()
	for i, entry := range entries {
		payouts[i] = lotterydomain.Payout{
			LotteryID: entry.LotteryID, UserID: entry.UserID, Tier: tiers[i],
			Amount: formatAmount(amounts[i]), CreatedAt: now,
		}
	}
	return payouts, formatAmount(total - budget), nil
}

func isLotteryAllocationFallbackError(err error) bool {
	return errors.Is(err, lotterydomain.ErrLotteryInsufficientParticipants) ||
		errors.Is(err, lotterydomain.ErrLotteryInvalidRules)
}

// lotteryAllocationHasUnused treats anything other than a valid zero as a
// reason to rebuild the allocation. A malformed or negative remainder must
// never be passed to Billing as an apparently successful settlement.
func lotteryAllocationHasUnused(value string) bool {
	amount, err := money.Parse(value)
	return err != nil || !amount.IsZero()
}

// fallbackLotteryAllocation deliberately relaxes tier bounds only after the
// configured allocator has proved infeasible. Consuming the complete pool is
// more important than preserving an impossible min/max combination; awards
// remain as even as the ledger precision allows so no participant gets a
// disproportionate share.
func fallbackLotteryAllocation(entries []lotterydomain.Entry, lottery lotterydomain.Lottery) ([]lotterydomain.Payout, string, error) {
	if len(entries) == 0 {
		return nil, "0.00", lotterydomain.ErrLotteryInvalidRules
	}
	total, err := money.Parse(lottery.TotalAmount)
	if err != nil || !total.IsPositive() {
		return nil, "0.00", lotterydomain.ErrLotteryInvalidRules
	}

	wholePoints := total.IsInteger()
	var totalUnits int64
	formatAmount := unitsAmount
	if wholePoints {
		totalUnits, err = wholePointUnits(total)
		formatAmount = wholePointsAmount
	} else {
		totalUnits, err = amountUnits(total)
	}
	if err != nil || totalUnits <= 0 {
		return nil, "0.00", lotterydomain.ErrLotteryInvalidRules
	}

	amounts := make([]int64, len(entries))
	if err := distributeEvenlyUnbounded(amounts, totalUnits); err != nil {
		return nil, "0.00", err
	}

	// Keep the configured tier counts visible to administrators when they are
	// usable, while allowing the amount fallback to ignore impossible bounds.
	tiers := make([]lotterydomain.Tier, len(entries))
	for i := range tiers {
		tiers[i] = lotterydomain.TierConsolation
	}
	weights := lottery.TierWeights
	if weights.ValidFixedCounts() {
		luckyCount := weights.Lucky
		if luckyCount > len(entries) {
			luckyCount = len(entries)
		}
		normalCount := weights.Normal
		if normalCount > len(entries)-luckyCount {
			normalCount = len(entries) - luckyCount
		}
		for i := 0; i < luckyCount; i++ {
			tiers[i] = lotterydomain.TierLucky
		}
		for i := luckyCount; i < luckyCount+normalCount; i++ {
			tiers[i] = lotterydomain.TierNormal
		}
	} else if weights.ValidLegacyPercentages() {
		counts := tierCounts(len(entries), weights)
		index := 0
		for i := 0; i < counts[2] && index < len(tiers); i++ {
			tiers[index] = lotterydomain.TierLucky
			index++
		}
		for i := 0; i < counts[1] && index < len(tiers); i++ {
			tiers[index] = lotterydomain.TierNormal
			index++
		}
	}

	var allocated int64
	for _, amount := range amounts {
		if amount < 0 || allocated > math.MaxInt64-amount {
			return nil, "0.00", lotterydomain.ErrLotteryInvalidRules
		}
		allocated += amount
	}
	if allocated != totalUnits {
		return nil, "0.00", lotterydomain.ErrLotteryInvalidRules
	}

	now := time.Now().UTC()
	payouts := make([]lotterydomain.Payout, 0, len(entries))
	for i, entry := range entries {
		if amounts[i] <= 0 {
			// Billing rejects non-positive awards. This only occurs when an
			// impossible pool is smaller than the number of participants.
			continue
		}
		payouts = append(payouts, lotterydomain.Payout{
			LotteryID: entry.LotteryID,
			UserID:    entry.UserID,
			Tier:      tiers[i],
			Amount:    formatAmount(amounts[i]),
			CreatedAt: now,
		})
	}
	if len(payouts) == 0 {
		return nil, "0.00", lotterydomain.ErrLotteryInvalidRules
	}
	return payouts, "0.00", nil
}

// distributeEvenlyUnbounded is the last-resort split: it has no min/max cap,
// so every representable unit is assigned and the caller can settle a full
// pool even when the configured rules are contradictory.
func distributeEvenlyUnbounded(amounts []int64, total int64) error {
	if len(amounts) == 0 || total <= 0 {
		return lotterydomain.ErrLotteryInvalidRules
	}
	count := int64(len(amounts))
	share, remainder := total/count, total%count
	offset := int64(0)
	if remainder > 0 && count > 1 {
		value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(count))
		if err != nil {
			return err
		}
		offset = value.Int64()
	}
	for i := range amounts {
		amount := share
		if int64(i) < remainder {
			if amount == math.MaxInt64 {
				return lotterydomain.ErrLotteryInvalidRules
			}
			amount++
		}
		index := (offset + int64(i)) % count
		amounts[index] = amount
	}
	return nil
}

func distributeBonus(amounts, weights []int64, bonus, capacity int64) error {
	if bonus < 0 || capacity < 0 || len(amounts) == 0 || len(amounts) != len(weights) {
		return lotterydomain.ErrLotteryInvalidRules
	}
	base := amounts[0]
	remaining := bonus
	for remaining > 0 {
		type candidate struct {
			index     int
			remainder *big.Int
		}
		active := make([]candidate, 0, len(amounts))
		var weightSum int64
		for i := range amounts {
			if amounts[i]-base < capacity {
				active = append(active, candidate{index: i})
				if weights[i] > math.MaxInt64-weightSum {
					return lotterydomain.ErrLotteryInvalidRules
				}
				weightSum += weights[i]
			}
		}
		if len(active) == 0 || weightSum <= 0 {
			return lotterydomain.ErrLotteryInvalidRules
		}
		// Every share in a round is calculated from the same snapshot. Updating
		// remaining while iterating would give earlier entries a systematic edge.
		snapshot := remaining
		progress := int64(0)
		weightTotal := big.NewInt(weightSum)
		for i := range active {
			index := active[i].index
			product := new(big.Int).Mul(big.NewInt(snapshot), big.NewInt(weights[index]))
			shareBig, remainder := new(big.Int), new(big.Int)
			shareBig.QuoRem(product, weightTotal, remainder)
			share := shareBig.Int64()
			room := capacity - (amounts[index] - base)
			if share > room {
				share = room
			}
			if share > 0 {
				amounts[index] += share
				progress += share
			}
			if amounts[index]-base < capacity {
				active[i].remainder = remainder
			}
		}
		remaining -= progress
		if remaining == 0 {
			break
		}
		// Largest remainders get the residual units. Ties follow the already
		// cryptographically shuffled entry order, so input order cannot bias pay.
		candidates := make([]candidate, 0, len(active))
		for _, item := range active {
			if item.remainder != nil && amounts[item.index]-base < capacity {
				candidates = append(candidates, item)
			}
		}
		if len(candidates) == 0 {
			return lotterydomain.ErrLotteryInvalidRules
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			return candidates[i].remainder.Cmp(candidates[j].remainder) > 0
		})
		for _, item := range candidates {
			if remaining == 0 {
				break
			}
			amounts[item.index]++
			remaining--
		}
	}
	return nil
}

// distributeEvenly gives each consolation entry the same whole-point share;
// a few entries receive one residual point when the share is not integral.
func distributeEvenly(amounts []int64, bonus, capacity int64) error {
	if bonus < 0 || capacity < 0 || len(amounts) == 0 {
		return lotterydomain.ErrLotteryInvalidRules
	}
	if capacity == 0 {
		if bonus == 0 {
			return nil
		}
		return lotterydomain.ErrLotteryInvalidRules
	}
	count := int64(len(amounts))
	if count > math.MaxInt64/capacity || bonus > count*capacity {
		return lotterydomain.ErrLotteryInvalidRules
	}
	share, remainder := bonus/count, bonus%count
	if share > capacity || (share == capacity && remainder > 0) {
		return lotterydomain.ErrLotteryInvalidRules
	}
	offset := int64(0)
	if remainder > 0 && count > 1 {
		value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(count))
		if err != nil {
			return err
		}
		offset = value.Int64()
	}
	for i := range amounts {
		add := share
		if int64(i) < remainder {
			add++
		}
		index := (offset + int64(i)) % count
		amounts[index] += add
	}
	return nil
}

func ensureVariation(amounts, weights []int64, minValue, maxValue int64) {
	allSame := true
	for _, amount := range amounts[1:] {
		if amount != amounts[0] {
			allSame = false
			break
		}
	}
	if !allSame || amounts[0] >= maxValue || amounts[0] <= minValue {
		return
	}
	donor, receiver := 0, 0
	for i := 1; i < len(amounts); i++ {
		if weights[i] < weights[donor] {
			donor = i
		}
		if weights[i] > weights[receiver] {
			receiver = i
		}
	}
	if donor == receiver {
		if value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(len(amounts)))); err == nil {
			donor = int(value.Int64())
		}
		receiver = (donor + 1) % len(amounts)
	}
	delta := (maxValue - minValue) / 10
	if delta < 1 {
		delta = 1
	}
	if room := amounts[donor] - minValue; delta > room {
		delta = room
	}
	if room := maxValue - amounts[receiver]; delta > room {
		delta = room
	}
	if delta > 0 {
		amounts[donor] -= delta
		amounts[receiver] += delta
	}
}

func tierCounts(n int, weights lotterydomain.TierWeights) [3]int {
	if n <= 0 || !weights.ValidLegacyPercentages() {
		return [3]int{}
	}
	counts := [3]int{n * weights.Consolation / 100, n * weights.Normal / 100, n * weights.Lucky / 100}
	for left := n - counts[0] - counts[1] - counts[2]; left > 0; left-- {
		if weights.Lucky >= weights.Normal && weights.Lucky >= weights.Consolation && weights.Lucky > 0 {
			counts[2]++
		} else if weights.Normal >= weights.Consolation && weights.Normal > 0 {
			counts[1]++
		} else {
			counts[0]++
		}
	}
	if counts[0] == 0 {
		counts[0] = 1
		if counts[1] > 0 {
			counts[1]--
		} else if counts[2] > 0 {
			counts[2]--
		}
	}
	return counts
}

func tierRandomWeight(tier lotterydomain.Tier) (int64, error) {
	minWeight, maxWeight := int64(1), int64(3)
	switch tier {
	case lotterydomain.TierNormal:
		minWeight, maxWeight = 3, 8
	case lotterydomain.TierLucky:
		minWeight, maxWeight = 8, 16
	}
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(maxWeight-minWeight+1))
	if err != nil {
		return 0, err
	}
	return minWeight + value.Int64(), nil
}

func entryUserIDs(entries []lotterydomain.Entry) []uint {
	ids := make([]uint, len(entries))
	for i, entry := range entries {
		ids[i] = entry.UserID
	}
	return ids
}

// rankEntriesByHistory puts the highest history fairness scores first. Equal scores are
// shuffled as a group so a boundary between tiers never favors insertion order.
func rankEntriesByHistory(entries []lotterydomain.Entry, stats map[uint]WinnerStats) error {
	if len(entries) < 2 {
		return nil
	}
	type rankedEntry struct {
		entry lotterydomain.Entry
		score int64
	}
	ranked := make([]rankedEntry, len(entries))
	for i, entry := range entries {
		winnerStats := stats[entry.UserID]
		ranked[i] = rankedEntry{entry: entry, score: winnerHistoryScore(winnerStats)}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	for start := 0; start < len(ranked); {
		end := start + 1
		for end < len(ranked) && ranked[end].score == ranked[start].score {
			end++
		}
		if end-start == 1 {
			entries[start] = ranked[start].entry
			start = end
			continue
		}
		group := make([]lotterydomain.Entry, end-start)
		for i := start; i < end; i++ {
			group[i-start] = ranked[i].entry
		}
		if err := randomShuffle(group); err != nil {
			return err
		}
		for i := start; i < end; i++ {
			entries[i] = group[i-start]
		}
		start = end
	}
	return nil
}

// score = max(0, 1000 - lucky awards*500 - normal awards*100 + consolation awards*50).
func winnerHistoryScore(stats WinnerStats) int64 {
	lucky := max(stats.LuckyCount, int64(0))
	normal := max(stats.NormalCount, int64(0))
	consolation := max(stats.ConsolationCount, int64(0))
	// Keep the intermediate arithmetic exact; the final score is the only
	// value that needs to be bounded to the ranking type.
	score := big.NewInt(winnerInitialScore)
	score.Sub(score, new(big.Int).Mul(big.NewInt(lucky), big.NewInt(winnerLuckyPenalty)))
	score.Sub(score, new(big.Int).Mul(big.NewInt(normal), big.NewInt(winnerNormalPenalty)))
	score.Add(score, new(big.Int).Mul(big.NewInt(consolation), big.NewInt(winnerConsolationBonus)))
	if score.Sign() <= 0 {
		return 0
	}
	if !score.IsInt64() {
		return math.MaxInt64
	}
	return score.Int64()
}

func randomShuffle(entries []lotterydomain.Entry) error {
	for i := len(entries) - 1; i > 0; i-- {
		index, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return err
		}
		j := int(index.Int64())
		entries[i], entries[j] = entries[j], entries[i]
	}
	return nil
}

func amountUnits(value decimal.Decimal) (int64, error) {
	scaled := value.Shift(money.Scale)
	if !scaled.IsInteger() {
		return 0, lotterydomain.ErrLotteryInvalidRules
	}
	units := scaled.BigInt()
	if !units.IsInt64() {
		return 0, lotterydomain.ErrLotteryInvalidRules
	}
	return units.Int64(), nil
}

func unitsAmount(value int64) string {
	return money.Format(decimal.New(value, -money.Scale))
}

func wholePointUnits(value decimal.Decimal) (int64, error) {
	if !value.IsInteger() {
		return 0, lotterydomain.ErrLotteryInvalidRules
	}
	units := value.BigInt()
	if !units.IsInt64() {
		return 0, lotterydomain.ErrLotteryInvalidRules
	}
	return units.Int64(), nil
}

func wholePointsAmount(value int64) string {
	return money.Format(decimal.NewFromInt(value))
}

func copyInt(value *int) *int {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func copyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copyValue := value.UTC()
	return &copyValue
}

func truncateTitle(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "ReMail 抽奖"
	}
	if len([]rune(value)) > 120 {
		return string([]rune(value)[:120])
	}
	return value
}

func lotteryRequestFingerprint(req CreateRequest, total, minPayout, maxPayout string) string {
	hash := sha256.New()
	parts := []string{
		"lottery.create.v1", truncateTitle(req.Title), total, minPayout, maxPayout,
		fmt.Sprintf("%d", req.TierWeights.Consolation),
		fmt.Sprintf("%d", req.TierWeights.Normal),
		fmt.Sprintf("%d", req.TierWeights.Lucky),
		fmt.Sprintf("%d", req.MinAccountAgeDays),
	}
	if req.DrawAt != nil {
		parts = append(parts, req.DrawAt.UTC().Format(time.RFC3339Nano))
	} else {
		parts = append(parts, "")
	}
	if req.ParticipantTarget != nil {
		parts = append(parts, fmt.Sprintf("%d", *req.ParticipantTarget))
	} else {
		parts = append(parts, "")
	}
	lotteryType := normalizeLotteryType(req.LotteryType)
	if lotteryType == lotterydomain.LotteryTypeGrowing {
		increment, _, incrementErr := normalizedPoolIncrement(req)
		if incrementErr == nil {
			parts = append(parts, lotteryType.String(), increment)
		}
	}
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func lotteryFingerprintFromDomain(lottery lotterydomain.Lottery) string {
	startingAmount := lottery.StartingAmount
	if strings.TrimSpace(startingAmount) == "" {
		startingAmount = lottery.TotalAmount
	}
	return lotteryRequestFingerprint(CreateRequest{
		Title: lottery.Title, LotteryType: normalizeLotteryType(lottery.LotteryType),
		TotalAmount: startingAmount, PoolIncrementAmount: lottery.PoolIncrementAmount, MinPayout: lottery.MinPayout,
		MaxPayout: lottery.MaxPayout, TierWeights: lottery.TierWeights,
		MinAccountAgeDays: lottery.MinAccountAgeDays, DrawAt: lottery.DrawAt,
		ParticipantTarget: copyInt(lottery.ParticipantTarget),
	}, startingAmount, lottery.MinPayout, lottery.MaxPayout)
}

func (s *Service) Public(ctx context.Context, token string, userID uint) (*lotterydomain.Lottery, *lotterydomain.Entry, *lotterydomain.Payout, error) {
	lottery, err := s.repo.GetByToken(ctx, strings.TrimSpace(token))
	if err != nil {
		return nil, nil, nil, err
	}
	var entry *lotterydomain.Entry
	var payout *lotterydomain.Payout
	if userID != 0 {
		// The repository exposes the current user's state through these small
		// helpers so the public endpoint never returns another user's payout.
		entry, payout, err = s.repoCurrentUser(ctx, lottery.ID, userID)
		if err != nil {
			return nil, nil, nil, err
		}
		if entry == nil {
			payout = nil
		}
		// Payout rows are written before Billing and lottery completion. Keep the
		// amount private until the award is final, rather than exposing a
		// provisional allocation while the activity is settling.
		if lottery.Status != lotterydomain.StatusCompleted {
			payout = nil
		}
	}
	return lottery, entry, payout, nil
}

type currentUserRepository interface {
	FindEntry(ctx context.Context, lotteryID, userID uint) (*lotterydomain.Entry, error)
	FindPayout(ctx context.Context, lotteryID, userID uint) (*lotterydomain.Payout, error)
}

func (s *Service) repoCurrentUser(ctx context.Context, lotteryID, userID uint) (*lotterydomain.Entry, *lotterydomain.Payout, error) {
	repo, ok := s.repo.(currentUserRepository)
	if !ok {
		return nil, nil, nil
	}
	entry, err := repo.FindEntry(ctx, lotteryID, userID)
	if err != nil && !errors.Is(err, lotterydomain.ErrLotteryNotFound) {
		return nil, nil, err
	}
	payout, err := repo.FindPayout(ctx, lotteryID, userID)
	if err != nil && !errors.Is(err, lotterydomain.ErrLotteryNotFound) {
		return nil, nil, err
	}
	return entry, payout, nil
}

func (s *Service) DispatchDue(ctx context.Context) error {
	now := s.now().UTC()
	items, err := s.listDue(ctx, now, 100)
	if err != nil {
		return err
	}
	for _, lottery := range items {
		if s.queue != nil {
			_ = s.queue.EnqueueDraw(ctx, lottery.ID, nil)
		}
	}
	return nil
}

type dueLotteryRepository interface {
	ListDue(ctx context.Context, now time.Time, limit int) ([]*lotterydomain.Lottery, error)
}

type settlingLotteryRepository interface {
	ListSettling(ctx context.Context, limit int) ([]*lotterydomain.Lottery, error)
}

func (s *Service) listDue(ctx context.Context, now time.Time, limit int) ([]*lotterydomain.Lottery, error) {
	if repo, ok := s.repo.(dueLotteryRepository); ok {
		return repo.ListDue(ctx, now, limit)
	}
	items, _, err := s.repo.List(ctx, ListFilter{Status: string(lotterydomain.StatusOpen), Limit: limit})
	if err != nil {
		return nil, err
	}
	result := items[:0]
	for _, lottery := range items {
		due := lottery.ParticipantTarget != nil && lottery.ParticipantCount >= *lottery.ParticipantTarget
		if lottery.DrawAt != nil && !lottery.DrawAt.After(now) {
			due = true
		}
		if due {
			result = append(result, lottery)
		}
	}
	return result, nil
}

// ReconcileSettling re-enqueues activities whose draw worker timed out or
// disappeared after claiming settlement. Billing's idempotency makes retries
// safe, while this scan prevents a permanent settling state.
func (s *Service) ReconcileSettling(ctx context.Context, limit int) error {
	if s.queue == nil {
		return nil
	}
	if limit <= 0 {
		limit = 100
	}
	repo, ok := s.repo.(settlingLotteryRepository)
	if !ok {
		return nil
	}
	// Allow the active task's unique key and transaction to expire before the
	// scanner creates a recovery task; immediate re-enqueueing can otherwise
	// produce concurrent settlement attempts while a healthy draw is running.
	cutoff := s.now().UTC().Add(-15 * time.Minute)
	items, err := repo.ListSettling(ctx, limit)
	if err != nil {
		return err
	}
	for _, lottery := range items {
		if lottery.UpdatedAt.After(cutoff) {
			continue
		}
		if err := s.queue.EnqueueDraw(ctx, lottery.ID, nil); err != nil {
			return err
		}
	}
	return nil
}

func remainingUnused(totalAmount string, payouts []lotterydomain.Payout) (string, error) {
	total, err := money.Parse(totalAmount)
	if err != nil {
		return "", err
	}
	paid := decimal.Zero
	for _, payout := range payouts {
		amount, parseErr := money.Parse(payout.Amount)
		if parseErr != nil {
			return "", parseErr
		}
		paid = paid.Add(amount)
	}
	unused := total.Sub(paid)
	if unused.IsNegative() {
		return "", lotterydomain.ErrLotteryInvalidRules
	}
	return money.Format(unused), nil
}

func validateSettlementAwards(payouts []lotterydomain.Payout, awards []billingapp.LotteryAwardResult) error {
	if len(payouts) != len(awards) {
		return fmt.Errorf("billing returned %d awards for %d payouts: %w", len(awards), len(payouts), lotterydomain.ErrLotterySettlement)
	}
	expected := make(map[uint]string, len(payouts))
	for _, payout := range payouts {
		if _, exists := expected[payout.UserID]; exists {
			return fmt.Errorf("duplicate payout user %d: %w", payout.UserID, lotterydomain.ErrLotterySettlement)
		}
		expected[payout.UserID] = payout.Amount
	}
	for _, award := range awards {
		expectedAmount, ok := expected[award.UserID]
		if !ok || strings.TrimSpace(award.Transaction.TransactionNo) == "" {
			return fmt.Errorf("billing returned an unknown or transaction-less award for user %d: %w", award.UserID, lotterydomain.ErrLotterySettlement)
		}
		expectedValue, expectedErr := money.Parse(expectedAmount)
		actualValue, actualErr := money.Parse(award.Amount)
		if expectedErr != nil || actualErr != nil || !expectedValue.Equal(actualValue) {
			return fmt.Errorf("billing award amount mismatch for user %d: %w", award.UserID, lotterydomain.ErrLotterySettlement)
		}
		delete(expected, award.UserID)
	}
	if len(expected) != 0 {
		return fmt.Errorf("billing omitted %d payout awards: %w", len(expected), lotterydomain.ErrLotterySettlement)
	}
	return nil
}
