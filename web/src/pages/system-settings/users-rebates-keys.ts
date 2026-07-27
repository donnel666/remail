export const RECHARGE_REBATE_KEYS = [
  "first_order_rebate_ratio",
  "single_rebate_cap",
  "cumulative_rebate_cap",
  "rebate_expiry_days",
] as const;

export const DAILY_CHECKIN_REWARD_KEYS = [
  "daily_checkin_enabled",
  "daily_checkin_reward_rules",
] as const;

export const LEADERBOARD_REWARD_KEYS = [
  "leaderboard_reward_enabled",
  "leaderboard_reward_rules",
  "leaderboard_settlement_time",
] as const;
