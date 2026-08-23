package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	lotteryapp "github.com/donnel666/remail/internal/lottery/app"
	lotterydomain "github.com/donnel666/remail/internal/lottery/domain"
	"github.com/donnel666/remail/internal/money"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type LotteryModel struct {
	ID                 uint       `gorm:"primaryKey;autoIncrement"`
	PublicToken        string     `gorm:"type:varchar(64);not null;uniqueIndex;column:public_token"`
	CreatedByUserID    uint       `gorm:"not null;column:created_by_user_id"`
	FundingUserID      uint       `gorm:"not null;column:funding_user_id"`
	Title              string     `gorm:"type:varchar(120);not null"`
	TotalAmount        string     `gorm:"type:decimal(18,6);not null;column:total_amount"`
	MinPayout          string     `gorm:"type:decimal(18,6);not null;column:min_payout"`
	MaxPayout          string     `gorm:"type:decimal(18,6);not null;column:max_payout"`
	TierWeightsJSON    string     `gorm:"type:json;not null;column:tier_weights"`
	MinAccountAgeDays  int        `gorm:"not null;default:0;column:min_account_age_days"`
	DrawAt             *time.Time `gorm:"column:draw_at"`
	ParticipantTarget  *int       `gorm:"column:participant_target"`
	ParticipantCount   int        `gorm:"not null;default:0;column:participant_count"`
	MaxParticipants    int        `gorm:"not null;column:max_participants"`
	Status             string     `gorm:"type:varchar(16);not null"`
	TriggeredBy        string     `gorm:"type:varchar(16);not null;default:'';column:triggered_by"`
	TargetReachedAt    *time.Time `gorm:"column:target_reached_at"`
	FundTransactionNo  string     `gorm:"type:varchar(64);not null;default:'';column:fund_transaction_no"`
	AlgorithmVersion   string     `gorm:"type:varchar(32);not null;column:algorithm_version"`
	UnusedAmount       string     `gorm:"type:decimal(18,6);not null;default:0;column:unused_amount"`
	IdempotencyKey     string     `gorm:"type:varchar(128);not null;column:idempotency_key"`
	RequestFingerprint string     `gorm:"type:char(64);not null;default:'';column:request_fingerprint"`
	FundingError       string     `gorm:"type:varchar(255);not null;default:'';column:funding_error"`
	SettledAt          *time.Time `gorm:"column:settled_at"`
	CreatedAt          time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt          time.Time  `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (LotteryModel) TableName() string { return "lotteries" }

type EntryModel struct {
	ID           uint      `gorm:"primaryKey;autoIncrement"`
	LotteryID    uint      `gorm:"not null;column:lottery_id"`
	UserID       uint      `gorm:"not null;column:user_id"`
	RegisteredAt time.Time `gorm:"not null;column:registered_at"`
	CreatedAt    time.Time `gorm:"not null;autoCreateTime;column:created_at"`
}

func (EntryModel) TableName() string { return "lottery_entries" }

type PayoutModel struct {
	ID                   uint       `gorm:"primaryKey;autoIncrement"`
	LotteryID            uint       `gorm:"not null;column:lottery_id"`
	UserID               uint       `gorm:"not null;column:user_id"`
	Tier                 string     `gorm:"type:varchar(16);not null"`
	Amount               string     `gorm:"type:decimal(18,6);not null"`
	BillingTransactionNo string     `gorm:"type:varchar(64);not null;default:'';column:billing_transaction_no"`
	MailQueuedAt         *time.Time `gorm:"column:mail_queued_at"`
	CreatedAt            time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
}

func (PayoutModel) TableName() string { return "lottery_payouts" }

type Repo struct{ db *gorm.DB }

func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

func (r *Repo) dbFor(ctx context.Context) *gorm.DB {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return tx.WithContext(ctx)
	}
	return r.db.WithContext(ctx)
}

func (r *Repo) withTx(ctx context.Context, fn func(context.Context, *gorm.DB) error) error {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return fn(ctx, tx.WithContext(ctx))
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(platform.WithGormTx(ctx, tx), tx)
	})
}

// WithDrawTransaction keeps selection, payout rows, Billing's ledger writes,
// and the terminal lottery state in one database transaction. Billing's repo
// reuses the GORM transaction carried by the context.
func (r *Repo) WithDrawTransaction(ctx context.Context, lotteryID uint, fn func(context.Context, *lotterydomain.Lottery) error) error {
	if lotteryID == 0 || fn == nil {
		return lotterydomain.ErrLotteryNotFound
	}
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return r.withLockedDraw(ctx, tx.WithContext(ctx), lotteryID, fn)
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := platform.WithGormTx(ctx, tx)
		return r.withLockedDraw(txCtx, tx, lotteryID, fn)
	})
}

func (r *Repo) withLockedDraw(ctx context.Context, tx *gorm.DB, lotteryID uint, fn func(context.Context, *lotterydomain.Lottery) error) error {
	var model LotteryModel
	if err := txCtxDB(ctx, tx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&model, "id = ?", lotteryID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return lotterydomain.ErrLotteryNotFound
		}
		return fmt.Errorf("lock lottery for draw: %w", err)
	}
	lottery, err := lotteryFromModel(model)
	if err != nil {
		return err
	}
	return fn(ctx, lottery)
}

// LockWinnerUsers serializes history snapshots for overlapping lotteries.
// IDs are sorted before locking so concurrent draws acquire rows in one order.
func (r *Repo) LockWinnerUsers(ctx context.Context, userIDs []uint) error {
	if !r.db.Migrator().HasTable("users") {
		return nil
	}
	ids := append([]uint(nil), userIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	unique := ids[:0]
	for _, id := range ids {
		if id == 0 || (len(unique) > 0 && unique[len(unique)-1] == id) {
			continue
		}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return nil
	}
	var rows []struct {
		ID uint `gorm:"column:id"`
	}
	if err := r.dbFor(ctx).Table("users").Select("id").Where("id IN ?", unique).
		Order("id ASC").Clauses(clause.Locking{Strength: "UPDATE"}).Find(&rows).Error; err != nil {
		return fmt.Errorf("lock lottery winner users: %w", err)
	}
	return nil
}

func (r *Repo) Create(ctx context.Context, lottery *lotterydomain.Lottery) error {
	weights, err := json.Marshal(lottery.TierWeights)
	if err != nil {
		return err
	}
	model := LotteryModel{
		ID: lottery.ID, PublicToken: lottery.PublicToken, CreatedByUserID: lottery.CreatedByUserID,
		FundingUserID: lottery.CreatedByUserID, Title: lottery.Title, TotalAmount: lottery.TotalAmount,
		MinPayout: lottery.MinPayout, MaxPayout: lottery.MaxPayout, TierWeightsJSON: string(weights),
		MinAccountAgeDays: lottery.MinAccountAgeDays, DrawAt: lottery.DrawAt, ParticipantTarget: lottery.ParticipantTarget,
		ParticipantCount: lottery.ParticipantCount, MaxParticipants: lottery.MaxParticipants,
		Status: string(lottery.Status), TriggeredBy: string(lottery.TriggeredBy), TargetReachedAt: lottery.TargetReachedAt,
		AlgorithmVersion: lottery.AlgorithmVersion, UnusedAmount: lottery.UnusedAmount,
		IdempotencyKey: lottery.IdempotencyKey, RequestFingerprint: lottery.RequestFingerprint,
		CreatedAt: lottery.CreatedAt, UpdatedAt: lottery.UpdatedAt,
	}
	if err := r.dbFor(ctx).Create(&model).Error; err != nil {
		return fmt.Errorf("create lottery: %w", err)
	}
	lottery.ID = model.ID
	return nil
}

func (r *Repo) FindByIdempotency(ctx context.Context, userID uint, key string) (*lotterydomain.Lottery, error) {
	var model LotteryModel
	err := r.dbFor(ctx).Where("created_by_user_id = ? AND idempotency_key = ?", userID, strings.TrimSpace(key)).First(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, lotterydomain.ErrLotteryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find lottery idempotency: %w", err)
	}
	return lotteryFromModel(model)
}

func (r *Repo) GetByID(ctx context.Context, id uint) (*lotterydomain.Lottery, error) {
	var model LotteryModel
	if err := r.dbFor(ctx).First(&model, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, lotterydomain.ErrLotteryNotFound
		}
		return nil, fmt.Errorf("get lottery: %w", err)
	}
	return lotteryFromModel(model)
}

func (r *Repo) GetByToken(ctx context.Context, token string) (*lotterydomain.Lottery, error) {
	var model LotteryModel
	if err := r.dbFor(ctx).Where("public_token = ?", strings.TrimSpace(token)).First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, lotterydomain.ErrLotteryNotFound
		}
		return nil, fmt.Errorf("get lottery by token: %w", err)
	}
	return lotteryFromModel(model)
}

func (r *Repo) List(ctx context.Context, filter lotteryapp.ListFilter) ([]*lotterydomain.Lottery, int64, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	query := r.dbFor(ctx).Model(&LotteryModel{})
	if status := strings.TrimSpace(filter.Status); status != "" {
		query = query.Where("status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count lotteries: %w", err)
	}
	var models []LotteryModel
	if err := query.Order("created_at DESC, id DESC").Offset(offset).Limit(limit).Find(&models).Error; err != nil {
		return nil, 0, fmt.Errorf("list lotteries: %w", err)
	}
	items, err := lotterySlice(models)
	return items, total, err
}

func (r *Repo) ListDue(ctx context.Context, now time.Time, limit int) ([]*lotterydomain.Lottery, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var models []LotteryModel
	query := r.dbFor(ctx).Where("status = ?", lotterydomain.StatusOpen).
		Where("(draw_at IS NOT NULL AND draw_at <= ?) OR (participant_target IS NOT NULL AND participant_count >= participant_target)", now.UTC()).
		Order("updated_at ASC, id ASC").Limit(limit)
	if err := query.Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list due lotteries: %w", err)
	}
	return lotterySlice(models)
}

func (r *Repo) ListSettling(ctx context.Context, limit int) ([]*lotterydomain.Lottery, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var models []LotteryModel
	if err := r.dbFor(ctx).Where("status = ?", lotterydomain.StatusSettling).
		Order("updated_at ASC, id ASC").Limit(limit).Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list settling lotteries: %w", err)
	}
	return lotterySlice(models)
}

func (r *Repo) ListEntries(ctx context.Context, lotteryID uint, offset, limit int) ([]lotterydomain.Entry, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var total int64
	query := r.dbFor(ctx).Model(&EntryModel{}).Where("lottery_id = ?", lotteryID)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count lottery entries: %w", err)
	}
	var models []EntryModel
	if err := query.Order("id ASC").Offset(maxInt(offset, 0)).Limit(limit).Find(&models).Error; err != nil {
		return nil, 0, fmt.Errorf("list lottery entries: %w", err)
	}
	items := make([]lotterydomain.Entry, len(models))
	for i := range models {
		items[i] = entryFromModel(models[i])
	}
	return items, total, nil
}

func (r *Repo) ListPayouts(ctx context.Context, lotteryID uint, offset, limit int) ([]lotterydomain.Payout, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var total int64
	query := r.dbFor(ctx).Model(&PayoutModel{}).Where("lottery_id = ?", lotteryID)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count lottery payouts: %w", err)
	}
	var models []PayoutModel
	if err := query.Order("amount DESC").Order("id ASC").Offset(maxInt(offset, 0)).Limit(limit).Find(&models).Error; err != nil {
		return nil, 0, fmt.Errorf("list lottery payouts: %w", err)
	}
	items := make([]lotterydomain.Payout, len(models))
	for i := range models {
		items[i] = payoutFromModel(models[i])
	}
	return items, total, nil
}

func (r *Repo) AddEntry(ctx context.Context, lotteryID, userID uint, registeredAt time.Time, now func() time.Time) (*lotteryapp.EntryResult, error) {
	var result lotteryapp.EntryResult
	err := r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		var lottery LotteryModel
		if err := txCtxDB(txCtx, tx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&lottery, "id = ?", lotteryID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return lotterydomain.ErrLotteryNotFound
			}
			return fmt.Errorf("lock lottery for entry: %w", err)
		}
		var existing EntryModel
		findErr := txCtxDB(txCtx, tx).Where("lottery_id = ? AND user_id = ?", lotteryID, userID).First(&existing).Error
		if findErr == nil {
			entry := entryFromModel(existing)
			lotteryValue, mapErr := lotteryFromModel(lottery)
			if mapErr != nil {
				return mapErr
			}
			result = lotteryapp.EntryResult{Lottery: lotteryValue, Entry: &entry, AlreadyExists: true}
			return nil
		}
		if !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("find lottery entry: %w", findErr)
		}
		current := time.Now().UTC()
		if now != nil {
			current = now().UTC()
		}
		if lottery.Status != string(lotterydomain.StatusOpen) || (lottery.DrawAt != nil && !lottery.DrawAt.After(current)) || (lottery.ParticipantTarget != nil && lottery.ParticipantCount >= *lottery.ParticipantTarget) || lottery.ParticipantCount >= lottery.MaxParticipants {
			return lotterydomain.ErrLotteryClosed
		}
		entryModel := EntryModel{LotteryID: lotteryID, UserID: userID, RegisteredAt: registeredAt, CreatedAt: current}
		if err := txCtxDB(txCtx, tx).Create(&entryModel).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return lotterydomain.ErrLotteryAlreadyEntered
			}
			return fmt.Errorf("create lottery entry: %w", err)
		}
		lottery.ParticipantCount++
		updates := map[string]any{"participant_count": lottery.ParticipantCount, "updated_at": current}
		if lottery.ParticipantTarget != nil && lottery.ParticipantCount >= *lottery.ParticipantTarget && lottery.TargetReachedAt == nil {
			lottery.TargetReachedAt = &current
			updates["target_reached_at"] = current
		}
		if err := txCtxDB(txCtx, tx).Model(&LotteryModel{}).Where("id = ?", lotteryID).Updates(updates).Error; err != nil {
			return fmt.Errorf("update lottery participant count: %w", err)
		}
		lotteryValue, mapErr := lotteryFromModel(lottery)
		if mapErr != nil {
			return mapErr
		}
		entry := entryFromModel(entryModel)
		result = lotteryapp.EntryResult{Lottery: lotteryValue, Entry: &entry}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *Repo) ListAllEntries(ctx context.Context, lotteryID uint) ([]lotterydomain.Entry, error) {
	var models []EntryModel
	if err := r.dbFor(ctx).Where("lottery_id = ?", lotteryID).Order("id ASC").Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list all lottery entries: %w", err)
	}
	items := make([]lotterydomain.Entry, len(models))
	for i := range models {
		items[i] = entryFromModel(models[i])
	}
	return items, nil
}

type winnerStatsRow struct {
	UserID     uint   `gorm:"column:user_id"`
	Tier       string `gorm:"column:tier"`
	AwardCount int64  `gorm:"column:award_count"`
}

func (r *Repo) LookupWinnerStats(ctx context.Context, userIDs []uint) (map[uint]lotteryapp.WinnerStats, error) {
	stats := make(map[uint]lotteryapp.WinnerStats, len(userIDs))
	if len(userIDs) == 0 {
		return stats, nil
	}
	rows := make([]winnerStatsRow, 0)
	query := r.dbFor(ctx).Model(&PayoutModel{}).
		Select("user_id, tier, COUNT(*) AS award_count").
		Where("user_id IN ? AND tier IN ?", userIDs, []string{string(lotterydomain.TierLucky), string(lotterydomain.TierConsolation)})
	// Older isolated repository tests (and pre-lottery installations) may not
	// have the parent table. Production uses the status predicate so provisional
	// payout rows cannot affect a later history snapshot.
	if r.db.Migrator().HasTable("lotteries") {
		query = query.Joins("JOIN lotteries ON lotteries.id = lottery_payouts.lottery_id").
			Where("lotteries.status = ?", lotterydomain.StatusCompleted)
	}
	if err := query.Group("user_id, tier").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("lookup lottery winner stats: %w", err)
	}
	for _, row := range rows {
		item := stats[row.UserID]
		switch lotterydomain.Tier(row.Tier) {
		case lotterydomain.TierLucky:
			item.LuckyCount = row.AwardCount
		case lotterydomain.TierConsolation:
			item.ConsolationCount = row.AwardCount
		}
		stats[row.UserID] = item
	}
	return stats, nil
}

func (r *Repo) ClaimSettlement(ctx context.Context, lotteryID uint, now time.Time) (*lotterydomain.Lottery, error) {
	var result *lotterydomain.Lottery
	err := r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		var model LotteryModel
		if err := txCtxDB(txCtx, tx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&model, "id = ?", lotteryID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return lotterydomain.ErrLotteryNotFound
			}
			return err
		}
		if model.Status == string(lotterydomain.StatusSettling) {
			mapped, mapErr := lotteryFromModel(model)
			result = mapped
			return mapErr
		}
		if model.Status != string(lotterydomain.StatusOpen) {
			return lotterydomain.ErrLotteryNotReady
		}
		timeDue := model.DrawAt != nil && !model.DrawAt.After(now)
		participantsDue := model.ParticipantTarget != nil && model.ParticipantCount >= *model.ParticipantTarget
		if !timeDue && !participantsDue {
			return lotterydomain.ErrLotteryNotReady
		}
		trigger := lotterydomain.TriggerTime
		if participantsDue && (!timeDue || (model.TargetReachedAt != nil && (model.DrawAt == nil || !model.TargetReachedAt.After(*model.DrawAt)))) {
			trigger = lotterydomain.TriggerParticipants
		}
		if err := txCtxDB(txCtx, tx).Model(&LotteryModel{}).Where("id = ? AND status = ?", lotteryID, lotterydomain.StatusOpen).Updates(map[string]any{
			"status": string(lotterydomain.StatusSettling), "triggered_by": string(trigger), "updated_at": now,
		}).Error; err != nil {
			return fmt.Errorf("claim lottery settlement: %w", err)
		}
		model.Status = string(lotterydomain.StatusSettling)
		model.TriggeredBy = string(trigger)
		mapped, mapErr := lotteryFromModel(model)
		result = mapped
		return mapErr
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (r *Repo) GetPayouts(ctx context.Context, lotteryID uint) ([]lotterydomain.Payout, error) {
	var models []PayoutModel
	if err := r.dbFor(ctx).Where("lottery_id = ?", lotteryID).Order("amount DESC").Order("id ASC").Find(&models).Error; err != nil {
		return nil, fmt.Errorf("get lottery payouts: %w", err)
	}
	items := make([]lotterydomain.Payout, len(models))
	for i := range models {
		items[i] = payoutFromModel(models[i])
	}
	return items, nil
}

func (r *Repo) SavePayouts(ctx context.Context, lotteryID uint, payouts []lotterydomain.Payout) error {
	return r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		var existing int64
		if err := txCtxDB(txCtx, tx).Model(&PayoutModel{}).Where("lottery_id = ?", lotteryID).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return nil
		}
		models := make([]PayoutModel, len(payouts))
		for i, payout := range payouts {
			createdAt := payout.CreatedAt
			if createdAt.IsZero() {
				createdAt = time.Now().UTC()
			}
			models[i] = PayoutModel{LotteryID: lotteryID, UserID: payout.UserID, Tier: string(payout.Tier), Amount: payout.Amount, CreatedAt: createdAt}
		}
		if len(models) == 0 {
			return nil
		}
		if err := txCtxDB(txCtx, tx).Create(&models).Error; errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil
		} else if err != nil {
			return fmt.Errorf("save lottery payouts: %w", err)
		}
		return nil
	})
}

func (r *Repo) RecordBillingTransactions(ctx context.Context, lotteryID uint, transactions map[uint]string, unusedAmount string) error {
	return r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		for userID, transactionNo := range transactions {
			result := txCtxDB(txCtx, tx).Model(&PayoutModel{}).
				Where("lottery_id = ? AND user_id = ? AND (billing_transaction_no = '' OR billing_transaction_no = ?)", lotteryID, userID, transactionNo).
				Update("billing_transaction_no", transactionNo)
			if result.Error != nil {
				return fmt.Errorf("record lottery award transaction: %w", result.Error)
			}
			if result.RowsAffected == 0 {
				var payout PayoutModel
				if err := txCtxDB(txCtx, tx).Where("lottery_id = ? AND user_id = ?", lotteryID, userID).First(&payout).Error; err != nil {
					return fmt.Errorf("record lottery award transaction: payout %w", err)
				}
				if payout.BillingTransactionNo != transactionNo {
					return fmt.Errorf("record lottery award transaction: payout for user %d is missing", userID)
				}
			}
		}
		result := txCtxDB(txCtx, tx).Model(&LotteryModel{}).
			Where("id = ? AND status = ? AND (unused_amount = 0 OR unused_amount = ?)", lotteryID, lotterydomain.StatusSettling, unusedAmount).
			Update("unused_amount", unusedAmount)
		if result.Error != nil {
			return fmt.Errorf("record lottery unused amount: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			var lottery LotteryModel
			if err := txCtxDB(txCtx, tx).First(&lottery, "id = ?", lotteryID).Error; err != nil {
				return fmt.Errorf("record lottery unused amount: %w", err)
			}
			stored, storedErr := money.Parse(lottery.UnusedAmount)
			expected, expectedErr := money.Parse(unusedAmount)
			if storedErr != nil || expectedErr != nil || !stored.Equal(expected) {
				return fmt.Errorf("record lottery unused amount: lottery %d has a different value", lotteryID)
			}
		}
		return nil
	})
}

func (r *Repo) Complete(ctx context.Context, lotteryID uint, status lotterydomain.Status, unusedAmount string, settledAt time.Time) error {
	result := r.dbFor(ctx).Model(&LotteryModel{}).Where("id = ? AND status = ?", lotteryID, lotterydomain.StatusSettling).Updates(map[string]any{
		"status": string(status), "unused_amount": unusedAmount, "settled_at": settledAt, "updated_at": settledAt,
	})
	if result.Error != nil {
		return fmt.Errorf("complete lottery: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		lottery, err := r.GetByID(ctx, lotteryID)
		if err != nil {
			return err
		}
		if lottery.Status == status {
			return nil
		}
		return lotterydomain.ErrLotterySettlement
	}
	return nil
}

func (r *Repo) FindEntry(ctx context.Context, lotteryID, userID uint) (*lotterydomain.Entry, error) {
	var model EntryModel
	if err := r.dbFor(ctx).Where("lottery_id = ? AND user_id = ?", lotteryID, userID).First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, lotterydomain.ErrLotteryNotFound
		}
		return nil, err
	}
	entry := entryFromModel(model)
	return &entry, nil
}

func (r *Repo) FindPayout(ctx context.Context, lotteryID, userID uint) (*lotterydomain.Payout, error) {
	var model PayoutModel
	if err := r.dbFor(ctx).Where("lottery_id = ? AND user_id = ?", lotteryID, userID).First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, lotterydomain.ErrLotteryNotFound
		}
		return nil, err
	}
	payout := payoutFromModel(model)
	return &payout, nil
}

func txCtxDB(ctx context.Context, tx *gorm.DB) *gorm.DB { return tx.WithContext(ctx) }

func maxInt(value, fallback int) int {
	if value < fallback {
		return fallback
	}
	return value
}

func lotteryFromModel(model LotteryModel) (*lotterydomain.Lottery, error) {
	var weights lotterydomain.TierWeights
	if err := json.Unmarshal([]byte(model.TierWeightsJSON), &weights); err != nil {
		return nil, fmt.Errorf("decode lottery tier weights: %w", err)
	}
	return &lotterydomain.Lottery{
		ID: model.ID, PublicToken: model.PublicToken, CreatedByUserID: model.CreatedByUserID,
		Title: model.Title, TotalAmount: model.TotalAmount, MinPayout: model.MinPayout, MaxPayout: model.MaxPayout,
		TierWeights: weights, MinAccountAgeDays: model.MinAccountAgeDays, DrawAt: model.DrawAt, ParticipantTarget: model.ParticipantTarget,
		ParticipantCount: model.ParticipantCount, MaxParticipants: model.MaxParticipants, Status: lotterydomain.Status(model.Status),
		TriggeredBy: lotterydomain.Trigger(model.TriggeredBy), TargetReachedAt: model.TargetReachedAt,
		AlgorithmVersion: model.AlgorithmVersion, UnusedAmount: model.UnusedAmount, IdempotencyKey: model.IdempotencyKey,
		RequestFingerprint: model.RequestFingerprint,
		SettledAt:          model.SettledAt, CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt,
	}, nil
}

func lotterySlice(models []LotteryModel) ([]*lotterydomain.Lottery, error) {
	items := make([]*lotterydomain.Lottery, len(models))
	for i := range models {
		item, err := lotteryFromModel(models[i])
		if err != nil {
			return nil, err
		}
		items[i] = item
	}
	return items, nil
}

func entryFromModel(model EntryModel) lotterydomain.Entry {
	return lotterydomain.Entry{ID: model.ID, LotteryID: model.LotteryID, UserID: model.UserID, RegisteredAt: model.RegisteredAt, CreatedAt: model.CreatedAt}
}

func payoutFromModel(model PayoutModel) lotterydomain.Payout {
	return lotterydomain.Payout{ID: model.ID, LotteryID: model.LotteryID, UserID: model.UserID, Tier: lotterydomain.Tier(model.Tier), Amount: model.Amount, BillingTransactionNo: model.BillingTransactionNo, MailQueuedAt: model.MailQueuedAt, CreatedAt: model.CreatedAt}
}

var _ lotteryapp.Repository = (*Repo)(nil)
var _ lotteryapp.DrawTransactionRepository = (*Repo)(nil)
var _ lotteryapp.WinnerUserLockRepository = (*Repo)(nil)
var _ interface {
	FindEntry(context.Context, uint, uint) (*lotterydomain.Entry, error)
	FindPayout(context.Context, uint, uint) (*lotterydomain.Payout, error)
} = (*Repo)(nil)
