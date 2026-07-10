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

func TestCaptchaOperandsStayInsideNineByNineRange(t *testing.T) {
	tests := []struct {
		name       string
		operator   string
		generate   func() (captchaExpression, error)
		assertions func(*testing.T, int, int, captchaExpression)
	}{
		{
			name:     "addition",
			operator: "+",
			generate: additionExpression,
			assertions: func(t *testing.T, left, right int, expression captchaExpression) {
				require.Equal(t, left+right, expression.answer)
			},
		},
		{
			name:     "subtraction",
			operator: "−",
			generate: subtractionExpression,
			assertions: func(t *testing.T, left, right int, expression captchaExpression) {
				require.GreaterOrEqual(t, left, right)
				require.Equal(t, left-right, expression.answer)
			},
		},
		{
			name:     "multiplication",
			operator: "×",
			generate: multiplicationExpression,
			assertions: func(t *testing.T, left, right int, expression captchaExpression) {
				require.Equal(t, left*right, expression.answer)
			},
		},
		{
			name:     "division",
			operator: "÷",
			generate: divisionExpression,
			assertions: func(t *testing.T, dividend, divisor int, expression captchaExpression) {
				require.NotZero(t, divisor)
				require.Zero(t, dividend%divisor)
				require.Equal(t, dividend/divisor, expression.answer)
				require.GreaterOrEqual(t, expression.answer, captchaOperandMin)
				require.LessOrEqual(t, expression.answer, captchaOperandMax)
				require.LessOrEqual(t, dividend, captchaOperandMax*captchaOperandMax)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for range 500 {
				expression, err := test.generate()
				require.NoError(t, err)
				parts := strings.Split(expression.question, test.operator)
				require.Len(t, parts, 2)
				left, err := strconv.Atoi(parts[0])
				require.NoError(t, err)
				right, err := strconv.Atoi(parts[1])
				require.NoError(t, err)
				require.GreaterOrEqual(t, right, captchaOperandMin)
				require.LessOrEqual(t, right, captchaOperandMax)
				if test.operator != "÷" {
					require.GreaterOrEqual(t, left, captchaOperandMin)
					require.LessOrEqual(t, left, captchaOperandMax)
				}
				require.GreaterOrEqual(t, expression.answer, 0)
				test.assertions(t, left, right, expression)
			}
		})
	}
}
