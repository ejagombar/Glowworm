package query

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"glowworm/internal/core/widgets"
	"glowworm/internal/money"
	"glowworm/internal/util"
)

// SpecVersion marks a saved widget plan as a Spec rather than a Plan.
const SpecVersion = 2

// Spec is a widget's saved recipe: one read-only query and how to show its
// result. The AI writes it on the Queries page; after that the app runs it on
// its own, so a pinned widget stays live without the AI.
//
// A query that uses :from and :to follows the date range the viewer has
// selected; one that names its own dates keeps them.
type Spec struct {
	V       int                   `json:"v"`
	SQL     string                `json:"sql"`
	Columns map[string]SpecColumn `json:"columns,omitempty"`
	Caption string                `json:"caption,omitempty"`
}

// SpecColumn says how to show one result column. Format is money (integer
// minor units), percent (a ratio: 0.25 is 25%), number, or text.
type SpecColumn struct {
	Label  string `json:"label,omitempty"`
	Format string `json:"format,omitempty"`
}

// UnmarshalJSON also takes a bare format string: "spend": "money".
func (c *SpecColumn) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		c.Format = s
		return nil
	}
	type plain SpecColumn
	return json.Unmarshal(b, (*plain)(c))
}

var dateParamRE = regexp.MustCompile(`:(from|to)\b`)

// UsesDates reports whether the query follows the selected date range.
func (sp *Spec) UsesDates() bool { return dateParamRE.MatchString(sp.SQL) }

// ParseSpec reads a saved plan, returning nil when it isn't a Spec.
func ParseSpec(planJSON string) *Spec {
	var sp Spec
	if json.Unmarshal([]byte(planJSON), &sp) != nil || sp.V != SpecVersion || strings.TrimSpace(sp.SQL) == "" {
		return nil
	}
	return &sp
}

// dateArgs binds :from and :to to the window, open ends reaching past any
// transaction, so "all time" and half-open ranges need no special SQL.
func dateArgs(query string, win *TimeWindow) []any {
	from, to := "0000-01-01", "9999-12-31"
	if win != nil {
		if win.From != "" {
			from = win.From
		}
		if win.To != "" {
			to = win.To
		}
	}
	var args []any
	used := map[string]bool{}
	for _, m := range dateParamRE.FindAllStringSubmatch(query, -1) {
		used[m[1]] = true
	}
	if used["from"] {
		args = append(args, sql.Named("from", from))
	}
	if used["to"] {
		args = append(args, sql.Named("to", to))
	}
	return args
}

func (sp *Spec) format(col string) string {
	if c, ok := sp.Columns[col]; ok && c.Format != "" {
		return strings.ToLower(c.Format)
	}
	return ""
}

func (sp *Spec) label(col string) string {
	if c, ok := sp.Columns[col]; ok && c.Label != "" {
		return c.Label
	}
	return col
}

// RunSpec runs the widget's query over win and shapes the result for its
// type. A number widget's caption gains the period it covers when the query
// follows the selected range.
func (s *Service) RunSpec(sp *Spec, typ string, win *TimeWindow) (*widgets.Widget, error) {
	w, _, err := s.runSpec(sp, typ, win)
	return w, err
}

func (s *Service) runSpec(sp *Spec, typ string, win *TimeWindow) (*widgets.Widget, *QueryResult, error) {
	res, err := RunReadonlyQuery(s.DB, sp.SQL, dateArgs(sp.SQL, win)...)
	if err != nil {
		return nil, nil, err
	}
	w, err := shapeSpec(sp, typ, win, res, s.currency())
	return w, res, err
}

