# 13 — Deployment & Operations

**Scope:** building and shipping Glowworm: configuration, data directory,
Docker, docker-compose, NGINX reverse proxy, systemd, TLS, backups/restore,
upgrades, health and monitoring.

Related: [01](01-architecture.md) §2/§7 · [14](14-security.md) ·
[02](02-data-model.md) §11 migrations.

---

## 1. Build & release

```bash
# local
go build -trimpath -ldflags "-s -w -X main.version=$(git describe --tags)" -o glowworm ./cmd/glowworm

# cross-compile (pure-Go SQLite → no cgo needed)
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o glowworm-linux-arm64 ./cmd/glowworm
```

- Release artefacts: single static binary per platform (linux amd64/arm64,
  darwin arm64/amd64, windows amd64) + checksums; Docker image.
- Static assets, templates, fonts and migrations are **embedded** (`go:embed`)
  — no external files required to run.
- Version string reported by `GET /healthz` and `glowworm version`.

## 2. Run modes

```bash
glowworm serve                      # default: config from env + optional file
glowworm version                    # print version
glowworm reset-password <username>  # host-side password recovery (out-of-band)
```

First boot: creates `GLOWWORM_DATA_DIR`, generates `secret.key` if absent,
runs migrations, seeds system categories + default settings, starts server,
and (if no user exists) serves the setup flow (create owner account →
in-app wizard, doc 12 §3).

## 3. Configuration

Environment variables (all optional; sensible defaults) + optional
`glowworm.toml` in the working directory / data dir. Precedence: env > file >
defaults.

| Env | Default | Purpose |
|---|---|---|
| `GLOWWORM_ADDR` | `127.0.0.1:8080` | Listen address |
| `GLOWWORM_DATA_DIR` | `./data` | DB, secret key, uploads temp |
| `GLOWWORM_DB` | `sqlite` | `sqlite` or `postgres` |
| `GLOWWORM_DB_DSN` | — | required for postgres (`postgres://…`) |
| `GLOWWORM_TIMEZONE` | system | "today" boundaries, greeting |
| `GLOWWORM_SECRET_KEY` | generated | 32-byte key (hex/base64) for cookie signing + AI key encryption |
| `GLOWWORM_AUTH_MODE` | `password` | `none` disables login (trusted networks only) |
| `GLOWWORM_LOG_FORMAT` | `text` | `text` or `json` |
| `GLOWWORM_LOG_LEVEL` | `info` | slog level |
| `GLOWWORM_UPLOAD_MAX_MB` | `25` | statement upload cap |
| `GLOWWORM_TRUSTED_PROXIES` | — | CIDR list for `X-Forwarded-For` (behind NGINX) |

`glowworm.example.toml` documents the same keys.

## 4. Data directory layout

```
data/
├── glowworm.db        # SQLite (WAL mode enabled at open)
├── glowworm.db-wal
├── secret.key         # 0600, 32-byte random (AI keys + session signing)
└── tmp/               # upload scratch (cleaned on boot)
```

SQLite PRAGMAs at open: `journal_mode=WAL`, `busy_timeout=5000`,
`foreign_keys=ON`, `synchronous=NORMAL`.

## 5. Docker

`deploy/Dockerfile` (multi-stage, minimal):

```dockerfile
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/glowworm ./cmd/glowworm

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/glowworm /glowworm
ENV GLOWWORM_ADDR=0.0.0.0:8080 GLOWWORM_DATA_DIR=/data GLOWWORM_LOG_FORMAT=json
VOLUME /data
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/glowworm", "serve"]
```

`deploy/compose.yaml`:

```yaml
services:
  glowworm:
    build: .
    # image: ghcr.io/<owner>/glowworm:latest
    restart: unless-stopped
    ports: ["127.0.0.1:8080:8080"]
    volumes: ["glowworm-data:/data"]
    environment:
      GLOWWORM_TIMEZONE: "Europe/London"
      GLOWWORM_UPLOAD_MAX_MB: "25"
    healthcheck:
      test: ["CMD", "/glowworm"]        # distroless: use /healthz via wget sidecar if needed
      interval: 30s
volumes: { glowworm-data: {} }
```

