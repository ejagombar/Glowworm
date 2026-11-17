#!/usr/bin/env bash
# Install a Glowworm release on this server. The release workflow uploads the
# binary and these scripts to ~/glowworm-deploy and runs:
#
#   bash ~/glowworm-deploy/deploy.sh <tag>
#
# as the user Glowworm runs as. It backs up the data, switches
# /opt/glowworm/current to the new release, restarts the service and checks
# it's healthy. If it isn't, it switches back to the previous release.
#
# Needs: /opt/glowworm owned by this user, and sudo without a password for
#   systemctl restart glowworm@<user>   (see deploy/README.md)
set -euo pipefail

TAG=${1:?usage: deploy.sh <tag>}
SRC=$(cd "$(dirname "$0")" && pwd)
ROOT=${GLOWWORM_ROOT:-/opt/glowworm}
UNIT="glowworm@$(id -un)"
KEEP_RELEASES=5

# The address the service listens on, for the health check.
ADDR=127.0.0.1:8094
if [ -r /etc/glowworm/glowworm.env ]; then
	line=$(grep -E '^GLOWWORM_ADDR=' /etc/glowworm/glowworm.env | tail -1 || true)
	[ -n "$line" ] && ADDR=${line#GLOWWORM_ADDR=}
fi
HEALTH=${GLOWWORM_HEALTH_URL:-"http://${ADDR/0.0.0.0/127.0.0.1}/healthz"}

say() { echo "deploy: $*"; }

# 1. Unpack the release and check it runs on this machine.
rel="$ROOT/releases/$TAG"
mkdir -p "$rel"
install -m 755 "$SRC/glowworm-linux-arm64" "$rel/glowworm"
install -m 755 "$SRC/backup.sh" "$ROOT/backup.sh"
say "new binary reports version $("$rel/glowworm" version)"

prev=""
[ -L "$ROOT/current" ] && prev=$(readlink -f "$ROOT/current")
if [ "$prev" = "$(readlink -f "$rel")" ]; then
	say "$TAG is already the running release; restarting it"
	prev=""
fi

# 2. Back up first: a new release may migrate the database, and migrations
#    only go forward. Kept on the server only; the nightly run uploads.
DATA=${GLOWWORM_DATA_DIR:-/var/lib/glowworm}
if [ -n "$prev" ] && [ -e "$DATA/glowworm.db" ]; then
	GLOWWORM_RCLONE_REMOTE="" GLOWWORM_DATA_DIR="$DATA" \
		GLOWWORM_BIN="$prev/glowworm" "$ROOT/backup.sh" "before-$TAG"
fi

# 3. Switch over atomically and restart.
ln -sfn "$rel" "$ROOT/current.new"
mv -T "$ROOT/current.new" "$ROOT/current"
sudo -n systemctl restart "$UNIT"

healthy() {
	for _ in $(seq 1 30); do
		if "$ROOT/current/glowworm" healthcheck --url "$HEALTH" >/dev/null 2>&1; then
			return 0
		fi
		sleep 1
	done
	return 1
}

if healthy; then
	say "$TAG is live"
else
	say "$TAG did not come up healthy"
	sudo -n journalctl -u "$UNIT" -n 40 --no-pager 2>/dev/null || true
	if [ -n "$prev" ]; then
		say "rolling back to $(basename "$prev")"
		ln -sfn "$prev" "$ROOT/current.new"
		mv -T "$ROOT/current.new" "$ROOT/current"
		sudo -n systemctl restart "$UNIT"
		if healthy; then
			say "rolled back. If $TAG changed the database, the old release may still"
			say "misbehave: restore the backup taken above with glowworm restore."
		else
			say "the previous release is not healthy either: check journalctl -u $UNIT"
		fi
	fi
	exit 1
fi

# 4. Keep the newest few releases for rolling back by hand.
# shellcheck disable=SC2012 # release names are tags: no odd characters
ls -1dt "$ROOT"/releases/*/ | tail -n +$((KEEP_RELEASES + 1)) | while read -r old; do
	[ "$(readlink -f "$old")" = "$(readlink -f "$ROOT/current")" ] || rm -rf "$old"
done
