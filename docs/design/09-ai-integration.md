# 09 — AI Integration

**Scope:** the provider abstraction, capability contracts, per-use-case
prompt templates, structured-output handling, key security, usage accounting,
degradation and cost control.

Related: [07](07-categorisation.md) §6 · [10](10-natural-language-query.md) ·
[06](06-import.md) §5 · [12](12-settings-setup.md) settings UI · [14](14-security.md) §6.

---

## 1. Principles (normative)

1. **AI agnostic.** Providers: OpenAI, Anthropic, Google Gemini, Ollama
   (local), any OpenAI-compatible custom endpoint [Requirements §AI Agnostic].
2. **AI is optional.** Every AI path has a non-AI fallback; with no provider
   configured the app is fully usable (doc 06 §11, doc 07 §1).
3. **One internal interface, several capabilities.** All features call the
   same `Provider` interface through named **capabilities**; a separate
   classifier capability serves the categorisation fallback. Any chat LLM can
   serve it via structured output; a dedicated decision model is optional and
   never required [Backend §11].
4. **Structured outputs only.** Every call requests JSON conforming to a
   documented schema; invalid output is retried with error feedback (max 2)
   then the capability fails gracefully.
5. **Every AI action is audited** and reversible (actor `ai`) and metered in
   `ai_calls`.
6. **Temperature 0** for all extraction/planning capabilities.

## 2. Provider interface

```go
// internal/ai/provider.go
type ChatMessage struct { Role, Content string }

type StructuredRequest struct {
    System   string
    Messages []ChatMessage
    Schema   string          // JSON-Schema-ish description embedded in prompt
    MaxTokens int
    Timeout  time.Duration
}

type Provider interface {
    Name() string
    CompleteStructured(ctx context.Context, req StructuredRequest, out any) (Usage, error)
}

type Usage struct { PromptTokens, CompletionTokens int }
```

- `CompleteStructured` appends the provider-specific JSON-mode instruction,
  calls the API, extracts the JSON (tolerating code fences), unmarshals into
  `out`, and on validation failure retries with
  `"Your previous reply was invalid: {err}. Reply with JSON only."` (≤ 2
  retries).
- Adapters: `openai.go` (also serves `custom` via configurable base URL),
  `anthropic.go`, `gemini.go`, `ollama.go` (local, structured via format=json).
- Timeouts default 30s (60s for schema detection on big PDFs); cancellable.

## 3. Capabilities registry

| Capability | Used by | Default model policy |
|---|---|---|
| `schema_detection` | import pipeline (doc 06 §5) | provider default |
| `schema_naming` | import save step | provider default (cheap) |
| `categorisation` | cascade tier 3 (doc 07 §6) | provider default or dedicated decision model if configured |
| `query_planning` | NL query (doc 10) | provider default |
| `merchant_extraction` | import commit enrichment | optional; provider default |

`ai_provider_config.models` (JSON) overrides any capability's model:
`{"query_planning": "claude-3-5-sonnet", "categorisation": "llama3.1:8b"}`.
"Auto" (empty) = provider default model. The Settings UI exposes provider +
API key + model (Auto/Latest) per prototype, plus advanced per-capability
model fields (same block, visually consistent `.field-row`s).

## 4. Provider configuration & key security

- `GET /api/v1/settings/ai` returns `{provider, models, base_url, key_masked:
  "sk-••••••••", has_key}` — **never** the raw key [Backend §11].
- `PUT` stores the key **encrypted at rest**: AES-256-GCM, key = 32-byte
  `GLOWWORM_SECRET_KEY` (auto-generated into the data dir on first boot if
  absent, doc 13 §4). Ciphertext in `ai_provider_config.key_cipher`.
- Changing provider clears nothing silently: switching providers keeps the
  old encrypted key until overwritten; `Test connection` verifies before the
  UI confirms success.
- Logging never includes keys; `ai_calls` logs provider/model only.

## 5. Prompt management

All prompts live in `internal/ai/prompts.go` as named constants with a
golden test pinning them (prompt changes are deliberate, reviewed changes).
Common system preamble:

> You are part of Glowworm, a personal finance tracker. You will be given
> financial data belonging to a single user. Respond ONLY with JSON
> conforming exactly to the schema provided. Never invent categories,
> accounts, or columns that are not listed. Dates are ISO YYYY-MM-DD. Amounts
> are integers in minor currency units (pence/cents).

## 6. Capability contracts (prompts + schemas)

### 6.1 `schema_detection` (doc 06 §5)

Input: format guess, header row, 5 sample lines (or extracted text excerpt),
user's account list (names + masked identifiers).

Output schema (normative, = doc 06 §5.2 mapping JSON):

