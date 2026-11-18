#!/bin/sh
# Install Glowworm on a Linux machine with systemd:
#
#   curl -fsSL https://github.com/ejagombar/Glowworm/releases/latest/download/install.sh | sh
#
# It downloads the latest release for this machine and checks it against the
# release's checksums, installs it as /usr/local/bin/glowworm, creates a
# glowworm user with its data in /var/lib/glowworm, and runs it as the
# glowworm systemd service. It asks for the address you'll open Glowworm at,
# can set up Caddy for HTTPS and daily updates, and ends with the link (and
# a QR code) that creates your account.
#
# Run it again to change those answers or reinstall; the data stays. Without
# a terminal it asks nothing and takes these from the environment instead:
#   GLOWWORM_PUBLIC_URL   e.g. https://money.example.com
#   GLOWWORM_ADDR         what to listen on (default 127.0.0.1:8080, or
#                         0.0.0.0:8080 to be reachable on your network)
#   GLOWWORM_CADDY=yes    install Caddy and serve GLOWWORM_PUBLIC_URL with HTTPS
#   GLOWWORM_AUTO_UPDATE=yes  install new releases daily
#   GLOWWORM_VERSION      a release tag such as v1.4.0 (default: the latest)
#   GLOWWORM_BINARY       a glowworm binary to install instead of downloading
set -eu

REPO=ejagombar/Glowworm
BIN=/usr/local/bin/glowworm
DATA=/var/lib/glowworm
ENV_DIR=/etc/glowworm
ENV_FILE=$ENV_DIR/glowworm.env
UNIT_DIR=/etc/systemd/system

say() { printf '%s\n' "$*"; }
die() { printf 'install: %s\n' "$*" >&2; exit 1; }

interactive() { [ -r /dev/tty ] && [ -w /dev/tty ] && [ -z "${GLOWWORM_NONINTERACTIVE:-}" ]; }

# ask QUESTION DEFAULT: the answer from the terminal, or DEFAULT without one.
ask() {
	if interactive; then
		printf '%s ' "$1" >/dev/tty
		read -r reply </dev/tty || reply=
		[ -n "$reply" ] && { printf '%s' "$reply"; return; }
	fi
	printf '%s' "$2"
}
yes_to() { case $1 in [Yy]*) return 0 ;; *) return 1 ;; esac; }

# --- Checks ---------------------------------------------------------------

[ "$(uname -s)" = Linux ] || die "this script is for Linux; on macOS or Windows download the binary from https://github.com/$REPO/releases"
[ -d /run/systemd/system ] || die "this script needs systemd. Run the binary yourself, or use the container image (see the README)"
if [ "$(id -u)" -ne 0 ]; then
	command -v sudo >/dev/null || die "run this as root"
	say "Installing needs root; asking sudo."
	if [ -f "$0" ] && [ "$0" != sh ]; then
		exec sudo -E sh "$0" "$@"
	fi
	# Piped into sh: fetch the script again under sudo.
	exec sudo -E sh -c "curl -fsSL https://github.com/$REPO/releases/latest/download/install.sh | sh"
fi
# deploy.sh's layout, run by the release workflow, isn't this script's to touch.
if [ -e /opt/glowworm/current ]; then
	die "/opt/glowworm/current exists: this machine runs Glowworm through deploy.sh (deploy/README.md)"
fi

case $(uname -m) in
x86_64 | amd64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
*) die "no release is built for $(uname -m); build from source with: go install github.com/$REPO/cmd/glowworm@latest" ;;
esac

# --- The binary -----------------------------------------------------------

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
trap 'exit 1' INT TERM

if [ -n "${GLOWWORM_BINARY:-}" ]; then
	cp "$GLOWWORM_BINARY" "$TMP/glowworm"
