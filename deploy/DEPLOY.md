# Deploying shixo-msn

## Quick path (this server: Debian + Jenkins on one box, fronted by Cloudflare Tunnel)

Origin runs HTTP on `127.0.0.1:6303`; Cloudflare Tunnel provides the public
HTTPS hostname. No Nginx/certbot needed.

```bash
# on the server, once, as a sudo user:
curl -fsSL https://raw.githubusercontent.com/sherif-audibene/shixo-msn/main/deploy/provision.sh -o /tmp/provision.sh
sudo bash /tmp/provision.sh
```

This installs everything (Go, git, openssl), clones + builds the server,
writes `/etc/clip/config.toml` with a generated bearer token, and starts the
service as a SysV init script (`sudo service shixo-msn {start|stop|restart|status}`).

The script prints the generated token at the end — copy it into each desktop
client's `~/.clip/config.toml`.

Then add a Cloudflare Tunnel ingress: `your-hostname -> http://localhost:6303`.

**Future updates** are handled by the Jenkins job **shixo-msn** (build → atomic
binary swap into `/opt/shixo-msn/clipsrv` → restart) — `provision.sh` also adds
the sudoers rule that lets Jenkins restart the service.

## Jenkins job setup

Create the pipeline job once via the helper:

```bash
export JENKINS_URL="https://your-jenkins.example"
export JENKINS_USER="you"
export JENKINS_TOKEN="api-token"
./deploy/jenkins-create-job.sh             # default job name: shixo-msn
```

Or in the Jenkins UI: New Item → Pipeline → Pipeline script from SCM →
Git URL `https://github.com/sherif-audibene/shixo-msn.git`, branch `main`,
script path `Jenkinsfile`.

Trigger: a GitHub webhook (`/github-webhook/`) or **Poll SCM**.

The pipeline:

1. `go vet ./...` — quick sanity check
2. `go build -ldflags="-s -w" -o dist/clipsrv ./cmd/clipsrv` (CGO off, pure Go)
3. Atomic swap of `/opt/shixo-msn/clipsrv` (mv-onto-running is safe on Linux)
4. `sudo service shixo-msn restart`
5. Smoke test: `curl http://127.0.0.1:6303/api/health` must return `ok`

If any stage fails the previous binary keeps running.

## Files this drops on the server

| Path | Owner | Purpose |
|---|---|---|
| `/opt/shixo-msn/` | `jenkins:jenkins` | clone of the repo + the built `clipsrv` binary |
| `/etc/clip/config.toml` | `root:jenkins`, 0640 | listen addr, data dir, bearer token |
| `/var/lib/clip/` | `jenkins:jenkins` | SQLite DB + uploaded files |
| `/var/log/shixo-msn.log` | `jenkins` | service log |
| `/etc/init.d/shixo-msn` | `root`, 0755 | SysV init script |
| `/etc/sudoers.d/shixo-msn` | `root`, 0440 | lets jenkins restart the service |

## Useful checks

```bash
sudo service shixo-msn status
tail -f /var/log/shixo-msn.log
curl -fsS http://127.0.0.1:6303/api/health        # → ok
curl -H "Authorization: Bearer <TOKEN>" http://127.0.0.1:6303/api/items
```

## Rotating the token

1. Edit `/etc/clip/config.toml` with a new value (`openssl rand -base64 48`).
2. `sudo service shixo-msn restart`.
3. Update each client's `~/.clip/config.toml` and relaunch the GUI.

## Daily off-site backup over SSH

Optional one-time setup. Snapshots SQLite via `sqlite3 .backup` (WAL-safe,
no service downtime) and rsyncs DB + file store to a **single rolling
mirror** on a remote host every day at 03:00 local time.

```bash
# on the cloud server, once:
sudo bash /opt/shixo-msn/deploy/backup-install.sh
```

That installs `sqlite3` + `rsync`, generates a dedicated ed25519 key at
`/etc/clip/shixo_backup_ed25519`, writes a template
`/etc/clip/backup.conf`, and installs the cron entry.

Then:
1. Edit `/etc/clip/backup.conf` and set `REMOTE=user@host:/path`.
2. Add the printed public key to that remote's `~/.ssh/authorized_keys`.
3. Test once: `sudo -u jenkins /opt/shixo-msn/deploy/backup.sh`.

**Layout on the remote:**

```
/srv/backups/shixo-msn/current/
├── clip.db
└── files/<item-id>/<filename>
```

The mirror always reflects the most recent backup. Deletes on the source
propagate via `rsync --delete` — no point-in-time history is kept, only
the latest state. Disk use ≈ size of your live data.

If you'd rather keep N daily snapshots (point-in-time recovery), look at
`git log` of `deploy/backup.sh` — the previous revision used
`--link-dest` to hardlink unchanged blobs across daily directories.

**Restore** is just rsync in reverse. Stop the service, replace
`/var/lib/clip/clip.db` and `/var/lib/clip/files/` from the mirror,
restart.

```bash
sudo service shixo-msn stop
sudo rsync -a --delete user@host:/srv/backups/shixo-msn/current/ /var/lib/clip/
sudo chown -R jenkins:jenkins /var/lib/clip
sudo service shixo-msn start
```

## Notes & gotchas

- **Health endpoint** (`/api/health`) is intentionally unauthenticated so the
  Jenkins smoke test and Cloudflare's liveness probes work without the token.
  Nothing sensitive leaks through it.
- **History is forever** — `du -sh /var/lib/clip/files` to check disk use.
- **Builds run on the box** — needs Go 1.23+ for the jenkins user. The
  `provision.sh` installs Go into `/usr/local/go` and symlinks `/usr/local/bin/go`.
- **Atomic restarts**: the Jenkinsfile copies `clipsrv.new` then `mv -f` over
  the running binary. The kernel keeps the old inode mapped until the running
  process exits, so the restart can never read a half-written binary.
- **Rollback**: `git revert` on `main` triggers another Jenkins build that
  rolls forward to the older code. There's no separate "previous" copy kept
  on disk; if you need that, snapshot `/opt/shixo-msn/clipsrv` to
  `clipsrv.prev` in the Deploy stage.
