# 15 — Implementation Plan

**Scope:** build order, milestones with tasks and acceptance criteria, and
guidance for agents working from this documentation set.

Related: everything — this doc sequences the others.

---

## 1. How to use these docs as a builder

1. Read `README.md`, `00-overview.md` (esp. the **Decision Log** — do not
   reopen decisions), `01-architecture.md`, `14-security.md`.
2. Read the docs for your milestone (below). Each doc's **Acceptance
   criteria** is the definition of done for its scope.
3. Frontend work starts by copying the prototype: `css/styles.css` verbatim,
   `js/widgets.js` near-verbatim, sprite + DOM structure from `index.html`.
   Visual deviations from the prototype are bugs.
4. Every mutating service writes audit entries **inside the same SQL
   transaction** — wire this from milestone M1 onward, never retrofit.
5. Money is minor-unit integers from the first migration onward; retrofitting
   floats later is a ban-worthy bug.
6. Keep `go test ./...` green; add golden template tests as soon as
   templates exist (M0).

## 2. Milestones

### M0 — Skeleton & shell
**Docs:** 01, 02 (conventions), 04 §1–4, 13, 14.

- Repo scaffold per doc 01 §3; config; logging; `secret.key` bootstrap.
- Store open (SQLite), migration runner + `0001_init` + seed (doc 02 §11).
- Auth (login page, sessions, CSRF, `AUTH_MODE=none`) + the setup-claim
  flow: owner account created in wizard step 1 [D24], no signup on the
  login page.
- Embedded templates: `layout.html` with masthead/sprite/period/privacy,
  all seven view routes rendering the real markup with **empty data**.
- Static assets served + hashed; vendored HTMX; self-hosted Inter.
- Golden template tests; CI (vet, staticcheck, race tests).

**Accept:** app runs; pages render pixel-identical to the prototype with
empty states; login works; setup creates the account and cannot be re-claimed
afterwards; `docker compose up` works (doc 13 §12.1).

### M1 — Core data & transaction surfaces
**Docs:** 02, 03 §2–6, 04 §5.1–5.2/5.3/5.5/5.8, 11 §5 (replay), 14.

- Accounts CRUD + valuations (checkpoint replay) + masked list API.
- Transactions: create/edit/delete, filters/search, `grouped|statement`
  display contract, inline editor (non-transfer parts).
- Categories CRUD (system categories, type masking validation).
- Audit writer + `audit_entries` for all of the above; audit list endpoint.
- Dashboard summary endpoint + stat cards + hero chart (client engine).
- Settings: theme/currency/name keys wired.

**Accept:** doc 02 §12, 03 §16, 11 §8.2; balances reconcile on fixtures;
prototype transactions view fully interactive against live data.

### M2 — Import pipeline (manual-first)
**Docs:** 06 (all), 04 §5.6, 03 §7.

- Upload → sniff → staged batches; CSV/**xlsx**/OFX/QIF parsers (+fixtures,
  incl. a multi-sheet workbook); PDF text extraction. The photo/vision path
  is optional and post-v1 [D23] — build only if time allows.
- Mapping model + fix-mapping UI; account resolution; dedupe.
- Review UI (doc 06 §7) with all bulk ops; commit; undo-import.
- Saved schemas CRUD + fingerprint auto-match.

**Accept:** doc 06 §12 (items 1, 3, 5 — AI-dependent items defer).

### M3 — Detection engines & categorisation
**Docs:** 07, 08, 03 §11–12.

- Normalisation + rules tier; local classifier (train/predict/persist) +
  retrain jobs; AI fallback via provider abstraction (needs M4's ai/ — build
  the interface + mock in M3, real adapters in M4).
- Transfer auto-detection + manual link/unlink + suggestions + inline editor
  transfer branch [D6].
- Recurring detection + `recurring_keys` + series modal endpoint.
- Import confirm hooks: cascade run, detections, retrain trigger.

**Accept:** doc 07 §10, 08 §8 (AI-fallback item verified with mock provider).

### M4 — AI integration
**Docs:** 09, 12 §2 (AI settings), 03 §13.

- Provider interface + adapters (OpenAI-compatible incl. custom, Anthropic,
  Gemini, Ollama); structured-output util with retry; encrypted key store;
  masked settings UI + test-connection; usage accounting; degradation paths
  replacing M3's mock.

**Accept:** doc 09 §11.

