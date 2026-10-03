# Glowworm

**Finance, made clear.** An open-source, self-hosted personal finance tracker:
drop in bank statements, let AI detect their format, categorise transactions
automatically, detect transfers and recurring bills, and ask questions about
your money in plain English.

## Quick start

```bash
make build
GLOWWORM_ADDR=127.0.0.1:8080 ./glowworm serve
```

Open http://127.0.0.1:8080 — the first-run wizard walks you through creating
the owner account, categories, importing a statement and picking an AI
provider. Then sign in at `/login`.

### Docker

```bash
docker compose -f deploy/compose.yaml up --build
```

### Behind NGINX

`deploy/nginx.conf` provides the TLS reverse proxy (statement uploads sized
to `client_max_body_size 25m`; NDJSON chat streaming needs `proxy_buffering
off`). Set `GLOWWORM_TRUSTED_PROXIES=127.0.0.1`.

## Configuration

Environment variables (see `glowworm.example.toml` for the file equivalent):

| Var | Default | Notes |
|---|---|---|
| `GLOWWORM_ADDR` | `127.0.0.1:8080` | Listen address |
| `GLOWWORM_DATA_DIR` | `./data` | SQLite DB + secret.key |
| `GLOWWORM_DB` | `sqlite` | `sqlite` or `postgres` |
| `GLOWWORM_TIMEZONE` | system | "Today" boundaries |
| `GLOWWORM_AUTH_MODE` | `password` | `none` only for trusted networks |
| `GLOWWORM_UPLOAD_MAX_MB` | `25` | Statement upload cap |
| `GLOWWORM_SETUP_TOKEN` | — | Optional gate for first-run setup |
| `GLOWWORM_AI_MOCK` | — | `=1` injects a deterministic mock AI (tests/dev) |

AI providers are configured in-app (Settings → AI provider): OpenAI,
Anthropic, Google Gemini, Ollama (local) or any OpenAI-compatible endpoint.
Keys are encrypted at rest with the instance secret key. **AI is optional** —
without it, imports use manual mapping and the rules-based categorisation.

## Backups

SQLite (WAL) lives in `GLOWWORM_DATA_DIR`:
`sqlite3 data/glowworm.db ".backup backup.db"`. Also back up
`data/secret.key` separately — it protects stored AI keys.

## Project layout

```
cmd/glowworm/          entrypoint (serve | healthcheck | version)
internal/              config, store (+migrations), repos, core services,
                       classifier, importer, ai, server (HTTP + templates)
web/static/            css (prototype stylesheet verbatim), js, fonts
docs/design/           the normative design documentation
deploy/                Dockerfile, compose, NGINX, systemd
```

## Design & development

The product is specified in `docs/design/` — start at
[`docs/design/README.md`](docs/design/README.md). The prototype UI in the
repo root (`index.html`, `js/`, `css/`) is the authoritative visual spec.

## License

TBD by the maintainer.
