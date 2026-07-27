package app

import (
	"testing"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

func TestCheckinRewardUsesExactProbabilityUnits(t *testing.T) {
	rules := []runtimeconfig.CheckinRewardRule{
		{Amount: "100.00", ProbabilityUnits: 5_000},
		{Amount: "50.00", ProbabilityUnits: 100_000},
	}
	checks := map[int64]string{0: "100.00", 4_999: "100.00", 5_000: "50.00", 104_999: "50.00", 105_000: "0.00"}
	for roll, want := range checks {
		if got := checkinRewardAt(rules, roll); got != want {
			t.Fatalf("roll %d got %s, want %s", roll, got, want)
		}
	}
}