### M5 — Widgets, dashboard & NL query
**Docs:** 05, 10, 04 §5.4/§7, 03 §8–9.

- Dataset + `derive()` (Go) mirroring `convertWidgetData` (JS); widget
  rendering into pinned grid + chat cards; drawer apply (client convert +
  server persist).
- Pinned widgets CRUD + plan re-execution + seeded defaults (doc 10 §11).
- Plan schema, validation, entity resolution, SQL compiler, steps builder,
  answer templates; chat streaming (NDJSON) with worm choreography;
  conversations persistence + sidebar.

**Accept:** doc 05 §9, 10 §12.

### M6 — History, timeline & reconciliation
**Docs:** 11, 04 §5.7.

- **History engine** (doc 11 §4): per-entry revert, **jump-back with
  shelved paths + collapsed UI groups, path restore (ID-stable soft
  deletion), Forget/Delete surgery** driven by the capability matrix.
- Timeline endpoints + slider fragment flow; reconcile endpoint + status
  card; audit filter chips; diffs rendering.

**Accept:** doc 11 §8.

### M7 — Setup wizard, polish, hardening, release
**Docs:** 12, 04 §8–12, 13, 14.

- First-run wizard (auto-open, persistence, re-run); empty-state hints.
- Accessibility pass (doc 04 §11), responsive verification at all
  breakpoints, privacy/theme/shortcuts final check.
- Security pass (doc 14 §12), fuzzed parser fixtures, rate limits.
- Deployment artefacts: Dockerfile, compose, NGINX, systemd, example config;
  README quick-start; backup/restore drill.

**Accept:** docs 12 §5, 13 §12, 14 §12; full manual checklist below.

## 3. Suggested agent split

| Agent | Scope | Primary docs |
|---|---|---|
| **Platform** | M0 + store/migrations/config/auth/deploy artefacts | 01, 02, 13, 14 |
| **Core data** | M1 accounts/transactions/categories/audit | 02, 03, 11 §5 |
| **Importer** | M2 | 06, 04 §5.6 |
| **Engines** | M3 detectors + classifier | 07, 08 |
| **AI** | M4 providers + M5 planning | 09, 10 |
| **Frontend** | templates/fragments/JS across milestones | 04, 05 |

Parallelisation: M2/M3 need M1's data model only; frontend and backend can
proceed in lockstep per milestone. The audit writer and money conventions are
cross-cutting prerequisites — land them first.

## 4. Definition of done (project level)

1. All acceptance criteria sections across docs 00–14 pass.
2. Side-by-side prototype fidelity verified (doc 04 §13.1) on light/dark ×
   desktop/mobile.
3. End-to-end story passes on a fresh instance: setup (creates account) →
   login → import real CSV → confirm with review → transfers/recurring
   detected → ask "how much did I spend on transport this year?" → pin the
   widget → make a correction → **jump back** to an earlier point (later
   actions collapse) → apply new changes → **restore the original path** →
   **delete an AI-made link from history** → all visible and audited.
4. Works with AI disabled (all non-AI features) and with a local Ollama.
5. `make release` produces binaries + image; compose/systemd/NGINX artefacts
   validated.

## 5. Manual QA checklist (release gate)

- [ ] Login, logout, bad-password lockout, password change.
- [ ] Wizard: complete flow, skip flow, re-run from Settings.
- [ ] Import: each format fixture; schema save; second import auto-matches;
      review bulk ops; duplicate skip; undo import.
- [ ] Categorisation: correction fixes all occurrences; retrain bump;
      needs-review queue drains via review.
- [ ] Transfers: auto-link across two statement imports; break link;
      manual link via editor with suggestions; stats exclude transfers.
- [ ] Recurring: badge click opens series; totals respect period.
- [ ] Dashboard: scope toggles; edit mode drag/resize persists; pinned
      widgets refresh on reload.
- [ ] Queries: 4 suggested prompts; clarify path; streaming steps; pin from
      chat; drawer conversions.
- [ ] Timeline: slider scrub; per-account scope; reconcile drift card.
- [ ] History: jump back with confirm; collapsed group expand/collapse;
      restore path after new changes; per-entry Revert/Forget/Delete per the
      capability matrix (blocked reasons visible).
- [ ] Settings: all blocks; masked key; test connection; currency/name.
- [ ] Privacy blur; theme switch incl. system; keyboard shortcuts; Esc
      layering; mobile layouts at 390px.
- [ ] Backups: SQLite backup while running; restore with secret.key.