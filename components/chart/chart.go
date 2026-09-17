// Package chart renders dependency-free SVG/CSS charts from Go data. Every
// chart is server-rendered, themes through CSS variables (var(--chart-1)
// etc.) and stays crisp at any width because strokes do not scale.
package chart

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Point is one x-position of a line or area chart.
type Point struct {
	Label string
	Value float64
}

// Series is one line on an area chart. Color is any CSS color, usually a
// theme token such as "var(--chart-1)".
type Series struct {
	Name   string
	Color  string
	Points []Point
	// Dashed draws a thin dashed line, e.g. for the previous period.
	Dashed bool
	// NoFill skips the gradient under the line.
	NoFill bool
}

// Slice is one segment of a donut.
type Slice struct {
	Label string
	Value float64
	Color string
}

// Group is one x-position of a stacked bar chart with one value per series.
type Group struct {
	Label  string
	Values []float64
}

// Legend names and colours one series of a bar chart.
type Legend struct {
	Name  string
	Color string
}

// The plot viewBox. Wide so curves have enough resolution; the SVG is
// stretched with preserveAspectRatio="none" and non-scaling strokes.
const plotW, plotH = 1000.0, 100.0

func f(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}

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

func seriesMax(series []Series) float64 {
	m := 0.0
	for _, s := range series {
		for _, p := range s.Points {
			m = math.Max(m, p.Value)
		}
	}
	return NiceMax(m)
}

func groupsMax(groups []Group) float64 {
	m := 0.0
	for _, g := range groups {
		sum := 0.0
		for _, v := range g.Values {
			sum += v
		}
		m = math.Max(m, sum)
	}
	return NiceMax(m)
}

type xy struct{ x, y float64 }

// coords maps points onto the plot viewBox; x spans the full width.
func coords(pts []Point, max float64) []xy {
	n := len(pts)
	out := make([]xy, n)
	for i, p := range pts {
		x := 0.0
		if n > 1 {
			x = float64(i) / float64(n-1) * plotW
		}
		out[i] = xy{x, plotH - p.Value/max*plotH}
	}
	return out
}

// sparkCoords scales the values between the min and max so small
// movements stay visible, with a little vertical padding.
func sparkCoords(values []float64) []xy {
	n := len(values)
	if n == 0 {
		return nil
	}
	lo, hi := values[0], values[0]
	for _, v := range values {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	span := hi - lo
	if span == 0 {
		span = 1
	}
	out := make([]xy, n)
	for i, v := range values {
		x := 0.0
		if n > 1 {
			x = float64(i) / float64(n-1) * plotW
		}
		out[i] = xy{x, 90 - (v-lo)/span*80}
	}
	return out
}

// linePath is a smooth Catmull-Rom spline through the points.
func linePath(c []xy) string {
	if len(c) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "M%s,%s", f(c[0].x), f(c[0].y))
	for i := 0; i < len(c)-1; i++ {
		p0, p1, p2, p3 := c[max(i-1, 0)], c[i], c[i+1], c[min(i+2, len(c)-1)]
		c1 := xy{p1.x + (p2.x-p0.x)/6, clamp(p1.y + (p2.y-p0.y)/6)}
		c2 := xy{p2.x - (p3.x-p1.x)/6, clamp(p2.y - (p3.y-p1.y)/6)}
		fmt.Fprintf(&b, " C%s,%s %s,%s %s,%s", f(c1.x), f(c1.y), f(c2.x), f(c2.y), f(p2.x), f(p2.y))
	}
	return b.String()
}

// areaPath closes the line down to the baseline.
func areaPath(c []xy) string {
	if len(c) == 0 {
		return ""
	}
	return fmt.Sprintf("%s L%s,%s L%s,%s Z", linePath(c), f(c[len(c)-1].x), f(plotH), f(c[0].x), f(plotH))
}

func clamp(y float64) float64 {
	return math.Max(0, math.Min(plotH, y))
}

