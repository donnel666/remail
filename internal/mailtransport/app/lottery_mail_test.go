package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLotteryWinnerMessageUsesBrandedFrameAndEscapesActivityData(t *testing.T) {
	message := LotteryWinnerMessage("winner@example.com", 12, "<抽奖>", "20.000000")
	require.Equal(t, "winner@example.com", message.To)
	require.Contains(t, message.HTMLBody, "<!doctype html>")
	require.Contains(t, message.HTMLBody, "抽奖奖励到账")
	require.Contains(t, message.HTMLBody, "&lt;抽奖&gt;")
	require.NotContains(t, message.HTMLBody, "<抽奖>")
	require.Contains(t, message.HTMLBody, "20 积分")
	require.NotContains(t, message.HTMLBody, "20.000000")
	require.NotContains(t, message.HTMLBody, "奖励档位")
	require.NotContains(t, message.HTMLBody, "安慰奖")
	require.Contains(t, message.HTMLBody, "Remail，轻松收码")
	require.Contains(t, message.TextBody, "20 积分")
	require.NotContains(t, message.TextBody, "20.000000")
	require.NotContains(t, message.TextBody, "奖励档位")
}

func TestFormatLotteryRewardUsesCompactDisplay(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "20.000000", want: "20"},
		{value: "20.500000", want: "20.5"},
		{value: "1000.000000", want: "1K"},
		{value: "12500.000000", want: "12.5K"},
		{value: "999950.000000", want: "1M"},
		{value: "1000000.000000", want: "1M"},
		{value: "2500000000.000000", want: "2.5B"},
	} {
		t.Run(test.value, func(t *testing.T) {
			require.Equal(t, test.want, formatLotteryReward(test.value))
		})
	}
}

func TestTrimDecimalZerosKeepsIntegerStrings(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "20", want: "20"},
		{value: "100", want: "100"},
		{value: "0", want: "0"},
		{value: "20.5000", want: "20.5"},
	} {
		t.Run(test.value, func(t *testing.T) {
			require.Equal(t, test.want, trimDecimalZeros(test.value))
		})
	}
}
