# 14 — Security & Privacy

**Scope:** threat model, authentication, sessions, CSRF, transport, upload
safety, injection, secrets, data masking, privacy mode, rate limiting, audit
integrity.

Related: [13](13-deployment.md) §6 NGINX · [02](02-data-model.md) masking ·
[09](09-ai-integration.md) §4 keys · [11](11-audit-history-timeline.md).

---

## 1. Threat model (self-hosted, single user)

| Threat | Mitigation |
|---|---|
| Public internet attacker | TLS at NGINX; auth required (D16/D24 — no signup surface); rate limits; security headers |
| Stolen session cookie | HttpOnly + SameSite=Lax + Secure; 30-day sliding expiry; rotation on login |
| CSRF on state-changing requests | SameSite=Lax + synchroniser token on all POST/PUT/PATCH/DELETE (double-submit cookie for HTMX `HX-Request` paths) |
| XSS via statement data | `html/template` auto-escaping everywhere; widget JSON transported as escaped `data-*`/JSON script attrs; no `innerHTML` of server strings outside the widget engine's `esc()` |
| SQL injection | Parameterised queries only; plan compiler whitelist (doc 10 §9); sqlmock-style tests assert no string interpolation |
| Malicious uploaded files | Parsers are non-executing text readers; PDF/xlsx libraries with fuzz-tested inputs; size caps; images only ever leave to the configured vision provider (optional path, doc 06 §2) |
| Secrets leakage | AI keys AES-256-GCM encrypted at rest; masked in API/logs; session signing key in data dir (0600) |
| Accidental/AI data corruption | Audit + revert everywhere (doc 11); AI never writes raw SQL (D14) |
| Local attacker on host | Data dir 0700/0600; systemd hardening (doc 13 §7); secrets never in env logs |

Out of scope: malicious host user, compromised browser (standard caveats),
multi-tenant isolation (single user by design).

## 2. Authentication [D16/D24] — public-internet posture

The instance is expected to be reachable from the public internet; an
unauthenticated visitor is the default threat.

- **Single owner account.** Exactly one user per instance (`users` table,
  doc 02 §2.2). **Account creation happens during setup only** [D24] — the
  login page exposes no signup: nothing to enumerate, squat, or abuse.
  `POST /api/v1/setup/account` exists only while no user exists and returns
  404 afterwards; the setup page redirects to `/login` once claimed.
  Optional `GLOWWORM_SETUP_TOKEN` gates the setup page for internet-exposed
  first boots (printed to the server log on first run).
- Login page (`GET /login`, doc 04 §5.9): username + password, generic
  failure message (never distinguishes which factor failed), constant-time
  bcrypt verification.
- Passwords: ≥ 10 characters (enforced server-side), bcrypt cost 12; only
  the hash is stored.
- `AUTH_MODE=password` (default): every route except `/login`, `/healthz`,
  `/static/*` requires a valid session. `AUTH_MODE=none` is a localhost/LAN
  convenience — it MUST NOT be used on a public deployment; the UI shows a
  persistent "Authentication disabled" warning when active.
- Login rate limit: 5 attempts / 15 min / source IP (in-memory) with
  progressive delay; failures logged with request id (no credentials).
- Password change requires the current password and invalidates all other
  sessions.
- Password reset is out-of-band only: `glowworm reset-password <username>`
  (host/CLI access required) — never an HTTP route.

## 3. Sessions & cookies

| Cookie | Attributes |
|---|---|
| `glowworm_session` | HttpOnly; Secure (when TLS detected via `X-Forwarded-Proto`); SameSite=Lax; Path=/ |
| `glowworm_csrf` | SameSite=Lax; **not** HttpOnly (double-submit pattern reads it in JS-free flows via hidden form field rendered by templates) |

- Sessions: signed (HMAC-SHA256 with secret key) + server-side record
  (`expires_at`, sliding 30-day renewal). Logout deletes the record.
- CSRF token: per-session random, embedded as hidden input in all forms and
  sent by a small JS hook on HTMX requests (`hx-vals` / `htmx:configRequest`
  adding `X-CSRF-Token`). Server validates on every unsafe method when
  `HX-Request` or form-encoded; JSON API clients use the same header.

## 4. Transport & headers

