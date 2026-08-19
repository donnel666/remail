package app

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
	algorithmVersion = "bounded-tier-v1"
	// ponytail: keep the first release bounded until settlement is resumable
	// in batches; raise this only with a chunked Billing contract.
	maxLotteryEntries = 1000
)

func (s *Service) Create(ctx context.Context, req CreateRequest) (*CreateResult, error) {
	now := s.now().UTC()
	key := strings.TrimSpace(req.IdempotencyKey)
	if req.CreatedByUserID == 0 || key == "" || len(key) > 128 {
		return nil, lotterydomain.ErrLotteryInvalidRules
	}
	total, minPayout, maxPayout, maxParticipants, err := validateRules(req)
	if err != nil {
		return nil, err
	}
	if req.DrawAt == nil || !req.DrawAt.UTC().After(now) {
		return nil, lotterydomain.ErrLotteryInvalidRules
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
	target := copyInt(req.ParticipantTarget)
	lottery := &lotterydomain.Lottery{
		PublicToken:        platform.NewUUIDV4String(),
		CreatedByUserID:    req.CreatedByUserID,
		Title:              truncateTitle(req.Title),
		TotalAmount:        total,
		MinPayout:          minPayout,
		MaxPayout:          maxPayout,
		TierWeights:        req.TierWeights,
		MinAccountAgeDays:  req.MinAccountAgeDays,
		DrawAt:             copyTime(req.DrawAt),
		ParticipantTarget:  target,
		MaxParticipants:    maxParticipants,
		Status:             lotterydomain.StatusOpen,
		AlgorithmVersion:   algorithmVersion,
		UnusedAmount:       "0.00",
		IdempotencyKey:     key,
		RequestFingerprint: fingerprint,
		CreatedAt:          now,
		UpdatedAt:          now,
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
	if s.queue != nil {
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
		return nil, lotterydomain.ErrLotteryClosed
	}
	user, err := s.users.FindLotteryUser(ctx, userID)
	if err != nil || user == nil || strings.ToLower(strings.TrimSpace(user.Status)) != "active" {
		return nil, lotterydomain.ErrLotteryNotEligible
	}
	if lottery.MinAccountAgeDays > 0 && user.CreatedAt.After(s.now().UTC().AddDate(0, 0, -lottery.MinAccountAgeDays)) {
		return nil, lotterydomain.ErrLotteryNotEligible
	}
	result, err := s.repo.AddEntry(ctx, lottery.ID, userID, user.CreatedAt, s.now().UTC())
	if err != nil {
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
	entries, err := s.repo.ListAllEntries(ctx, lotteryID)
	if err != nil {
		return err
	}
	existing, err := s.repo.GetPayouts(ctx, lotteryID)
	if err != nil {
		return err
	}
	var payouts []lotterydomain.Payout
	var unusedAmount string
	if len(existing) > 0 {
		payouts = existing
		unusedAmount, err = remainingUnused(lottery.TotalAmount, payouts)
		if err != nil {
			return err
		}
	} else if len(entries) > 0 {
		payouts, unusedAmount, err = Allocate(entries, lottery.TotalAmount, lottery.MinPayout, lottery.MaxPayout, lottery.TierWeights)
		if err != nil {
			return err
		}
		if err := s.repo.SavePayouts(ctx, lotteryID, payouts); err != nil {
			return err
		}
		// Another worker may have won the insert race. Always settle using the
		// persisted payout rows, never a discarded in-memory allocation.
		payouts, err = s.repo.GetPayouts(ctx, lotteryID)
		if err != nil {
			return err
		}
	} else {
		unusedAmount = lottery.TotalAmount
	}
	if len(payouts) == 0 {
		// No participant wallet is credited. The unused system budget is only
		// recorded for audit.
		if err := s.repo.RecordBillingTransactions(ctx, lotteryID, nil, unusedAmount); err != nil {
			return err
		}
		if err := s.repo.Complete(ctx, lotteryID, lotterydomain.StatusCancelled, unusedAmount, s.now().UTC()); err != nil {
			return err
		}
		return nil
	}
	awards := make([]billingapp.LotteryAward, len(payouts))
	for i := range payouts {
		awards[i] = billingapp.LotteryAward{UserID: payouts[i].UserID, Amount: payouts[i].Amount}
	}
	if s.billing == nil {
		return lotterydomain.ErrLotterySettlement
	}
	settlement, err := s.billing.SettleLotteryPool(ctx, billingapp.LotterySettlementRequest{
		LotteryID: lotteryID, TotalAmount: lottery.TotalAmount,
		Awards: awards, UnusedAmount: unusedAmount,
	})
	if err != nil {
		return fmt.Errorf("settle lottery %d: %w", lotteryID, err)
	}
	transactions := make(map[uint]string, len(settlement.Awards))
	for _, award := range settlement.Awards {
		transactions[award.UserID] = award.Transaction.TransactionNo
	}
	if err := s.repo.RecordBillingTransactions(ctx, lotteryID, transactions, unusedAmount); err != nil {
		return err
	}
	if err := s.repo.Complete(ctx, lotteryID, lotterydomain.StatusCompleted, unusedAmount, s.now().UTC()); err != nil {
		return err
	}
	s.sendWinnerEmails(ctx, *lottery, payouts)
	return nil
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
		_ = s.delivery.Send(ctx, mailapp.LotteryWinnerMessage(user.Email, lottery.ID, lottery.Title, payout.Amount, payout.Tier.String()))
	}
}

func validateRules(req CreateRequest) (total, minPayout, maxPayout string, maxParticipants int, err error) {
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
	if req.MinAccountAgeDays < 0 || !req.TierWeights.Valid() || req.DrawAt == nil || req.ParticipantTarget == nil {
		err = lotterydomain.ErrLotteryInvalidRules
		return
	}
	if req.ParticipantTarget != nil && (*req.ParticipantTarget <= 0 || *req.ParticipantTarget > maxLotteryEntries) {
		err = lotterydomain.ErrLotteryInvalidRules
		return
	}
	totalUnits, totalErr := amountUnits(totalDec)
	minUnits, minErr := amountUnits(minDec)
	maxUnits, maxErr := amountUnits(maxDec)
	if totalErr != nil || minErr != nil || maxErr != nil || minUnits <= 0 || maxUnits <= minUnits {
		err = lotterydomain.ErrLotteryInvalidRules
		return
	}
	target := int64(*req.ParticipantTarget)
	if target > math.MaxInt64/minUnits || target*minUnits >= totalUnits {
		// The target must leave at least one unit of variable budget; otherwise
		// a full target draw is necessarily flat at the minimum payout.
		err = lotterydomain.ErrLotteryInvalidRules
		return
	}
	maxParticipants = int(target)
	return money.Format(totalDec), money.Format(minDec), money.Format(maxDec), maxParticipants, nil
}

func Allocate(entries []lotterydomain.Entry, totalAmount, minPayout, maxPayout string, weights lotterydomain.TierWeights) ([]lotterydomain.Payout, string, error) {
	if len(entries) == 0 || !weights.Valid() {
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
	total, _ := amountUnits(totalDec)
	minValue, _ := amountUnits(minDec)
	maxValue, _ := amountUnits(maxDec)
	n := int64(len(entries))
	if n <= 0 || n > maxLotteryEntries || minValue <= 0 || maxValue < minValue {
		return nil, "0.00", lotterydomain.ErrLotteryInvalidRules
	}
	if n > math.MaxInt64/maxValue || n > math.MaxInt64/minValue {
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
			Amount: unitsAmount(amounts[i]), CreatedAt: now,
		}
	}
	return payouts, unitsAmount(total - budget), nil
}

func distributeBonus(amounts, weights []int64, bonus, capacity int64) error {
	if bonus < 0 || capacity < 0 || len(amounts) != len(weights) {
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
	units := scaled.IntPart()
	if !scaled.Equal(decimal.NewFromInt(units)) {
		return 0, lotterydomain.ErrLotteryInvalidRules
	}
	return units, nil
}

func unitsAmount(value int64) string {
	return money.Format(decimal.New(value, -money.Scale))
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
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func lotteryFingerprintFromDomain(lottery lotterydomain.Lottery) string {
	return lotteryRequestFingerprint(CreateRequest{
		Title: lottery.Title, TotalAmount: lottery.TotalAmount, MinPayout: lottery.MinPayout,
		MaxPayout: lottery.MaxPayout, TierWeights: lottery.TierWeights,
		MinAccountAgeDays: lottery.MinAccountAgeDays, DrawAt: lottery.DrawAt,
		ParticipantTarget: copyInt(lottery.ParticipantTarget),
	}, lottery.TotalAmount, lottery.MinPayout, lottery.MaxPayout)
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
