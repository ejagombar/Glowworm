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

## Backup, restore and deletion

A full backup is a `.zip` holding a snapshot of the database plus
`secret.key` (which decrypts the stored AI key), so keep it private.

- **Back up** from Settings → Backup & deletion (asks for your password),
  or on the server: `glowworm backup [-o file.zip]` — safe while running.
- **Restore** only on the server, with Glowworm stopped:
  `glowworm restore [--force] file.zip`. It checks the backup's integrity
  and moves the current data to `data/replaced-<time>/` rather than
  deleting it; `--force` is needed when data already exists.
  - systemd: `sudo systemctl stop glowworm && sudo -u glowworm GLOWWORM_DATA_DIR=/var/lib/glowworm glowworm restore --force file.zip && sudo systemctl start glowworm`
  - Docker: copy the file into the volume, then `docker compose stop glowworm && docker compose run --rm glowworm restore --force /data/file.zip && docker compose start glowworm`
- **Delete** everything from Settings → Backup & deletion (password +
  typing `DELETE`). Glowworm returns to first-run setup.

Individual bank accounts are deleted from their settings dialog and can be
restored from Settings → Deleted accounts.

## Logs

Logs go to stdout: one line per request plus errors and notable events
(backups, deletions). `GLOWWORM_LOG_LEVEL=debug|info|warn|error` and
`GLOWWORM_LOG_FORMAT=text|json` control them.

- Run by hand: they stream in that terminal. To keep a file you can follow
  elsewhere: `./glowworm serve 2>&1 | tee -a glowworm.log`, then
  `tail -f glowworm.log`.
- systemd: `journalctl -u glowworm -f` (`-o cat | jq` to pretty-print JSON).
- Docker: `docker compose logs -f glowworm`.

## Project layout

```
cmd/glowworm/          entrypoint (serve | backup | restore | version | healthcheck)
internal/              config, store (+migrations), repos, core services,
                       classifier, importer, ai, server (HTTP + templates)
web/static/            css, js, fonts
docs/design/           the normative design documentation
deploy/                Dockerfile, compose, NGINX, systemd
```

## Design & development

The product is specified in `docs/design/` — start at
[`docs/design/README.md`](docs/design/README.md). The running app
(`web/static/` + `internal/render/templates/`) is the visual reference.

## License

TBD by the maintainer.
