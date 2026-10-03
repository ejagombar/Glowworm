# 06 — Import Pipeline

**Scope:** everything between "user drops a file" and "transactions exist":
format detection, parsing, schema matching/learning, the review UI, commit,
dedupe, undo, and error handling.

Related: [02](02-data-model.md) §7 tables · [03](03-backend-api.md) §7 endpoints ·
[07](07-categorisation.md) post-import cascade · [08](08-transfers-recurring.md) detection jobs ·
[04](04-frontend.md) §5.6 UI.

---

## 1. Pipeline overview

```
upload ──► sniff format ──► match saved schema ──►(miss)──► LLM detection ──►(fail/off)──► manual mapping
                                   │                    │                        │
                                   ▼                    ▼                        ▼
                                    parse with mapping (deterministic parser)
                                   │
                                   ▼
                        stage rows (import_staging) + dedupe flags
                                   │
                                   ▼
                        categorisation cascade (doc 07) per row
                                   │
                                   ▼
                        USER REVIEW (preview schema → rows)
                                   │  confirm
                                   ▼
              commit txs ──► transfer detection ──► recurring detection
                                   │                      │
                                   ▼                      ▼
                         classifier retrain            audit
```

Every stage is a job (doc 01 §6); UI polls/streams stage transitions.

## 2. Accepted inputs

Priority per owner decision [D23]: **Excel (.xlsx) is a first-class,
prioritised format — above photos.** The photo/vision path is optional
(post-v1); it must never block the core pipeline.

| Format | Parser | Priority | Notes |
|---|---|---|---|
| CSV / TSV | `importer/csv` | v1 core | Delimiter sniff (`,` `;` `\t` `|`), quote handling, BOM, encoding detection (UTF-8 default; fall back Latin-1), header detection. |
| **Excel (`.xlsx`)** | `importer/xlsx` | **v1 priority** | Pure-Go `github.com/xuri/excelize/v2` (no cgo). Per-sheet selection (default: first non-empty sheet; sheet picker when a workbook has >1); typed cell reads — dates from serial numbers + cell number formats, numeric cells read as numbers (never re-parsed from formatted strings, preserving minor-unit exactness); `has_header` default true; merged/spacer rows skipped. Legacy `.xls` (BIFF) is best-effort and may be deferred. |
| OFX (v1 SGML & v2 XML) | `importer/ofx` | v1 core | `STMTTRN` blocks: `DTPOSTED` (`YYYYMMDD*`), `NAME`/`PAYEE`, `MEMO`, `TRNAMT`, `FITID` (→ dedupe key). |
| QIF | `importer/qif` | v1 core | `!Type:Bank` records: `D` date, `T` amount, `P` payee, `M` memo; `!Account` headers. |
| PDF | `importer/pdf` | v1 core | Text extraction (pure-Go). Tabular heuristics (column x-coordinates). Scanned/image PDFs → optional vision path. |
| Images (jpg/png) — "camera photos" | optional vision path | **Optional / post-v1** | OCR + structure extraction via the optional `vision_ocr` capability (doc 09 §6.6); result normalised to the same column-mapping model. Only when a multimodal provider is configured; otherwise these inputs get the standard `unsupported` error. The dropzone copy ("anything goes") stays verbatim — the UI is final. |
| Dividend / interest history exports | generic CSV path | v1 core | Amount columns may be split (`Dividend`, `Interest`) — mapping supports multiple amount columns summed into one signed amount, with a `type_hint`. |

Multi-file upload: each file becomes its own batch (prototype dropzone is
`multiple`).

## 3. Format sniffing

Deterministic first (extension + content probes: `%OFX` header, `!Type:`,
PDF magic, xlsx ZIP signature (`PK\x03\x04` + `xl/` entries) → sheet list,
delimiter statistics). If ambiguous → treat as CSV (most common)
and let mapping fix roles. Vision OCR is an **optional** path used only for
images and scanned PDFs (no text layer) when a multimodal provider is
configured — not part of the core pipeline (doc 09 §6.6).

## 4. Account resolution [D21]

1. If the upload specifies `account_id` → locked.
2. Else the detector scans the raw text for the user's account identifiers
   (account numbers, sort codes, IBAN fragments) → pre-selects the matching
   account ("the AI will detect the account" — prototype dropzone copy).
