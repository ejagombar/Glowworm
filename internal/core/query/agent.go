package query

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"glowworm/internal/ai"
	"glowworm/internal/core/widgets"
	"glowworm/internal/util"
)

const (
	agentMaxRounds     = 6
	agentMaxWidgets    = 3
	agentMaxTranscript = 8000
	agentCallTimeout   = 90 * time.Second
)

type agentWidgetSpec struct {
	Type  string          `json:"type"`
	Title string          `json:"title"`
	Data  json.RawMessage `json:"data"`
}

type agentAction struct {
	Action  string            `json:"action"`
	SQL     string            `json:"sql"`
	Text    string            `json:"text"`
	Widgets []agentWidgetSpec `json:"widgets"`
}

var errNoContent = errors.New("agent: empty final answer")

func parseAction(resp string) (agentAction, error) {
	var act struct {
		Action  string            `json:"action"`
		SQL     string            `json:"sql"`
		Text    string            `json:"text"`
		Widgets []json.RawMessage `json:"widgets"`
	}
	if err := json.Unmarshal([]byte(resp), &act); err != nil {
		return agentAction{}, err
	}
	out := agentAction{Action: act.Action, SQL: act.SQL, Text: act.Text}
	for _, raw := range act.Widgets {
		out.Widgets = append(out.Widgets, normalizeSpec(raw))
	}
	return out, nil
}

var widgetPayloadKeys = []string{
	"transactions", "labels", "series", "values", "colors",
	"columns", "rows", "value", "caption", "body",
}

func normalizeSpec(raw json.RawMessage) agentWidgetSpec {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return agentWidgetSpec{Type: "?"}
	}
	sp := agentWidgetSpec{Type: rawStr(m["type"]), Title: rawStr(m["title"])}
	if d, ok := m["data"]; ok {
		sp.Data = d
	}
	if sp.Data != nil && strings.EqualFold(sp.Type, "transactions") {
		var arr []json.RawMessage
		if json.Unmarshal(sp.Data, &arr) == nil {
			b, _ := json.Marshal(map[string]any{"transactions": arr})
			sp.Data = b
			return sp
		}
	}
	if sp.Data != nil && strings.EqualFold(sp.Type, "pie") {
		var arr []map[string]any
		if json.Unmarshal(sp.Data, &arr) == nil && len(arr) > 0 {
			var labels []string
			var values []int64
			ok := true
			for _, it := range arr {
				l, lOk := it["label"].(string)
				v, vOk := toMinorUnits(it["value"])
				if !lOk || !vOk {
					ok = false
					break
				}
				labels = append(labels, l)
				values = append(values, v)
			}
			if ok {
				b, _ := json.Marshal(map[string]any{"labels": labels, "values": values})
				sp.Data = b
				return sp
			}
		}
	}
	var dm map[string]json.RawMessage
	if sp.Data != nil {
		_ = json.Unmarshal(sp.Data, &dm)
	}
	if dm == nil {
		dm = map[string]json.RawMessage{}
	}
	merged := false
	for _, k := range widgetPayloadKeys {
		if _, ok := dm[k]; !ok {
			if v, ok := m[k]; ok {
				dm[k] = v
				merged = true
			}
		}
	}
	if sp.Data == nil || merged {
		b, _ := json.Marshal(dm)
		sp.Data = b
	}
	return sp
}

func rawStr(v json.RawMessage) string {
	if len(v) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		return strings.TrimSpace(s)
	}
	return ""
}

