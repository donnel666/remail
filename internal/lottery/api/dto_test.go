package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	lotterydomain "github.com/donnel666/remail/internal/lottery/domain"
	"github.com/stretchr/testify/require"
)

func TestPublicLotteryResponseOmitsPrivateRulesAndAccounting(t *testing.T) {
	drawAt := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	participantTarget := 100
	lottery := &lotterydomain.Lottery{
		Title:             "Weekend points",
		TotalAmount:       "300.00",
		MinPayout:         "1.00",
		MaxPayout:         "20.00",
		TierWeights:       lotterydomain.TierWeights{Consolation: 80, Normal: 15, Lucky: 5},
		MinAccountAgeDays: 30,
		DrawAt:            &drawAt,
		ParticipantCount:  12,
		ParticipantTarget: &participantTarget,
		MaxParticipants:   500,
		Status:            lotterydomain.StatusCompleted,
	}

	payload, err := json.Marshal(PublicLotteryResponse{
		Lottery: publicLotterySummary(lottery), HasEntered: true,
		MyPayout: &PublicPayoutResponse{Amount: "8.25"},
	})
	require.NoError(t, err)
	text := string(payload)
	require.Contains(t, text, `"totalAmount":"300.00"`)
	require.Contains(t, text, `"participantCount":12`)
	require.Contains(t, text, `"participantTarget":100`)
	require.Contains(t, text, `"drawAt":"2026-08-22T12:00:00Z"`)
	for _, privateField := range []string{
		"minPayout", "maxPayout", "tierWeights", "minAccountAgeDays", "maxParticipants",
		"userId", "billingTransactionNo", "tier",
	} {
		require.False(t, strings.Contains(text, `"`+privateField+`"`), privateField)
	}
}

func TestPublicLotterySummaryOmitsUnsetParticipantTarget(t *testing.T) {
	lottery := &lotterydomain.Lottery{
		Title:            "Time-only points",
		TotalAmount:      "300.00",
		ParticipantCount: 100,
		Status:           lotterydomain.StatusOpen,
	}

	payload, err := json.Marshal(publicLotterySummary(lottery))
	require.NoError(t, err)
	text := string(payload)
	require.Contains(t, text, `"participantCount":100`)
	require.NotContains(t, text, `"participantTarget"`)
}