3. Else the schema stage asks the user to pick (account select styled
   `.field.select`), defaulting to the most recently used account.
4. Statement files that internally reference **another** of the user's
   accounts (e.g. `To Savings *8823`) do **not** split batches; the
   identifiers feed the transfer detector instead (doc 08 §4).

## 5. Schema detection

### 5.1 Order [D22]

1. **Saved schema fingerprint match** — `sample_hash` =
   `sha256(kind ‖ normalised header row ‖ delimiter)` (OFX/QIF: kind +
   top-level tags). Exact hash match → auto-apply, confidence 1.0, show as
   "Matched saved schema “Monzo CSV”". Near-match (header distance < 0.15
   Levenshtein-normalised) → propose with the saved mapping pre-filled.
2. **LLM detection** (`capability: schema_detection`, doc 09 §6.1) over the
   header row + 5 sample lines + (PDF) extracted text sample. For xlsx the
   sample lines come from the selected sheet with typed cell values.
3. **Manual mapping** — always available, pre-filled from whichever
   detection ran.

### 5.2 Mapping model (normative JSON)

```jsonc
{
  "kind": "csv",
  "has_header": true,
  "delimiter": ",",
  "columns": [
    {"source": "Date",         "role": "date",        "date_format": "02/01/2006"},
    {"source": "Description",  "role": "description"},
    {"source": "Amount",       "role": "amount",      "decimal_separator": ".", "negatives":"signed"},
    {"source": "Type",         "role": "type_hint"}
  ],
  "bank_hint": "Monzo",
  "proposed_name": "Monzo CSV",
  "confidence": 0.98
}
```

Roles: `date, description, amount, amount_in, amount_out, currency,
balance, type_hint, memo, ignore`. Rules:
- `amount` = signed column; `amount_in`/`amount_out` = debit/credit pair
  (either role set allowed, never both).
- Multiple amount columns (dividends) → repeated `amount` roles **summed**.
- `date_format` uses Go reference layout; detector proposes from common
  formats (`2006-01-02, 02/01/2006, 01-02-2006, 2 Jan 2006, 20060102`, …)
  by successfully parsing ≥90% of sample values.
- Amount parsing: strip currency symbols/spaces, handle thousands
  separators, parentheses negatives, trailing `DR`/`CR`.
- `kind=xlsx`: add `"sheet"` (selected sheet name; default = first
  non-empty sheet). `delimiter` is ignored; `has_header` defaults true.
  `date_format` is unnecessary for formatted date cells (read typed) but
  applies when the sheet stores text dates.

Saved-schema fingerprints for xlsx include the selected sheet name:
`sample_hash = sha256(kind ‖ sheet ‖ normalised header row)`.

### 5.3 AI schema naming

After a successful parse the LLM proposes a name (`capability:
schema_naming`) — e.g. "Monzo CSV" from the bank hint — user-editable in the
save step [Requirements §AI Assisted importer].

## 6. Staging & validation

For each parsed row:

- `date` required (unparseable rows fail the whole stage with the first
  error reported; the fix-mapping UI allows re-parse).
- `amount_minor` computed from string per mapping; zero amounts allowed but
  flagged `needs_review`.
- `description`: `description` role, else `memo`, else `name`. Empty →
  `needs_review`.
- `dedupe_hash` (doc 02 §5.1) compared against existing transactions for the
  target account → `duplicate_of` set (row shown ticked "duplicate — skip" by
  default, user can force-import).
- `type` initial guess: signed amount → `income` if > 0 else `expense`;
  refined later by transfer detection at commit.
- Categorisation cascade runs **per staged row** (doc 07 §2): rules → local
  → (AI, batched) → `needs_review=1` when source=`none` or confidence below
  threshold. Rows keep `category_source`/`category_confidence`.

## 7. Review UI (spec — the prototype does not include this screen) [F3]

Rendered in the Import view (doc 04 §5.6 stage 4) using existing primitives.

**Header row:** `Importing into: {account card inline}` · `{n} rows ·
{date range}`; primary button `Import {n} transactions`; ghost `Cancel`.

**Toolbar (`.filters.panel`):**
- Chip `Needs review ({n})` — filters to uncertain rows (default **on** when
  n > 0).