func (s *Service) agentSystemPrompt(win *TimeWindow) string {
	cats, _ := s.nameList(`SELECT name FROM categories WHERE deleted_at IS NULL AND is_system = 0`)
	accounts, _ := s.nameList(`SELECT name FROM accounts WHERE deleted_at IS NULL`)
	merchants, _ := s.nameList(`SELECT COALESCE(NULLIF(merchant,''), description) FROM transactions
		WHERE deleted_at IS NULL GROUP BY 1 ORDER BY COUNT(*) DESC LIMIT 20`)
	var b strings.Builder
	b.WriteString(`You are Glowworm's query interface: a machine that answers
personal-finance questions over one user's SQLite database. You respond ONLY
with JSON, one object per turn.

TONE: impersonal and efficient. State facts and figures only — no filler, no
greetings, no chit-chat, no first person. Never suggest the user work
something out themselves, never offer or ask follow-up questions, and never
propose asking something else; if more detail is needed, fetch it yourself
with a query.

DATABASE (SQLite). Tables you may query:
- transactions(id, date TEXT 'YYYY-MM-DD', description, merchant, amount_minor INTEGER
  [negative = expense, positive = income, in minor units e.g. pence], type TEXT
  'expense'|'income'|'transfer', category_id, account_id, currency, deleted_at)
  IMPORTANT: merchant is frequently empty — the payee is usually in
  description. When the user names a merchant/payee/shop, match
  COALESCE(NULLIF(merchant,''), description), never merchant alone.
- categories(id, name)
- accounts(id, name, type TEXT 'debit'|'credit'|'savings', opening_balance_minor, opening_balance_date)
Rules: ignore rows with deleted_at NOT NULL. "Spend" = SUM(-amount_minor) over type='expense'
(transfers are never spend or income). Balance(account) = opening_balance_minor +
SUM(amount_minor) of its transactions (only those after opening_balance_date when set).

`)
	b.WriteString(fmt.Sprintf("USER'S CATEGORIES: %s\n", joinOrNone(cats)))
	b.WriteString(fmt.Sprintf("USER'S ACCOUNTS: %s\n", joinOrNone(accounts)))
	b.WriteString(fmt.Sprintf("TOP MERCHANTS: %s\n", joinOrNone(merchants)))
	b.WriteString(fmt.Sprintf("Today is %s. Currency is %s.\n\n", s.Clock.Today(), s.currency()))
	if win != nil && (win.From != "" || win.To != "") {
		rangeDesc := win.From + " to " + win.To
		if win.From == "" {
			rangeDesc = "up to " + win.To
		} else if win.To == "" {
			rangeDesc = "from " + win.From
		}
		b.WriteString(fmt.Sprintf("CURRENT VIEW PERIOD: the user is browsing %s (%s). Unless the\nquestion names a different date range (\"this month\", \"last year\", a date,\netc.), restrict every query and widget to this period and mention the period\nin the text.\n\n", win.Label, rangeDesc))
	}

	b.WriteString(`WIDGET POLICY (default ON): embed one widget that visualises the answer
unless the question is purely conversational. Choose by question shape:
- change over time ("how has X changed", "over the year") → line chart,
  labels = months/weeks/days in chronological order
- breakdown of an amount ("where did my money go") → bar or pie
- a single value ("what's my balance", "how much did I spend on X",
  "in total") → ALWAYS a number widget. This is mandatory: a single-figure
  answer with no number widget is wrong.
- a few itemised rows → table
- list/recent-transaction questions ("list my recent transactions",
  "show my last expenses") → transactions widget. NEVER enumerate the
  transactions in the text; the widget alone answers, so "text" may be an
  empty string (or one short sentence at most) — but ONLY in this case: for
  every other question "text" must be non-empty. A final with empty text
  and no widgets is always wrong.
The text is 1-3 short factual sentences citing the key figures; the widget
carries the detail.
EXAMPLE FINAL (single total — number widget is mandatory):
  {"action":"final","text":"£25.50 at Virgin Trains across 2 transactions, Jun 22-30.",
   "widgets":[{"type":"number","title":"Virgin Trains spend","data":{"value":"£25.50","caption":"2 transactions · Jun 2026"}}]}
EXAMPLE FINAL (list question — transactions widget, empty text):
  {"action":"final","text":"",
   "widgets":[{"type":"transactions","title":"Recent transactions",
    "data":{"transactions":[{"date":"2024-06-30","description":"Rent","category":"Bills & Utilities","amount_minor":-106266,"currency":"GBP","type":"expense"}]}}]}

EXAMPLE QUERIES (verified against this schema — prefer these patterns):
- Current balance per account:
  SELECT a.name, a.opening_balance_minor + COALESCE((SELECT SUM(t.amount_minor)
    FROM transactions t WHERE t.account_id = a.id AND t.deleted_at IS NULL
    AND (a.opening_balance_date IS NULL OR a.opening_balance_date = '' OR t.date > a.opening_balance_date)), 0)
    AS balance FROM accounts a WHERE a.deleted_at IS NULL
- Balance at each month end (a series, for line charts):
  WITH months AS (SELECT DISTINCT strftime('%Y-%m', date) AS m FROM transactions WHERE deleted_at IS NULL)
  SELECT m.m AS month, (SELECT COALESCE(SUM(opening_balance_minor),0) FROM accounts WHERE deleted_at IS NULL)
    + COALESCE(SUM(t.amount_minor), 0) AS balance
  FROM months m LEFT JOIN transactions t ON t.deleted_at IS NULL AND strftime('%Y-%m', t.date) <= m.m
  GROUP BY m.m ORDER BY m.m
- Spend by category in a range:
  SELECT c.name AS category, SUM(-t.amount_minor) AS spend FROM transactions t
    LEFT JOIN categories c ON c.id = t.category_id
    WHERE t.deleted_at IS NULL AND t.type = 'expense' AND t.date BETWEEN '2026-01-01' AND '2026-06-30'
    GROUP BY 1 ORDER BY spend DESC
- Spend at a named merchant/payee (description fallback — merchant is often empty):
  SELECT SUM(-t.amount_minor) AS spend, COUNT(*) AS n, MIN(t.date) AS first, MAX(t.date) AS last
    FROM transactions t
    WHERE t.deleted_at IS NULL AND t.type = 'expense'
    AND COALESCE(NULLIF(t.merchant,''), t.description) LIKE '%Virgin Trains%'

PROTOCOL — every reply is ONE JSON object, no prose outside it:
1. To fetch data: {"action":"run_query","sql":"SELECT ..."}
   - If a query errors, fix it and retry; if it returns 0 rows, do not conclude
     there is no data: first check what the data covers (SELECT MIN(date),
     MAX(date) FROM transactions WHERE deleted_at IS NULL) and re-ask over the
     actual range; when matching by name (merchant/category), retry with a
     substring search — LIKE '%name%' on
     COALESCE(NULLIF(merchant,''), description) — before concluding it is absent.
   - Single read-only SELECT statement only (no INSERT/UPDATE/DELETE/PRAGMA; no semicolons).
   - Always aggregate/filter in SQL, add a LIMIT (<= 200), never dump raw rows.
   - You receive the result as a JSON object {columns, rows, row_count, truncated}.
2. When you can answer: {"action":"final","text":"...","widgets":[...]}
   - "text": short, factual, impersonal. Quote amounts in major units
     (e.g. £128.40). Reference the numbers you actually found; never invent figures.
   - "widgets": 0-3 charts/tables that visualise the answer. Each is
     {"type": ..., "title": ..., "data": {...}} with type one of:
       bar/line: {"labels":["2024-01",...], "series":[{"name":"Spend","values":[12840, 9900]}]}
                 (labels in chronological order; values are integer MINOR units)
       pie:      {"labels":["Food",...], "values":[12000,...]} (integer minor units, all >= 0)
       number:   {"value":"£128.40","caption":"on Transport this year"} (display strings)
       table:    {"columns":["Month","Total"], "rows":[["2024-01","£128.40"]]} (display strings)
       transactions: {"transactions":[{"date":"2024-06-30","description":"Rent",
                     "category":"Bills & Utilities","amount_minor":-106266,
                     "currency":"GBP","type":"expense"}]} (newest first; type
                     is expense|income|transfer; amount_minor is signed)
   - Use at most one widget unless the question clearly needs more.
3. If the user names something that isn't in their categories/accounts/merchants,
   say which of their categories/accounts come closest — as a plain statement,
   not an offer to try again.
Prefer at most 3 queries (range/coverage checks don't count).
Resolve ambiguity yourself from the catalog; do not ask the user questions
unless truly impossible. Never say there is no financial data without having
run at least one query over the data's actual date coverage.`)
	return b.String()
}

