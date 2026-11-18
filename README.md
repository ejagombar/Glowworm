# Glowworm

**Finance, made clear.** An open-source, self-hosted personal finance tracker:
drop in bank statements, let AI detect their format, categorise transactions
automatically, detect transfers and recurring bills, and ask questions about
your money in plain English.

## Running Glowworm

Glowworm is one program with its data in one directory. Pick how to run it:

| Way | Good for | Updates |
|---|---|---|
| [Install script](#install-script-linux) | A Linux server or Raspberry Pi with systemd | `sudo glowworm update`, or daily by itself |
| [Container](#container-docker-or-podman) | Machines that already run Docker or Podman | Pull the new image; Podman can do it by itself |
| [Binary](#binary) | macOS, Windows, or running it your own way | `glowworm update` |
| [From source](#from-source) | Working on Glowworm | `git pull`, build again |

Whichever you pick, the first start prints a link that creates your account,
with a QR code to open it on your phone:

```
  Glowworm is ready to set up. Open this address to create the account:

    https://money.example.com/setup?token=4GHBEWOKPFPAQI6ZC3PHLX64UC
```

Until the account exists, the setup page needs that token, so nobody else can
claim a new Glowworm first. `glowworm setup-url --qr` shows the link again.

For HTTPS, which passkeys, reminders and installing Glowworm as an app need,
give it a domain name: set `GLOWWORM_PUBLIC_URL` to the address and put a
proxy in front. Caddy gets the certificate by itself (`deploy/Caddyfile`;
the install script and the compose file can set it up). Glowworm then checks
it can reach itself at that address when it starts.

### Install script (Linux)

```bash
curl -fsSL https://github.com/ejagombar/Glowworm/releases/latest/download/install.sh | sh
```

It downloads the latest release and checks its checksum, then runs Glowworm
as the `glowworm` systemd service, with its data in `/var/lib/glowworm` and
settings in `/etc/glowworm/glowworm.env`. It asks for the address you'll use,
whether to set up Caddy for HTTPS and whether to install updates daily, then
prints the setup link. Run it again to change the answers; the data stays.
Read [the script](deploy/install.sh) first if you like; it also runs without
questions (the settings it reads are at the top).

### Container (Docker or Podman)

The image is `ghcr.io/ejagombar/glowworm`, for amd64 and arm64:

```bash
cd deploy
docker compose up -d
docker compose logs glowworm     # the setup link and QR code
```

That serves http://localhost:8080. For HTTPS at a domain pointing at the
machine, put `GLOWWORM_PUBLIC_URL=https://money.example.com` in `deploy/.env`
and run `docker compose --profile caddy up -d`. To update:
`docker compose pull && docker compose up -d`. Watchtower can also do this.

With Podman, `deploy/glowworm.container` is a Quadlet: systemd runs the
container, and `podman-auto-update.timer` keeps it on the latest image (the
file has the steps). Or, by hand:

```bash
podman run -d --name glowworm -p 127.0.0.1:8080:8080 -v glowworm-data:/data ghcr.io/ejagombar/glowworm
```

For a host directory instead of a named volume, add `:Z` on SELinux systems
(`-v ./data:/data:Z`); the container runs as uid 65532.

### Binary

Download `glowworm-<os>-<arch>` for your machine from
[the releases](https://github.com/ejagombar/Glowworm/releases) (Linux and
macOS on amd64 and arm64, Windows on amd64), check it against
`checksums.txt`, and run:

```bash
./glowworm serve
```

It listens on http://127.0.0.1:8080 and keeps its data in `./data`. With Go
installed, `go install github.com/ejagombar/Glowworm/cmd/glowworm@latest`
builds the same thing into `~/go/bin`.

### From source

```bash
make build
GLOWWORM_ADDR=127.0.0.1:8080 ./glowworm serve
```

`make release` builds every platform into `dist/`, and `make image` builds the
container image.

### Behind NGINX

`deploy/nginx.conf` provides the TLS reverse proxy (statement uploads sized
to `client_max_body_size 25m`; NDJSON chat streaming needs `proxy_buffering
off`). Set `GLOWWORM_TRUSTED_PROXIES=127.0.0.1`.

### Updates

A release build looks for a newer release once a day (it asks GitHub's API,
sending only the server's address and the version), and Settings shows when
one is out and how to install it. `GLOWWORM_UPDATE_CHECK=off` stops it.
`glowworm update` downloads the new binary, checks it, backs up the data to
`<data dir>/backups`, and replaces itself; `--restart glowworm` then restarts
the service. A new version can upgrade the database; to go back to an older
one, restore the backup taken before the update.

### Releases and the live server

Tagging `vX.Y` or `vX.Y.Z` on `main` builds, publishes and deploys to the
server at glowworm.eagombar.uk, with nightly backups to Google Drive. See
[deploy/README.md](deploy/README.md) for the setup.

## Configuration

Environment variables (see `glowworm.example.toml` for the file equivalent):

| Var | Default | Notes |
|---|---|---|
| `GLOWWORM_ADDR` | `127.0.0.1:8080` | Listen address |
| `GLOWWORM_DATA_DIR` | `./data` | SQLite DB + secret.key |
| `GLOWWORM_TIMEZONE` | system | "Today" boundaries |
| `GLOWWORM_AUTH_MODE` | `password` | `none` only for trusted networks |
| `GLOWWORM_UPLOAD_MAX_MB` | `25` | Statement upload cap |
| `GLOWWORM_PUBLIC_URL` | — | The address you open Glowworm at, e.g. `https://money.example.com`. Used for the setup link and its QR code |
| `GLOWWORM_SETUP_TOKEN` | made on first run | Token the setup page needs while no account exists. Unset: Glowworm makes one and prints the link at startup (`glowworm setup-url` shows it again). `off`: no token |
| `GLOWWORM_UPDATE_CHECK` | `on` | `off` stops the daily look for a new release |
| `GLOWWORM_TRUSTED_PROXIES` | — | Proxy addresses or ranges whose `X-Forwarded-For` is believed, e.g. `127.0.0.1` |
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
deploy/                Dockerfile, compose, NGINX, systemd
```

## License

TBD by the maintainer.
