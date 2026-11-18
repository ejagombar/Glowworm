package query

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SpecProjection carries a time series on past its last point: a line or
// bar chart gains projected points after its own, drawn apart from the
// real ones, and a number widget shows where its series ends up. The x
// labels must be dates: YYYY, YYYY-MM or YYYY-MM-DD, evenly spaced.
//
// Methods:
//   - trend (the default): the slope of a straight line fitted to the
//     points, carried on from the last one.
//   - average: the average change per period, from the first point used
//     to the last, carried on.
//   - compound: from the last point, each period grows by Rate (a yearly
//     ratio, 0.04 for 4%) and adds Contribution (minor units a month),
//     both spread over the period's length.
type SpecProjection struct {
	Method string `json:"method,omitempty"`
	// How far: Until, a date (YYYY, YYYY-MM or YYYY-MM-DD) or a span past
	// the last point ("+5y", "+18m", "+12w", "+30d"); else Periods; else 12
	// periods.
	Until   string `json:"until,omitempty"`
	Periods int    `json:"periods,omitempty"`
	// Basis fits trend and average to the last Basis points only (all of
	// them when 0), so an old pattern doesn't drag on a new one.
	Basis        int     `json:"basis,omitempty"`
	Rate         float64 `json:"rate,omitempty"`
	Contribution float64 `json:"contribution,omitempty"`
	// Series names the series to project; all of them when empty.
	Series []string `json:"series,omitempty"`
}

// maxProjected caps how many periods a projection adds.
const maxProjected = 600

var untilSpanRE = regexp.MustCompile(`^\+(\d+)\s*([dwmy])$`)

func (p *SpecProjection) method() string {
	switch m := strings.ToLower(strings.TrimSpace(p.Method)); m {
	case "average", "compound":
		return m
	}
	return "trend"
}

// check reports a projection that can't work whatever the data.
func (p *SpecProjection) check() error {
	if p == nil {
		return nil
	}
	switch m := strings.ToLower(strings.TrimSpace(p.Method)); m {
	case "", "trend", "average", "compound":
	default:
		return fmt.Errorf("projection method %q isn't one of trend, average or compound", p.Method)
	}
	if p.Periods < 0 || p.Basis < 0 {
		return fmt.Errorf("projection periods and basis can't be negative")
	}
	if u := strings.TrimSpace(p.Until); u != "" && !untilSpanRE.MatchString(strings.ToLower(u)) {
		if _, _, ok := parseLabelDate(u); !ok {
			return fmt.Errorf("projection until %q is neither a date (YYYY, YYYY-MM, YYYY-MM-DD) nor a span like +5y", p.Until)
		}
	}
	if p.Rate <= -1 || p.Rate > 10 {
		return fmt.Errorf("projection rate is a yearly ratio (0.04 for 4%%)")
	}
	return nil
}

// parseLabelDate reads a date label, returning its date and its form:
// "year", "month" or "day".
func parseLabelDate(s string) (time.Time, string, bool) {
	s = strings.TrimSpace(s)
	for _, f := range []struct{ layout, kind string }{
		{"2006-01-02", "day"}, {"2006-01", "month"}, {"2006", "year"},
	} {
		if len(s) == len(f.layout) {
			if t, err := time.Parse(f.layout, s); err == nil {
				return t, f.kind, true
			}
		}
	}
	return time.Time{}, "", false
}

// labelStep works out the series' spacing from its last two labels.
type labelStep struct {
	kind  string // year, month, week or day
	days  int    // for day labels: the gap, 1 or 7 (or any)
	last  time.Time
	shape string // the label's layout
}

func stepOf(labels []string) (labelStep, error) {
	errDates := fmt.Errorf("a projection needs dates on the x axis (YYYY, YYYY-MM or YYYY-MM-DD)")
	if len(labels) == 0 {
		return labelStep{}, errDates
	}
	last, kind, ok := parseLabelDate(labels[len(labels)-1])
	if !ok {
		return labelStep{}, errDates
	}
	st := labelStep{kind: kind, last: last}
	switch kind {
	case "year":
		st.shape = "2006"
	case "month":
		st.shape = "2006-01"
	default:
		st.shape = "2006-01-02"
		st.days = 1
		if len(labels) > 1 {
			prev, pk, ok := parseLabelDate(labels[len(labels)-2])
			if !ok || pk != "day" {
				return labelStep{}, errDates
			}
			if gap := int(math.Round(last.Sub(prev).Hours() / 24)); gap > 0 {
				st.days = gap
			}
		}
		switch {
		case st.days == 7:
			st.kind = "week"
		case st.days >= 28 && st.days <= 31:
			st.kind = "month"
		case st.days >= 365 && st.days <= 366:
			st.kind = "year"
		}
	}
	return st, nil
}

func (st labelStep) at(k int) time.Time {
	switch {
	case st.shape == "2006":
		return st.last.AddDate(k, 0, 0)
	case st.shape == "2006-01":
		return st.last.AddDate(0, k, 0)
	case st.kind == "month":
		return st.last.AddDate(0, k, 0)
	case st.kind == "year":
		return st.last.AddDate(k, 0, 0)
	}
	return st.last.AddDate(0, 0, k*st.days)
}

// perYear is how many of the series' periods make a year.
func (st labelStep) perYear() float64 {
	switch st.kind {
	case "year":
		return 1
	case "month":
		return 12
	}
	return 365.25 / float64(st.days)
}