func joinOrNone(list []string) string {
	if len(list) == 0 {
		return "(none)"
	}
	return strings.Join(list, ", ")
}

func (s *Service) AskAgent(question string, onStep func(string)) (*Answer, error) {
	return s.AskAgentWindow(question, nil, onStep)
}

func (s *Service) AskAgentWindow(question string, win *TimeWindow, onStep func(string)) (*Answer, error) {
	system := s.agentSystemPrompt(win)
	msgs := []ai.ChatMessage{{Role: "user", Content: question}}
	var callStart time.Time
	logCall := func(usage *ai.Usage, err error) {
		s.logCall("query_agent", callStart, usage, err)
	}
	var steps []string
	emit := func(step string) {
		steps = append(steps, step)
		if onStep != nil {
			onStep(step)
		}
	}
	invalidFeedback := `Invalid reply. Respond with ONE JSON object only: {"action":"run_query","sql":"SELECT ..."} to run a query, or {"action":"final","text":"...","widgets":[]} to answer.`
	emptyFinalFeedback := `Your final action had no content: "text" must be non-empty, or at least one valid widget must be present. Reply with the final JSON again.`
	emptyFinalRetried := false

	for round := 0; round < agentMaxRounds; round++ {
		callStart = time.Now()
		resp, usage, err := s.Provider.CompleteStructured(&ai.StructuredRequest{
			Capability: "query_agent",
			System:     system,
			Messages:   msgs,
			MaxTokens:  4000,
			Timeout:    agentCallTimeout,
		})
		logCall(usage, err)
		if err != nil {
			return nil, err
		}
		var act agentAction
		act, perr := parseAction(resp)
		if perr != nil || act.Action == "" {
			msgs = append(msgs,
				ai.ChatMessage{Role: "assistant", Content: resp},
				ai.ChatMessage{Role: "user", Content: invalidFeedback})
			continue
		}
		switch act.Action {
		case "run_query":
			msgs = append(msgs, ai.ChatMessage{Role: "assistant", Content: resp})
			sql := stripFences(act.SQL)
			if strings.TrimSpace(sql) == "" {
				msgs = append(msgs, ai.ChatMessage{Role: "user", Content: "TOOL RESULT: " + invalidFeedback})
				continue
			}
			res, qerr := RunReadonlyQuery(s.DB, sql)
			if qerr != nil {
				emit(truncateChars(fmt.Sprintf("Query failed (%s): %s", qerr.Error(), oneLine(sql, 120)), 260))
				msgs = append(msgs, ai.ChatMessage{Role: "user", Content: "TOOL RESULT: " + truncateChars("error: "+qerr.Error(), 600)})
				continue
			}
			emit(fmt.Sprintf("Generated query: %s → %d rows", oneLine(sql, 200), res.RowCount))
			var resultJSON []byte
			resultJSON, _ = json.Marshal(res)
			msgs = append(msgs, ai.ChatMessage{Role: "user", Content: "TOOL RESULT: " + truncateChars(string(resultJSON), agentMaxTranscript)})
		case "final":
			ans, ferr := s.finishAgent(act, steps, emit)
			if ferr == nil {
				return ans, nil
			}
			if errors.Is(ferr, errNoContent) && !emptyFinalRetried {
				emptyFinalRetried = true
				msgs = append(msgs,
					ai.ChatMessage{Role: "assistant", Content: resp},
					ai.ChatMessage{Role: "user", Content: emptyFinalFeedback})
				continue
			}
			return nil, ferr
		default:
			msgs = append(msgs,
				ai.ChatMessage{Role: "assistant", Content: resp},
				ai.ChatMessage{Role: "user", Content: "TOOL RESULT: " + invalidFeedback})
		}
	}
	msgs = append(msgs, ai.ChatMessage{Role: "user",
		Content: "You have used your query budget. Reply now with the final JSON answer {\"action\":\"final\",...}. You may not run more queries."})
	callStart = time.Now()
	resp, usage, err := s.Provider.CompleteStructured(&ai.StructuredRequest{
		Capability: "query_agent",
		System:     system,
		Messages:   msgs,
		MaxTokens:  4000,
		Timeout:    agentCallTimeout,
	})
	logCall(usage, err)
	if err != nil {
		return nil, err
	}
	var act agentAction
	if jerr := json.Unmarshal([]byte(resp), &act); jerr != nil {
		return nil, fmt.Errorf("agent: no valid final answer")
	}
	return s.finishAgent(act, steps, emit)
}

