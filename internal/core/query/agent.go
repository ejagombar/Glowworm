package query

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
// with how to show it (SQL, Columns, Caption), which becomes a Spec.
type agentWidgetSpec struct {
	Type    string                `json:"type"`
	Title   string                `json:"title"`
	SQL     string                `json:"sql"`
	Columns map[string]SpecColumn `json:"columns"`
	Caption string                `json:"caption"`
	Series  string                `json:"series"`
	Fill    string                `json:"fill"`
	Stacked bool                  `json:"stacked"`
	Project *SpecProjection       `json:"project"`
	Targets []SpecTarget          `json:"targets"`
	// A text widget: its words, and no query.
	Body string `json:"body"`
	// A group_rule widget: grouping rules drafted for the user to create.
	Group string               `json:"group"`
	Rules []grouping.DraftRule `json:"rules"`
	// A change widget: an edit drafted for the user to apply. SQL selects
	// the transactions' ids.
	Action   string `json:"action"`
	Category string `json:"category"`
	// A valuation widget: an account's value on a date, drafted for the
	// user to record.
	Account    string `json:"account"`
	ValueMinor *int64 `json:"value_minor"`
	Date       string `json:"date"`
}

type agentAction struct {
	Action  string            `json:"action"`
	SQL     string            `json:"sql"`
	Text    string            `json:"text"`
	Widgets []agentWidgetSpec `json:"widgets"`
}

// errNoAnswer is the model finishing without text or a widget, or without
// a reply that could be read at all.
var errNoAnswer = &NoAnswerError{Message: "That didn't produce an answer. Try asking it another way."}

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

