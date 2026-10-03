# 08 — Transfers & Recurring Detection

**Scope:** automatic transfer detection between the user's own accounts,
manual linking/unlinking (with the suggestion-driven editor), display rules
for linked legs, and automatic recurring/bill detection.

Related: [02](02-data-model.md) §5.2/§6.1 · [03](03-backend-api.md) §5/§12 ·
[04](04-frontend.md) §5.2/§6 · [11](11-audit-history-timeline.md) stats exclusion.

---

## 1. Transfer model (recap — normative)

- A real-world transfer is **two transaction legs** (one per account
  statement), joined by a `transfers` row [D3].
- Outbound leg: `amount < 0`, on the sending account; inbound leg:
  `amount > 0`, on the receiving account. Both `type='transfer'`,
  category = system `Internal transfer`.
- Invariants (doc 02 §5.2): different accounts, one transfer per leg,
  amounts SHOULD match.
- **Statistics:** linked transfers are excluded from all spend/income
  aggregates (dashboard cards, category summaries, widgets, timeline
  `in-period` counts use `type != 'transfer'` for spend) [Backend §2.4].

## 2. Auto-detection algorithm

Runs: after each import confirm (over the batch + a ±7-day window of
existing rows), nightly sweep (cheap pass over recent unlinked rows), and
after manual edits that change amount/date/type/account.

```
candidates := unlinked transactions with no transfer (both legs)
for each candidate A (ordered by date desc):
  for each candidate B on a DIFFERENT account:
    if B.amount == -A.amount (minor-exact; tolerance 0 by default):
      dateGap := |A.date − B.date| ≤ 4 days (configurable)
      score   := 0.2 (exact amount)
               + 0.2 if dateGap ≤ 1
               + 0.5 if description(A or B) references an identifier
                       (account number, sort code, IBAN last4/full) of the
                       OTHER leg's account   [identifiers stored verbatim, doc 02 §3.1]
               + 0.2 if description matches transfer lexicon
                       (transfer|tfr|standing order|faster payment|to/from savings|
                        to/from {account name})
      if score ≥ 0.7 (configurable `transfer_threshold`) and both unlinked:
        create transfer(detected='auto', method, confidence=score)
        set both legs type/category/transfer_id  [audit `transfer.linked`, actor `system`]
```

Guard rails:
- Never auto-link two legs of the **same account** (even if a statement
  duplicates a line) — dedupe (doc 06 §8) handles same-account copies.
- Never auto-link when either leg already has a manual transfer.
- Ambiguity: if 2+ candidates tie at the same score for one leg, **no** auto
  link; the leg stays unlinked (the UI surfaces "possible transfer" in
  suggestions instead). Deterministic and safe.
- Auto-links are reversible like any change (audit revert unlinks).

## 3. UI display rules [D3]

| Surface | Rule |
|---|---|
| Transactions view, all accounts (`view=grouped`) | Two legs collapse to **one row**: representative = outbound leg; icon `⇄`; description `Source → Destination` account names; amount `⇄ £x` (transfer colour); `Linked` badge; `tr.linked` styling. |
| Statement view (per account) | Each leg shown **separately exactly as on that statement**, with To/From counterparty ("To Savings" / "From Current"). |
| Recent activity / recurring modal / transactions widget | Grouped (linked) display. |
| Statement-view totals & balances | Each leg counts on its own account (money left A, arrived B). |

## 4. Manual linking & unlinking [D6]

Endpoints (doc 03 §5.2): `link`, `unlink`, `transfer-suggestions`.

### 4.1 Linking flow (inline editor, doc 04 §5.2.4)

1. User opens the editor on a transaction, sets **Type = Transfer**.
2. Category locks to `Internal transfer`; a **counterpart select** appears,
   populated by `GET /transactions/{id}/transfer-suggestions`.
3. Save → `link` (creates `transfers(detected='manual')`) → both legs
   updated, audit `transfer.linked` (actor `user`, diff shows both legs).
4. The user can instead just pick a suggested counterpart — no type change
   needed if the row is already a transfer.

### 4.2 Unlinking flow