func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```sql")
		s = strings.TrimPrefix(s, "```")
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}
	return s
}

func (s *Service) finishAgent(act agentAction, steps []string, emit func(string)) (*Answer, error) {
	widgetsList := sanitizeWidgets(act.Widgets)
	text := strings.TrimSpace(act.Text)
	if text == "" && len(widgetsList) == 0 {
		return nil, errNoContent
	}
	if len(widgetsList) > 0 {
		step := fmt.Sprintf("Built %d widget%s for the answer", len(widgetsList), plural(len(widgetsList)))
		steps = append(steps, step)
		if emit != nil {
			emit(step)
		}
	}
	return &Answer{Text: text, Steps: steps, Widgets: widgetsList}, nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func oneLine(sql string, max int) string {
	s := strings.Join(strings.Fields(sql), " ")
	return truncateChars(s, max)
}

func truncateChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

var colorRE = regexp.MustCompile(`^#[0-9a-fA-F]{3,8}$`)

func sanitizeWidgets(specs []agentWidgetSpec) []*widgets.Widget {
	out := make([]*widgets.Widget, 0, len(specs))
	for _, sp := range specs {
		if len(out) >= agentMaxWidgets {
			break
		}
		if w := sanitizeWidget(sp); w != nil {
			out = append(out, w)
		}
	}
	return out
}

func sanitizeWidget(sp agentWidgetSpec) *widgets.Widget {
	title := truncateChars(strings.TrimSpace(sp.Title), 120)
	switch strings.ToLower(strings.TrimSpace(sp.Type)) {
	case "bar", "line":
		if d := sanitizeSeries(sp.Data); d != nil {
			return &widgets.Widget{ID: "w-" + util.NewID(), Type: sp.Type, Title: title, Data: d}
		}
	case "pie":
		if d := sanitizePie(sp.Data); d != nil {
			return &widgets.Widget{ID: "w-" + util.NewID(), Type: "pie", Title: title, Data: d}
		}
	case "number":
		if d := sanitizeNumber(sp.Data); d != nil {
			return &widgets.Widget{ID: "w-" + util.NewID(), Type: "number", Title: title, Data: d}
		}
	case "text":
		if d := sanitizeText(sp.Data); d != nil {
			return &widgets.Widget{ID: "w-" + util.NewID(), Type: "text", Title: title, Data: d}
		}
	case "table":
		if d := sanitizeTable(sp.Data); d != nil {
			return &widgets.Widget{ID: "w-" + util.NewID(), Type: "table", Title: title, Data: d}
		}
	case "transactions":
		if d := sanitizeTransactions(sp.Data); d != nil {
			return &widgets.Widget{ID: "w-" + util.NewID(), Type: "transactions", Title: title, Data: d}
		}
	}
	return nil
}

func decodeData(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

func toStrings(v any, max int) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if len(out) >= max {
			break
		}
		s, _ := item.(string)
		out = append(out, truncateChars(strings.TrimSpace(s), 60))
	}
	return out
}

