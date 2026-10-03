# 07 — Automatic Categorisation

**Scope:** the four-tier categorisation cascade, text normalisation, the
pure-Go local classifier (math + persistence), AI fallback, thresholds,
training data, review queue and endpoints.

Related: [02](02-data-model.md) §4/§6.2 · [06](06-import.md) staging ·
[09](09-ai-integration.md) capability contracts · [08](08-transfers-recurring.md).

---

## 1. Cascade (normative order — cheapest first)

```
transaction ──► 1. RULES          exact normalised-description → category
            ──► 2. LOCAL          TF-IDF + multinomial LR, in-process, offline
            ──► 3. AI FALLBACK    LLM structured output (only if AI configured)
            ──► 4. HUMAN REVIEW   uncategorised + flagged needs_review
```

[Backend §7]: local tiers resolve most transactions offline at zero API
cost; the AI only decides what they cannot; review catches the rest.

**Candidate masking (normative):** candidate categories are always masked by
transaction type — `expense` → `spend` categories, `income` → `income`
categories, `transfer` → the system `internal` category. No tier may suggest
an incompatible category [doc 02 §4]. For `transfer` the cascade short-circuits:
category is set to `Internal transfer`, `category_source='local'`, confidence 1.

**Category source values:** `user` (human-confirmed — training data),
`local` (rules or classifier), `ai`, `none` (needs review).

## 2. Thresholds (defaults; configurable in settings)

| Tier | Assign automatically when | Otherwise |
|---|---|---|
| Rules | pattern hit (majority-confirmed) | confidence 1.0, source `local` |
| Local | softmax top-p ≥ **0.75** | pass through |
| AI | model confidence ≥ **0.8** | source `none`, `needs_review=1` |
| Guard | category has ≥ **3** user-confirmed training examples (per category) | that category is not eligible for *local auto-assign* (it may still be predicted, flagged for review) |

`min_examples`, `local_threshold`, `ai_threshold` live in settings
(`cat_*` keys, doc 12 §2) and are returned by `GET /api/v1/categoriser/status`.

## 3. Text normalisation (shared with dedupe)

`normalise(desc) → key`:

1. Unicode-lowercase, trim.
2. Strip card/ISO identifiers: `\b\d{4}[\s-]?\d{4}[\s-]?\d{4}\b` (card
   chunks), ISO timestamps `\d{2}-\d{2}-\d{2}T\d{2}:\d{2}`, long digit runs
   (≥5) → `#`.
3. Strip UK payment suffixes: `FASTER PAYMENT`, `FPS`, `SEPA`, `DD `, `SO `,
   `TFR `, `VIA CARD`, `WEB`, `POS`.
4. Collapse whitespace/punctuation to single spaces.
5. Trailing number tokens dropped (`tesco stores 4521` → `tesco stores`).

Deterministic, unit-tested, exported for reuse (dedupe, transfer detector).

## 4. Tier 1 — Rules

Table `category_rules(pattern → category_id, votes)` (doc 02 §6.2), rebuilt
from user-confirmed history and refreshed incrementally on every
`category_source='user'` write:

- Pattern = `normalise(description)`.
- A pattern maps to category C when C is the **majority** (≥ 60% and ≥ 2
  votes) of user-confirmed assignments for that pattern. One correction fixes
  every occurrence [Requirements §AI Assisted importer].
- Lookup is exact-match on the normalised key (index hit, nanoseconds).
- Rules never violate type masking (rebuild filters by type compatibility).

## 5. Tier 2 — Local classifier (pure Go, no dependencies)

Model: **TF-IDF (unigrams + bigrams) → multinomial logistic regression
(softmax)**, trained on transactions with `category_source='user'`.

### 5.1 Features
- Tokenise normalised text; features = unigrams + adjacent bigrams.
- Vocab: top 20,000 terms by document frequency, `min_df = 2`.
- TF-IDF with L2-normalised vectors (sublinear TF: `1 + ln(tf)`).

### 5.2 Model
- Weight matrix `W ∈ R^{K×V}` + bias `b ∈ R^K` (K = eligible categories).
- Softmax over the **type-masked candidate set** at inference:
  `p(c|x) = softmax_c(x·W_c + b_c)` computed only over candidates, renormalised.
- Training: mini-batch SGD with Adam (lr 0.05, β1 0.9, β2 0.999), L2 λ 1e-4,
  ≤ 30 epochs or until train loss plateaus; deterministic seed.
- Implementation: `internal/classifier` — dense ops over sparse vectors,
  no external ML libraries. ~300 lines. Benchmarks: < 10ms/1k predictions.

### 5.3 Training data & retraining
- Training rows: `category_source = 'user'` transactions (description,
  merchant, type, category).
- Retrain triggers: after each import confirm, manual
  `POST /api/v1/categoriser/retrain`, nightly if new user rows exist.
- Persistence: `classifier_models` row (version++) with JSON payload
  {vocab, idf, weights (sparse per class), categories, config} — derived
  data, fully reproducible from the DB [Backend §7].
- The active model is the latest row; revert restores the previous version
  (audit `classifier.reverted`).

## 6. Tier 3 — AI fallback

- Batched: up to 50 uncategorised transactions per call (`capability:
  categorisation`, doc 09 §6.2). Prompt includes the user's eligible
  categories with names + a one-line usage hint derived from existing
  assignments; the LLM returns `{items:[{index, category, confidence}]}`,
  validated (unknown category → retry with error feedback once, else drop
  to review).
- Accepted when confidence ≥ 0.8 → `category_source='ai'`.
- All AI categorisation is audit-logged as **one batch entry**
  (`categorisation.applied`, actor `ai`, diff: `+ N → Category` lines —
  prototype History parity) and reversible (restores previous sources).

## 7. Tier 4 — Human review

- Any transaction with `category_source='none'` (or `needs_review` staged
  row) is surfaced: during import review (doc 06 §7) and afterwards via the
  transactions filter `type=...` + a `needs_review` chip (doc 03 §11).
- A user-set category becomes `category_source='user'` → rules + classifier
  training data; retrain triggered (debounced, ≤ 1 per minute).

## 8. Manual override & re-run

- Manual override at any time wins over everything (and never silently flips
  back; re-categorise endpoints skip `user` rows unless forced) [Backend §7].
- `POST /transactions/{id}/categorise` and `categorise-batch` re-run the
  cascade respecting the override rule.

## 9. Endpoints

Doc 03 §11. Status response:

```json
{ "model_version": 7, "trained_at": "...", "training_count": 412,
  "rules_count": 63, "thresholds": {"local": 0.75, "ai": 0.8, "min_examples": 3} }
```

## 10. Acceptance criteria

1. Golden fixture ledger: rules resolve repeated merchant strings instantly;
   classifier reaches ≥ 85% held-out accuracy on the fixture after 50
   confirmed rows; uncertain rows land in review.
2. With zero AI configured, imports still categorise (rules+local) and flag
   the rest.
3. Type masking is enforced at every tier (tests: expense tx never receives
   an income category).
4. Per-category <3 examples guard: predicting a fresh category only flags
   review, never auto-assigns.
5. Retrain is idempotent and versioned; revert restores the prior model.
6. One user correction updates the rule table and (after retrain) flips all
   matching future transactions.