- Chip `Duplicates ({n})`.
- Button `Accept all confident ({n})` — sets every row with
  `needs_review=0` to accepted.
- Category select + `Apply to selected`.

**Table (`.tx-table`):** per row: ✔/⚠ status, Date, Description (editable
inline), Amount (signed, mono), **Proposed category** (dot + name +
`source` badge: `rules`→`local` style, `ai` sparkle-blue, none → `needs
review` amber badge) with confidence %, actions:
- Category cell is a `<select>` of **type-masked** categories (expense rows
  → spend categories, income → income categories [doc 02 §4]).
- `Apply to similar` button — applies the chosen category to all staged rows
  with the same normalised description (shows affected count in tooltip;
  prototype History demonstrates the concept: "one correction fixes every
  occurrence").
- Duplicate rows: checkbox pre-ticked under a `Duplicates` group; unticking
  imports them anyway (audit notes it).

**Footer summary:** `{n} will be imported · {d} duplicates skipped · {r}
still uncategorised` — importing with uncategorised rows is allowed (they
land `category_source='none'` and appear in the review queue, doc 07 §8).

**Behaviour:** any change re-renders the fragment (HTMX). Confirm →
`POST /imports/{id}/confirm` → success panel + toast + link to the new
transactions.

## 8. Dedupe & re-import

- `dedupe_hash = sha256(account_id ‖ date ‖ amount_minor ‖
  normalised(description))` where `normalised` = doc 07 §3.1 pipeline
  truncated to 64 chars.
- Existing hash → staged as duplicate (default skip). OFX `FITID`/QIF
  presence adds `fitid:{value}` alternative hash; either matching counts.
- Re-uploading the **same file** (sha256 match on an already-`confirmed`
  batch) → immediate friendly error: "This exact file was already imported on
  {date}." with a link to those transactions.

## 9. Commit (atomic) [normative]

1. `UPDATE import_batches SET status='confirmed'` and insert all non-skipped
   staged rows into `transactions` (with `import_batch_id`) **in one SQL
   transaction**; one audit entry `import.confirmed` (diff: counts + per-
   category summary — prototype History shows `+ 6 → Food & Dining` style).
2. Post-commit jobs (same request, async): transfer detection over the new
   window, recurring detection refresh, classifier retrain trigger.
3. `POST /imports/{id}/cancel` or abandonment: staged rows deleted with the
   batch after 7 days idle (`status='awaiting_review'`) — housekeeping job.

## 10. Undo an import

`import.confirmed` audit entries carry `revert = {batch_id}`. Revert:
- If **any** transaction of the batch was edited after import (updated_at >
  confirmed_at, excluding detector touches) → **blocked**; error lists the
  blocking rows ("Edit or unlink them first").
- Else delete the batch's transactions (and their transfer links), restore
  `use_count` on the schema, audit `import.reverted`. Balance histories heal
  automatically (replay).

## 11. Error taxonomy (surfaced in UI)

| Error | Where | Copy |
|---|---|---|
| `file_too_large` | upload | "That file is larger than {max} MB." |
| `unsupported` | sniff | "Couldn't read that file. Try CSV, Excel, OFX or QIF, or a text-based PDF." |
| `parse_failed` | stage | "Only {n} of {m} rows parsed — check the column mapping." |
| `date_format` | mapping fix | "Couldn't read dates like “{example}”. Pick the right format." |
| `ai_unavailable` | detection | Falls back to manual mapping with notice "AI detection unavailable — set the mapping manually." |
| `duplicate_file` | upload | §8 copy. |

## 12. Acceptance criteria

1. Fixture files for each core format (CSV, **xlsx including a multi-sheet
   workbook**, OFX, QIF, PDF) import correctly end-to-end. The optional
   photo/vision path is verified separately and only when enabled.
2. Saved schema auto-match demonstrably skips the LLM on the second import
   of the same bank (log/`ai_calls` assertion).
3. Review UI supports: per-row category change, apply-to-similar, accept-all-
   confident, duplicate skip, and commit with a mixed bag (confident +
   needs-review + duplicates).
4. Reverting a confirmed import removes exactly its rows (and links) and is
   blocked when rows were edited.
5. With AI disabled the flow still completes via manual mapping.