func toMinorUnits(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		if n > 1e15 || n < -1e15 {
			return 0, false
		}
		return int64(n), true
	case string:
		n = strings.TrimSpace(strings.ReplaceAll(n, ",", ""))
		if f, err := strconv.ParseFloat(n, 64); err == nil && f < 1e15 && f > -1e15 {
			return int64(f), true
		}
	}
	return 0, false
}

func sanitizeSeries(raw json.RawMessage) map[string]any {
	d := decodeData(raw)
	if d == nil {
		return nil
	}
	labels := toStrings(d["labels"], 240)
	if len(labels) == 0 {
		return nil
	}
	seriesIn, ok := d["series"].([]any)
	if !ok || len(seriesIn) == 0 {
		return nil
	}
	series := []map[string]any{}
	for i, s := range seriesIn {
		if len(series) >= 6 {
			break
		}
		sm, ok := s.(map[string]any)
		if !ok {
			continue
		}
		name := truncateChars(str(sm["name"]), 60)
		if name == "" {
			name = fmt.Sprintf("Series %d", i+1)
		}
		valsIn, ok := sm["values"].([]any)
		if !ok || len(valsIn) == 0 {
			continue
		}
		vals := make([]int64, 0, len(labels))
		for j := 0; j < len(labels) && j < len(valsIn); j++ {
			if v, ok := toMinorUnits(valsIn[j]); ok {
				vals = append(vals, v)
			} else {
				vals = append(vals, 0)
			}
		}
		color := str(sm["color"])
		if !colorRE.MatchString(color) {
			color = widgets.PaletteAt(i)
		}
		series = append(series, map[string]any{"name": name, "color": color, "values": vals})
	}
	if len(series) == 0 {
		return nil
	}
	return map[string]any{"labels": labels, "series": series}
}

