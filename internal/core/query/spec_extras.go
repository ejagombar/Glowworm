package query

import (
	"fmt"
	"strings"

	"github.com/ejagombar/Glowworm/internal/core/widgets"
)

// SpecTarget is a reference line drawn across a line or bar chart: a
// savings target, a budget, a limit. It is either a fixed Value, in the chart's own units
// (minor units for money), or a SQL query whose rows are label, value (or
// just value), one line each, re-run with the widget so it stays current
// (e.g. SELECT 'Highest', MAX(balance_minor) FROM account_status).
type SpecTarget struct {
	Label string   `json:"label,omitempty"`
	Value *float64 `json:"value,omitempty"`
	SQL   string   `json:"sql,omitempty"`
	Color string   `json:"color,omitempty"`
}

// maxTargets caps the reference lines a chart draws.
const maxTargets = 6

// target is a reference line as drawn.
type target struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
	Color string  `json:"color"`
}

// resolved is what a spec's own queries said when it last ran: its target
// lines and any projection settings that come from a query.
type resolved struct {
	targets []target
}

// runner runs one of a spec's extra queries over the widget's window.
type runner func(sql string) (*QueryResult, error)

// resolve runs a spec's extra queries (target lines, a projection's rate
// and contribution) and returns the spec to shape: a copy with those filled
// in. The saved spec is left as it is.
func resolve(sp *Spec, run runner) (*Spec, error) {
	if len(sp.Targets) == 0 && (sp.Project == nil || (sp.Project.RateSQL == "" && sp.Project.ContributionSQL == "")) {
		return sp, nil
	}
	cp := *sp
	cp.extra = &resolved{}
	for i, t := range sp.Targets {
		color := t.Color
		if color == "" || !validHex(color) {
			color = widgets.PaletteAt(3 + i)
		}
		if strings.TrimSpace(t.SQL) == "" {
			if t.Value == nil {
				return nil, fmt.Errorf("target line %d has neither a value nor a query", i+1)
			}
			cp.extra.targets = append(cp.extra.targets, target{Label: t.Label, Value: *t.Value, Color: color})
			continue
		}
		res, err := run(t.SQL)
		if err != nil {
			return nil, fmt.Errorf("target line %d: %w", i+1, err)
		}
		for j, row := range res.Rows {
			label, cell := t.Label, row[0]
			if len(row) > 1 {
				label, cell = cellText(row[0]), row[1]
			}
			v, ok := cellNumber(cell)
			if !ok {
				continue
			}
			c := color
			if j > 0 {
				c = widgets.PaletteAt(3 + i + j)
			}
			cp.extra.targets = append(cp.extra.targets, target{Label: label, Value: v, Color: c})
		}
	}
	if len(cp.extra.targets) > maxTargets {
		cp.extra.targets = cp.extra.targets[:maxTargets]
	}
	if p := sp.Project; p != nil && (p.RateSQL != "" || p.ContributionSQL != "") {
		pc := *p
		cp.Project = &pc
		if p.RateSQL != "" {
			v, err := scalar(run, p.RateSQL)
			if err != nil {
				return nil, fmt.Errorf("projection rate: %w", err)
			}
			pc.Rate = v
		}
		if p.ContributionSQL != "" {
			v, err := scalar(run, p.ContributionSQL)
			if err != nil {
				return nil, fmt.Errorf("projection contribution: %w", err)
			}
			pc.Contribution = v
		}
	}
	return &cp, nil
}

// scalar runs a query for one number: its first row's first column. No
// row, or NULL, is 0.
func scalar(run runner, sql string) (float64, error) {
	res, err := run(sql)
	if err != nil {
		return 0, err
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return 0, nil
	}
	v, _ := cellNumber(res.Rows[0][0])
	return v, nil
}

func validHex(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

// shapeProgress shapes a progress widget: rows of label, value, target and
// optionally a colour, one bar each showing value against target.
func shapeProgress(w *widgets.Widget, sp *Spec, res *QueryResult, cur string) (*widgets.Widget, error) {
	if len(res.Columns) < 3 {
		return nil, fmt.Errorf("a progress widget needs three columns: label, value and target")
	}
	format := sp.format(res.Columns[1])
	items := []map[string]any{}
	for i, row := range res.Rows {
		if len(items) >= 20 {
			break
		}
		v, _ := cellNumber(row[1])
		t, _ := cellNumber(row[2])
		pct := 0.0
		if t > 0 {
			pct = v / t
		}
		color := widgets.PaletteAt(i)
		if len(row) > 3 {
			if c := cellText(row[3]); validHex(c) {
				color = c
			}
		}
		items = append(items, map[string]any{
			"label": cellText(row[0]), "value": v, "target": t, "progress": pct, "color": color,
			"value_text": formatCell(row[1], format, cur), "target_text": formatCell(row[2], format, cur),
		})
	}
	w.Data = map[string]any{"items": items, "caption": sp.Caption}
	return w, nil
}
