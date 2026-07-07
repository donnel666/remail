package app

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateCaptchaExpressionUsesIntegerNonNegativeAnswer(t *testing.T) {
	for range 200 {
		expression, err := generateCaptchaExpression()
		require.NoError(t, err)

		require.GreaterOrEqual(t, expression.answer, 0)
		require.False(t, strings.ContainsAny(expression.question, ".。"))
	}
}

func TestDivisionCaptchaExpressionUsesIntegerAnswer(t *testing.T) {
	for range 100 {
		expression, err := divisionExpression()
		require.NoError(t, err)

		parts := strings.Split(expression.question, "÷")
		require.Len(t, parts, 2)
		dividend, err := strconv.Atoi(parts[0])
		require.NoError(t, err)
		divisor, err := strconv.Atoi(parts[1])
		require.NoError(t, err)

		require.NotZero(t, divisor)
		require.Zero(t, dividend%divisor)
		require.Equal(t, dividend/divisor, expression.answer)
		require.GreaterOrEqual(t, expression.answer, 0)
	}
}
