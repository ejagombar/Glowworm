# Deploying Glowworm

Glowworm runs on the server as a plain binary under systemd, as your own user,
behind nginx at **https://glowworm.eagombar.uk**. It isn't in a container.

- **Releases.** Pushing a tag like `v1.2` or `v1.2.3` on `main` runs
  `.github/workflows/release.yml`. That workflow tests the code and builds it
  for arm64. It then publishes a GitHub release and deploys the release over SSH.
- **Backups.** A nightly systemd timer backs up the data and copies it to
  Google Drive with rclone. A backup is also taken on the server before every
  deploy.

Below, `ed` stands for your username on the server. Use yours.

## Layout on the server

| Path | What |
| --- | --- |
| `/opt/glowworm/releases/<tag>/glowworm` | one folder per release (the newest 5 are kept) |
| `/opt/glowworm/current` | symlink to the running release |
| `/opt/glowworm/backup.sh` | backup script (updated by each deploy) |
| `/var/lib/glowworm/` | data: `glowworm.db` and `secret.key` (created by systemd) |
| `/var/backups/glowworm/` | local backups, kept 14 days |
| `/etc/glowworm/glowworm.env` | settings and secrets for the service (optional) |
| `/etc/glowworm/backup.env` | backup settings (optional) |
| `~/glowworm-deploy/` | where the workflow uploads each release |

## One-off setup

1. **Folders.** Run:
   ```sh
   sudo mkdir -p /opt/glowworm/releases /var/backups/glowworm /etc/glowworm
   sudo chown -R ed: /opt/glowworm /var/backups/glowworm
   ```

2. **Port.** The service listens on `127.0.0.1:8094` (8080 is taken by the
   recipes site). Check it's free with `ss -ltn | grep 8094`. If it isn't,
   choose another port and set it in `/etc/glowworm/glowworm.env`
   (`GLOWWORM_ADDR=127.0.0.1:8095`). Change `proxy_pass` in the nginx config
   to match.

3. **Settings file.** Create `/etc/glowworm/glowworm.env`, readable only by
   root and `ed` (the deploy script reads the port from it). Give it the
   public address, which the setup link uses. Until the account exists the
   setup page needs a token: Glowworm makes one and logs the whole link, or set
   your own with `GLOWWORM_SETUP_TOKEN`:
   ```sh
   echo "GLOWWORM_PUBLIC_URL=https://glowworm.eagombar.uk" | sudo tee /etc/glowworm/glowworm.env
   sudo chown root:ed /etc/glowworm/glowworm.env && sudo chmod 640 /etc/glowworm/glowworm.env
   ```
   The unit already sets the data directory, JSON logs, timezone (Europe/London)
   and `GLOWWORM_TRUSTED_PROXIES=127.0.0.1`, which is needed behind nginx.

4. **systemd units.** Copy `glowworm@.service`, `glowworm-backup@.service` and
   `glowworm-backup@.timer` from this folder into `/etc/systemd/system/`. Then
   run:
   ```sh
   sudo systemctl daemon-reload
   sudo systemctl enable glowworm@ed          # starts at boot; first start comes with the first deploy
   sudo systemctl enable --now glowworm-backup@ed.timer
   ```

5. **sudo for deploys.** The deploy script only needs to restart the service,
   plus read its log when a deploy fails. Add a rule with
   `sudo visudo -f /etc/sudoers.d/glowworm`:
   ```
   ed ALL=(root) NOPASSWD: /usr/bin/systemctl restart glowworm@ed, /usr/bin/journalctl -u glowworm@ed *
   ```
   Check the path with `command -v systemctl`. If `ed` already has passwordless
   sudo, you can skip this step.

6. **nginx and HTTPS.** Point a `glowworm.eagombar.uk` DNS record at the
   server. Copy `nginx.conf` to `/etc/nginx/conf.d/glowworm.eagombar.uk.conf`
   (it's plain HTTP), then let certbot add HTTPS to it, as for the other sites:
   ```sh
   sudo nginx -t && sudo systemctl reload nginx
   sudo certbot --nginx -d glowworm.eagombar.uk
   ```
   `client_max_body_size`, `proxy_buffering off` and the 120s read timeout are
   all needed: the first two for statement uploads and streamed replies, the
   last for slow AI calls.

7. **Backups to Google Drive.** Glowworm backups contain all your data and the
   key that decrypts the saved AI keys, so encrypt them before they leave the
   server. If rclone already has a Google Drive remote, add a `crypt` remote on
   top of it. Otherwise set up both with `rclone config`, as `ed`:
   - a `drive` remote (e.g. `gdrive`); on a headless server, use rclone's
     "remote authorise" step from a machine with a browser
   - a `crypt` remote named `gdrive-crypt`, pointing at `gdrive:backups/glowworm`.
     Keep its passwords somewhere safe off the server, or the backups can't be
     read.

   If your remote has a different name, or you want different retention, set
   it in `/etc/glowworm/backup.env`:
   ```
   GLOWWORM_RCLONE_REMOTE=gdrive-crypt:glowworm
   GLOWWORM_BACKUP_KEEP=14
   GLOWWORM_REMOTE_KEEP=90
   ```
   If backups already go to Drive some other way, `/var/backups/glowworm` can
   be added to that instead. Set `GLOWWORM_RCLONE_REMOTE=` (empty) so only the
   local copies are made.

8. **GitHub.** In the glowworm repo's *Settings → Secrets and variables →
   Actions*, add `DEPLOY_KEY` (a private key in `ed`'s `authorized_keys`) and
   `SERVER_USER` (`ed`). These can be the same values as the website repo's.
   If the server isn't reached as `eagombar.uk`, also add a `SERVER_HOST`
   variable.

## First release

```sh
git checkout main && git pull
git tag v1.0.0 && git push origin v1.0.0
```

Watch it run under *Actions*. Then open the setup link to create the
account: it's in the log (`journalctl -u glowworm@ed`, the "no account yet"
line), or run `GLOWWORM_DATA_DIR=/var/lib/glowworm /opt/glowworm/current/glowworm setup-url`
as `ed`. If you set `GLOWWORM_SETUP_TOKEN`, it's
`https://glowworm.eagombar.uk/setup?token=<GLOWWORM_SETUP_TOKEN>`, and you can
remove it from `glowworm.env` once the account exists.

**Bringing data across.** To start from existing data instead, use *Settings →
Backup & deletion → Download full backup* on the old copy. Copy the zip to the
server, then run:
```sh
sudo systemctl stop glowworm@ed
GLOWWORM_DATA_DIR=/var/lib/glowworm /opt/glowworm/current/glowworm restore glowworm-backup-….zip
sudo systemctl start glowworm@ed
```

## Day to day

- **Release:** merge to `main`, then tag `vX.Y` or `vX.Y.Z` and push the tag.
  Tags that aren't on `main` are refused.
- **A deploy that doesn't come up healthy** within 30 seconds is rolled back to
  the previous release automatically, and the workflow fails. If that release
  had already migrated the database, restore the backup taken just before the
  deploy (`/var/backups/glowworm/*-before-<tag>.zip`).
- **Roll back by hand:**
  `ln -sfn /opt/glowworm/releases/v1.2.0 /opt/glowworm/current && sudo systemctl restart glowworm@ed`
- **Back up now:** `sudo systemctl start glowworm-backup@ed`. Check it with
  `journalctl -u glowworm-backup@ed -n 20`.
- **Logs:** `journalctl -u glowworm@ed -f`