// futureLabels are the labels the projection adds after the last one.
func (p *SpecProjection) futureLabels(st labelStep) []string {
	n := p.Periods
	var until time.Time
	if u := strings.ToLower(strings.TrimSpace(p.Until)); u != "" {
		if m := untilSpanRE.FindStringSubmatch(u); m != nil {
			k, _ := strconv.Atoi(m[1])
			switch m[2] {
			case "d":
				until = st.last.AddDate(0, 0, k)
			case "w":
				until = st.last.AddDate(0, 0, 7*k)
			case "m":
				until = st.last.AddDate(0, k, 0)
			default:
				until = st.last.AddDate(k, 0, 0)
			}
		} else if t, _, ok := parseLabelDate(u); ok {
			until = t
		}
	}
	if until.IsZero() && n <= 0 {
		n = 12
	}
	var out []string
	for k := 1; k <= maxProjected; k++ {
		t := st.at(k)
		if !until.IsZero() {
			if t.After(until) {
				break
			}
		} else if k > n {
			break
		}
		out = append(out, t.Format(st.shape))
	}
	return out
}

// projectValues carries vals on for n periods by the projection's method.
// It returns nil when the series has too few points to go on.
func (p *SpecProjection) projectValues(vals []*float64, n int, st labelStep) []float64 {
	var pts []float64
	for _, v := range vals {
		if v != nil {
			pts = append(pts, *v)
		}
	}
	if len(pts) == 0 || n == 0 {
		return nil
	}
	method := p.method()
	if p.Basis > 0 && len(pts) > p.Basis {
		pts = pts[len(pts)-p.Basis:]
	}
	last := pts[len(pts)-1]
	out := make([]float64, n)
	switch method {
	case "compound":
		py := st.perYear()
		r := math.Pow(1+p.Rate, 1/py) - 1
		c := p.Contribution * 12 / py
		v := last
		for k := range out {
			v = v*(1+r) + c
			out[k] = v
		}
		return out
	case "average":
		if len(pts) < 2 {
			return nil
		}
		step := (last - pts[0]) / float64(len(pts)-1)
		for k := range out {
			out[k] = last + step*float64(k+1)
		}
		return out
	}
	if len(pts) < 2 {
		return nil
	}
	slope := fitSlope(pts)
	for k := range out {
		out[k] = last + slope*float64(k+1)
	}
	return out
}

// fitSlope is the least-squares slope of ys over 0, 1, 2, ...
func fitSlope(ys []float64) float64 {
	n := float64(len(ys))
	var sx, sy, sxx, sxy float64
	for i, y := range ys {
		x := float64(i)
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0
	}
	return (n*sxy - sx*sy) / den
}

func (p *SpecProjection) wants(name string) bool {
	if len(p.Series) == 0 {
		return true
	}
	for _, s := range p.Series {
		if strings.EqualFold(strings.TrimSpace(s), name) {
			return true
		}
	}
	return false
}

func valuesPtrs(v any) []*float64 {
	switch vs := v.(type) {
	case []*float64:
		return vs
	case []float64:
		out := make([]*float64, len(vs))
		for i := range vs {
			out[i] = &vs[i]
		}
		return out
	}
	return nil
}

// projectChart adds the projection to a bar or line chart: labels past the
// last, and for each projected series a series of its own, marked
// projected, in the same colour. A line's projected series starts at the
// last real point so the two join up; real series hold nothing past it.
func (p *SpecProjection) projectChart(typ string, labels []string, series []map[string]any) ([]string, []map[string]any, error) {
	if len(labels) == 0 {
		return labels, series, nil // nothing yet to go on
	}
	st, err := stepOf(labels)
	if err != nil {
		return nil, nil, err
	}
	future := p.futureLabels(st)
	if len(future) == 0 {
		return labels, series, nil
	}
	n, total := len(labels), len(labels)+len(future)
	var out, added []map[string]any
	for _, s := range series {
		vals := valuesPtrs(s["values"])
		ext := make([]*float64, total)
		copy(ext, vals)
		s["values"] = ext
		out = append(out, s)
		name, _ := s["name"].(string)
		if !p.wants(name) {
			continue
		}
		proj := p.projectValues(vals, len(future), st)
		if proj == nil {
			continue
		}
		pv := make([]*float64, total)
		if typ == "line" {
			for i := n - 1; i >= 0; i-- {
				if i < len(vals) && vals[i] != nil {
					v := *vals[i]
					pv[n-1] = &v
					break
				}
			}
		}
		for k := range proj {
			v := math.Round(proj[k]*1e6) / 1e6
			pv[n+k] = &v
		}
		added = append(added, map[string]any{"name": name + " (projected)", "color": s["color"], "values": pv, "projected": true})
	}
	return append(labels, future...), append(out, added...), nil
}

// projectNumber is where a series (label, value rows) ends up: the last
// projected value and its label.
func (p *SpecProjection) projectNumber(labels []string, vals []*float64) (float64, string, error) {
	st, err := stepOf(labels)
	if err != nil {
		return 0, "", err
	}
	future := p.futureLabels(st)
	if len(future) == 0 {
		return 0, "", fmt.Errorf("the projection ends before the series does")
	}
	proj := p.projectValues(vals, len(future), st)
	if proj == nil {
		return 0, "", fmt.Errorf("a projection needs at least two points with values")
	}
	return proj[len(proj)-1], future[len(future)-1], nil
}
