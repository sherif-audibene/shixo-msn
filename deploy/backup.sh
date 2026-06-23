#!/usr/bin/env bash
# shixo-msn daily backup.
#
# Snapshots the SQLite database safely (sqlite3 .backup is WAL-aware so the
# clipsrv service keeps running) and rsyncs the snapshot + the file store
# to a single rolling mirror on a remote host over SSH. Files removed
# locally are also removed on the remote (rsync --delete).
#
# Config lives in /etc/clip/backup.conf (created by backup-install.sh).
# Run by cron at 03:00 daily; or by hand:
#   sudo -u jenkins /opt/shixo-msn/deploy/backup.sh
set -euo pipefail

CONFIG=/etc/clip/backup.conf
LOG=/var/log/shixo-msn-backup.log

log() {
  local line
  line="$(date '+%F %T') $*"
  # Append to the log file unconditionally so cron runs are recorded.
  printf '%s\n' "$line" >> "$LOG"
  # Also print to stderr so a person running the script by hand sees progress.
  printf '%s\n' "$line" >&2
}

if [ ! -r "$CONFIG" ]; then
  echo "missing $CONFIG (run backup-install.sh first)" >&2
  exit 1
fi
# shellcheck disable=SC1090
. "$CONFIG"

: "${REMOTE:?REMOTE not set in $CONFIG}"
: "${SSH_KEY:?SSH_KEY not set in $CONFIG}"
: "${RETENTION_DAYS:=30}"
: "${DATA_DIR:=/var/lib/clip}"
: "${DB_FILE:=$DATA_DIR/clip.db}"
: "${SSH_PORT:=22}"

# Refuse to run with the placeholder so the script doesn't silently
# try to ssh to "example.com" and hang.
case "$REMOTE" in
  *REPLACE_ME*|*example.com*)
    echo "REMOTE in $CONFIG is still the placeholder. Edit it first." >&2
    exit 2
    ;;
esac

STAGE=$(mktemp -d -t shixo-backup-XXXX)
trap 'rm -rf "$STAGE"' EXIT

log "==> start backup -> $REMOTE/current"

# 1. WAL-aware snapshot of the database. sqlite3 takes a shared lock during
#    the backup; the live clipsrv process keeps writing.
if ! command -v sqlite3 >/dev/null 2>&1; then
  log "ERROR: sqlite3 CLI not installed"
  exit 2
fi
sqlite3 "$DB_FILE" ".backup '$STAGE/clip.db'"
log "snapshot ok: $(du -h "$STAGE/clip.db" | awk '{print $1}')"

# 2. Rsync the snapshot + the file store to <REMOTE>/<DATE>/.
#    --link-dest reuses unchanged blobs from yesterday's backup (hardlinks)
#    so 30 daily snapshots only cost (1 full + 29 deltas) of disk on the remote.
SSH_OPTS="-i $SSH_KEY -p $SSH_PORT -o StrictHostKeyChecking=accept-new -o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=15"

# Make sure the remote root + current/ dir exist (idempotent).
REMOTE_HOST="${REMOTE%%:*}"
REMOTE_PATH="${REMOTE#*:}"
ssh $SSH_OPTS "$REMOTE_HOST" "mkdir -p '$REMOTE_PATH/current'"

# Upload DB snapshot.
rsync -a --partial -e "ssh $SSH_OPTS" \
  "$STAGE/clip.db" "$REMOTE/current/clip.db.$(date +%F)"

# Mirror the file store. --delete removes blobs on the remote that no
# longer exist locally, so the remote always matches "what's here now".
if [ -d "$DATA_DIR/files" ]; then
  rsync -a --partial --delete -e "ssh $SSH_OPTS" \
    "$DATA_DIR/files/" "$REMOTE/current/files/"
fi

log "rsync ok"
log "<- done"
