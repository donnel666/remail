package money

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestFormatPreservesSubCentAmountsAndCentCompatibility(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "0", want: "0.00"},
		{input: "1", want: "1.00"},
		{input: "1.2", want: "1.20"},
		{input: "0.008", want: "0.008"},
		{input: "0.005", want: "0.005"},
		{input: "0.007", want: "0.007"},
		{input: "-0.008", want: "-0.008"},
		{input: "1.2345678", want: "1.234568"},
	} {
		t.Run(test.input, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, test.want, Format(decimal.RequireFromString(test.input)))
		})
	}
}

func TestParseRejectsAmountsOutsideLedgerPrecision(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		"",
		".008",
		"1e3",
		"0.0000001",
		"999999999999.9999999",
		"1000000000000.00",
	} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(input)
			require.Error(t, err)
		})
	}

	amount, err := Parse("999999999999.999999")
	require.NoError(t, err)
	require.Equal(t, "999999999999.999999", Format(amount))
}
