// Package chart renders shadcn-style charts. This file holds the plain
// number formatters the dashboard uses for axis ticks, tooltips and the
// figures next to a chart. They are independent of the chart engine in
// chart.templ, which is generated from the shadcn-templ registry.
package chart

import (
	"math"
	"strconv"
	"strings"
)

// NiceMax rounds v up to a tidy axis maximum (1, 1.5, 2, 2.5, 3, 4, 5, 6, 8
// or 10 times a power of ten).
func NiceMax(v float64) float64 {
	if v <= 0 {
		return 1
	}
	mag := math.Pow(10, math.Floor(math.Log10(v)))
	frac := v / mag
	for _, n := range []float64{1, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10} {
		if frac <= n {
			return n * mag
		}
	}
	return 10 * mag
}

// Compact formats 1234567 as "1.2M", 45000 as "45K".
func Compact(v float64) string {
	abs := math.Abs(v)
	switch {
	case abs >= 1e9:
		return trimZero(v/1e9, 1) + "B"
	case abs >= 1e6:
		return trimZero(v/1e6, 1) + "M"
	case abs >= 1e3:
		return trimZero(v/1e3, 1) + "K"
	}
	return trimZero(v, 0)
}

func trimZero(v float64, decimals int) string {
	s := strconv.FormatFloat(v, 'f', decimals, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// Money formats 1284320.5 as "$1,284,320.50".
func Money(v float64) string {
	return "$" + Thousands(v, 2)
}

// Thousands formats v with thousands separators and the given decimals.
func Thousands(v float64, decimals int) string {
	neg := v < 0
	s := strconv.FormatFloat(math.Abs(v), 'f', decimals, 64)
	intPart, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, frac = s[:i], s[i:]
	}
	var b strings.Builder
	for i, r := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	out := b.String() + frac
	if neg {
		return "-" + out
	}
	return out
}

// Share is v as a percentage of total, e.g. "46.2%".
func Share(v, total float64) string {
	if total <= 0 {
		return "0%"
	}
	return trimZero(v/total*100, 1) + "%"
}
