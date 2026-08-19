package app

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLotteryWinnerMessageUsesBrandedFrameAndEscapesActivityData(t *testing.T) {
	message := LotteryWinnerMessage("winner@example.com", 12, "<抽奖>", "1.25", "lucky")
	require.Equal(t, "winner@example.com", message.To)
	require.Contains(t, message.HTMLBody, "<!doctype html>")
	require.Contains(t, message.HTMLBody, "抽奖奖励到账")
	require.Contains(t, message.HTMLBody, "&lt;抽奖&gt;")
	require.NotContains(t, message.HTMLBody, "<抽奖>")
	require.Contains(t, message.HTMLBody, "Remail，轻松收码")
	require.True(t, strings.Contains(message.TextBody, "1.25"))
}
