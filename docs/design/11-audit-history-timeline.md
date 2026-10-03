# 11 — Audit Log, History, Timeline & Reconciliation

**Scope:** the rich audit trail (diffs, actors, revert) and the **history
engine** — entry states and paths, jump-back, path restore, per-entry
Forget/Delete — plus the timeline viewer (state at any date), balance replay
and reconciliation status.

Related: [02](02-data-model.md) §8.3 · [03](03-backend-api.md) §10 ·
[04](04-frontend.md) §5.7 · [09](09-ai-integration.md) §7.

---

## 1. Principles [F13]

- **Every** mutation — by user, AI, or system — creates an audit entry, in
  the same SQL transaction as the change [Backend §9/§14].
- Entries are a **rich log, not text**: structured before/after plus a
  renderable diff (`+ / −` lines) — git-like.
- The log is **engine-owned** [D25]: entry *content* (title, sub, diff,
  before/after/revert payloads) is immutable once written, but the **audit
  engine** (`core/audit`) alone may transition an entry's *state*, re-apply
  it, or remove records — always atomically and mirrored by meta entries
  (§4). This is what makes time travel (jump back), path restore and
  per-entry deletion possible.
- Each entry carries enough data to **revert**, **re-apply (restore)** and —
  where the engine permits — **disappear** from the log.

## 2. Entry anatomy

```jsonc
{
  "id": 128,                       // monotonic
  "timestamp": "2024-06-12T09:12:00Z",
  "actor": "ai",                   // user | ai | system
  "actor_detail": "claude-3-5-sonnet",
  "action": "categorisation.applied",
  "entity_type": "transaction_batch",
  "entity_id": "batch:...",
  "title": "Categorised 12 transactions",
  "sub": "From Monzo May statement",
  "diff": [
    {"line": "+ 6 → Food & Dining",  "cls": "diff-add"},
    {"line": "+ 4 → Transport",      "cls": "diff-add"},
    {"line": "+ 2 → Bills & Utilities", "cls": "diff-add"}
  ],
  "before": { /* entity snapshot / field map */ },
  "after":  { /* entity snapshot / field map */ },
  "revert":  { /* revert payload, §4 */ }
}
```

UI rendering = prototype `.history-item` exactly: actor-coloured icon tile
(Y / sparkle / import-box by `kind` derived from actor+action), `who`-coloured
actor in the title, sub line, mono diff block (`diff-add` green `+`,
`diff-del` red strikethrough `−`), timestamp, `Restore` button.

## 3. Action catalogue (normative)

| Action | Actor | before/after | Revert |
|---|---|---|---|
| `account.created` | user | — / full row | delete account (cascade) |
| `account.updated` | user | field diffs | restore fields |
| `account.deleted` | user | full row / — | *(not revertible — delete is destructive; entry retains snapshot for reference)* |
| `valuation.created` | user/system | {computed_before} / {value,date} | delete valuation |
| `category.created` `category.updated` `category.deleted` | user | field diffs (+ affected tx count on rename/delete) | inverse op |
| `transaction.created` | user/import | — / row | delete row |
| `transaction.edited` | user | field diffs (date, description, amount, category, type, account) | restore fields |
| `transaction.deleted` | user | row / — | recreate from `before` |
| `transfer.linked` | user/system | legs before / legs after | unlink |
| `transfer.unlink` | user | legs before / legs after | re-link |
| `recurring.detected` | system | — / members+key | clear keys (rarely used) |
| `import.confirmed` | user | counts / per-category summary | delete batch (doc 06 §10) |
| `import.reverted` | user | — / count | (no auto re-import) |
| `import.schema_detected` | ai | — / mapping+confidence | — |
| `schema.saved` `schema.updated` `schema.deleted` | user | mapping diffs | inverse |
| `categorisation.applied` | ai | per-row source map | restore sources |
| `widget.pinned` `widget.updated` `widget.unpinned` `widget.reordered` | user | widget JSON diffs | inverse |
| `settings.ai.updated` | user | masked config diff | restore |
| `classifier.retrained` | system | {version} / {version, metrics} | restore previous model row |
| `audit.reverted` | user | — / {reverted_entry} | *(no re-apply)* |
| `audit.jumpback` | user | — / {to_entry, deactivated:[ids]} | — (see §4) |
| `audit.path.activated` | user | — / {path, reactivated:[ids], shelved:[ids]} | — (see §4) |
| `audit.entry.deleted` | user | — / {mode: forget\|undo, removed:[ids]} | — (tombstone for history surgery) |
| `setup.completed` | user | — / flag | — |