func normalizeSpec(raw json.RawMessage) agentWidgetSpec {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return agentWidgetSpec{Type: "?"}
	}
	sp := agentWidgetSpec{Type: rawStr(m["type"]), Title: rawStr(m["title"]),
		SQL: rawStr(m["sql"]), Caption: rawStr(m["caption"])}
	_ = json.Unmarshal(m["columns"], &sp.Columns)
	sp.Series, sp.Fill = rawStr(m["series"]), strings.ToLower(rawStr(m["fill"]))
	_ = json.Unmarshal(m["stacked"], &sp.Stacked)
	_ = json.Unmarshal(m["targets"], &sp.Targets)
	if len(m["project"]) > 0 && string(m["project"]) != "null" {
		var p SpecProjection
		if json.Unmarshal(m["project"], &p) == nil {
			sp.Project = &p
		}
	}
	// Models put a widget's own fields either beside its type or under "data".
	var data map[string]json.RawMessage
	_ = json.Unmarshal(m["data"], &data)
	if strings.EqualFold(sp.Type, "text") {
		sp.Body = rawStr(m["body"])
		if data["body"] != nil {
			sp.Body = rawStr(data["body"])
		}
		return sp
	}
	if strings.EqualFold(sp.Type, "group_rule") {
		src := m
		if data["rules"] != nil {
			src = data
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
	if strings.EqualFold(sp.Type, "valuation") {
		sp.Account, sp.Date = rawStr(m["account"]), rawStr(m["date"])
		if v, ok := toMinorUnits(decodeAny(m["value_minor"])); ok {
			sp.ValueMinor = &v
		}
		return sp
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
- accounts(id, name, type TEXT 'debit'|'credit'|'savings', opening_balance_minor, opening_balance_date, deleted_at)
- goals(id, name, target_minor, target_date TEXT 'YYYY-MM-DD' or NULL, color, deleted_at) and
  goal_accounts(goal_id, account_id): savings goals; a goal with no goal_accounts rows
  covers every savings account. Prefer goal_progress (below) for how a goal is going.
- group_rules(id, group_name, match TEXT 'is'|'contains', text, created_at, deleted_at)
- valuations(id, account_id, date TEXT 'YYYY-MM-DD', value_minor, created_at, deleted_at):
  an account's balance on a date, recorded by the user (a savings account's
  value, or a debit/credit account's statement balance). Any account's
  balance on a day is its latest valuation on or before that day plus its
  transactions after the valuation's date; with no valuation, the formula
  below.
- account_periods(grain TEXT 'day'|'week'|'month'|'year', period, start_date,
  end_date, account_id, account, account_type, currency, opening_minor,
  closing_minor, deposits_minor, withdrawals_minor, income_minor, spend_minor,
  growth_minor): worked out by the app, not stored. One row per account per
  period at every grain, from the account's start to today with no gaps
  (period is labelled like TIME SERIES buckets: the day, the Monday of the
  week, YYYY-MM, YYYY). opening/closing are the balance at the period's
  start and end, valuations and opening balances already applied — PREFER
  THIS over computing balances from transactions and valuations yourself.
  deposits/withdrawals = transfers in/out (money the user moved, both
  positive); income and spend are net of refunds; growth = the change no
  transaction explains (interest and returns, from recorded balances).
  closing = opening + deposits - withdrawals + income - spend + growth.
  Pick one grain (WHERE grain = :grain to follow the chart, or a fixed
  one such as 'month' or 'year'). Balances add up across accounts per
  period (SUM(closing_minor) GROUP BY period); flows add up over periods.
  Savings: paid in = deposits - withdrawals; returns = income + growth.
  Filter dates with start_date/end_date (e.g. end_date >= :from AND
  start_date <= :to), not period, so the filter is applied early.
- account_status(account_id, account, account_type, currency, balance_minor,
  last_valued, days_since, reminder_days, due):
  worked out by the app. Each account's balance now, the date of its latest
  recorded balance and how many days ago that was (NULL if never), the
  reminder interval that applies (0 = none) and due = 1 when the balance is
  older than that and needs updating.
- goal_progress(goal_id, goal, color, target_minor, target_date,
  current_minor, remaining_minor, progress [ratio, 1 = reached], pace_minor
  [average change a month over the last 6 months], months_left,
  monthly_needed_minor [to reach it by target_date], projected_month
  [YYYY-MM it's reached at pace, NULL if never], on_track [1/0, NULL with no
  date], accounts [names]): worked out by the app.
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
            bucket under TIME SERIES); every further column is a series —
            or, with "series" set, three columns: label, series, value
            (see SEVERAL SERIES).
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

SEVERAL SERIES — when the user wants things compared or split on one chart
("each account", "all my accounts", "by category over time", "income vs
spending"), give a bar or line chart several series; the app draws a key.
- A fixed few series (income vs spend): one value column each.
- One series per account, category, merchant, ... : return rows of
  label, series, value (one row per label per series) and set
  "series":"<series column>". The app makes a series per distinct value, in
  the order they first appear, and adds all but the largest 7 together as
  "Other". ORDER BY the label first so the x axis stays chronological.
- "fill": what a series with no row at a label shows. "zero" (the default)
  for amounts per period; "previous" for balances, so an account that had no
  transactions in a bucket keeps its last balance.
- Bars: "stacked":true stacks the series into one bar per label, split by
  colour — use it for parts of a whole (spend by category per period). Leave
  it off for side-by-side comparisons (income vs spend).
- Lines suit balances and trends per account; one line per account, each
  that account's own running balance (PARTITION BY the account).
EXAMPLE FINAL (every account's balance over time — one line each):
  {"action":"final","text":"Balances of all 3 accounts over the period; the Visa card is negative while money is owed.",
   "widgets":[{"type":"line","title":"Account balances","series":"account","fill":"previous",
    "sql":"SELECT period, account, balance FROM (SELECT CASE :grain WHEN 'day' THEN t.date WHEN 'week' THEN date(t.date, 'weekday 0', '-6 days') ELSE strftime('%Y-%m', t.date) END AS period, t.date, a.name AS account, a.opening_balance_minor + SUM(t.amount_minor) OVER (PARTITION BY a.id ORDER BY t.date, t.id) AS balance, ROW_NUMBER() OVER (PARTITION BY a.id, CASE :grain WHEN 'day' THEN t.date WHEN 'week' THEN date(t.date, 'weekday 0', '-6 days') ELSE strftime('%Y-%m', t.date) END ORDER BY t.date DESC, t.id DESC) AS rn FROM transactions t JOIN accounts a ON a.id = t.account_id WHERE t.deleted_at IS NULL AND a.deleted_at IS NULL AND (a.opening_balance_date IS NULL OR a.opening_balance_date = '' OR t.date > a.opening_balance_date)) WHERE rn = 1 AND date BETWEEN :from AND :to ORDER BY period, account",
    "columns":{"period":{"format":"text"},"account":{"format":"text"},"balance":{"label":"Balance","format":"money"}},"caption":"Balance at the end of each period, per account"}]}
EXAMPLE FINAL (spending split by category over time — stacked bars):
  {"action":"final","text":"£2,310.00 spent over the period; Groceries was the largest share.",
   "widgets":[{"type":"bar","title":"Spending by category","series":"category","stacked":true,
    "sql":"SELECT CASE :grain WHEN 'day' THEN t.date WHEN 'week' THEN date(t.date, 'weekday 0', '-6 days') ELSE strftime('%Y-%m', t.date) END AS period, COALESCE(c.name, 'Uncategorised') AS category, SUM(-t.amount_minor) AS spend FROM transactions t LEFT JOIN categories c ON c.id = t.category_id WHERE t.deleted_at IS NULL AND t.type = 'expense' AND t.date BETWEEN :from AND :to GROUP BY 1, 2 ORDER BY 1, 3 DESC",
    "columns":{"period":{"format":"text"},"category":{"format":"text"},"spend":{"label":"Spend","format":"money"}},"caption":"Spend per period by category, refunds netted off"}]}

PROGRESS WIDGET — {"type":"progress"}: one bar per row showing a value
against a target. Rows: label, value, target, and optionally a colour
('#rrggbb'). Use it for goals, budgets and limits. Format the value column
(the target column shares it). Example: goals →
  "sql":"SELECT goal, current_minor, target_minor, color FROM goal_progress",
  "columns":{"current_minor":{"format":"money"},"target_minor":{"format":"money"}}

TARGET LINES — a line or bar chart can draw reference lines across it:
  "targets":[{"label":"House deposit","value":2500000},
             {"sql":"SELECT name, target_minor FROM goals WHERE deleted_at IS NULL"}]
A fixed "value" is in the chart's units (minor units for money); a "sql"
target is re-run with the widget, its rows label, value (one line each), so
use it for goals and anything else stored. At most 6 lines.

PROJECTIONS — a line, bar or number widget can carry its series on into the
future: add "project" to the widget. Use it whenever the user asks where
something is heading, "at this rate", "by 2030", "in 5 years", forecasts or
what-ifs ("if I save £300 a month at 4%"). The query returns the history
as a time series (dates as labels: YYYY, YYYY-MM or YYYY-MM-DD, oldest
first); the app adds the projected points, drawn dashed after the real ones.
  "project":{"method":"trend|average|compound","until":"+5y"|"2030-12",
    "periods":12,"basis":24,"rate":0.04,"contribution":30000,"series":["..."]}
- method: trend (default; slope of a line fitted to the points), average
  (average change per period), compound (each period grows by "rate", a
  yearly ratio, and adds "contribution", minor units per MONTH; use it for
  savings what-ifs or when the user gives a rate).
- until: a date or a span past the last point (+18m, +5y, +12w); else
  "periods" (default 12). basis: fit to the last N points only.
- series: names of the series to project (default all).
- rate_sql / contribution_sql: take the rate or the monthly contribution
  from a query each time it runs, e.g. the account's growth over the last
  year as a ratio of its balance, or the average paid in a month over the
  last year, both from account_periods. Prefer these to fixed numbers when
  the figure can be worked out.
- number widget with "project": the rows are label, value (the series),
  and the number shown is the projected value at the end; caption WITHOUT
  dates (the app adds "projected to ...").
- Projections need a steady bucket: use a fixed grain ('month' usually)
  rather than :grain, and history that doesn't follow :from/:to when the
  user asks about the future (a projection from a short window is noisy).
EXAMPLE FINAL (total savings, monthly, projected two years on):
  {"action":"final","text":"Savings total £12,400.00; at the trend of the last 12 months they reach about £16,900 by 2028-11.",
   "widgets":[{"type":"line","title":"Savings projection",
    "sql":"SELECT period, SUM(closing_minor) AS balance FROM account_periods WHERE grain = 'month' AND account_type = 'savings' GROUP BY period ORDER BY period",
    "columns":{"period":{"format":"text"},"balance":{"label":"Savings","format":"money"}},
    "caption":"Savings at each month end, then projected","project":{"method":"trend","basis":12,"until":"+2y"}}]}
EXAMPLE FINAL (paid in vs returns per year — stacked bars):
  {"action":"final","text":"Paid in and returns on savings for each year.",
   "widgets":[{"type":"bar","title":"Savings: paid in vs returns","stacked":true,
    "sql":"SELECT period, SUM(deposits_minor - withdrawals_minor) AS paid_in, SUM(income_minor + growth_minor) AS returns FROM account_periods WHERE grain = 'year' AND account_type = 'savings' GROUP BY period ORDER BY period",
    "columns":{"period":{"format":"text"},"paid_in":{"label":"Paid in","format":"money"},"returns":{"label":"Returns","format":"money"}},
    "caption":"Net money paid in, and interest and growth, per year"}]}

EXAMPLE QUERIES (verified against this schema — prefer these patterns):
- An account's balance at the end of each :grain bucket (long periods):
  SELECT period, balance FROM (SELECT CASE :grain WHEN 'day' THEN t.date WHEN 'week' THEN date(t.date, 'weekday 0', '-6 days') ELSE strftime('%Y-%m', t.date) END AS period, t.date,
      a.opening_balance_minor + SUM(t.amount_minor) OVER (ORDER BY t.date, t.id) AS balance,
      ROW_NUMBER() OVER (PARTITION BY CASE :grain WHEN 'day' THEN t.date WHEN 'week' THEN date(t.date, 'weekday 0', '-6 days') ELSE strftime('%Y-%m', t.date) END ORDER BY t.date DESC, t.id DESC) AS rn
    FROM transactions t JOIN accounts a ON a.id = t.account_id
    WHERE t.deleted_at IS NULL AND a.name = 'Current account'
    AND (a.opening_balance_date IS NULL OR a.opening_balance_date = '' OR t.date > a.opening_balance_date))
  WHERE rn = 1 AND date BETWEEN :from AND :to ORDER BY period
- Current balance per account (from its latest valuation when it has one):
  SELECT a.name, COALESCE(v.value_minor, a.opening_balance_minor) + COALESCE((SELECT SUM(t.amount_minor)
    FROM transactions t WHERE t.account_id = a.id AND t.deleted_at IS NULL
    AND t.date > COALESCE(v.date, NULLIF(a.opening_balance_date, ''), '0000-01-01')), 0)
    AS balance FROM accounts a
    LEFT JOIN valuations v ON v.id = (SELECT id FROM valuations WHERE account_id = a.id
      AND deleted_at IS NULL ORDER BY date DESC, created_at DESC LIMIT 1)
    WHERE a.deleted_at IS NULL AND a.type <> 'savings'
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
- Transfer linking (pairing every transfer's two sides automatically, which
  normally runs after each import): when the user asks to run it, find
  transfers, link transactions, check for unlinked transfers and the like,
  answer with {"type":"link_transfers"} and no sql or query. It lists the
  pairs it would link and links them all when the user presses Link. Text:
  one sentence saying pressing Link links them — never claim it is done.
- Recurring series are detected automatically from the data and can't be
  set by hand; say so if asked. Account settings (other than a recorded
  balance, below), categories, imports and
  anything else can't be changed from here: say where in the app to do it.
- Say in the text what pressing Apply would do — never claim it is done.

BALANCES: the user records an account's balance on a date. For a savings
account it's what the account is worth ("my ISA is 50,000", "the house
fund is at 12.3k now", "my pension was 80k at the end of March"); for a
debit or credit account it's what the bank shows ("my Chase balance is
85.99", "Amex says I owe 209.48"), which the app checks against the
transactions. When the user states an account's balance, answer with a
valuation widget, which they record with one press:
  {"type":"valuation","account":"ISA","value_minor":5000000,"date":"2026-11-05"}
- "account" is the exact name from USER'S ACCOUNTS; match loose wording
  ("my ISA", "stocks and shares", "my current account") to the closest one.
- "value_minor" is the stated value in minor units: 50,000 → 5000000,
  12.3k → 1230000. Money owed on a credit card is negative: owing
  209.48 → -20948.
- "date" is the day the value is for: today unless the user names a day.
- No query is needed first. Text: one sentence saying pressing Record sets
  the account to that balance (for a debit or credit account, checked
  against its transactions) — never claim it is done.

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
     or a change or link_transfers widget under CHANGES, or a valuation
     widget under BALANCES.
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

// historyGuide goes with earlier turns, so questions without any don't pay
// for it.
const historyGuide = `USING EARLIER TURNS: they are only there so a follow-up ("same but for
Amex", "why was that so high?", "show those as a chart") can be understood.
Resolve words like that, those, it and same from them, and re-run or adapt
their SQL instead of exploring again (:from and :to bind to the current
view period). If the new question is about something else, ignore them
entirely: carry over no filters, accounts or categories it doesn't ask for.
Never repeat an earlier answer.`

func (s *Service) askAgent(question string, history []Turn, win *TimeWindow, onStep func(string)) (*Answer, error) {
	system := s.agentSystemPrompt(win)
	first := question
	capability := "query_agent"
	if block := historyBlock(history); block != "" {
		// After the system prompt, which stays the same from one question
		// to the next, so providers that cache a repeated start still can.
		first = block + "\n" + historyGuide + "\n\nQUESTION: " + question
		capability = "query_agent_ctx" // logged apart, to compare token use
	}
	msgs := []ai.ChatMessage{{Role: "user", Content: first}}
	var callStart time.Time
	logCall := func(usage *ai.Usage, err error) {
		s.logCall(capability, callStart, usage, err)
	}
	var steps []string
	emit := func(step string) {
		steps = append(steps, step)
		if onStep != nil {
			onStep(step)
		}
	}
	if len(history) > 0 {
		emit(fmt.Sprintf("Used %d earlier question%s as context", len(history), plural(len(history))))
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
			res, qerr := runReadonlyQuery(s.DB, s.today(), sql, s.queryArgs(sql, win)...)
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
			if errors.Is(ferr, errNoAnswer) && !emptyFinalRetried {
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
	var bad *ai.ErrBadReply
	if errors.As(err, &bad) {
		return nil, errNoAnswer
	}
	if err != nil {
		return nil, err
	}
	act, perr := parseAction(resp)
	if perr != nil {
		return nil, errNoAnswer
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
// for the model to fix, unless lenient: then the widgets that fail are dropped.
func (s *Service) finishAgent(act agentAction, steps []string, emit func(string), win *TimeWindow, lenient bool) (*Answer, []string, error) {
	widgetsList, problems := s.buildWidgets(act.Widgets, win)
	if len(problems) > 0 && !lenient {
		return nil, problems, nil
	}
	text := strings.TrimSpace(act.Text)
	if text == "" && len(widgetsList) == 0 {
		return nil, nil, errNoAnswer
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
// queries, so what the answer shows is what a pinned copy will show. It
// leaves out the ones that fail and says what was wrong with each.
func (s *Service) buildWidgets(specs []agentWidgetSpec, win *TimeWindow) ([]*widgets.Widget, []string) {
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
		case "link_transfers":
			if title == "" {
				title = "Link transfers"
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
				problems = append(problems, name+": "+err.Error())
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
				problems = append(problems, name+": "+err.Error())
				continue
			}
			if title == "" {
				title = "Proposed change"
			}
			out = append(out, &widgets.Widget{ID: "w-" + util.NewID(), Type: typ, Title: title, Data: pv.Proposal})
			continue
		case "valuation":
			d, err := s.draftValuation(sp)
			if err != nil {
				problems = append(problems, name+": "+err.Error())
				continue
			}
			if title == "" {
				title = "Update " + d.Account
			}
			out = append(out, &widgets.Widget{ID: "w-" + util.NewID(), Type: typ, Title: title, Data: d})
			continue
		}
		if strings.TrimSpace(sp.SQL) == "" {
			if typ != "text" {
				problems = append(problems, name+` has no "sql": every widget except text is a query the app runs`)
			} else if sp.Body != "" {
				out = append(out, &widgets.Widget{ID: "w-" + util.NewID(), Type: typ, Title: title,
					Data: map[string]any{"body": truncateChars(sp.Body, 4000)}})
			}
			continue
		}
		spec := &Spec{V: SpecVersion, SQL: stripFences(sp.SQL), Columns: sp.Columns,
			Caption: truncateChars(strings.TrimSpace(sp.Caption), 120),
			Series:  sp.Series, Fill: sp.Fill, Stacked: sp.Stacked, Project: sp.Project,
			Targets: sp.Targets}
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

// ValuationDraft is an account's value on a date, for the user to record.
type ValuationDraft struct {
	AccountID  string `json:"account_id"`
	Account    string `json:"account"`
	ValueMinor int64  `json:"value_minor"`
	Date       string `json:"date"`
	Currency   string `json:"currency"`
}

// draftValuation checks a valuation widget names one account, a
// value and a real date (today when left out). Nothing is recorded.
func (s *Service) draftValuation(sp agentWidgetSpec) (*ValuationDraft, error) {
	name := strings.TrimSpace(sp.Account)
	if name == "" {
		return nil, fmt.Errorf(`a valuation needs "account", the account's name`)
	}
	if sp.ValueMinor == nil {
		return nil, fmt.Errorf(`a valuation needs "value_minor", the value in minor units`)
	}
	d := &ValuationDraft{Account: name, ValueMinor: *sp.ValueMinor, Date: strings.TrimSpace(sp.Date)}
	if d.Date == "" {
		d.Date = s.Clock.Today()
	}
	if _, err := time.Parse("2006-01-02", d.Date); err != nil {
		return nil, fmt.Errorf(`"date" must be YYYY-MM-DD`)
	}
	if d.Date > s.Clock.Today() {
		return nil, fmt.Errorf("the date %s is in the future", d.Date)
	}
	err := s.DB.QueryRow(`SELECT id, name, currency FROM accounts
		WHERE deleted_at IS NULL AND name = ? COLLATE NOCASE`, name).Scan(&d.AccountID, &d.Account, &d.Currency)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("no account is called %q; use the exact name from USER'S ACCOUNTS", name)
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

// selectIDs runs a change's read-only query and returns its first column.
func (s *Service) selectIDs(query string, win *TimeWindow) ([]string, error) {
	var ids []string
	err := withReadonlyRows(s.DB, s.today(), query, changes.MaxTransactions+1, s.queryArgs(query, win), func(rows *sql.Rows) error {
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

func decodeAny(raw json.RawMessage) any {
	var v any
	_ = json.Unmarshal(raw, &v)
	return v
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
