package query

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"glowworm/internal/ai"
	"glowworm/internal/core/changes"
	"glowworm/internal/core/grouping"
	"glowworm/internal/core/widgets"
	"glowworm/internal/util"
)

const (
	agentMaxRounds     = 6
	agentMaxWidgets    = 3
	agentMaxTranscript = 8000
	agentCallTimeout   = 90 * time.Second
	// agentWidgetRetries is how many times a final answer whose widgets fail
	// to run goes back to the model to be fixed.
	agentWidgetRetries = 2
)

// agentWidgetSpec is a widget in the model's final answer: normally a query
// with how to show it (SQL, Columns, Caption), which becomes a Spec. Data is
// the older form, a finished snapshot, kept only as a last resort.
type agentWidgetSpec struct {
	Type    string                `json:"type"`
	Title   string                `json:"title"`
	SQL     string                `json:"sql"`
	Columns map[string]SpecColumn `json:"columns"`
	Caption string                `json:"caption"`
	Data    json.RawMessage       `json:"data"`
	// A group_rule widget: grouping rules drafted for the user to create.
	Group string               `json:"group"`
	Rules []grouping.DraftRule `json:"rules"`
	// A change widget: an edit drafted for the user to apply. SQL selects
	// the transactions' ids.
	Action   string `json:"action"`
	Category string `json:"category"`
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
	sp := agentWidgetSpec{Type: rawStr(m["type"]), Title: rawStr(m["title"]),
		SQL: rawStr(m["sql"]), Caption: rawStr(m["caption"])}
	if c, ok := m["columns"]; ok && sp.SQL != "" {
		_ = json.Unmarshal(c, &sp.Columns)
	}
	if strings.EqualFold(sp.Type, "group_rule") {
		src := m
		if d, ok := m["data"]; ok {
			var dm map[string]json.RawMessage
			if json.Unmarshal(d, &dm) == nil && dm["rules"] != nil {
				src = dm
			}
		}
		sp.Group = rawStr(src["group"])
		_ = json.Unmarshal(src["rules"], &sp.Rules)
		return sp
	}
	if strings.EqualFold(sp.Type, "change") {
		sp.Action = strings.ToLower(rawStr(m["action"]))
		sp.Category = rawStr(m["category"])
		return sp
	}
	if sp.SQL != "" {
		return sp
	}
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
  IMPORTANT: merchant is the GROUP the transaction belongs to by one of the
  user's grouping rules (see GROUPING), and is NULL for most transactions —
  the payee is in description. Group and label payees by
  COALESCE(NULLIF(merchant,''), description); when the user names a
  merchant/payee/shop, match that expression, never merchant alone.
- categories(id, name)
- accounts(id, name, type TEXT 'debit'|'credit'|'savings', opening_balance_minor, opening_balance_date)
- group_rules(id, group_name, match TEXT 'is'|'contains', text, created_at, deleted_at)
Rules: ignore rows with deleted_at NOT NULL. "Spend" = SUM(-amount_minor) over type='expense'
(transfers are never spend or income). Leave type='transfer' rows (moves between the
user's own accounts) out of every figure, list and chart unless the question explicitly
asks about transfers, or is about one account's balance or money in and out: to a
single account a transfer is real money moving in or out (a credit card's
repayments, a top-up), so an account's balance and running totals include its
transfers. Balance(account) = opening_balance_minor +
SUM(amount_minor) of its transactions (only those after opening_balance_date when set).
SAVINGS ARE EXCLUDED BY DEFAULT: leave savings accounts (accounts.type = 'savings')
out of every figure — balances, spend, income, counts, lists, charts — unless the
question mentions savings or names a savings account. Filter accounts with
a.type <> 'savings', and transactions with
t.account_id NOT IN (SELECT id FROM accounts WHERE type = 'savings').

`)
	fmt.Fprintf(&b, "USER'S CATEGORIES: %s\n", joinOrNone(cats))
	fmt.Fprintf(&b, "USER'S ACCOUNTS: %s\n", joinOrNone(accounts))
	fmt.Fprintf(&b, "TOP MERCHANTS: %s\n", joinOrNone(merchants))
	fmt.Fprintf(&b, "Today is %s. Currency is %s.\n\n", s.Clock.Today(), s.currency())
	if win != nil && (win.From != "" || win.To != "") {
		rangeDesc := win.From + " to " + win.To
		if win.From == "" {
			rangeDesc = "up to " + win.To
		} else if win.To == "" {
			rangeDesc = "from " + win.From
		}
		fmt.Fprintf(&b, "CURRENT VIEW PERIOD: the user is browsing %s (%s). Unless the\nquestion names a different date range (\"this month\", \"last year\", a date,\netc.), restrict every query and widget to this period and mention the period\nin the text.\n\n", win.Label, rangeDesc)
	} else {
		b.WriteString("CURRENT VIEW PERIOD: all time.\n\n")
	}
	b.WriteString(`DATE PARAMETERS: :from and :to are bound to the current view period as
'YYYY-MM-DD' strings (all time binds 0000-01-01 and 9999-12-31), in run_query
and in widget sql. Filter with t.date BETWEEN :from AND :to.
:grain is bound to 'day', 'week' or 'month': the bucket size that gives a
readable number of points over the period being viewed (see TIME SERIES).

`)

	b.WriteString(`WIDGET POLICY (default ON): embed one widget that visualises the answer
unless the question is purely conversational. Choose by question shape:
- change over time ("how has X changed", "over time", "trend") → line or
  bar chart built as described under TIME SERIES
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

WIDGETS ARE LIVE QUERIES. A widget is not a snapshot of numbers: it is ONE
read-only SELECT that the app re-runs every time the widget is shown (the
user can pin it to their dashboard, where it must keep working with no AI).
Do all arithmetic in SQL — ratios, differences, percentages, running totals,
comparisons — combining as many sub-queries or CTEs as needed in one
statement. Each widget is:
  {"type": ..., "title": ..., "sql": "SELECT ...",
   "columns": {"<result column>": {"label": "...", "format": "money|percent|number|text"}},
   "caption": "..."}
- format is REQUIRED for every numeric column: money = integer minor units
  (pence), percent = a ratio (0.25 shows as 25%), number = a plain count or
  quantity. label is optional (defaults to the column name).
- Dates: to follow the period the user is viewing (the default, and what a
  pinned widget should do), filter with :from/:to. Only when the question names
  its own period ("this month", "in 2025", "last 6 months") write that range
  in SQL relative to date('now'), e.g. t.date >= date('now','start of month'),
  so it stays correct over time. Never hard-code today's dates.
- Guard divisions: CASE WHEN x > 0 THEN y * 1.0 / x END (NULL shows as "—").
- Result shape by type:
    number: one row; the first column is the value. caption is a short
            description WITHOUT dates (the app adds the period).
    bar/line: first column = x labels (chronological for time: the :grain
            bucket under TIME SERIES); every further column is a series.
    pie:    first column = label, second = value (>= 0); at most 12 rows.
    table:  every column is shown, in order; at most 50 rows.
    transactions: columns date, description, category, amount_minor (signed),
            type; newest first; at most 50 rows.
    text:   {"type":"text","title":...,"data":{"body":"..."}} (no sql).
- The app runs each widget before showing the answer; if one fails you get
  WIDGET CHECK FAILED with the error — fix it and resend the whole final.
EXAMPLE FINAL (single total — number widget is mandatory):
  {"action":"final","text":"£25.50 at Virgin Trains across 2 transactions.",
   "widgets":[{"type":"number","title":"Virgin Trains spend",
    "sql":"SELECT COALESCE(SUM(-t.amount_minor),0) AS spend FROM transactions t WHERE t.deleted_at IS NULL AND t.type = 'expense' AND COALESCE(NULLIF(t.merchant,''), t.description) LIKE '%Virgin Trains%' AND t.date BETWEEN :from AND :to",
    "columns":{"spend":{"format":"money"}},"caption":"at Virgin Trains"}]}
EXAMPLE FINAL (a calculation — savings rate):
  {"action":"final","text":"18% of income was left unspent.",
   "widgets":[{"type":"number","title":"Savings rate",
    "sql":"SELECT CASE WHEN inc > 0 THEN (inc - spend) * 1.0 / inc END AS rate FROM (SELECT COALESCE(SUM(CASE WHEN t.type = 'income' THEN t.amount_minor END),0) AS inc, COALESCE(SUM(CASE WHEN t.type = 'expense' THEN -t.amount_minor END),0) AS spend FROM transactions t WHERE t.deleted_at IS NULL AND t.date BETWEEN :from AND :to)",
    "columns":{"rate":{"format":"percent"}},"caption":"of income not spent"}]}
EXAMPLE FINAL (list question — transactions widget, empty text):
  {"action":"final","text":"",
   "widgets":[{"type":"transactions","title":"Recent transactions",
    "sql":"SELECT t.date, t.description, c.name AS category, t.amount_minor, t.type FROM transactions t LEFT JOIN categories c ON c.id = t.category_id WHERE t.deleted_at IS NULL AND t.type <> 'transfer' AND t.date BETWEEN :from AND :to ORDER BY t.date DESC LIMIT 20"}]}

TIME SERIES — before writing SQL, decide what the user wants to see, then
say in the text and caption exactly what is plotted:
- WHAT TO PLOT. Spending (or income) over time = the amount per period, refunds
  netted off. An account's history, or how much is owed or held over time =
  the account's running BALANCE. Short requests leave this implicit: choose
  the reading that is most useful and say which it is.
- BUCKETS. Unless the user names a bucket size ("monthly", "per week",
  "daily"), group by :grain — never pick months on your own: a handful of
  points hides the shape of the data. :grain follows the period, so a pinned
  chart keeps a sensible bucket when the user changes it. Bucket label (the
  first day of the bucket, or YYYY-MM):
    CASE :grain WHEN 'day' THEN t.date WHEN 'week' THEN date(t.date, 'weekday 0', '-6 days') ELSE strftime('%Y-%m', t.date) END
- EVERY TRANSACTION ("each transaction", "every payment"): no bucketing, one
  point per transaction, ORDER BY t.date, t.id. When the question is about an
  account (or its spending), plot the running balance after each transaction,
  so every purchase, refund and repayment moves the line. A line of each
  transaction's own amount is a jagged row of unrelated values: only when the
  user asks for the size of each transaction, show those as bars.
- RUNNING TOTALS. A cumulative line must be a real balance: the opening
  balance plus SUM(amount_minor) over EVERY transaction of the account(s) up to
  that point — income, refunds and transfers included — computed over all
  history and only then filtered to :from/:to, so it starts at the right level.
  Never draw a running sum of one kind of transaction (spend alone): it can
  only rise, and hides repayments and refunds. Only when the user explicitly
  asks for cumulative spend is that the answer.
- CREDIT CARDS (accounts.type = 'credit'): the balance is negative while money
  is owed. Plot the amount owed, -balance, labelled "Owed": purchases push it
  up, repayments bring it down.
- With more than ~150 points in the period, show the balance at the end of
  each :grain bucket instead of after every transaction (pattern below).
EXAMPLE FINAL (spending over time, bucket follows the period):
  {"action":"final","text":"£1,240.00 spent on the Visa card over the period, peaking in the week of 2026-03-09 at £310.50.",
   "widgets":[{"type":"bar","title":"Visa spending",
    "sql":"SELECT CASE :grain WHEN 'day' THEN t.date WHEN 'week' THEN date(t.date, 'weekday 0', '-6 days') ELSE strftime('%Y-%m', t.date) END AS period, SUM(-t.amount_minor) AS spend FROM transactions t JOIN accounts a ON a.id = t.account_id WHERE t.deleted_at IS NULL AND t.type = 'expense' AND a.name = 'Visa' AND t.date BETWEEN :from AND :to GROUP BY 1 ORDER BY 1",
    "columns":{"period":{"format":"text"},"spend":{"label":"Spend","format":"money"}},"caption":"Spend per period, refunds netted off"}]}
EXAMPLE FINAL (a credit card over time, every transaction — running balance):
  {"action":"final","text":"The Visa card's balance after each transaction: purchases raise what is owed, repayments lower it. £420.00 is owed now.",
   "widgets":[{"type":"line","title":"Visa balance owed",
    "sql":"SELECT date, owed FROM (SELECT t.date, t.id, -(a.opening_balance_minor + SUM(t.amount_minor) OVER (ORDER BY t.date, t.id)) AS owed FROM transactions t JOIN accounts a ON a.id = t.account_id WHERE t.deleted_at IS NULL AND a.name = 'Visa' AND (a.opening_balance_date IS NULL OR a.opening_balance_date = '' OR t.date > a.opening_balance_date)) WHERE date BETWEEN :from AND :to ORDER BY date, id",
    "columns":{"date":{"format":"text"},"owed":{"label":"Owed","format":"money"}},"caption":"Amount owed after each transaction, repayments included"}]}

EXAMPLE QUERIES (verified against this schema — prefer these patterns):
- An account's balance at the end of each :grain bucket (long periods):
  SELECT period, balance FROM (SELECT CASE :grain WHEN 'day' THEN t.date WHEN 'week' THEN date(t.date, 'weekday 0', '-6 days') ELSE strftime('%Y-%m', t.date) END AS period, t.date,
      a.opening_balance_minor + SUM(t.amount_minor) OVER (ORDER BY t.date, t.id) AS balance,
      ROW_NUMBER() OVER (PARTITION BY CASE :grain WHEN 'day' THEN t.date WHEN 'week' THEN date(t.date, 'weekday 0', '-6 days') ELSE strftime('%Y-%m', t.date) END ORDER BY t.date DESC, t.id DESC) AS rn
    FROM transactions t JOIN accounts a ON a.id = t.account_id
    WHERE t.deleted_at IS NULL AND a.name = 'Current account'
    AND (a.opening_balance_date IS NULL OR a.opening_balance_date = '' OR t.date > a.opening_balance_date))
  WHERE rn = 1 AND date BETWEEN :from AND :to ORDER BY period
- Current balance per account:
  SELECT a.name, a.opening_balance_minor + COALESCE((SELECT SUM(t.amount_minor)
    FROM transactions t WHERE t.account_id = a.id AND t.deleted_at IS NULL
    AND (a.opening_balance_date IS NULL OR a.opening_balance_date = '' OR t.date > a.opening_balance_date)), 0)
    AS balance FROM accounts a WHERE a.deleted_at IS NULL AND a.type <> 'savings'
- Balance at each month end (a series, for line charts; savings excluded):
  WITH months AS (SELECT DISTINCT strftime('%Y-%m', date) AS m FROM transactions WHERE deleted_at IS NULL)
  SELECT m.m AS month, (SELECT COALESCE(SUM(opening_balance_minor),0) FROM accounts
      WHERE deleted_at IS NULL AND type <> 'savings')
    + COALESCE(SUM(t.amount_minor), 0) AS balance
  FROM months m LEFT JOIN transactions t ON t.deleted_at IS NULL AND strftime('%Y-%m', t.date) <= m.m
    AND t.account_id NOT IN (SELECT id FROM accounts WHERE type = 'savings')
  GROUP BY m.m ORDER BY m.m
- Spend by category in a range:
  SELECT c.name AS category, SUM(-t.amount_minor) AS spend FROM transactions t
    LEFT JOIN categories c ON c.id = t.category_id
    WHERE t.deleted_at IS NULL AND t.type = 'expense' AND t.date BETWEEN '2026-01-01' AND '2026-06-30'
    AND t.account_id NOT IN (SELECT id FROM accounts WHERE type = 'savings')
    GROUP BY 1 ORDER BY spend DESC
- Spend at a named merchant/payee (description fallback — merchant is often empty):
  SELECT SUM(-t.amount_minor) AS spend, COUNT(*) AS n, MIN(t.date) AS first, MAX(t.date) AS last
    FROM transactions t
    WHERE t.deleted_at IS NULL AND t.type = 'expense'
    AND COALESCE(NULLIF(t.merchant,''), t.description) LIKE '%Virgin Trains%'

GROUPING: the same payee or purpose can appear under slightly different
descriptions ("To Jaeren Rodrigues - rent and water", "Jaeren Rodrigues -
Rent and water"), so it shows up as separate merchants. The user fixes this
with grouping rules: each rule puts transactions whose description IS, or
CONTAINS, some text into a named group (comparison ignores case and
punctuation; "contains" matches whole words). A group exists only through
its rules. You never create or delete rules yourself — you DRAFT them and
the user presses a button:
- When the user wants transactions grouped, first query the descriptions
  involved (SELECT description, COUNT(*) ... GROUP BY description), then
  answer with a group_rule widget:
    {"type":"group_rule","group":"Rent and water",
     "rules":[{"match":"contains","text":"rent and water"}]}
  Prefer the fewest, most specific rules that catch exactly the intended
  descriptions: "is" with the full description for one-offs, "contains"
  with a distinctive phrase for variants. Never draft text so general it
  would pull in unrelated payees (a single common word, a person's name
  when only one purpose was meant). The widget shows the user which
  descriptions it would catch. Say in the text that pressing Create rule
  applies it — never claim it is done.
- To show, review or remove rules: {"type":"group_rules","title":"Grouping rules"}
  — a live list where each rule can be deleted. Removing a group means
  deleting its rules from that list.

CHANGES: you can't change data yourself, but you can PROPOSE a change to
transactions with a change widget. The user sees every transaction it would
touch and nothing happens until they press Apply. It can be undone from History.
  {"type":"change","action":"set_category","category":"Groceries",
   "sql":"SELECT t.id FROM transactions t WHERE t.deleted_at IS NULL AND t.description LIKE '%TESCO%'"}
- "sql" selects the ids (first column, t.id) of exactly the transactions to
  change, at most 500. Always include t.deleted_at IS NULL. Check the
  selection with run_query first (description, date, amount, category), and
  narrow it until it holds only what the user meant.
- Actions:
  - set_category, with "category" (an existing spend or income category by
    name). Expenses take spend categories and income takes income
    categories; transfers are skipped.
  - delete: deletes the selected transactions.
  - link_transfer: links exactly two transactions — money out of one account
    and the same money into another — as an internal transfer. Use this
    (not a category) to make transactions internal transfers.
  - unlink_transfer: breaks the transfer link on the selected transactions.
- Recurring series are detected automatically from the data and can't be
  set by hand; say so if asked. Account settings, categories, imports and
  anything else can't be changed from here: say where in the app to do it.
- Say in the text what pressing Apply would do — never claim it is done.

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
   - "text": short, factual, impersonal. Quote amounts in major units:
     amount_minor / 100, so 11200 is £112.00 and 12840 is £128.40. Reference the numbers you actually found; never invent figures.
   - "widgets": 0-3 live query widgets as described under WIDGETS ARE LIVE
     QUERIES, type one of number, bar, line, pie, table, transactions, text;
     or the group_rule / group_rules widgets under GROUPING (these have no sql),
     or a change widget under CHANGES.
     Test a widget's sql with run_query first when unsure.
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
	widgetRetries := 0

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
		// A reply with no JSON at all (prose, or cut off) is sent back like
		// any other invalid reply rather than ending the answer.
		var bad *ai.ErrBadReply
		if errors.As(err, &bad) {
			msgs = append(msgs,
				ai.ChatMessage{Role: "assistant", Content: bad.Reply},
				ai.ChatMessage{Role: "user", Content: invalidFeedback})
			continue
		}
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
			res, qerr := RunReadonlyQuery(s.DB, sql, s.queryArgs(sql, win)...)
			if qerr != nil {
				// A step is a heading line, then the full SQL (shown as code).
				emit(fmt.Sprintf("Query failed: %s\n%s", truncateChars(oneLine(qerr.Error(), 200), 200), strings.TrimSpace(sql)))
				msgs = append(msgs, ai.ChatMessage{Role: "user", Content: "TOOL RESULT: " + truncateChars("error: "+qerr.Error(), 600)})
				continue
			}
			emit(fmt.Sprintf("Ran a query → %d row%s\n%s", res.RowCount, plural(res.RowCount), strings.TrimSpace(sql)))
			var resultJSON []byte
			resultJSON, _ = json.Marshal(res)
			msgs = append(msgs, ai.ChatMessage{Role: "user", Content: "TOOL RESULT: " + truncateChars(string(resultJSON), agentMaxTranscript)})
		case "final":
			lenient := widgetRetries >= agentWidgetRetries
			ans, problems, ferr := s.finishAgent(act, steps, emit, win, lenient)
			if len(problems) > 0 {
				widgetRetries++
				for _, p := range problems {
					emit("Widget check failed: " + truncateChars(p, 200))
				}
				msgs = append(msgs,
					ai.ChatMessage{Role: "assistant", Content: resp},
					ai.ChatMessage{Role: "user", Content: "WIDGET CHECK FAILED: " + truncateChars(strings.Join(problems, "; "), 1200) +
						". Fix the widgets and reply with the whole final JSON again."})
				continue
			}
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
	act, perr := parseAction(resp)
	if perr != nil {
		return nil, fmt.Errorf("agent: no valid final answer")
	}
	ans, _, ferr := s.finishAgent(act, steps, emit, win, true)
	return ans, ferr
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

// finishAgent runs the answer's widget queries. Problems with them come back
// for the model to fix, unless lenient: then widgets that fail are dropped
// and snapshot-only ones are kept as they are.
func (s *Service) finishAgent(act agentAction, steps []string, emit func(string), win *TimeWindow, lenient bool) (*Answer, []string, error) {
	widgetsList, problems := s.buildWidgets(act.Widgets, win, lenient)
	if len(problems) > 0 && !lenient {
		return nil, problems, nil
	}
	text := strings.TrimSpace(act.Text)
	if text == "" && len(widgetsList) == 0 {
		return nil, nil, errNoContent
	}
	if len(widgetsList) > 0 {
		step := fmt.Sprintf("Built %d widget%s for the answer", len(widgetsList), plural(len(widgetsList)))
		steps = append(steps, step)
		if emit != nil {
			emit(step)
		}
	}
	return &Answer{Text: text, Steps: steps, Widgets: widgetsList}, nil, nil
}

// buildWidgets turns the answer's widgets into live ones by running their
// queries, so what the answer shows is what a pinned copy will show.
func (s *Service) buildWidgets(specs []agentWidgetSpec, win *TimeWindow, lenient bool) ([]*widgets.Widget, []string) {
	out := make([]*widgets.Widget, 0, len(specs))
	var problems []string
	for i, sp := range specs {
		if len(out) >= agentMaxWidgets {
			break
		}
		typ := strings.ToLower(strings.TrimSpace(sp.Type))
		title := truncateChars(strings.TrimSpace(sp.Title), 120)
		name := fmt.Sprintf("widget %d (%q)", i+1, title)
		switch typ {
		case "group_rules":
			if title == "" {
				title = "Grouping rules"
			}
			out = append(out, &widgets.Widget{ID: "w-" + util.NewID(), Type: typ, Title: title, Data: map[string]any{}})
			continue
		case "group_rule":
			draft := grouping.Draft{Group: sp.Group, Rules: sp.Rules}
			p, err := (&grouping.Service{DB: s.DB}).Preview(draft)
			if err == nil && len(p.Matched) == 0 {
				err = fmt.Errorf("these rules match no transactions; check the text against real descriptions")
			}
			if err != nil {
				if !lenient {
					problems = append(problems, name+": "+err.Error())
				}
				continue
			}
			if title == "" {
				title = "Group as “" + p.Group + "”"
			}
			out = append(out, &widgets.Widget{ID: "w-" + util.NewID(), Type: typ, Title: title, Data: p.Draft})
			continue
		case "change":
			pv, err := s.draftChange(sp, win)
			if err != nil {
				if !lenient {
					problems = append(problems, name+": "+err.Error())
				}
				continue
			}
			if title == "" {
				title = "Proposed change"
			}
			out = append(out, &widgets.Widget{ID: "w-" + util.NewID(), Type: typ, Title: title, Data: pv.Proposal})
			continue
		}
		if strings.TrimSpace(sp.SQL) == "" {
			if typ == "text" || lenient {
				if w := sanitizeWidget(sp); w != nil {
					out = append(out, w)
				}
				continue
			}
			problems = append(problems, name+` has no "sql": every widget except text is a query the app runs`)
			continue
		}
		spec := &Spec{V: SpecVersion, SQL: stripFences(sp.SQL), Columns: sp.Columns,
			Caption: truncateChars(strings.TrimSpace(sp.Caption), 120)}
		w, res, err := s.runSpec(spec, typ, win)
		if err == nil {
			err = checkSpec(spec, typ, res)
		}
		if err != nil {
			problems = append(problems, name+": "+err.Error())
			continue
		}
		w.Title = title
		out = append(out, w)
	}
	return out, problems
}

// draftChange resolves a change widget's query to the transactions it would
// touch and checks the change makes sense. Nothing is changed.
func (s *Service) draftChange(sp agentWidgetSpec, win *TimeWindow) (*changes.Preview, error) {
	query := stripFences(sp.SQL)
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf(`a change needs "sql" selecting the transactions' ids`)
	}
	ids, err := s.selectIDs(query, win)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("the sql selects no transactions; check it with run_query")
	}
	svc := &changes.Service{DB: s.DB}
	p := changes.Proposal{Action: sp.Action, IDs: ids}
	if sp.Action == changes.SetCategory {
		if p.CategoryID, err = svc.CategoryByName(sp.Category); err != nil {
			return nil, err
		}
	}
	pv, err := svc.Draft(p)
	if err != nil {
		return nil, err
	}
	if len(pv.Rows) < len(ids) {
		return nil, fmt.Errorf("%d of the %d selected ids aren't transactions: select t.id from transactions",
			len(ids)-len(pv.Rows), len(ids))
	}
	if pv.Ready == 0 {
		return nil, fmt.Errorf("it would change nothing: every selected transaction is skipped (%s)", pv.Rows[0].Skip)
	}
	return pv, nil
}

// selectIDs runs a change's read-only query and returns its first column.
func (s *Service) selectIDs(query string, win *TimeWindow) ([]string, error) {
	var ids []string
	err := WithReadonlyRows(s.DB, query, changes.MaxTransactions+1, s.queryArgs(query, win), func(rows *sql.Rows) error {
		cols, err := rows.Columns()
		if err != nil {
			return err
		}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return err
			}
			id := vals[0]
			if b, ok := id.([]byte); ok {
				id = string(b)
			}
			ids = append(ids, cellText(id))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(ids) > changes.MaxTransactions {
		return nil, fmt.Errorf("it selects more than %d transactions; narrow it down", changes.MaxTransactions)
	}
	return ids, nil
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