Diff style guide: field diffs render as `− old` / `+ new` pairs (e.g.
`− £500 · Current account` / `+ £500 · Savings` — prototype History entry 5).
Money rendered with `£` formatting; dates `2 Jun 2024`.

## 4. History engine (normative) [D25]

The audit log is a **branchable timeline**, owned entirely by the audit
engine (`core/audit`). It supports five operations; what is possible for
each item is decided by the **engine's capability matrix** (§4.3), never by
handlers or the UI.

### 4.1 Entry states & paths

```
audit_entries.state  ∈ active | inactive      (engine-managed column)
audit_paths          id, parent_path_id, fork_entry_id, label, is_active, created_at
```

- Every entry belongs to a **path** (`path_id`, immutable at insert). There
  is exactly one root path (`main`, `fork_entry_id = NULL`). Exactly one
  path is `is_active = 1`.
- **Jump back** to entry `E` creates a new path forked at `E`. Every active
  entry with `id > E.id` is **physically reverted** (its effect leaves the
  live data, reverse seq order) and marked `state = 'inactive'` — but the
  records, with their full payloads, survive so the range stays visible and
  **restorable**.
- The application's **active state** is defined by the active path's
  ancestor chain: each ancestor path's entries with `id ≤ fork_entry_id`,
  plus all entries of the active path. Those entries are `state='active'`;
  all others are `state='inactive'`.
- Inactive entries render **collapsed** in the UI (§7): expandable, viewable,
  but not live.

### 4.2 Operations

| Operation | Effect |
|---|---|
| `Revert(e)` | Undo the effect of entry `e` via the normal service layer, append `audit.reverted` (recorded in `audit_reverts`). The record remains in the timeline but is **excluded from future path re-application**. |
| `JumpBack(e)` | Create path `P'` (parent = path of `e`, `fork_entry_id = e.id`, active); revert + mark inactive every active entry with `id > e.id`; append `audit.jumpback {to_entry, deactivated:[ids]}`. State of the app = as of `e`. |
| `RestorePath(P)` | **Restore a previously jumped-away path.** (1) Physically revert every currently-active entry that is *not* in `P`'s chain-union (reverse seq order) and mark them inactive — including any newer work done since the jump. (2) Re-apply, in seq order, every entry of `P`'s chain-union that is inactive — skipping entries previously reverted (reported). (3) Mark `P`'s chain active, shelve the previously active path; append `audit.path.activated {path, reactivated:[…], shelved:[…]}`. This restores the original path **even after new changes were applied**: the new changes are themselves shelved (and remain restorable by the same mechanism). |
| `Forget(e)` | **Record-only deletion:** the effect stays applied; the entry record is removed from the log (engine-permitted items only, §4.3). A tombstone meta entry `audit.entry.deleted {mode:"forget"}` records the surgery. E.g. keep an AI-made link, remove the trace. |
| `Delete(e)` | **Undo + scrub:** revert the effect (where the entry has one, subject to §4.4) **and** remove the entry record plus its derived revert links. One tombstone meta entry remains. The change disappears from history as if it never happened — **without touching any preceding changes**. E.g. an incorrect AI auto-link: unlinked and scrubbed in one step. |

All operations run in one SQL transaction. `JumpBack` and `RestorePath`
never destroy records — every path stays restorable until its entries are
explicitly Forgotten or Deleted.

### 4.3 Capability matrix (engine-decided, per item) [normative]

`GET /api/v1/audit/{id}/capabilities` returns the engine's verdict; the UI
MUST render only available operations and surface blocked reasons as
tooltips. Default policy lives in `core/audit/policy.go`:

| Action | Revert | Jump-back participation | Forget | Delete |
|---|---|---|---|---|
| `account.created` | ✓ (soft-delete; blocked if live transactions) | ✓ | ✓ (warns: earlier restores won't recreate it) | ✓ |
| `account.updated` | ✓ | ✓ | ✓ | ✓ |
| `account.deleted` | ✗ (destructive tombstone) | ✓ | ✓ | ✓ |
| `valuation.created` | ✓ | ✓ | ✓ | ✓ |
| `category.created/updated/deleted` | ✓ (soft) | ✓ | ✓ | ✓ |
| `transaction.created/edited/deleted` | ✓ (blocked rules §4.4) | ✓ | ✓ (warns: path restores will skip it) | ✓ (§4.4) |
| `transfer.linked` / `transfer.unlink` | ✓ | ✓ | ✓ (keeps the link, drops the trace — the motivating case, AI or manual) | ✓ (undoes the link and scrubs both records) |
| `recurring.detected` | ✓ (clears keys) | ✓ | ✓ | ✓ |
| `import.schema_detected` | ✗ (record-only) | ✓ | ✓ | ✓ |
| `import.confirmed` | ✓ (blocked rule §4.4) | ✓ | ✗ (orphans the batch audit) | ✗ (revert first) |
| `schema.*`, `widget.*`, `settings.ai.updated` | ✓ | ✓ | ✓ | ✓ |
| `categorisation.applied` | ✓ | ✓ | ✓ | ✓ |
| `audit.*` meta entries | ✗ | ✓ (markers participate as no-ops) | ✓ | ✓ (once their groups are gone) |

### 4.4 Blocked rules (revert **and** Delete-with-undo)
- `import.confirmed`: blocked if any imported tx was edited/deleted after
  commit (doc 06 §10).
- `account.created`: blocked if the account has live transactions.
- `transaction.deleted`: blocked if the tx was a transfer leg whose peer was
  also mutated since.
- Any entry whose entity is referenced by a **later** entry's payloads
  (e.g. a later edit of the same transaction): blocked for `Delete` with the
  reason "a later change depends on this one".

### 4.5 ID stability & soft deletion [normative]

Path restore re-applies stored payloads, so entity **IDs must survive** a
revert/restore round-trip:
- Reverting the creation of an entity (`transaction.created`,
  `account.created`, `category.created`, `valuation.created`,
  `transfer.linked`) **soft-deletes** it (`deleted_at` set, row retained);
  restoring un-deletes it. Doc 02 §2 conventions apply.
- Hard `DELETE` is reserved for explicit, confirmed destructive endpoints
  (e.g. account delete) and engine purges — never an audit-driven revert.

### 4.6 Interaction rules (engine)
- Deleting/forgetting records **inside a shelved range** removes them from
  that range; the range stays restorable minus those records, and
  `RestorePath` reports the skips.
- Per-entry `Revert` is unavailable on `inactive` entries ("already inactive
  — restore the path instead").
- Jump-back to a point **on a shelved path's tail** re-applies that tail up
  to the jump point (§4.2 step 2) — i.e. any past point of any path is a
  valid jump target.
- The prototype's `Restore` button becomes **Jump back** (label and toast
  copy unchanged); extra operations live in a `⋯` menu (§7).

## 5. Balance replay & timeline [F12]

### 5.1 Replay (normative formula, doc 02 §3.2)

```
balance(account, D) = latest valuation V (V.date ≤ D)
    ? V.value_minor + Σ tx.amount_minor for V.date < tx.date ≤ D
    : opening_balance_minor + Σ tx.amount_minor for opening_date < tx.date ≤ D
```

- Per-account SQL: one indexed scan + one valuation lookup. Instance totals
  sum accounts by type (`available` = non-savings, `savings` = savings).
- Transactions counted "in period" for a date D = txs with `date ≤ D` on the
  account (timeline card).

### 5.2 Series endpoints
`GET /api/v1/timeline?from&to&interval=day|week|month&account_id=` returns
`labels[]` + one series per requested account (or totals), plus per-account
current values for the timeline account list.

### 5.3 Snapshot
`GET /api/v1/snapshot?date=` → per-account balances at date, totals, tx
count, reconciliation status (§6). Powers the timeline slider fragment.

### 5.4 Performance note
Computed on demand (indexed); at doc 00 §7 scale this is <10ms. Escape hatch
(a `daily_balances` cache) is documented but not built (doc 02 §10).

## 6. Reconciliation [normative]

A **reconciliation** = recording a known statement balance
(`POST /api/v1/reconcile {account_id, date, value_minor}`):

1. Compute `computed_minor = balance(account, date)` **before** the
   correction.
2. Create the valuation (`source='manual'`) with
   `drift_minor = value_minor − computed_minor` and audit
   `valuation.created` (diff `− £computed` / `+ £known`, valuation date).
3. Return `{drift_minor, computed_minor}` — the UI toasts "Saved — £X
   difference corrected" when drift ≠ 0.

**Status per account at date D** (timeline "Reconciled" card):

| Condition | Status |
|---|---|
| Latest valuation ≤ D has `drift_minor == 0` | `verified` ("In sync") |
| Latest valuation ≤ D has `drift_minor != 0` | `diverged` ("N divergences" — action: open that account's transactions around the valuation date) |
| No valuation ≤ D | `unverified` ("Unverified") |

Aggregate card = worst-of: any `diverged` → divergences count; else any
`verified` → "In sync"; else "Unverified". Divergences are actionable: the
card links to `/transactions?account={id}` and the audit entries for the
valuation.

## 7. UI behaviour recap (prototype parity)

- **Timeline mode:** slider over all months in the data range (min month →
  current), chip `Latest`/`N months back`, ≤7 ticks, debounced snapshot
  fetch; stat cards; clickable account rows scoping the chart; line chart up
  to the selected date.
- **Audit mode:** filter chips map to `actor`/action classes: All · By you
  (`actor=user`) · By AI (`actor=ai`) · Imports (`action LIKE 'import%'`).
  Rows render diffs.
- **Per-entry actions:** the prototype's `Restore` button = **Jump back**
  (label + toast copy unchanged); a `⋯` (`.widget-btn`) menu exposes the
  engine's other capabilities for that entry: `Revert change`, `Keep effect,
  remove record` (Forget), `Delete from history` (danger — confirm modal
  naming the tombstone). Options render only when the engine permits them
  (§4.3); blocked reasons show as tooltips.
- **Collapsed groups:** after a jump back, the superseded entries render as
  one collapsed block inline where they sit chronologically — a
  `.msg-reason`-style `<details>`: header `{n} hidden actions · jumped back
  to {date}`, expandable to the rows rendered muted (no live actions inside
  except the group's `⋯` → `Restore these actions`), restorable at any time
  (§4.2 `RestorePath`).

## 8. Acceptance criteria

1. Fixture script performs 15 mixed mutations; audit list shows every one
   with correct actor/diff; capabilities match the §4.3 matrix.
2. **Jump back:** after jumping to entry 8, the live data equals the state
   at entry 8; entries 9–15 render collapsed (expandable, muted); all
   balances/replays reflect the deactivated effects.
3. **Restore path:** after new changes (16–18), restoring the original path
   (9–15) shelves 16–18 and re-applies 9–15 with IDs preserved (soft-deleted
   rows un-deleted); the result is byte-identical to the original state at
   entry 15. Shelved 16–18 remain restorable afterwards.
4. **Delete:** an AI `transfer.linked` entry is deleted from history (undo +
   scrub) with all other entries untouched; a tombstone meta entry remains;
   blocked cases (§4.4) return the reason verbatim.
5. Replay formula matches hand-computed ledgers including: opening balance
   date, multiple corrections, transfer legs on both accounts — and excludes
   soft-deleted rows.
6. Reconciliation flow records drift and flips the timeline card from
   "Unverified" → "In sync"; a deliberate mismatch yields "divergences" with
   a working link.
7. Audit list filters (All/you/AI/imports) map correctly; golden template
   tests pin the entry markup and the collapsed-group markup.