- `Break link` button in the editor (linked rows) → `unlink` → both legs
  revert to their pre-link type (`expense`/`income` by sign) with
  `category_source` reset to `none` (review queue), audit `transfer.unlink`.

### 4.3 Suggestions ordering (normative)

```
score := amount_exact (|B.amount| == |A.amount|)  ? 2.0
       : amounts within 1%                        ? 1.0 : 0
       + date proximity: 1 − min(|Δdays|,30)/30
sort desc by (score, |Δdays|, date desc); limit 50
```
Response includes each candidate's account, so the UI groups options by
account ("Current account — 3 Jun · Transfer to Savings · £500.00").

## 5. Editing linked legs

- Editing one leg's amount/date/description is allowed; the other leg is
  untouched (bank statements differ). The linked badge gains `⚠` when
  amounts no longer match (tooltip: "Amounts differ — review the other leg").
- Changing a leg's type away from `transfer` is blocked: the UI forces
  breaking the link first (confirm modal: "This will break the transfer
  link.").
- Deleting one leg breaks the link and deletes the row (audit both).

## 6. Recurring detection [F6] [D5] [D9]

Detects bills, direct debits, salaries, subscriptions — **automatic only**;
the badge is never user-settable [D5].

### 6.1 Algorithm

```
pool := all transactions (excluding linked-transfer legs) grouped by
        normalised description key (doc 07 §3) AND amount bucket
amount bucket := round(amount_minor, ±1%) tolerance
for each group with ≥ 3 members:
  dates sorted; gaps := Δdays between consecutive dates
  cadence := classify(median(gaps)):
     6–8   → weekly        12–16 → fortnightly
     27–33 → monthly       88–95 → quarterly
     360–370 → annually    else → irregular (skip)
  regularity := stdev(gaps)/mean(gaps) ≤ 0.2 required
  direction  := all members same sign (else split by sign)
  key        := sha256(normalised desc ‖ amount bucket ‖ cadence)[0:16]
  upsert recurring_keys row:
     label = most common raw description, cadence, interval_days=median,
     amount_minor = median amount, first/last seen,
     next_expected = last_seen + cadence (if ≤ today + 2 intervals → active)
  stamp members: transactions.recurring_key = key (audit once per sweep:
     `recurring.detected` actor system, diff `+ Netflix → monthly`)
```

- Idempotent: re-running produces identical keys; member edits (description/
  amount changed by user) drop that member out at the next sweep.
- Salaries/income recur identically (direction `in`); bills recur `out`.
- "Bill" is not a separate badge [D9]; a recurring **expense** series is what
  users experience as bills; the requirement's "Automatic Bill Detection" is
  satisfied by this detector.

### 6.2 UI (prototype parity)

- `Recurring` badge (bill-amber) on member rows in every transaction list;
  clicking opens the **Recurring series modal**: series label, `{n}
  transactions · {period} · auto-detected series`, total header
  (`Total spent` / `Total received`; transfer series → `Total transferred`
  with legs grouped), rows ordered by date ascending.
- Settings/queries may list series via `GET /api/v1/recurring` (future
  surfaces; not in v1 UI beyond the modal).

## 7. Statistics integration (normative)

- Spend aggregates (dashboard, category cards, widgets, plans): include
  `type='expense'` only.
- Income aggregates: `type='income'` only.
- Transfers appear only in: transaction lists, balance replay (both legs),
  recurring series viewer.
- The timeline "Transactions in period" counts exclude linked duplicates by
  counting transfer legs once (via the grouped view).

## 8. Acceptance criteria

1. Fixture: two statements (current + savings) with crossing transfers →
   auto-linked with correct legs, methods and audit entries; totals exclude
   them from spend.
2. Ambiguous pair (two equal-amount candidates same day) → no auto-link;
   both appear in suggestions sorted by §4.3.
3. Manual link/unlink round-trip via API and via the inline editor works and
   is audited.
4. Recurring: monthly salary + monthly Netflix + weekly coffee fixtures
   detected with correct cadences; an irregular group is not labelled.
5. Series modal lists all members over time; period selector filters it.
6. Nightly sweep is idempotent (re-run produces zero diffs).