else
	command -v curl >/dev/null || die "needs curl"
	VERSION=${GLOWWORM_VERSION:-latest}
	if [ "$VERSION" = latest ]; then
		BASE=https://github.com/$REPO/releases/latest/download
	else
		BASE=https://github.com/$REPO/releases/download/$VERSION
	fi
	ASSET=glowworm-linux-$ARCH
	say "Downloading $ASSET ($VERSION)…"
	curl -fsSL -o "$TMP/glowworm" "$BASE/$ASSET" || die "download failed: $BASE/$ASSET"
	curl -fsSL -o "$TMP/checksums.txt" "$BASE/checksums.txt" || die "download failed: $BASE/checksums.txt"
	want=$(awk -v n="$ASSET" '$2 == n { print $1 }' "$TMP/checksums.txt")
	got=$(sha256sum "$TMP/glowworm" | awk '{ print $1 }')
	if [ -z "$want" ] || [ "$want" != "$got" ]; then
		die "$ASSET doesn't match its checksum; not installing it"
	fi
fi
chmod 755 "$TMP/glowworm"
"$TMP/glowworm" version >/dev/null 2>&1 || die "the downloaded binary doesn't run on this machine"
NEW_VERSION=$("$TMP/glowworm" version)

# Upgrading: back up first, as `glowworm update` does.
if [ -x "$BIN" ] && [ -e "$DATA/glowworm.db" ]; then
	mkdir -p "$DATA/backups"
	out="$DATA/backups/before-$NEW_VERSION-$(date +%Y%m%d-%H%M%S).zip"
	if GLOWWORM_DATA_DIR=$DATA "$BIN" backup -o "$out" >/dev/null; then
		chown -R glowworm:glowworm "$DATA/backups" 2>/dev/null || true
		say "Backed up the data to $out"
	else
		die "couldn't back up the data first; not upgrading"
	fi
fi
install -m 755 "$TMP/glowworm" "$BIN"
say "Installed Glowworm $NEW_VERSION as $BIN"

# --- User, settings and service ------------------------------------------

if ! id glowworm >/dev/null 2>&1; then
	useradd --system --home-dir "$DATA" --no-create-home --shell /usr/sbin/nologin glowworm
fi
mkdir -p "$DATA" "$ENV_DIR"
chown glowworm:glowworm "$DATA"
chmod 700 "$DATA"

# current NAME: its value in the settings file from an earlier install.
current() {
	if [ -r "$ENV_FILE" ]; then sed -n "s/^$1=//p" "$ENV_FILE" | tail -1; fi
}

say ""
say "Glowworm needs an address to be opened at. With a domain name pointing at"
say "this machine, it can have HTTPS, which passkeys, reminders and installing it"
say "as an app need. Leave it empty to open it on this machine or your network."
PUBLIC_URL=${GLOWWORM_PUBLIC_URL-$(current GLOWWORM_PUBLIC_URL)}
PUBLIC_URL=$(ask "Address (e.g. https://money.example.com) [${PUBLIC_URL:-none}]:" "$PUBLIC_URL")
[ "$PUBLIC_URL" = none ] && PUBLIC_URL=
PUBLIC_URL=${PUBLIC_URL%/}
case $PUBLIC_URL in
"" | http://* | https://*) ;;
*) PUBLIC_URL=https://$PUBLIC_URL ;;
esac

CADDY=no
ADDR=${GLOWWORM_ADDR:-$(current GLOWWORM_ADDR)}
if [ -n "$PUBLIC_URL" ]; then
	ADDR=${GLOWWORM_ADDR:-127.0.0.1:8080} # behind the proxy
	case $PUBLIC_URL in https://*)
		if command -v caddy >/dev/null; then
			# Not touching a Caddy that's already set up for other sites.
			say "Caddy is already installed: add $PUBLIC_URL with reverse_proxy $ADDR (see deploy/Caddyfile)."
		elif interactive; then
			CADDY=$(ask "Install Caddy to serve $PUBLIC_URL with HTTPS? Needs ports 80 and 443 [Y/n]:" yes)
		else
			CADDY=${GLOWWORM_CADDY:-no}
		fi
		;;
	esac
elif [ -z "$ADDR" ]; then
	open=$(ask "Open Glowworm to other devices on your network, over plain HTTP? [y/N]:" no)
	if yes_to "$open"; then ADDR=0.0.0.0:8080; else ADDR=127.0.0.1:8080; fi
fi

UPDATES=$(ask "Install new releases automatically, daily? [y/N]:" "${GLOWWORM_AUTO_UPDATE:-no}")

