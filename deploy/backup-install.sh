#!/usr/bin/env bash
# Idempotent installer for the shixo-msn daily SSH backup.
#
# Installs sqlite3 + rsync, generates a dedicated ed25519 key for the
# jenkins user, writes /etc/clip/backup.conf with placeholders, and
# installs a cron entry for 03:00 daily.
#
#   sudo bash backup-install.sh
#
# Re-running is safe — won't overwrite existing config or key.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

if [ "$(id -u)" -ne 0 ]; then
  echo "Please run as root: sudo bash $0" >&2
  exit 1
fi

SVC_USER=jenkins
DEPLOY_DIR=/opt/shixo-msn
CONFIG_DIR=/etc/clip
CONFIG_FILE=$CONFIG_DIR/backup.conf
SSH_KEY_PATH=$CONFIG_DIR/shixo_backup_ed25519
LOG=/var/log/shixo-msn-backup.log
CRON_FILE=/etc/cron.d/shixo-msn-backup

id "$SVC_USER" >/dev/null 2>&1 || { echo "User '$SVC_USER' not found."; exit 1; }

echo "==> [1/5] System packages (sqlite3, rsync, openssh-client)"
apt-get update -y
apt-get install -y sqlite3 rsync openssh-client

echo "==> [2/5] Backup config ($CONFIG_FILE)"
mkdir -p "$CONFIG_DIR"
if [ -f "$CONFIG_FILE" ]; then
  echo "    Exists — leaving as-is."
else
  umask 077
  cat > "$CONFIG_FILE" <<EOF
# /etc/clip/backup.conf
# Edit REMOTE before the first run. Format: user@host:/absolute/path
REMOTE="REPLACE_ME@example.com:/srv/backups/shixo-msn"

# Optional SSH port (default 22)
SSH_PORT=22

# Path to the SSH private key used for backups.
SSH_KEY="$SSH_KEY_PATH"

# Server data dir + DB path (match clipsrv config).
DATA_DIR="/var/lib/clip"
DB_FILE="\$DATA_DIR/clip.db"
EOF
  chmod 640 "$CONFIG_FILE"
  chown root:"$SVC_USER" "$CONFIG_FILE"
  echo "    Wrote template — edit REMOTE before the first run."
fi

echo "==> [3/5] SSH key for $SVC_USER ($SSH_KEY_PATH)"
if [ -f "$SSH_KEY_PATH" ]; then
  echo "    Exists — leaving as-is."
else
  # ssh-keygen needs to write to /etc/clip/, which is root-owned. Generate
  # as root, then hand ownership to the service user that runs the backup.
  ssh-keygen -q -t ed25519 -N '' -C "shixo-msn-backup@$(hostname)" -f "$SSH_KEY_PATH"
  chown "$SVC_USER:$SVC_USER" "$SSH_KEY_PATH" "$SSH_KEY_PATH.pub"
  chmod 600 "$SSH_KEY_PATH"
  chmod 644 "$SSH_KEY_PATH.pub"
fi

echo "==> [4/5] Log file ($LOG)"
touch "$LOG"
chown "$SVC_USER:$SVC_USER" "$LOG"
chmod 640 "$LOG"

echo "==> [5/5] Cron entry ($CRON_FILE)"
cat > "$CRON_FILE" <<EOF
# Daily shixo-msn backup at 03:00 local time.
# Edit /etc/clip/backup.conf for target + retention.
SHELL=/bin/sh
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
0 3 * * * $SVC_USER $DEPLOY_DIR/deploy/backup.sh >> $LOG 2>&1
EOF
chmod 644 "$CRON_FILE"

echo
echo "================================================================"
echo "✅ Backup installed."
echo
echo "Next steps:"
echo "  1. Edit /etc/clip/backup.conf and set REMOTE to your destination."
echo "     (e.g. backups@nas.example.com:/srv/backups/shixo-msn)"
echo
echo "  2. Authorize this key on the remote host. The public key is:"
echo "----------------------------------------------------------------"
cat "$SSH_KEY_PATH.pub"
echo "----------------------------------------------------------------"
echo "     On the remote, append it to ~/.ssh/authorized_keys for the"
echo "     user named in REMOTE. Recommended hardening — restrict the key"
echo "     to rsync-only by prefixing the line with:"
echo "       command=\"rrsync /srv/backups/shixo-msn\",no-pty,no-agent-forwarding,no-port-forwarding "
echo "     (rrsync ships in /usr/share/doc/rsync/scripts/ on Debian)"
echo
echo "  3. Test it manually:"
echo "       sudo -u $SVC_USER $DEPLOY_DIR/deploy/backup.sh"
echo
echo "  4. Cron will run it daily at 03:00. Logs: tail -f $LOG"
echo "================================================================"
