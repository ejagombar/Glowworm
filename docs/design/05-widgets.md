# 05 — Widget System

**Scope:** the JSON widget contract — types, data shapes, the canonical
dataset, presentation derivation, type conversion, sizing/pinning, and the
calculator. Widgets are **frontend-agnostic**: JSON in, any renderer out.

Related: [04](04-frontend.md) §7 rendering · [03](03-backend-api.md) §8 endpoints ·
[10](10-natural-language-query.md) how widgets are produced.

---

## 1. Principles (normative)

1. A widget is a **JSON object**; the exact visual specifics are **not**
   defined by the widget system. The JSON must be renderable as HTML/CSS
   today and by another stack (e.g. native QT) tomorrow [Requirements §Dynamic
   Widgets].
2. Widgets are **editable by the user** after creation: change chart type,
   title, size, pin state — without re-querying the backend [D13 nuance: chat
   edits convert client-side; pinned widgets persist and refresh server-side].
3. Monetary values inside widget data are **integer minor units** plus a
   `currency` field [D15]. Renderers format via the shared helper:
   `fmtMoney(minor, currency)` → `£1,284.50` (grouping, 2 dp, minus sign for
   negatives; charts' `k`-abbreviation operates on major units).
4. The LLM may emit one **or more** widgets per answer; simple widgets (text,
   number) are first-class [Requirements].

## 2. Widget object

```jsonc
{
  "id": "w-171",            // unique; server-assigned (or client temp id pre-pin)
  "type": "bar",            // bar|line|pie|table|text|number|transactions|calculator
  "title": "Transport spend over the past year",
  "data": { /* presentation-shaped payload, §4 */ },
  "dataset": { /* canonical dataset, §3 — present when derived from a query */ },
  "plan": { /* query plan, doc 10 — present when server-executable */ }
}
```

Rules:
- `data` MUST always be present and complete for the declared `type` (a
  consumer should never need `dataset` to render).
- `dataset` + `plan` are optional metadata enabling editing/refresh.

## 3. Canonical dataset

The single result shape every query produces; presentations are derived from
it by a pure function `derive(type, dataset)` implemented in **both** Go
(`core/widgets`) and JS (`widgets.js`).

```jsonc
{
  "columns": [
    {"key": "month",  "label": "Month",    "type": "string"},
    {"key": "amount", "label": "Transport", "type": "money"}   // string|number|money|date
  ],
  "rows": [
    ["2023-07", 9600], ["2023-08", 8800]   // money cells: minor units ints
  ],
  "meta": {
    "currency": "GBP",
    "row_count": 12,
    "truncated": false,                    // true if limit cut rows
    "total_minor": 128400,                 // when meaningful
    "entity_label": "Transport",           // main entity name for titles/captions
    "time_range": {"from": "2023-07-01", "to": "2024-06-30"}
  }
}
```

**Derivation rules (`derive`):**

| Target type | Rule |
|---|---|
| `bar`/`line` | x = first string/date column; series = each money/number column (name = column label; colour from palette by index). Single value column → one series. |
| `pie` | labels = first string column values; values = first money column; colours = palette by index. |
| `table` | columns verbatim (money cells pre-formatted label retained); rows verbatim. |
| `number` | `meta.total_minor` (or first money cell if no total): `{value: "£128.40", caption: entity_label + " · " + time_range label, trend?, trendText?}` |
| `text` | server/template sentence (`answer` field) → `{body}`. |
| `transactions` | query must return tx display rows (doc 03 §5.1) → `{transactions: [...]}`. |
| `calculator` | n/a (self-contained). |

Default presentation colour palette (prototype `WIDGET_COLORS`):
`#0e6f51 #4f5bc7 #c9822e #c0483f #2f7fb0 #7a58b8 #2e9d8f #6b7280`.

## 4. Widget types & `data` payloads (normative — prototype parity)

### 4.1 `bar`
```jsonc
{ "labels": ["Jul","Aug"], "series": [ {"name":"Transport", "color":"#4f5bc7", "values":[9600,8800]} ] }
```
Rendered as grouped SVG bar chart (prototype `barChartSVG`): nice-step y
ticks, gridlines, `<title>` tooltips `{series}: {fmtMoney(v)}`, category
labels on x. Multi-series grouping supported.

### 4.2 `line`
Same shape as bar; rendered as prototype `lineChartSVG`: area gradient fill,
dots with tooltips, first/last x labels (shortLabel ≤ 8 chars), min-aware
scale (balances may be negative).

### 4.3 `pie`
```jsonc
{ "labels": ["Groceries","Eating out"], "values": [11820,7460], "colors": ["#c9822e","..."] }
```
Donut SVG with centre total (`fmtMoney(sum)`) + legend rows with %; hover
`<title>` per slice. Renderer keeps the prototype's proportions
(168px chart in grid, flexible).

### 4.4 `table`
```jsonc
{ "columns": ["Merchant","Category","Total"], "rows": [["Tesco Express","Food & Dining","£142.20"]] }
```
Last column right-aligned mono (`td.num`). Values may be pre-formatted strings
(when server emits presentation data) or minor units + `meta.currency` (then
client formats). Prototype styling unchanged.

### 4.5 `text`
```jsonc
{ "body": "Your transport spend averaged £10.70 per month over the past year." }
```
Simple paragraph (`text-widget p`).

### 4.6 `number`
```jsonc
{ "value": "£128.40", "caption": "Of income this month",
  "trend": "up|down", "trendText": "3% vs last month" }
```
Big mono figure + caption + optional trend arrow (▲/▼ with income/expense
colours). `value` is a **formatted string**; renderer never reformats.

### 4.7 `transactions`
```jsonc
{ "transactions": [ /* display rows, doc 03 §5.1; uses date/description/category/amount/type */ ] }
```
Compact table: Date (`d MMM`), Description, Category, Amount (`+`/`−`/`⇄`
coloured). Respects the linked-transfer display rules when rows are grouped.

### 4.8 `calculator` (specified; optional to ship [D12])
```jsonc
{ "expression": "2450.00 - 1284.50", "result": "£1,165.50",
  "caption": "Income minus spend this month" }
```
The **expression is server-computed** by a validated arithmetic evaluator
(numbers, `+ - * / % ( )`, currency-aware minor-unit math); the LLM never
executes code. Render: mono expression (muted) + large result + caption.

## 5. Type conversion (drawer "Chart type" change)

Pure client function `convertWidgetData(from, to, data)` — prototype logic is
normative, extended with the dataset fast-path:

| From → To | Behaviour |
|---|---|
| same | identity |
| bar/line/pie ⇄ bar/line | re-shape series (`toSeries`) |
| any chart → table | labels × series → columns `[Label, …series names]`, money-formatted cells |
| any chart → pie | first series |
| any chart → number/text | `meta`-backed fallback; if `dataset` present use `derive`, else value/caption best-effort |
| number ↔ text | carry value↔body |
| → transactions | keep `transactions` data; otherwise **not offered** (drawer hides invalid targets) |
| table → chart | only when the widget has a `dataset` (server re-derives); drawer marks those options as available |

The drawer MUST only offer conversions that produce a valid result (disable
others with a tooltip "Not available for this data").

## 6. Sizes & grid

- Grid: 12 columns × 72px rows, 12px gap (matches `.widgets-grid`).
- Defaults (`spanCol/spanRows`): number/text/pie → col 4; transactions → col
  12, rows 4; bar/line/table → col 8, rows 4; rows: number/text 2, pie 3.
- Edit mode clamps: col 2–12, rows 1–8. Sizes persist on the pinned widget.

## 7. Pinned widgets lifecycle [F9]

1. **Pin** (from chat widget): `POST /api/v1/widgets/pinned` with widget JSON
   (incl. dataset+plan). Toast "Pinned to dashboard".
2. **Dashboard render**: server orders by `position`, re-executes `plan`
   (deterministic, no LLM) → fresh dataset → `derive(type)` → `data` [D13].
   Plan execution failure (e.g. entity deleted) → widget renders with a
   `stale` badge and last-known dataset.
3. **Edit** (drawer): title/type/pin/size → `PATCH`; type change re-derives
   server-side; chat copies convert client-side for instant feedback.
4. **Reorder/resize** (edit mode): `PUT /api/v1/widgets/pinned` full map.
5. **Unpin**: drawer switch off or `DELETE`; toast "Removed from dashboard".
6. Defaults for a fresh instance (seeded, prototype parity): bar "Spending by
   category — this month", number "Savings rate", pie "Spending mix — this
   month", table "Top merchants" — each defined as a **plan** so they stay
   live (plans: `spend_by_category`, `savings_rate`, `spend_mix`,
   `top_merchants`; doc 10 §7).

## 8. Frontend-agnosticism checklist

- No HTML/CSS class names, JS or DOM in widget JSON.
- All colours as hex strings; all money as minor units + currency; all dates
  ISO strings.
- A second renderer can be built from: §2 object, §4 payloads, §5 conversion
  table, §3 derivation rules.

## 9. Acceptance criteria

1. Every widget type renders correctly from `data` alone (golden HTML tests).
2. `derive()` (Go) and `convertWidgetData` (JS) agree on the conversion table
   §5 (contract tests with shared fixtures).
3. Pinned widgets refresh data on dashboard load without LLM calls.
4. Money in widgets is minor-unit integers + currency end-to-end; formatting
   matches `£1,234.56` conventions with privacy-mode blur intact.