- Terminates TLS at NGINX (doc 13 §6). App sets (html pages + fragments):
  - `Content-Security-Policy: default-src 'self'; img-src 'self' data:;
    style-src 'self'; font-src 'self'; script-src 'self'; connect-src 'self'`
    (inline theme bootstrap script allowed via its SHA-256 hash).
  - `X-Content-Type-Options: nosniff`, `Referrer-Policy:
    strict-origin-when-cross-origin`, `X-Frame-Options: DENY`.
  - No `Access-Control-Allow-Origin` on app pages; the JSON API allows CORS
    only from an explicitly configured origin list (empty by default — API is
    same-origin).

## 5. Input handling & uploads

- `multipart/form-data` up to `GLOWWORM_UPLOAD_MAX_MB`; per-request body cap
  enforced in middleware; temp files in `data/tmp`, deleted after parse.
- Uploads never executed; parsers run with per-file time budget (5s text,
  30s vision round-trip on the optional path) and panic-recovery.
- Filename sanitised for display only; stored name is the batch UUID.
- All user strings length-capped at API boundaries (descriptions 500,
  category names 60, account names 80, chat 500).

## 6. Secrets

- `secret.key` (data dir, 0600): session signing + AES-GCM master key for AI
  API keys. Auto-generated once; rotating it invalidates sessions and stored
  AI keys (documented re-entry path).
- AI keys: ciphertext-only at rest (doc 09 §4); masked in every read
  (`sk-••••••••`); never logged; cleared on explicit user action only.
- Error pages/logs never include identifiers or balances (masking §7).

## 7. Data masking (normative)

- Account numbers/IBANs are **never** returned in list endpoints — masked
  `ref_last4` + `has_*` flags (doc 03 §4). Full values only on the account
  detail endpoint used by the edit modal.
- Log lines mask identifiers the same way (`···· 4211`).
- The **privacy mode** is presentation-only (doc 04 §8.2) — the API keeps
  serving real values to the authenticated session; screenshots-blur is its
  purpose.
- Audit entries: diffs may contain amounts/categories (user's own data,
  same trust boundary) but never full identifiers.

## 8. AI data boundaries

- Prompts include only: descriptions, amounts, dates, category/account
  **names** — never account numbers, sort codes, IBANs or BICs.
- The schema-detection prompt may include sample statement lines verbatim
  (necessary for mapping) — documented in Settings ("Statement excerpts are
  sent to your AI provider during import detection").
- Ollama/local mode keeps everything on-host (the wizard's "Private" note).

## 9. Rate limiting & abuse

In-memory limiter (per session+IP): login 5/15min; chat 10/min; imports
6/hour; generic API 300/min. 429 responses use the standard error shape; UI
toasts.

## 10. Audit integrity

- `audit_entries` *content* is immutable (doc 11 §1): the history engine
  alone transitions `state` or removes records, always inside a transaction
  and tombstoned. Postgres mode can enforce this with a `BEFORE UPDATE`
  trigger rejecting content-column changes; SQLite relies on process
  discipline plus tests asserting no mutation paths outside `core/audit`.
- `audit_reverts` links and `audit.entry.deleted` tombstones keep a
  tamper-evident chain; path restores report skips rather than hiding them;
  entry *content* is never rewritten — only the engine's state transitions
  and record removals are possible, and each is tombstoned (doc 11 §4).

## 11. Dependency policy

- Minimal deps; `go.mod` reviewed; `govulncheck` in CI; dependabot-style
  bumps monthly; vendored HTMX pinned by hash.
- No telemetry, no outbound calls except: configured AI provider endpoints
  and (if enabled) nothing else. Fonts self-hosted [D17].

## 12. Acceptance criteria

1. Unauthenticated request to any page/API → redirect/401; health endpoints
   exempt. After the account exists, `POST /api/v1/setup/account` → 404;
   `/login` contains no signup path; lockout engages after 5 failures.
2. CSRF: a cross-origin POST without token is rejected (403); HTMX flows
   pass with the hook.
3. Fuzzed CSV/OFX/QIF/PDF fixtures never crash the server (panic-recovery +
   5xx) and never exceed the time budget.
4. `govulncheck` clean; no `unsafe` string SQL anywhere (grep + tests).
5. Masking: list responses verified to contain no full identifiers
   (contract test).