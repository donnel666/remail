package app

import (
	"testing"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

func TestCheckinRewardUsesNormalizedWeightsAndAdjacentRanges(t *testing.T) {
	rules := []runtimeconfig.CheckinRewardRule{
		{Amount: "1000.00", ProbabilityUnits: 1},
		{Amount: "500.00", ProbabilityUnits: 2},
		{Amount: "10.00", ProbabilityUnits: 7},
	}
	checks := map[int64]int{0: 0, 1: 1, 2: 1, 3: 2, 9: 2, 10: -1}
	for roll, want := range checks {
		if got := checkinRewardIndexAt(rules, roll); got != want {
			t.Fatalf("roll %d got tier %d, want %d", roll, got, want)
		}
	}

	ranges := []struct {
		index  int
		offset int64
		want   string
	}{
		{index: 0, offset: 0, want: "501.00"},
		{index: 0, offset: 499, want: "1000.00"},
		{index: 1, offset: 0, want: "11.00"},
		{index: 1, offset: 489, want: "500.00"},
		{index: 2, offset: 0, want: "10.00"},
	}
	for _, check := range ranges {
		if got := checkinRewardAt(rules, check.index, check.offset); got != check.want {
			t.Fatalf("tier %d offset %d got %s, want %s", check.index, check.offset, got, check.want)
		}
	}
}