# Keep any other settings in the file; replace the ones asked about.
touch "$ENV_FILE"
grep -vE '^(GLOWWORM_PUBLIC_URL|GLOWWORM_ADDR|GLOWWORM_TRUSTED_PROXIES)=' "$ENV_FILE" >"$TMP/env" || true
{
	cat "$TMP/env"
	say "GLOWWORM_ADDR=$ADDR"
	if [ -n "$PUBLIC_URL" ]; then
		say "GLOWWORM_PUBLIC_URL=$PUBLIC_URL"
		say "GLOWWORM_TRUSTED_PROXIES=127.0.0.1"
	fi
} >"$ENV_FILE"
chown root:glowworm "$ENV_FILE"
chmod 640 "$ENV_FILE"

cat >"$UNIT_DIR/glowworm.service" <<EOF
# Installed by Glowworm's install.sh. Settings: $ENV_FILE
[Unit]
Description=Glowworm finance tracker
Wants=network-online.target
After=network-online.target

[Service]
User=glowworm
Group=glowworm
Environment=GLOWWORM_DATA_DIR=$DATA
EnvironmentFile=-$ENV_FILE
ExecStart=$BIN serve
Restart=on-failure
RestartSec=3
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=$DATA

[Install]
WantedBy=multi-user.target
EOF

if yes_to "$UPDATES"; then
	cat >"$UNIT_DIR/glowworm-update.service" <<EOF
# Installed by Glowworm's install.sh: installs a new release, after a backup.
[Unit]
Description=Install the latest Glowworm release
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
Environment=GLOWWORM_DATA_DIR=$DATA
EnvironmentFile=-$ENV_FILE
ExecStart=$BIN update --restart glowworm.service
EOF
	cat >"$UNIT_DIR/glowworm-update.timer" <<'EOF'
# Installed by Glowworm's install.sh.
[Unit]
Description=Look for a new Glowworm release daily

[Timer]
OnCalendar=*-*-* 04:00
RandomizedDelaySec=2h
Persistent=true

[Install]
WantedBy=timers.target
EOF
fi

systemctl daemon-reload
systemctl enable glowworm.service >/dev/null 2>&1
systemctl restart glowworm.service
if yes_to "$UPDATES"; then
	systemctl enable --now glowworm-update.timer >/dev/null 2>&1
	say "New releases will be installed daily (systemctl disable glowworm-update.timer to stop)."
elif [ -e "$UNIT_DIR/glowworm-update.timer" ]; then
	systemctl disable --now glowworm-update.timer >/dev/null 2>&1 || true
fi

# --- Caddy ----------------------------------------------------------------

if yes_to "$CADDY"; then
	if ! command -v apt-get >/dev/null; then
		say "Install Caddy yourself (https://caddyserver.com/docs/install) and use deploy/Caddyfile."
	else
		say "Installing Caddy…"
		apt-get install -y -qq caddy >/dev/null
		host=${PUBLIC_URL#https://}
		cat >/etc/caddy/Caddyfile <<EOF
# Written by Glowworm's install.sh: HTTPS for $PUBLIC_URL, passed to Glowworm.
$host {
	encode zstd gzip
	reverse_proxy 127.0.0.1:${ADDR##*:}
}
EOF
		systemctl reload-or-restart caddy
		say "Caddy serves $PUBLIC_URL (it gets the certificate on the first visit)."
	fi
fi

# --- Done -----------------------------------------------------------------

for _ in 1 2 3 4 5 6 7 8 9 10; do
	"$BIN" healthcheck --url "http://127.0.0.1:${ADDR##*:}/healthz" >/dev/null 2>&1 && break
	sleep 1
done
"$BIN" healthcheck --url "http://127.0.0.1:${ADDR##*:}/healthz" >/dev/null 2>&1 ||
	die "Glowworm didn't start; see: journalctl -u glowworm"

say ""
say "Glowworm is running (systemctl status glowworm; logs: journalctl -u glowworm)."
(
	set -a
	# shellcheck source=/dev/null
	. "$ENV_FILE"
	set +a
	GLOWWORM_DATA_DIR=$DATA runuser -u glowworm -- "$BIN" setup-url --qr
)
say "Update later with: sudo glowworm update --restart glowworm"
