package runtimeconfig

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/money"
	"github.com/shopspring/decimal"
)

const (
	maxRewardRules = 100
)

type CheckinRewardRule struct {
	Amount           string
	ProbabilityUnits int64
}

type LeaderboardRewardRule struct {
	RankFrom int    `json:"rankFrom"`
	RankTo   int    `json:"rankTo"`
	Amount   string `json:"amount"`
}

type rawCheckinRewardRule struct {
	Amount      json.Number `json:"amount"`
	Probability json.Number `json:"probability"`
}

type rawLeaderboardRewardRule struct {
	RankFrom int         `json:"rankFrom"`
	RankTo   int         `json:"rankTo"`
	Amount   json.Number `json:"amount"`
}

func ParseCheckinRewardRules(value string) ([]CheckinRewardRule, error) {
	var raw []rawCheckinRewardRule
	if err := decodeRewardJSON(value, &raw); err != nil || len(raw) > maxRewardRules {
		return nil, fmt.Errorf("invalid check-in reward rules")
	}
	rules := make([]CheckinRewardRule, len(raw))
	var total int64
	for i, rule := range raw {
		amount, err := money.Parse(string(rule.Amount))
		weight, weightErr := decimal.NewFromString(string(rule.Probability))
		if err != nil || !amount.IsPositive() || !amount.Equal(amount.Truncate(0)) || weightErr != nil || !weight.IsPositive() || weight.Exponent() < -money.Scale {
			return nil, fmt.Errorf("invalid check-in reward rule")
		}
		unitsDecimal := weight.Shift(money.Scale)
		if unitsDecimal.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
			return nil, fmt.Errorf("invalid check-in reward weight")
		}
		units := unitsDecimal.IntPart()
		if units <= 0 || total > math.MaxInt64-units {
			return nil, fmt.Errorf("invalid check-in reward weight")
		}
		total += units
		rules[i] = CheckinRewardRule{Amount: money.Format(amount), ProbabilityUnits: units}
	}
	sort.Slice(rules, func(i, j int) bool { return checkinRewardAmount(rules[i]) > checkinRewardAmount(rules[j]) })
	for i := 1; i < len(rules); i++ {
		if checkinRewardAmount(rules[i]) == checkinRewardAmount(rules[i-1]) {
			return nil, fmt.Errorf("duplicate check-in reward amount")
		}
	}
	return rules, nil
}

func checkinRewardAmount(rule CheckinRewardRule) int64 {
	amount, _ := money.Parse(rule.Amount)
	return amount.IntPart()
}

func ParseLeaderboardRewardRules(value string) ([]LeaderboardRewardRule, error) {
	var raw []rawLeaderboardRewardRule
	if err := decodeRewardJSON(value, &raw); err != nil || len(raw) > maxRewardRules {
		return nil, fmt.Errorf("invalid leaderboard reward rules")
	}
	rules := make([]LeaderboardRewardRule, len(raw))
	for i, rule := range raw {
		amount, err := money.Parse(string(rule.Amount))
		if err != nil || !amount.IsPositive() || rule.RankFrom <= 0 || rule.RankFrom > rule.RankTo || rule.RankTo > 100 {
			return nil, fmt.Errorf("invalid leaderboard reward rule")
		}
		for _, previous := range rules[:i] {
			if rule.RankFrom <= previous.RankTo && previous.RankFrom <= rule.RankTo {
				return nil, fmt.Errorf("overlapping leaderboard reward ranks")
			}
		}
		rules[i] = LeaderboardRewardRule{RankFrom: rule.RankFrom, RankTo: rule.RankTo, Amount: money.Format(amount)}
	}
	return rules, nil
}

func ParseSettlementClock(value string) (hour, minute int, err error) {
	value = strings.TrimSpace(value)
	if len(value) != 5 || value[2] != ':' {
		return 0, 0, fmt.Errorf("invalid settlement clock")
	}
	if _, err = time.Parse("15:04", value); err != nil {
		return 0, 0, err
	}
	hour, _ = strconv.Atoi(value[:2])
	minute, _ = strconv.Atoi(value[3:])
	return hour, minute, nil
}

func decodeRewardJSON(value string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(value)))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("extra reward JSON")
	}
	return nil
}
