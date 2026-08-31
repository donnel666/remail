package infra

import (
	"context"
	"errors"
	"fmt"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"gorm.io/gorm"
)

func (r *BillingRepo) LatestLeaderboardRewards(ctx context.Context, limit int) (*billingapp.LeaderboardRewardSettlement, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var settlement LeaderboardSettlementModel
	if err := r.db.WithContext(ctx).Where("status = ?", "completed").
		Order("business_date DESC, id DESC").First(&settlement).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("load latest leaderboard settlement: %w", err)
	}
	var rows []struct {
		UserID   uint   `gorm:"column:user_id"`
		Nickname string `gorm:"column:nickname"`
		Rank     int    `gorm:"column:rank_no"`
		Score    int    `gorm:"column:score"`
		Amount   string `gorm:"column:reward_amount"`
	}
	if err := r.db.WithContext(ctx).Table("leaderboard_rewards AS reward").
		Select("reward.user_id, COALESCE(user.nickname, '') AS nickname, reward.rank_no, reward.score, reward.reward_amount").
		Joins("JOIN users AS user ON user.id = reward.user_id").
		Where("reward.settlement_id = ?", settlement.ID).
		Order("reward.rank_no ASC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list latest leaderboard rewards: %w", err)
	}
	result := &billingapp.LeaderboardRewardSettlement{
		BusinessDate: settlement.BusinessDate, PeriodStart: settlement.PeriodStart,
		PeriodEnd: settlement.PeriodEnd, SettledAt: settlement.SettledAt,
		Rewards: make([]billingapp.LeaderboardRewardRecord, len(rows)),
	}
	for i, row := range rows {
		result.Rewards[i] = billingapp.LeaderboardRewardRecord{
			UserID: row.UserID, Nickname: row.Nickname, Rank: row.Rank,
			Score: row.Score, Amount: normalizeMoneyString(row.Amount),
		}
	}
	return result, nil
}