(Healthcheck note: distroless has no shell/wget — either ship a tiny static
`glowworm healthcheck` subcommand that curls localhost, or rely on NGINX
upstream checks. The design ships `glowworm healthcheck --url
http://127.0.0.1:8080/healthz`.)

## 6. NGINX reverse proxy (typical public deployment)

`deploy/nginx.conf`:

```nginx
server {
    listen 80;
    server_name glowworm.example.com;
    location /.well-known/acme-challenge/ { root /var/www/certbot; }
    location / { return 301 https://$host$request_uri; }
}

server {
    listen 443 ssl http2;
    server_name glowworm.example.com;

    ssl_certificate     /etc/letsencrypt/live/glowworm.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/glowworm.example.com/privkey.pem;

    # security headers (Glowworm sets CSP too; belt-and-braces)
    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
    add_header X-Content-Type-Options nosniff always;
    add_header Referrer-Policy strict-origin-when-cross-origin always;

    client_max_body_size 25m;        # statement uploads (match GLOWWORM_UPLOAD_MAX_MB)

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 120s;     # AI calls can be slow (schema detection)
        proxy_buffering off;         # required for the NDJSON chat stream
        proxy_cache off;
    }

    # long-cache hashed static assets
    location /static/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        add_header Cache-Control "public, max-age=31536000, immutable";
    }
}
```

- `GLOWWORM_TRUSTED_PROXIES=127.0.0.1` on the app so real client IPs log
  correctly.
- TLS via certbot/Let's Encrypt or any wildcard cert; Glowworm itself serves
  plain HTTP and MUST NOT be exposed directly on public networks.
- Public exposure assumes `AUTH_MODE=password` (the login page is the only
  public surface; signup happens during setup only — doc 14 §2).

## 7. systemd (bare-metal)

`deploy/glowworm.service`:

```ini
[Unit]
Description=Glowworm finance tracker
After=network-online.target

[Service]
User=glowworm
StateDirectory=glowworm
Environment=GLOWWORM_DATA_DIR=/var/lib/glowworm
Environment=GLOWWORM_ADDR=127.0.0.1:8080
Environment=GLOWWORM_LOG_FORMAT=json
ExecStart=/usr/local/bin/glowworm serve
Restart=on-failure
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

## 8. Backups & restore

- **SQLite:** preferred `VPS (volume snapshot)` or
  `sqlite3 glowworm.db ".backup backup.db"` while running (WAL-safe), nightly
  cron; also `Litestream` is compatible (document in README) for continuous
  replication to S3-compatible storage.
- The secret.key MUST be backed up **separately and securely** — without it,
  stored AI keys and sessions are unrecoverable (data itself stays
  readable).
- **Postgres:** standard `pg_dump`/WAL tooling.
- Restore = stop, restore DB (+ secret.key), start; migrations are
  version-tracked so downgrades require the matching binary version.

## 9. Upgrades

1. Pull/replace binary or `docker compose pull && up -d`.
2. On boot, embedded migrations apply incrementally; a failed migration
   aborts startup (DB untouched — migrate runner is transactional per
   migration).
3. Release notes list breaking config changes; DB down-migrations exist for
   one minor version back (dev convenience).

## 10. Health & monitoring

- `GET /healthz` (no auth): `{"status":"ok","version":"..."}`
- `GET /readyz`: DB reachable + migrations current.
- Logs: structured; behind NGINX, real IP via `X-Forwarded-For`.
- Optional `GLOWWORM_METRICS=1` exposes `/metrics` (Prometheus): request
  histogram, AI call counters, job outcomes — bind to localhost only.

## 11. Multi-instance notes

Single-instance by design (SQLite file lock). Postgres mode allows one
writer + read replicas but the product does not coordinate multi-writers;
document "one instance per dataset" in the README.

## 12. Acceptance criteria

1. `docker compose up` from a clean checkout serves the UI with persisted
   data across restarts.
2. NGINX config passes `nginx -t`; chat streaming works through the proxy
   (buffering off verified); uploads of 25 MB succeed.
3. Binary + compose + systemd artefacts exist in `deploy/`; README documents
   all three paths.
4. Backup/restore drill documented and tested (SQLite backup while running;
   restore on a clean machine with secret.key).