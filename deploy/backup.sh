#!/usr/bin/env bash
# Back up Glowworm and copy the backup to Google Drive.
#
# Run nightly by glowworm-backup@<user>.service, and by deploy.sh before every
# upgrade. Settings (all optional) come from /etc/glowworm/backup.env:
#
#   GLOWWORM_BACKUP_DIR      where backups are kept on the server
#                            (default /var/backups/glowworm)
#   GLOWWORM_BACKUP_KEEP     days to keep them on the server (default 14)
#   GLOWWORM_RCLONE_REMOTE   rclone destination; empty skips the upload
#                            (default gdrive-crypt:glowworm)
#   GLOWWORM_REMOTE_KEEP     days to keep them on Drive (default 90)
#
# A backup holds all the data and the key that decrypts the saved AI keys, so
# the remote should be an rclone "crypt" remote layered over Google Drive.
set -euo pipefail

BIN=${GLOWWORM_BIN:-/opt/glowworm/current/glowworm}
DIR=${GLOWWORM_BACKUP_DIR:-/var/backups/glowworm}
KEEP=${GLOWWORM_BACKUP_KEEP:-14}
REMOTE=${GLOWWORM_RCLONE_REMOTE-gdrive-crypt:glowworm}
REMOTE_KEEP=${GLOWWORM_REMOTE_KEEP:-90}
LABEL=${1:-nightly}

mkdir -p "$DIR"
chmod 700 "$DIR"
out="$DIR/glowworm-$(date -u +%Y-%m-%dT%H%M%SZ)-$LABEL.zip"
"$BIN" backup -o "$out" >/dev/null
echo "backup written: $out ($(du -h "$out" | cut -f1))"

find "$DIR" -name 'glowworm-*.zip' -mtime +"$KEEP" -delete

if [ -n "$REMOTE" ]; then
	rclone copy "$out" "$REMOTE" --log-level NOTICE
	rclone delete "$REMOTE" --min-age "${REMOTE_KEEP}d" --include 'glowworm-*.zip' --log-level NOTICE
	echo "uploaded to $REMOTE"
fi
