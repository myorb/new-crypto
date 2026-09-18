package money

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestNumericRoundTrip(t *testing.T) {
	for _, s := range []string{"0", "1", "1.5", "2450.000001", "0.000000000000000001", "123456789012345678.123456789012345678", "-7.25"} {
		d := MustParse(s)
		back := FromNumeric(ToNumeric(d))
		if !back.Equal(d) {
			t.Errorf("%s -> %s", s, back)
		}
	}
	if !FromNumeric(NullNumeric(nil)).IsZero() {
		t.Error("NULL numeric should read as zero")
	}
	if _, err := FromNumericStrict(NullNumeric(nil)); err == nil {
		t.Error("strict conversion must reject NULL")
	}
}

func TestBpsAndClamp(t *testing.T) {
	if got := Bps(MustParse("1000"), 100); !got.Equal(MustParse("10")) {
		t.Errorf("100 bps of 1000 = %s", got)
	}
	if got := Bps(MustParse("1000"), 0); !got.IsZero() {
		t.Errorf("0 bps should be zero, got %s", got)
	}
	min, max := MustParse("1"), MustParse("5")
	if got := Clamp(MustParse("0.5"), &min, &max); !got.Equal(min) {
		t.Errorf("clamp below min = %s", got)
	}
	if got := Clamp(MustParse("9"), &min, &max); !got.Equal(max) {
		t.Errorf("clamp above max = %s", got)
	}
	if got := Clamp(MustParse("3"), nil, nil); !got.Equal(MustParse("3")) {
		t.Errorf("clamp without bounds = %s", got)
	}
}

func TestRounding(t *testing.T) {
	if got := RoundUp(MustParse("1.0000001"), 6); !got.Equal(MustParse("1.000001")) {
		t.Errorf("round up = %s", got)
	}
	if got := RoundDown(MustParse("1.9999999"), 6); !got.Equal(MustParse("1.999999")) {
		t.Errorf("round down = %s", got)
	}
	if got := Format(decimal.NewFromInt(2450), 2); got != "2450" {
		t.Errorf("format = %s", got)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "abc", "1..2"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) should fail", s)
		}
	}
}