```json
{ "kind":"csv|ofx|qif|pdf", "has_header": true, "delimiter": ",",
  "columns": [{"source":"...", "role":"date|description|amount|amount_in|amount_out|currency|balance|type_hint|memo|ignore",
               "date_format":"...", "decimal_separator":".", "negatives":"signed|paren|dr_cr"}],
  "bank_hint": "...", "proposed_name": "...", "confidence": 0.0 }
```

Validation: at least one `date` and one amount-ish role; columns exist in the
source; date_format parses the samples (deterministic check — LLM picks,
Go verifies, retry on mismatch).

### 6.2 `categorisation` (doc 07 §6)

Input (batched ≤ 50):

```json
{ "transactions":[{"index":0,"date":"2024-06-02","description":"Tesco Express","amount":-4235,"type":"expense"}],
  "allowed_categories":[{"name":"Food & Dining","hint":"supermarkets, takeaways"}] }
```

Output: `{"items":[{"index":0,"category":"Food & Dining","confidence":0.95}]}`
— every index must be answered; unknown categories rejected (retry once with
the error, else those rows go to review).

### 6.3 `query_planning` (doc 10 §4)

Output = the Plan JSON (doc 10 §3) — validated against the entity catalogue;
invalid entity references trigger one correction round with the fuzzy
candidate list, else a clarification response.

### 6.4 `merchant_extraction`

Input: batched descriptions; output
`{"items":[{"index":0,"merchant":"Tesco Express","confidence":0.9}]}`. Runs
as a post-commit enrichment job; sets `transactions.merchant` (never
overwrites non-empty values; user edits win).

### 6.5 `schema_naming`

Input: mapping + bank hint; output `{"name":"Monzo CSV"}`. Length-clamped.

### 6.6 `vision_ocr` — optional capability

Used **only** by the optional photo/scanned-PDF import path (doc 06 §2 —
explicitly deprioritised behind Excel); not part of the core v1 pipeline and
never required by any flow. Requires a multimodal provider; when unconfigured
or the model lacks vision, image inputs fail with the importer's standard
`unsupported` error. Contract: image bytes in → the schema-detection output
schema (doc 09 §6.1) applied to the OCR'd text.

## 7. Audit & reversibility of AI actions

- `categorisation.applied` — one entry per batch (actor `ai`,
  `actor_detail` = model), diff `+ 6 → Food & Dining` lines, revert restores
  prior `category_source`s.
- `import.schema_detected` — records the detection + confidence (actor `ai`;
  the prototype's History card "Detected schema “Monzo CSV” · confidence 98%
  · 4 columns" is the required rendering).
- `merchant.enriched` — batch entry, per-row diffs.
- `query.answered` — records the question + plan (actor `user` on the entry,
  AI noted in actor_detail); widget/answer revert is N/A but the entry
  carries full inputs for transparency.
- Revert engine treats AI batch entries exactly like user batch entries
  (doc 11 §4).

## 8. Usage accounting

Every call appends to `ai_calls` (capability, provider, model, tokens,
latency, ok, error). `GET /api/v1/settings/ai` includes a trailing 30-day
usage summary (calls + tokens per capability) — enough for cost sanity
without a billing integration.

## 9. Degradation matrix (normative)

| Capability absent (no AI / provider down) | Behaviour |
|---|---|
| `schema_detection` | Manual mapping UI (pre-filled by sniffing) — flow unchanged |
| `schema_naming` | Name input starts empty |
| `categorisation` | Tier 3 skipped; uncertain rows → review |
| `query_planning` | Queries view shows a notice: "Connect an AI provider in Settings to ask questions." Suggestions remain visible but disabled |
| `merchant_extraction` | Skipped silently |
| Ollama local | Same contracts; slower timeouts allowed (`timeout` config per provider) |

Provider errors surface once as a toast ("AI provider unreachable — {n}
transactions sent to review") and are logged; retries are bounded (doc 01 §6
`ai.retry`) with exponential backoff (≤ 3 attempts).

## 10. Cost controls

- Batch sizes fixed (50 categorisation, 20 merchant, 5 samples for schema).
- Caches: schema detection per file sha256; query plans per identical plan
  hash (short-lived in-memory LRU, 10 min) so pinned dashboard refreshes
  never re-plan.
- Hard cap setting `ai_daily_token_cap` (0 = unlimited): exceeded →
  capabilities fail gracefully that day.

## 11. Acceptance criteria

1. Contract tests run the same fixture through a mock provider for each
   capability and assert validation + retry behaviour.
2. OpenAI/Anthropic/Gemini/Ollama adapters compile against interface tests;
   live smoke test optional and manual.
3. Keys are encrypted at rest (ciphertext column), masked in all reads, and
   never logged.
4. With AI disabled end-to-end, import + categorisation + review flows pass
   their acceptance tests (docs 06/07).
5. Every AI mutation appears in the audit log with actor `ai` and is
   revertible.