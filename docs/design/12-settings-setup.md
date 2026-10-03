# 12 — Settings & Setup Walkthrough

**Scope:** settings model and UI mapping, AI provider configuration, and the
first-run setup & configuration walkthrough.

Related: [03](03-backend-api.md) §13 · [04](04-frontend.md) §5.8/§6 ·
[09](09-ai-integration.md) §4 · [06](06-import.md) (wizard import step).

---

## 1. Settings storage

Key-value rows in `settings` (JSON values), read/written through
`core/settings` with typed getters. Keys (normative):

| Key | Type | Default | Notes |
|---|---|---|---|
| `theme` | `light|dark|system` | `system` | client-applied; server stores for new devices |
| `currency` | ISO-4217 | `GBP` | display currency [D18]; new accounts inherit |
| `user_name` | string | `""` | greeting [D19] |
| `default_period` | period id | `last-12m` | masthead default after first choice |
| `privacy_default` | bool | `false` | privacy mode on load |
| `setup_completed` | bool | `false` | walkthrough flag [F1] |
| `cat_local_threshold` | float | `0.75` | doc 07 §2 |
| `cat_ai_threshold` | float | `0.8` | |
| `cat_min_examples` | int | `3` | |
| `transfer_threshold` | float | `0.7` | doc 08 §2 |
| `transfer_date_window_days` | int | `4` | |
| `ai_daily_token_cap` | int | `0` | doc 09 §10 |

## 2. Settings UI (prototype parity + documented additions)

Settings view blocks (doc 04 §5.8):

1. **Accounts** — CRUD via modal; valuations as corrections; masked refs.
2. **AI provider** — provider select (OpenAI (GPT) / Anthropic (Claude) /
   Google (Gemini) / Ollama (local) / Custom endpoint), API key
   (password field, masked placeholder `sk-••••••••••••••••`), Model select
   (Auto / Latest; "Latest" = provider's recommended current model mapping
   table in `ai/adapters`), `Test connection` ghost button. Saving audits
   `settings.ai.updated` (masked diff only).
   - Custom endpoint adds a Base URL field (styled `.field-row`).
   - Ollama hides the key field, shows Base URL (default
     `http://localhost:11434`).
3. **Appearance** — Theme / Currency / **Your name** [D19].
4. **Getting started** — `Run setup walkthrough` re-runs the wizard
   (`setup_completed` stays true; wizard just opens).

Audit exemptions [Backend §12]: `theme`, `privacy_default`,
`default_period`, `user_name`, `currency` changes are **not** audited; AI
config changes are.

## 3. Setup & configuration walkthrough [F1]

First run (`setup_completed=false`) opens the wizard automatically after
load (prototype's deferred `openWizard()`). **Six steps** — prototype DOM
plus one requirement-driven addition [D24]:

| Step | Content | Backend |
|---|---|---|
| 0 · Welcome | Worm hero, "Welcome to Glowworm" + tagline | — |
| 1 · **Create your account** | Username, password (+ confirm) fields; server-validated (§3.1). Only step that cannot be skipped while no user exists — the instance must be claimed before use. | `POST /api/v1/setup/account` — only while no user exists |
| 2 · Create your categories | Chip list with default suggestions (Transport, Food & Dining, Bills & Utilities, Entertainment, Shopping, Health — prototype set), remove ✕, `Add category` (name prompt → palette colour cycling). | `POST /api/v1/categories` per kept chip (type `spend`) on Continue |
| 3 · Import your first statement | Small dropzone ("CSV, Excel, PDF, OFX, QIF…"), skippable | Drop → creates batch + jumps into the standard import flow (docs 06); on completion returns to the wizard; Skip is fine |
| 4 · Choose your AI provider | `.provider-pick` cards: OpenAI / Anthropic / Local (Ollama) — selectable; + key/model inline fields when a hosted provider chosen | `PUT /api/v1/settings/ai` (+ `test`) on Continue |
| 5 · You're all set | Success check, "Glowworm is ready. Ask it anything about your money." | `POST /api/v1/setup/complete` (audit `setup.completed`) |

### 3.1 Account creation & instance claim [D24]

- **Account creation happens during setup, not on the login page.** On a
  public-internet deployment the login page must expose no signup surface:
  no enumeration target, no account-squatting, nothing to abuse.
- `POST /api/v1/setup/account` works **only while no user exists** and
  returns 404 afterwards; the setup page redirects to `/login` once claimed.
  Optional `GLOWWORM_SETUP_TOKEN` env gates the setup page (paranoid
  internet-facing first boots — the token is printed to the server log on
  first run).
- Password: ≥ 10 chars, bcrypt cost 12; username 3–40 chars. Login errors
  are generic; lockout 5 attempts / 15 min (doc 14 §2).
- "Re-run setup walkthrough" (Settings) **never** touches the account.
- Password reset is out-of-band: `glowworm reset-password <username>` on the
  host (doc 01 §2) — never an HTTP route.

Footer: `Back` (hidden on step 0), `Skip for now` (hidden on last step,
closes without completing — wizard reopens next load until completed),
`Continue`→`Finish`. Each partially-completed step persists what it can
(categories created stay; provider saved stays).

**Backend status** `GET /api/v1/setup/status` returns `{completed,
has_categories, has_accounts, has_transactions, ai_configured}` — used to
show contextual empty-state hints on dashboard ("Import a statement to get
started") after completion.

## 4. Accounts in setup

The wizard does **not** create accounts (prototype); the first import asks
for the target account, and Settings → Accounts is linked from the import
step with a hint. This matches the prototype and backend flow (doc 06 §4).

## 5. Acceptance criteria

1. Fresh instance: every route redirects to `/setup`; step 1 creates the
   account and cannot be skipped while no user exists; after completion the
   wizard never re-opens (re-runnable from Settings) and login is required.
2. A claimed instance rejects `POST /api/v1/setup/account` with 404 and the
   setup page redirects to `/login`; password rules and lockout enforce
   (doc 14 §2).
3. Steps persist partial progress; completing sets the flag.
4. Categories created in the wizard exist with palette colours; the
   transactions filter and category grid reflect them immediately.
5. Provider picked in the wizard is stored encrypted and `Test connection`
   reports real latency or a clear error.
6. Settings changes follow the audit exemptions table exactly; re-running
   the walkthrough never touches the account.