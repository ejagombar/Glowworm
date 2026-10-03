// Widgets service methods on App (doc 05 §7): pinned CRUD + dashboard render
// with plan re-execution.
package server

import (
	"database/sql"
	"encoding/json"

	"glowworm/internal/core/query"
	"glowworm/internal/core/widgets"
	"glowworm/internal/util"
)

// query_Plan aliases the query plan type.
type query_Plan = query.Plan

// defaultSpanCol mirrors the prototype spanCol.
func defaultSpanCol(t string) int {
	switch t {
	case "number", "text", "pie":
		return 4
	case "transactions":
		return 12
	default:
		return 8
	}
}

func defaultSpanRows(t string) int {
	switch t {
	case "number", "text":
		return 2
	case "pie":
		return 3
	default:
		return 4
	}
}

// WidgetView is the template projection of a pinned widget (doc 05 §2).
type WidgetView struct {
	ID            string     `json:"id"`
	Type          string     `json:"type"`
	Title         string     `json:"title"`
	Col           int        `json:"col"`
	Rows          int        `json:"rows"`
	DataJSON      string     `json:"data"`
	DatasetJSON   string     `json:"dataset"`
	NumberValue   string     `json:"number_value,omitempty"`
	NumberCaption string     `json:"number_caption,omitempty"`
	TextBody      string     `json:"text_body,omitempty"`
	TableColumns  []string   `json:"table_columns,omitempty"`
	TableRows     [][]string `json:"table_rows,omitempty"`
	Stale         bool       `json:"stale"`
}

