# 10 — Natural-Language Query & Chat

**Scope:** the dynamic data viewer: question → plan → safe SQL → dataset →
widgets; reasoning-step display; the chat UX with streaming; conversation
persistence; guardrails.

Related: [05](05-widgets.md) datasets · [09](09-ai-integration.md) §6.3 ·
[03](03-backend-api.md) §9 · [04](04-frontend.md) §5.4 · [11](11-audit-history-timeline.md) aggregates.

---

## 1. Feature recap [F7]

The user asks e.g. *"How much have I spent on Transport over the past
year?"*. Glowworm:

1. Interprets the question (LLM).
2. Detects entities — "Transport" is a **category**; assumes **all bank
   accounts**; extracts the time range.
3. Produces a safe, validated **query plan** (never raw SQL [D14]) and
   executes it.
4. Shows **highlights of the thought process** (prototype `steps` parity,
   including the generated SQL as a display string).
5. Renders the result as one or more **widgets** (bar/line/pie/table/number/
   text/transactions), which the user can retarget, resize and pin.

## 2. Pipeline

```
question text
   │
   ▼
[LLM query_planning]  ──► Plan JSON (draft)
   │
   ▼
[validate + entity resolution]  ──► candidates fuzzy-matched against DB
   │        (one LLM correction round if ambiguous, then fail→clarify)
   ▼
[plan compiler]  deterministic, whitelisted, parameterised SQL
   │
   ▼
[execute] ──► Dataset (doc 05 §3)
   │
   ▼
[presentation selector] chart_hint ∨ heuristics ──► widget(s) data
   │
   ▼
[steps builder]  human-readable highlights incl. rendered SQL
   │
   ▼
[answer sentence]  template-driven from dataset (no extra LLM call)
   │
   ▼
persist message(+dataset+plan) ──► stream to client
```

## 3. Plan schema (normative)

```jsonc
{
  "v": 1,
  "metric": "spend|income|net|balance|count|list",      // spend = Σ expense tx (excl. transfers)
  "entity": {                                            // optional
    "kind": "category|account|merchant|recurring",
    "id": "c1...",              // MUST resolve to an existing row (validated)
    "resolved_name": "Transport",
    "match": "exact|fuzzy"      // fuzzy = LLM matched a synonym; steps note it
  },
  "breakdown": "none|category|account|merchant|month|week|day",
  "time": {"preset": "last-12m|null", "from": "2023-07-01", "to": "2024-06-30"},
  "account_scope": "all|savings|spending|single",        // single → account entity holds the id
  "sort": "value_desc|date_asc",
  "limit": 12,
  "chart_hint": "bar|line|pie|table|number|transactions",
  "title": "Transport spend over the past year"
}
```

Validation rules (hard failures → clarification):
- `entity.id` must exist (category/account) or be a known merchant
  (≥1 tx) — else return a clarification message listing close matches as
  chips ("Did you mean Transport or Travel?").
- `metric=balance` requires breakdown `month|week|day` (a series) or `none`
  (number = current balance).
- `time.from ≤ time.to`; presets resolved server-side (same semantics as the
  period selector, doc 03 §1).
- `limit` clamp 1–50; breakdown row cap 200.
- Unknown fields rejected (strict decode).

## 4. LLM planning prompt (contract)

System = common preamble (doc 09 §5) +:

> Translate the user's question into a query plan JSON. Available categories:
> [names…]. Available accounts: [names…]. Known merchants (most frequent):
> [names…]. Today is {date}. Rules: "spend" sums expense transactions;
> transfers are never spend or income. If the user names one of the
> categories/accounts, use it verbatim. If they name something similar,
> choose the closest and set match="fuzzy". If they don't specify accounts,
> use account_scope="all". If they don't specify a time range, use
> preset="last-12m" for trend questions, preset="last-month" for
> "this month".

Few-shot examples (3, pinned in prompts.go) cover: category over time,
breakdown pie, single number.

## 5. SQL compiler (whitelist)

One Go function per (metric, breakdown) pair — a **fixed table**, parameterised
only by bound values:

| metric | breakdown | SQL shape (SQLite/PG portable) |
|---|---|---|
| spend/income/net | month/week/day | `SELECT strftime('%Y-%m', date) AS bucket, SUM(-amount_minor) … FROM transactions WHERE type='expense' AND category_id=? AND date BETWEEN ? AND ? GROUP BY bucket ORDER BY bucket` (sign folded per metric; income sums `+amount`) |
| spend/income/net | category/account/merchant | `SELECT {dim}, SUM(…) … GROUP BY {dim} ORDER BY 2 DESC LIMIT ?` (merchant = `COALESCE(NULLIF(merchant,''), description)`) |
| balance | month/week/day | replay series (doc 11 §5) sampled per bucket |
| balance | none | `balance(account, today)` total |
| count | any | `COUNT(*)` variants |
| list | none | display-row query, `view=grouped`, order date desc, limit N |

All identifiers come from code, all values from bound parameters — **no
string interpolation of user input, ever**. The compiled SQL (with literals
substituted for display only) is what the `steps` show, exactly like the
prototype's "Generated query: SELECT month, SUM(amount) …" line.

## 6. Presentation selection

1. `plan.chart_hint` wins if compatible with the dataset.
2. Heuristics: time-series rows (≥3 buckets) → `line` (balance/net) or `bar`
   (spend/income); small categorical breakdown (≤8) → `pie`; larger → `bar`;
   `metric=list` → `transactions`; single scalar → `number`.
3. `answer` text: template per metric, e.g.
   *"You spent **£128.40** on Transport between Jul 2023 and Jun 2024 —
   averaging £10.70/month."* — computed from the dataset (totals, average,
   top bucket) with no second LLM call.

## 7. Reasoning steps (prototype parity)

Built server-side, ordered:

1. `Detected “Transport” is a category ✓` (entity resolution; fuzzy matches
   note the assumption).
2. `Assuming all bank accounts` (or the account scope chosen).
3. `Generated query: SELECT month, SUM(amount) FROM tx WHERE category =
   'Transport' GROUP BY month` (display SQL).
4. Optional: `Limited to the {n} largest categories`, `Rows truncated to
   {n}`.

Rendered in the collapsible "How I worked this out" block with tick icons —
streamed as `steps` NDJSON events as they become known (entity → scope →
query → data), giving the progressive feel of the loader choreography.

## 8. Chat UX & streaming (normative)

- Send: `POST /queries/messages` `{text, conversation_id}` → creates user
  message, returns user bubble + pending assistant bubble with worm loader.
- Stream: `POST /queries/messages/stream` (fetch + NDJSON):
  `{"event":"steps","items":[...]}` → append ticks; `{"event":"text",
  "text":...}` → set answer; `{"event":"widget","widget":{...}}` → render
  widget card (pinnable); `{"event":"done","message_id":...}` →
  worm-out → reveal; `{"event":"error","message":...}` → inline error text.
- Multiple widgets per answer: multiple `widget` events [Requirements].
- Conversations: sidebar groups messages by day (client-side grouping from
  `created_at`); switching conversation swaps the message region;
  `DELETE` removes it (confirm modal). Auto-title = first question
  (truncated) unless renamed.
- Widget interactions: pin (creates pinned widget), settings drawer (type/
  title/pin; conversions client-side), all per docs 04/05.
- Suggestion chips (static, prototype copy) remain under the composer.

## 9. Guardrails (normative)

- Plans only — the LLM cannot express SQL, identifiers, or column names [D14].
- Entity whitelist: only the user's own categories/accounts/merchants.
- Row/limit caps; no full-table dumps (`list` caps at 50).
- Rate limit: 10 queries/min per session (429 → toast).
- Question text ≤ 500 chars.
- Every query+plan is stored with the message (transparency, replay) and
  audit-logged (doc 09 §7).

## 10. Endpoints & persistence

Doc 03 §9. `messages` rows persist text, steps, widget (with dataset) and
plan; chat history survives reloads; day grouping is presentation-only.

## 11. Pinned-widget plans (doc 05 §7)

Plans are stored with pinned widgets and re-executed without the LLM — the
compiled query is cached by plan hash. Defaults seeded on first run:
`spend_by_category` (bar), `savings_rate` (number), `spend_mix` (pie),
`top_merchants` (table) — matching the prototype's default dashboard.

## 12. Acceptance criteria

1. The prototype's four suggested prompts each return the expected widget
   type and steps on fixture data.
2. An unresolvable entity returns a clarification with suggestion chips
   (never a wrong chart).
3. Compiler is table-driven; a fuzz test asserts every emitted query uses
   only whitelisted templates with bound params.
4. Streaming renders steps progressively and completes with the worm-out
   choreography; conversation reload reproduces the same message.
5. Pinning a chat widget then editing its type in the drawer updates the
   dashboard card without re-querying (client convert) and the dashboard
   re-fetches fresh data on next load (server plan re-run).