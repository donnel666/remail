package money

import (
	"errors"
	"regexp"
	"strings"

	"github.com/shopspring/decimal"
)

const Scale int32 = 6

var (
	errInvalidAmount = errors.New("invalid money amount")
	decimalPattern   = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]{1,6})?$`)
	maxAmount        = decimal.RequireFromString("999999999999.999999")
)

// Format returns the canonical API/domain representation for an amount.
// Cent-only values keep two decimal places for backward compatibility, while
// sub-cent values retain up to the full ledger precision.
func Format(amount decimal.Decimal) string {
	fixed := amount.StringFixedBank(Scale)
	whole, fraction, found := strings.Cut(fixed, ".")
	if !found {
		return fixed + ".00"
	}

	fraction = strings.TrimRight(fraction, "0")
	if len(fraction) < 2 {
		fraction += strings.Repeat("0", 2-len(fraction))
	}
	return whole + "." + fraction
}

func Normalize(value string) (string, error) {
	amount, err := Parse(value)
	if err != nil {
		return "", err
	}
	return Format(amount), nil
}

func Parse(value string) (decimal.Decimal, error) {
	trimmed := strings.TrimSpace(value)
	if !decimalPattern.MatchString(trimmed) {
		return decimal.Zero, errInvalidAmount
	}
	amount, err := decimal.NewFromString(trimmed)
	if err != nil || amount.Abs().GreaterThan(maxAmount) {
		return decimal.Zero, errInvalidAmount
	}
	return amount, nil
}
