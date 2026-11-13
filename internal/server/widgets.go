package server

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"glowworm/internal/core/periods"
	"glowworm/internal/core/query"
	"glowworm/internal/money"
	"glowworm/internal/util"
)

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

type WidgetView struct {
	ID            string        `json:"id"`
	Type          string        `json:"type"`
	Title         string        `json:"title"`
	Col           int           `json:"col"`
	Rows          int           `json:"rows"`
	DataJSON      string        `json:"data"`
	NumberValue   string        `json:"number_value,omitempty"`
	NumberCaption string        `json:"number_caption,omitempty"`
	TextBody      string        `json:"text_body,omitempty"`
	TableColumns  []string      `json:"table_columns,omitempty"`
	TableRows     [][]string    `json:"table_rows,omitempty"`
	TableLinks    []string      `json:"table_links,omitempty"`
	Transactions  []WidgetTxRow `json:"transactions,omitempty"`
	Stale         bool          `json:"stale"`
}

type WidgetTxRow struct {
	Date        string
	Description string
	Category    string
	Amount      string
	Class       string
}

// WidgetsList renders the pinned widgets, running each one's saved query
// again. One whose query uses :from and :to follows the date range selected
// in the masthead (period, with from/to for a custom range); one that names
// its own dates keeps them.
func (a *App) WidgetsList(period, from, to string) ([]*WidgetView, error) {
	rows, err := a.DB.Query(`SELECT id, position, col, rows, title, type, data, plan FROM pinned_widgets ORDER BY position ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*WidgetView
	var batch *query.SpecBatch
	if a.Query != nil {
		batch = a.Query.Batch(a.specWindow(period, from, to))
	}
	for rows.Next() {
		var id, title, typ, data string
		var position, col, rowsN int
		var plan sql.NullString
		if err := rows.Scan(&id, &position, &col, &rowsN, &title, &typ, &data, &plan); err != nil {
			return nil, err
		}
		view := &WidgetView{ID: id, Type: typ, Title: title, Col: spanOr(col, typ, defaultSpanCol),
			Rows: spanOr(rowsN, typ, defaultSpanRows)}
		stale := false
		if spec := query.ParseSpec(plan.String); spec != nil && batch != nil {
			if w, err := batch.Run(spec, typ); err == nil {
				if m, ok := w.Data.(map[string]any); ok && typ == "table" {
					tableRows, _ := m["rows"].([][]string)
					if links := a.tableRowLinks(spec.Links, tableRows); links != nil {
						m["links"] = links
					}
				}
				if b, err := json.Marshal(w.Data); err == nil {
					data = string(b)
				}
			} else {
				stale = true
			}
		}
		view.DataJSON = data
		view.Stale = stale
		applyPresentation(view, typ, data)
		out = append(out, view)
	}
	return out, rows.Err()
}

// specWindow is the selected date range for a widget query's :from and :to;
// all time leaves both open.
func (a *App) specWindow(period, from, to string) *query.TimeWindow {
	f, t, label := periods.Resolve(period, from, to, a.Clock.TodayTime())
	if period == periods.Custom && f != "" {
		label = shortDate(f) + " – " + shortDate(t)
	}
	return &query.TimeWindow{From: f, To: t, Label: label}
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// tableRowLinks points each row of a merchant, category or account breakdown
// at the transactions behind it: the Transactions page filtered to that row
// and the widget's type. It names no dates, so the page keeps the date range
// the user has selected. Rows it can't filter to (Uncategorised) get an empty
// link; a table that doesn't say what its rows are gets none at all.
func (a *App) tableRowLinks(l *query.SpecLinks, tableRows [][]string) []string {
	if l == nil || (l.By != "merchant" && l.By != "category" && l.By != "account") {
		return nil
	}
	ids := map[string]string{}
	if l.By == "category" || l.By == "account" {
		table := map[string]string{"category": "categories", "account": "accounts"}[l.By]
		rows, err := a.DB.Query(`SELECT id, name FROM ` + table + ` WHERE deleted_at IS NULL`)
		if err != nil {
			return nil
		}
		for rows.Next() {
			var id, name string
			if rows.Scan(&id, &name) == nil {
				ids[name] = id
			}
		}
		rows.Close()
	}
	links := make([]string, len(tableRows))
	for i, row := range tableRows {
		if len(row) == 0 {
			continue
		}
		label := row[0]
		v := url.Values{}
		if l.Type == "expense" || l.Type == "income" {
			v.Set("type", l.Type)
		}
		switch l.By {
		case "merchant":
			v.Set("q", label)
		case "category":
			id, ok := ids[label]
			if !ok {
				continue
			}
			v.Set("category", id)
		case "account":
			id, ok := ids[label]
			if !ok {
				continue
			}
			v.Set("account", id)
		}
		links[i] = "/transactions?" + v.Encode()
	}
	return links
}

func spanOr(v int, typ string, def func(string) int) int {
	if v > 0 {
		return v
	}
	return def(typ)
}

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
		if links, ok := data["links"].([]any); ok && len(links) == len(asSlice(data["rows"])) {
			for _, l := range links {
				v.TableLinks = append(v.TableLinks, str(l))
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
	case "transactions":
		if txs, ok := data["transactions"].([]any); ok {
			for _, t := range txs {
				tm, ok := t.(map[string]any)
				if !ok {
					continue
				}
				amount := 0
				switch n := tm["amount_minor"].(type) {
				case float64:
					amount = int(n)
				case string:
					if f, err := strconv.ParseFloat(strings.ReplaceAll(n, ",", ""), 64); err == nil {
						amount = int(f)
					}
				}
				cur := str(tm["currency"])
				if cur == "" {
					cur = "GBP"
				}
				cls := "neg"
				sign := "−"
				if amount >= 0 {
					cls = "pos"
					sign = "+"
				}
				if str(tm["type"]) == "transfer" {
					cls = "transfer"
					sign = "⇄ "
				}
				v.Transactions = append(v.Transactions, WidgetTxRow{
					Date:        dayMonth(str(tm["date"])),
					Description: str(tm["description"]),
					Category:    str(tm["category"]),
					Amount:      sign + money.Format(int64(abs64(int64(amount))), cur),
					Class:       cls,
				})
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

// refreshable says whether a widget can be re-run on the dashboard: it
// carries a saved query, or it's text, which has nothing to go out of date.
// Only these can be pinned, so the dashboard never shows a snapshot that
// quietly stops matching the data.
func refreshable(typ, planJSON string) bool {
	return typ == "text" || query.ParseSpec(planJSON) != nil
}

func (a *App) PinWidget(title, typ, dataJSON, planJSON string, col, rowsN int) (string, error) {
	if !refreshable(typ, planJSON) {
		return "", &badRequest{"This chart has no saved query, so it couldn't stay up to date. Ask the question again and pin the new answer."}
	}
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
	// Re-pinning returns the existing row: a page reload loses the client's
	// pinned id, which otherwise duplicated dashboard cards.
	var existing string
	err := a.DB.QueryRow(`SELECT id FROM pinned_widgets
		WHERE title = ? AND type = ? AND data = ? AND COALESCE(plan,'') = COALESCE(?, '') LIMIT 1`,
		title, typ, dataJSON, planJSON).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	_, err = a.DB.Exec(`INSERT INTO pinned_widgets (id, position, col, rows, title, type, data, plan, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		id, position, col, rowsN, title, typ, dataJSON, plan, util.NowStr(), util.NowStr())
	return id, err
}

func (a *App) WidgetPatch(id, title, typ, dataJSON string) error {
	var curTitle, curType, curData string
	err := a.DB.QueryRow(`SELECT title, type, data FROM pinned_widgets WHERE id = ?`, id).
		Scan(&curTitle, &curType, &curData)
	if err != nil {
		return err
	}
	if title == "" {
		title = curTitle
	}
	if typ == "" {
		typ = curType
	}
	if dataJSON == "" || dataJSON == "null" {
		dataJSON = curData
	}
	_, err = a.DB.Exec(`UPDATE pinned_widgets SET title=?, type=?, data=?, updated_at=? WHERE id=?`,
		title, typ, dataJSON, util.NowStr(), id)
	return err
}

func (a *App) WidgetUnpin(id string) error {
	_, err := a.DB.Exec(`DELETE FROM pinned_widgets WHERE id = ?`, id)
	return err
}

// The example widgets a fresh dashboard starts with (see SeedExamples).

// spendByCategorySQL covers the calendar month to date, rolling over with
// it, as the "— this month" titles say.
const spendByCategorySQL = `SELECT COALESCE(c.name, 'Uncategorised') AS category, SUM(-t.amount_minor) AS spend
FROM transactions t LEFT JOIN categories c ON c.id = t.category_id
WHERE t.deleted_at IS NULL AND t.type = 'expense'
  AND t.date BETWEEN date('now', 'start of month') AND date('now')
GROUP BY t.category_id ORDER BY spend DESC LIMIT 8`

// savingsRateSQL is the share of income not spent over the selected range.
// Transfers between the user's own accounts count as neither.
const savingsRateSQL = `SELECT CASE WHEN inc > 0 THEN (inc - spend) * 1.0 / inc END AS rate FROM (
  SELECT COALESCE(SUM(CASE WHEN t.type = 'income' THEN t.amount_minor END), 0) AS inc,
         COALESCE(SUM(CASE WHEN t.type = 'expense' THEN -t.amount_minor END), 0) AS spend
  FROM transactions t WHERE t.deleted_at IS NULL AND t.date BETWEEN :from AND :to)`

// topMerchantsSQL follows the selected date range.
const topMerchantsSQL = `SELECT COALESCE(NULLIF(t.merchant, ''), t.description) AS merchant, SUM(-t.amount_minor) AS spend
FROM transactions t
WHERE t.deleted_at IS NULL AND t.type = 'expense' AND t.date BETWEEN :from AND :to
GROUP BY 1 ORDER BY spend DESC LIMIT 5`

func seedSpec(sp query.Spec) string {
	sp.V = query.SpecVersion
	b, _ := json.Marshal(sp)
	return string(b)
}

func seedSpecSpendByCategory() string {
	return seedSpec(query.Spec{SQL: spendByCategorySQL,
		Columns: map[string]query.SpecColumn{"spend": {Label: "Spend", Format: "money"}}})
}

func seedSpecSavingsRate() string {
	return seedSpec(query.Spec{SQL: savingsRateSQL, Caption: "of income not spent",
		Columns: map[string]query.SpecColumn{"rate": {Format: "percent"}}})
}

func seedSpecTopMerchants() string {
	return seedSpec(query.Spec{SQL: topMerchantsSQL,
		Columns: map[string]query.SpecColumn{"merchant": {Label: "Merchant"}, "spend": {Label: "Spend", Format: "money"}},
		Links:   &query.SpecLinks{By: "merchant", Type: "expense"}})
}