// WidgetsList returns dashboard widgets with fresh data (plans re-executed).
func (a *App) WidgetsList() ([]*WidgetView, error) {
	rows, err := a.DB.Query(`SELECT id, position, col, rows, title, type, dataset, plan FROM pinned_widgets ORDER BY position ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*WidgetView
	for rows.Next() {
		var id, title, typ, dataset string
		var position, col, rowsN int
		var plan sql.NullString
		if err := rows.Scan(&id, &position, &col, &rowsN, &title, &typ, &dataset, &plan); err != nil {
			return nil, err
		}
		view := &WidgetView{ID: id, Type: typ, Title: title, Col: spanOr(col, typ, defaultSpanCol),
			Rows: spanOr(rowsN, typ, defaultSpanRows), DatasetJSON: dataset}
		data := dataset
		stale := false
		if plan.Valid && plan.String != "" && a.Query != nil {
			var p query_Plan
			if json.Unmarshal([]byte(plan.String), &p) == nil {
				if ds, _, err := a.Query.CompilePublic(&p); err == nil && ds != nil {
					w := widgets.Derive(typ, ds, title)
					if b, err := json.Marshal(w.Data); err == nil {
						data = string(b)
					}
					if b, err := json.Marshal(ds); err == nil {
						view.DatasetJSON = string(b)
					}
				} else {
					stale = true
				}
			}
		}
		view.DataJSON = data
		view.Stale = stale
		applyPresentation(view, typ, data)
		out = append(out, view)
	}
	return out, rows.Err()
}

func spanOr(v int, typ string, def func(string) int) int {
	if v > 0 {
		return v
	}
	return def(typ)
}

// applyPresentation extracts the template-friendly fields from the data JSON.
func applyPresentation(v *WidgetView, typ, dataJSON string) {
	var data map[string]any
	if json.Unmarshal([]byte(dataJSON), &data) != nil {
		return
	}
	switch typ {
	case "number":
		if s, ok := data["value"].(string); ok {
			v.NumberValue = s
		}
		if s, ok := data["caption"].(string); ok {
			v.NumberCaption = s
		}
	case "text":
		if s, ok := data["body"].(string); ok {
			v.TextBody = s
		}
	case "table":
		if cols, ok := data["columns"].([]any); ok {
			for _, c := range cols {
				v.TableColumns = append(v.TableColumns, str(c))
			}
		}
		if rows, ok := data["rows"].([]any); ok {
			for _, r := range rows {
				if cells, ok := r.([]any); ok {
					var out []string
					for _, c := range cells {
						out = append(out, str(c))
					}
					v.TableRows = append(v.TableRows, out)
				}
			}
		}
	}
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// PinWidget persists a pinned widget (from chat).
func (a *App) PinWidget(title, typ, datasetJSON, planJSON string, col, rowsN int) (string, error) {
	id := util.NewID()
	if col == 0 {
		col = defaultSpanCol(typ)
	}
	if rowsN == 0 {
		rowsN = defaultSpanRows(typ)
	}
	var position int
	_ = a.DB.QueryRow(`SELECT COALESCE(MAX(position),0)+1 FROM pinned_widgets`).Scan(&position)
	var plan any
	if planJSON != "" && planJSON != "null" {
		plan = planJSON
	}
	_, err := a.DB.Exec(`INSERT INTO pinned_widgets (id, position, col, rows, title, type, dataset, plan, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		id, position, col, rowsN, title, typ, datasetJSON, plan, util.NowStr(), util.NowStr())
	return id, err
}

// WidgetPatch updates title/type and re-derives data from the dataset [D13].
func (a *App) WidgetPatch(id, title, typ, datasetJSON string) error {
	var curTitle, curType, dataset string
	err := a.DB.QueryRow(`SELECT title, type, COALESCE(dataset,'') FROM pinned_widgets WHERE id = ?`, id).
		Scan(&curTitle, &curType, &dataset)
	if err != nil {
		return err
	}
	newTitle := title
	if newTitle == "" {
		newTitle = curTitle
	}
	newType := typ
	if newType == "" {
		newType = curType
	}
	newDataset := dataset
	if datasetJSON != "" && datasetJSON != "null" {
		newDataset = datasetJSON
	}
	var dataJSON string
	var ds *widgets.Dataset
	if json.Unmarshal([]byte(newDataset), &ds) == nil && ds != nil {
		w := widgets.Derive(newType, ds, newTitle)
		dataJSON = w.DataJSON()
	} else {
		dataJSON = newDataset
	}
	_, err = a.DB.Exec(`UPDATE pinned_widgets SET title=?, type=?, dataset=?, updated_at=? WHERE id=?`,
		newTitle, newType, newDataset, util.NowStr(), id)
	if err != nil {
		return err
	}
	_ = dataJSON
	return nil
}

// WidgetUnpin removes a pinned widget.
func (a *App) WidgetUnpin(id string) error {
	_, err := a.DB.Exec(`DELETE FROM pinned_widgets WHERE id = ?`, id)
	return err
}

// SeedDefaultWidgets creates the prototype's default dashboard (doc 10 §11)
// once, on setup completion.
func (a *App) SeedDefaultWidgets() error {
	var n int
	if err := a.DB.QueryRow(`SELECT COUNT(*) FROM pinned_widgets`).Scan(&n); err != nil || n > 0 {
		return err
	}
	var cc string
	_ = a.DB.QueryRow(`SELECT value FROM settings WHERE key='currency'`).Scan(&cc)
	cc = trim2(cc)
	if cc == "" {
		cc = "GBP"
	}
	from, to, _ := monthRangeToday(a.Clock)
	seeds := []struct {
		title, typ, plan string
		col, rows        int
	}{
		{"Spending by category — this month", "bar", seedPlan("spend", "category", from, to), 8, 4},
		{"Savings rate", "number", seedPlanNumber(), 4, 2},
		{"Spending mix — this month", "pie", seedPlan("spend", "category", from, to), 4, 3},
		{"Top merchants", "table", seedPlanTopMerchants(from, to), 8, 4},
	}
	for i, s := range seeds {
		id := util.NewID()
		created := util.NowStr()
		_, err := a.DB.Exec(`INSERT INTO pinned_widgets (id, position, col, rows, title, type, dataset, plan, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, i, s.col, s.rows, s.title, s.typ, "{}", s.plan, created, created)
		if err != nil {
			return err
		}
	}
	return nil
}

func trim2(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

func monthRangeToday(c interface{ Today() string }) (string, string, bool) {
	today := c.Today()
	from := today[:8] + "01"
	return from, today, true
}

func seedPlan(metric, breakdown, from, to string) string {
	b, _ := json.Marshal(query_Plan{
		V: 1, Metric: metric, Breakdown: breakdown, From: from, To: to,
		AccountScope: "all", Limit: 8,
		Title: "Spending by category — this month",
	})
	return string(b)
}

func seedPlanNumber() string {
	b, _ := json.Marshal(query_Plan{
		V: 1, Metric: "spend", Breakdown: "none",
		AccountScope: "all", Limit: 1, ChartHint: "number", Title: "Savings rate",
	})
	return string(b)
}

func seedPlanTopMerchants(from, to string) string {
	b, _ := json.Marshal(query_Plan{
		V: 1, Metric: "spend", Breakdown: "merchant", From: from, To: to,
		AccountScope: "all", Limit: 5, ChartHint: "table", Title: "Top merchants",
	})
	return string(b)
}
