# shixo-msn

Tiny self-hosted "paste it here, see it everywhere" app. One window per machine. Paste text or drop a file; it shows up on every other machine connected to the same server. History kept forever, no file size limit, with upload/download progress.

## Architecture

```
┌─────────┐    HTTPS + WebSocket    ┌──────────────────┐
│ mac GUI │  ─────────────────────▶ │  Linux cloud box │
└─────────┘                         │  ┌────────────┐  │
┌─────────┐                         │  │  clipsrv   │  │
│ win GUI │  ─────────────────────▶ │  │ SQLite +   │  │
└─────────┘                         │  │ files/     │  │
┌─────────┐                         │  └────────────┘  │
│ lin GUI │  ─────────────────────▶ │      Caddy       │
└─────────┘                         └──────────────────┘
```

- **Server** (`clipsrv`): pure Go (no CGO), SQLite + flat-file storage, bearer-token auth, WebSocket push.
- **Client** (`shixo-msn`): Go + Fyne window. Paste text, drop files, click history entries to copy back or save to disk.
- **Transport**: HTTPS terminated by Caddy in front. Long-lived bodies for huge files; WebSocket for push notifications.

## 1. Deploy the server

The cloud Linux box uses **SysV init + Jenkins on the same host, fronted by
Cloudflare Tunnel** — see [deploy/DEPLOY.md](deploy/DEPLOY.md) for the full
walkthrough.

TL;DR — one-shot installer (run once as root on the server):

```bash
curl -fsSL https://raw.githubusercontent.com/sherif-audibene/shixo-msn/main/deploy/provision.sh -o /tmp/provision.sh
sudo bash /tmp/provision.sh
```

It installs Go, clones + builds, writes `/etc/clip/config.toml` with a freshly
generated bearer token, registers a SysV service, and adds the sudoers rule
Jenkins needs. After it finishes, point a Cloudflare Tunnel ingress at
`http://localhost:6303`.

Future pushes to `main` are auto-deployed by the Jenkins job set up via
`deploy/jenkins-create-job.sh`.

## 2. Build + run the GUI on each machine

Fyne uses native graphics; requires a C toolchain on the target machine.

### macOS (Apple Silicon)

```bash
xcode-select --install   # one-time
make gui-mac
./dist/shixo-msn         # first run prompts for server URL + token
```

### Windows x64

Install Go from <https://go.dev> and TDM-GCC or MSYS2 mingw. Then in a shell:

```bash
make gui-windows
.\dist\shixo-msn.exe
```

### Linux x64 (desktop)

```bash
sudo apt install -y gcc libgl1-mesa-dev xorg-dev libglu1-mesa-dev libxcursor-dev libxrandr-dev libxinerama-dev libxi-dev libxxf86vm-dev
make gui-linux
./dist/shixo-msn-linux
```

### Or: cross-build all three from the Linux box

```bash
go install github.com/fyne-io/fyne-cross@latest
# requires Docker
make gui-cross-all
# binaries land under fyne-cross/dist/
```

## 3. First-run setup

When you launch the GUI for the first time, it prompts for:

- **Server URL**: your Cloudflare Tunnel hostname (e.g. `https://clip.yourdomain.com`)
- **Token**: paste the bearer token from `/etc/clip/config.toml` (provision.sh printed it on first install)

It saves to `~/.clip/config.toml` and connects.

## 4. How to use

- **Paste text** into the top box → click `Send Text` → appears on every other machine immediately.
- **Drag a file** anywhere on the window (or click `Send File...`) → uploads with a progress bar.
- **History list** shows everything received from every machine. Click `Copy` on a text item to put it on your clipboard; click `Save` on a file item to download it to disk.
- When a new item arrives from another machine, the window pops to the front and a system beep + notification fire.

## Notes & limits

- **No size limit**: server streams uploads straight to disk; downloads support range requests.
- **History forever**: nothing is auto-deleted. `du -sh /var/lib/clip/files` to check disk use.
- **Auth**: a single shared bearer token. Treat the token like a password. Rotate by editing `config.toml` + every client and restarting.
- **TLS**: Cloudflare Tunnel terminates HTTPS in front. If you want the server to terminate TLS directly, set `tls_cert`/`tls_key` in the config and expose the port.
- **Single user**: no per-user isolation; this is intentional for personal use.

## Layout

```
cmd/clipsrv/    server entry point
cmd/shixo-msn/  Fyne window app entry point
internal/proto/ shared wire types
internal/server/ HTTP, WebSocket, SQLite, file store
internal/client/ HTTP+WS client + config
deploy/         SysV init script, Caddyfile, server config example
```
