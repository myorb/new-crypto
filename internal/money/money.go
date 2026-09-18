// Package money converts between the database's NUMERIC(38,18) amounts and
// decimal.Decimal, and holds the small arithmetic helpers shared by pricing,
// payments and payouts. All amounts are in human units (1.5 = 1.5 USDT).
package money

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"
)

// Scale is the number of fractional digits crypto_amount stores.
const Scale = 18

// BpsDenominator turns basis points into a fraction: 100 bps = 1 %.
const BpsDenominator = 10_000

var ErrNotANumber = errors.New("money: numeric is NaN or infinite")

// FromNumeric converts a pgtype.Numeric into a decimal. An invalid (NULL)
// numeric becomes zero, which is what every nullable amount column means.
func FromNumeric(n pgtype.Numeric) decimal.Decimal {
	if !n.Valid || n.Int == nil || n.NaN || n.InfinityModifier != pgtype.Finite {
		return decimal.Zero
	}
	return decimal.NewFromBigInt(n.Int, n.Exp)
}

// FromNumericStrict is FromNumeric that reports NULL / NaN instead of
// silently returning zero. Use it where a missing amount is a bug.
func FromNumericStrict(n pgtype.Numeric) (decimal.Decimal, error) {
	if !n.Valid {
		return decimal.Zero, errors.New("money: numeric is NULL")
	}
	if n.Int == nil || n.NaN || n.InfinityModifier != pgtype.Finite {
		return decimal.Zero, ErrNotANumber
	}
	return decimal.NewFromBigInt(n.Int, n.Exp), nil
}

// ToNumeric converts a decimal into a pgtype.Numeric suitable for a
// crypto_amount / NUMERIC column.
func ToNumeric(d decimal.Decimal) pgtype.Numeric {
	return pgtype.Numeric{Int: new(big.Int).Set(d.Coefficient()), Exp: d.Exponent(), Valid: true}
}

// NullNumeric converts an optional decimal; nil becomes SQL NULL.
func NullNumeric(d *decimal.Decimal) pgtype.Numeric {
	if d == nil {
		return pgtype.Numeric{}
	}
	return ToNumeric(*d)
}

// Ptr is a small helper for building NullNumeric arguments inline.
func Ptr(d decimal.Decimal) *decimal.Decimal { return &d }

// Bps returns amount × bps / 10 000 at full precision.
func Bps(amount decimal.Decimal, bps int32) decimal.Decimal {
	if bps == 0 || amount.IsZero() {
		return decimal.Zero
	}
	return amount.Mul(decimal.NewFromInt32(bps)).Div(decimal.NewFromInt(BpsDenominator))
}

// Clamp bounds v to [min, max]; a nil bound is ignored.
func Clamp(v decimal.Decimal, min, max *decimal.Decimal) decimal.Decimal {
	if min != nil && v.LessThan(*min) {
		v = *min
	}
	if max != nil && v.GreaterThan(*max) {
		v = *max
	}
	return v
}

// RoundUp rounds v up (away from zero for positives) to the asset's decimals,
// which is what a payer-facing "amount due" needs: never quote less than owed.
func RoundUp(v decimal.Decimal, decimals int16) decimal.Decimal {
	return v.RoundCeil(int32(decimals))
}

// RoundDown truncates v to the asset's decimals; used for fees so rounding
// never charges more than the schedule says.
func RoundDown(v decimal.Decimal, decimals int16) decimal.Decimal {
	return v.RoundFloor(int32(decimals))
}

// Parse parses a decimal string, rejecting empty input and NaN / Inf.
func Parse(s string) (decimal.Decimal, error) {
	if s == "" {
		return decimal.Zero, errors.New("money: empty amount")
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero, fmt.Errorf("money: parse %q: %w", s, err)
	}
	return d, nil
}

// MustParse is Parse for constants in code and tests.
func MustParse(s string) decimal.Decimal {
	d, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return d
}

// Format renders v with at most the asset's decimals and no trailing zeros
// beyond the minimum, e.g. "2450" -> "2450.00" for a 6-decimal stablecoin is
// left to the UI; this is the canonical machine string.
func Format(v decimal.Decimal, decimals int16) string {
	return v.Round(int32(decimals)).String()
}