func shapeSpec(sp *Spec, typ string, win *TimeWindow, res *QueryResult, cur string) (*widgets.Widget, error) {
	w := &widgets.Widget{ID: "w-" + util.NewID(), Type: typ, Plan: sp}
	switch typ {
	case "number":
		value := "—"
		if len(res.Rows) > 0 && len(res.Columns) > 0 {
			value = formatCell(res.Rows[0][0], sp.format(res.Columns[0]), cur)
		}
		caption := sp.Caption
		if sp.UsesDates() && win != nil && win.Label != "" {
			caption = strings.TrimPrefix(caption+" · "+win.Label, " · ")
		}
		w.Data = map[string]any{"value": value, "caption": caption}
	case "bar", "line":
		if len(res.Columns) < 2 {
			return nil, fmt.Errorf("a %s chart needs a label column and at least one value column", typ)
		}
		labels := make([]string, len(res.Rows))
		for i, row := range res.Rows {
			labels[i] = cellText(row[0])
		}
		var series []map[string]any
		for j := 1; j < len(res.Columns) && len(series) < 6; j++ {
			vals := make([]float64, len(res.Rows))
			for i, row := range res.Rows {
				vals[i], _ = cellNumber(row[j])
			}
			series = append(series, map[string]any{"name": sp.label(res.Columns[j]),
				"color": widgets.PaletteAt(j - 1), "values": vals})
		}
		w.Data = map[string]any{"labels": labels, "series": series, "format": chartFormat(sp.format(res.Columns[1]))}
	case "pie":
		if len(res.Columns) < 2 {
			return nil, fmt.Errorf("a pie chart needs a label column and a value column")
		}
		var labels []string
		var values []float64
		for _, row := range res.Rows {
			if len(labels) >= 12 {
				break
			}
			v, _ := cellNumber(row[1])
			labels = append(labels, cellText(row[0]))
			values = append(values, math.Max(v, 0))
		}
		w.Data = map[string]any{"labels": labels, "values": values, "format": chartFormat(sp.format(res.Columns[1]))}
	case "table":
		cols := make([]string, len(res.Columns))
		for j, c := range res.Columns {
			cols[j] = sp.label(c)
		}
		rows := [][]string{}
		for _, row := range res.Rows {
			if len(rows) >= 50 {
				break
			}
			cells := make([]string, len(row))
			for j, cell := range row {
				cells[j] = formatCell(cell, sp.format(res.Columns[j]), cur)
			}
			rows = append(rows, cells)
		}
		w.Data = map[string]any{"columns": cols, "rows": rows}
	case "transactions":
		idx := map[string]int{}
		for j, c := range res.Columns {
			idx[strings.ToLower(c)] = j
		}
		get := func(row []any, names ...string) any {
			for _, n := range names {
				if j, ok := idx[n]; ok {
					return row[j]
				}
			}
			return nil
		}
		txs := []map[string]any{}
		for _, row := range res.Rows {
			if len(txs) >= 50 {
				break
			}
			amount, _ := cellNumber(get(row, "amount_minor", "amount"))
			typ := cellText(get(row, "type"))
			if typ != "income" && typ != "transfer" {
				typ = "expense"
			}
			currency := cellText(get(row, "currency"))
			if currency == "" {
				currency = cur
			}
			txs = append(txs, map[string]any{
				"date":         truncateChars(cellText(get(row, "date")), 10),
				"description":  truncateChars(cellText(get(row, "description", "merchant")), 80),
				"category":     truncateChars(cellText(get(row, "category")), 40),
				"amount_minor": int64(amount),
				"currency":     currency,
				"type":         typ,
			})
		}
		w.Data = map[string]any{"transactions": txs}
	default:
		return nil, fmt.Errorf("unknown widget type %q", typ)
	}
	return w, nil
}

// checkSpec catches what would run but show wrongly: a numeric column with
// no format would print pence as pounds, or a ratio as 0.2.
func checkSpec(sp *Spec, typ string, res *QueryResult) error {
	if typ == "transactions" || res == nil || len(res.Rows) == 0 {
		return nil
	}
	first := 0
	if typ == "bar" || typ == "line" || typ == "pie" {
		first = 1
	}
	var missing []string
	for j := first; j < len(res.Columns); j++ {
		if _, ok := cellNumber(res.Rows[0][j]); ok && sp.format(res.Columns[j]) == "" {
			missing = append(missing, res.Columns[j])
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("give a format (money, percent or number) for column(s) %s", strings.Join(missing, ", "))
	}
	return nil
}

func chartFormat(f string) string {
	switch f {
	case "percent", "number":
		return f
	}
	return "money"
}

func formatCell(v any, format, currency string) string {
	if v == nil {
		return "—"
	}
	n, isNum := cellNumber(v)
	if !isNum {
		return cellText(v)
	}
	switch format {
	case "money":
		return money.Format(int64(math.Round(n)), currency)
	case "percent":
		return formatPercent(n)
	case "number":
		return formatNumber(n)
	}
	return cellText(v)
}

func formatPercent(ratio float64) string {
	p := ratio * 100
	if math.Abs(p) >= 10 {
		return strconv.FormatFloat(math.Round(p), 'f', 0, 64) + "%"
	}
	return strings.TrimSuffix(strconv.FormatFloat(math.Round(p*10)/10, 'f', 1, 64), ".0") + "%"
}

func formatNumber(n float64) string {
	if n == math.Trunc(n) && math.Abs(n) < 1e15 {
		neg := n < 0
		s := groupThousands(strconv.FormatInt(int64(math.Abs(n)), 10))
		if neg {
			return "-" + s
		}
		return s
	}
	return strconv.FormatFloat(math.Round(n*100)/100, 'f', -1, 64)
}

func groupThousands(digits string) string {
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return digits
}

func cellNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case int:
		return float64(n), true
	}
	return 0, false
}

func cellText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}