// tickY is the viewBox y of gridline i out of ticks (0 is the baseline).
func tickY(i, ticks int) string {
	return f(plotH - plotH*float64(i)/float64(ticks))
}

// pctTop is the CSS top offset of a value inside the plot.
func pctTop(v, max float64) string {
	return f((1-v/max)*100) + "%"
}

// pctHeight is the CSS height of a value inside the plot.
func pctHeight(v, max float64) string {
	return f(v/max*100) + "%"
}

// xPct is the CSS left offset of point i out of n.
func xPct(i, n int) string {
	if n <= 1 {
		return "0%"
	}
	return f(float64(i)/float64(n-1)*100) + "%"
}

// xLabelClass keeps edge labels inside the plot.
func xLabelClass(i, n int) string {
	switch {
	case n <= 1 || i == 0:
		return ""
	case i == n-1:
		return "-translate-x-full"
	}
	return "-translate-x-1/2"
}

// labelIndices picks count evenly spaced indices out of n, always
// including the first and last.
func labelIndices(n, count int) []int {
	if n == 0 {
		return nil
	}
	if count >= n || count < 2 {
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out
	}
	out := make([]int, 0, count)
	for k := 0; k < count; k++ {
		i := int(math.Round(float64(k) * float64(n-1) / float64(count-1)))
		if len(out) == 0 || out[len(out)-1] != i {
			out = append(out, i)
		}
	}
	return out
}

// region is the hover hit-area around point i: half a step either side,
// clipped at the edges so the first and last are half width.
type region struct {
	Left, Width string
	edge        string // "start", "center" or "end"
}

func regions(n int) []region {
	if n == 0 {
		return nil
	}
	if n == 1 {
		return []region{{"0%", "100%", "center"}}
	}
	step := 100 / float64(n-1)
	out := make([]region, n)
	for i := range out {
		switch i {
		case 0:
			out[i] = region{"0%", f(step/2) + "%", "start"}
		case n - 1:
			out[i] = region{f((float64(i)-0.5)*step) + "%", f(step/2) + "%", "end"}
		default:
			out[i] = region{f((float64(i)-0.5)*step) + "%", f(step) + "%", "center"}
		}
	}
	return out
}

// markerClass positions the hover line and dots at the point's x.
func (r region) markerClass() string {
	switch r.edge {
	case "start":
		return "left-0"
	case "end":
		return "left-full"
	}
	return "left-1/2"
}

// tooltipClass keeps the tooltip inside the plot at the edges.
func (r region) tooltipClass() string {
	switch r.edge {
	case "start":
		return "left-0"
	case "end":
		return "right-0"
	}
	return "left-1/2 -translate-x-1/2"
}

type arc struct {
	Label, Color, Dash, Offset string
}

// donutArcs turns slices into stroke-dasharray/offset pairs on one circle.
// thickness is in viewBox units (percent of the diameter).
func donutArcs(slices []Slice, thickness float64) (r string, arcs []arc) {
	radius := 50 - thickness/2
	circ := 2 * math.Pi * radius
	total := 0.0
	for _, s := range slices {
		total += s.Value
	}
	if total <= 0 {
		return f(radius), nil
	}
	gap := math.Min(1.5, circ/float64(len(slices))/4)
	start := 0.0
	for _, s := range slices {
		length := s.Value / total * circ
		visible := math.Max(0, length-gap)
		arcs = append(arcs, arc{
			Label:  fmt.Sprintf("%s: %s", s.Label, trimZero(s.Value/total*100, 1)+"%"),
			Color:  s.Color,
			Dash:   f(visible) + " " + f(circ-visible),
			Offset: f(-(start + gap/2)),
		})
		start += length
	}
	return f(radius), arcs
}

// Share is v as a percentage of total, e.g. "46.2%".
func Share(v, total float64) string {
	if total <= 0 {
		return "0%"
	}
	return trimZero(v/total*100, 1) + "%"
}