func sanitizePie(raw json.RawMessage) map[string]any {
	d := decodeData(raw)
	if d == nil {
		return nil
	}
	labels := toStrings(d["labels"], 12)
	if len(labels) == 0 {
		return nil
	}
	valsIn, ok := d["values"].([]any)
	if !ok {
		return nil
	}
	vals := make([]int64, 0, len(valsIn))
	for i := 0; i < len(labels) && i < len(valsIn); i++ {
		v, ok := toMinorUnits(valsIn[i])
		if !ok || v < 0 {
			v = 0
		}
		vals = append(vals, v)
	}
	return map[string]any{"labels": labels, "values": vals}
}

func sanitizeNumber(raw json.RawMessage) map[string]any {
	d := decodeData(raw)
	if d == nil {
		return nil
	}
	value := truncateChars(str(d["value"]), 40)
	if value == "" {
		return nil
	}
	caption := truncateChars(str(d["caption"]), 120)
	return map[string]any{"value": value, "caption": caption}
}

func sanitizeText(raw json.RawMessage) map[string]any {
	d := decodeData(raw)
	if d == nil {
		return nil
	}
	body := truncateChars(str(d["body"]), 4000)
	if body == "" {
		return nil
	}
	return map[string]any{"body": body}
}

func sanitizeTable(raw json.RawMessage) map[string]any {
	d := decodeData(raw)
	if d == nil {
		return nil
	}
	columns := toStrings(d["columns"], 8)
	if len(columns) == 0 {
		return nil
	}
	rowsIn, ok := d["rows"].([]any)
	if !ok {
		return nil
	}
	rows := [][]string{}
	for _, r := range rowsIn {
		if len(rows) >= 50 {
			break
		}
		cellsIn, ok := r.([]any)
		if !ok {
			continue
		}
		cells := make([]string, 0, len(columns))
		for i := 0; i < len(columns); i++ {
			if i < len(cellsIn) {
				cells = append(cells, truncateChars(str(cellsIn[i]), 60))
			} else {
				cells = append(cells, "")
			}
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 {
		return nil
	}
	return map[string]any{"columns": columns, "rows": rows}
}

func sanitizeTransactions(raw json.RawMessage) map[string]any {
	d := decodeData(raw)
	if d == nil {
		return nil
	}
	txsIn, ok := d["transactions"].([]any)
	if !ok {
		return nil
	}
	txs := []map[string]any{}
	for _, t := range txsIn {
		if len(txs) >= 50 {
			break
		}
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		amount, _ := toMinorUnits(tm["amount_minor"])
		typ := str(tm["type"])
		if typ != "income" && typ != "transfer" {
			typ = "expense"
		}
		cur := str(tm["currency"])
		if cur == "" {
			cur = "GBP"
		}
		txs = append(txs, map[string]any{
			"date":         truncateChars(str(tm["date"]), 10),
			"description":  truncateChars(str(tm["description"]), 80),
			"category":     truncateChars(str(tm["category"]), 40),
			"amount_minor": amount,
			"currency":     cur,
			"type":         typ,
		})
	}
	if len(txs) == 0 {
		return nil
	}
	return map[string]any{"transactions": txs}
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
