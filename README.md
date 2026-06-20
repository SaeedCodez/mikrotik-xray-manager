# Xray Manager

A self-hosted, mobile-first web app for managing [Xray-core](https://github.com/XTLS/Xray-core)
proxy configurations. Add proxies (by pasting share links or via subscription
URLs), test their latency, pick which one is active, and start/stop the xray
process — all from a polished phone-friendly UI protected by a password.

Built as a single Go binary with no runtime dependencies. The web UI is embedded
into the binary, so deployment is "copy one file and run it."

> **Design.** The UI follows the **Ghab** design system — a warm, editorial,
> Claude.ai-inspired look: off-white paper background, a single terra-cotta
> accent (`#D97757`), a Source Serif display face, Inter for UI, and JetBrains
> Mono for metadata. It was implemented to match the provided design hand-off
> pixel-for-pixel.

---

## Features

- **Proxies** — parse `vmess://`, `vless://`, `trojan://`, `ss://`, and
  `hy2://` / `hysteria2://` links. Search, expand for details, set active, test,
  and delete. Latency is a **real delay measured through the proxy** (not a TCP
  ping). "Test all" streams results in live as each one lands.
- **Subscriptions** — add a URL once, refresh to pull the latest proxies.
  Deleting a subscription lets you keep or remove its proxies. Failed refreshes
  are surfaced without dropping the proxies you already have.
- **Status** — process state + uptime, the active proxy, copyable SOCKS5/HTTP
  ports, a live xray log feed, and Start / Stop / Restart controls.
- **Auth** — a single password, HMAC-signed session cookie (7-day expiry).
- Graceful shutdown stops the xray child process cleanly on `SIGINT`/`SIGTERM`.

---

## Prerequisites

- **Go 1.22+** (only to build — the result is a single static binary).
- **An Xray-core binary** on the host, for the app to manage. The app still runs
  without it (it shows a warning in the Status tab), but process controls are
  disabled until the binary exists.

### Download the Xray binary

Grab the release for your architecture from
[XTLS/Xray-core releases](https://github.com/XTLS/Xray-core/releases):

```bash
# Example: Linux x86-64
cd /tmp
curl -fsSL -o xray.zip \
  https://github.com/XTLS/Xray-core/releases/latest/download/Xray-linux-64.zip
unzip xray.zip xray
sudo install -m 0755 xray /usr/local/bin/xray
xray version
```

For other targets swap the asset name (`Xray-linux-arm64-v8a.zip`,
`Xray-macos-arm64.zip`, etc.).

---

## Configuration

All configuration is via environment variables (see `.env.example`):

| Variable | Default | Description |
|---|---|---|
| `APP_PASSWORD` | _(empty)_ | Password to unlock the UI. **If empty, any password unlocks it** — set this in production. |
| `APP_PORT` | `8080` | Port the web UI listens on. |
| `DATA_DIR` | `./data` | Directory for JSON state + generated xray config. |
| `XRAY_BINARY` | `/usr/local/bin/xray` | Path to the xray binary to manage. |
| `XRAY_CONFIG_PATH` | `$DATA_DIR/xray/config.json` | Where the active config is written. |
| `XRAY_INBOUND_PORT` | `10808` | SOCKS5 inbound port xray listens on. |
| `XRAY_INBOUND_HTTP_PORT` | `10809` | HTTP inbound port xray listens on. |
| `HEALTH_TEST_URL` | `http://www.gstatic.com/generate_204` | URL fetched through each proxy to measure real delay. |
| `SESSION_SECRET` | _(random)_ | Secret for signing session cookies. If unset, a random one is generated and sessions reset on restart. |

---

## Build & run

```bash
# Development
go run .

# With config
APP_PASSWORD=secret123 APP_PORT=8080 go run .

# Build a single binary (web UI is embedded)
go build -o xray-manager .
./xray-manager
```

Then open `http://<host>:8080`.

> **Note for self-hosting:** the SOCKS5/HTTP inbounds listen on `0.0.0.0` so
> other machines on your LAN (e.g. a MikroTik router) can use them. Keep the
> machine behind your LAN firewall, or restrict the ports as appropriate.

### Docker — one command (recommended for Raspberry Pi / always-on)

Run this **on the target machine** (the Pi, or any Linux box). It installs Docker
if missing, fetches the source, generates a config with a random session secret
and password, then builds and starts the panel as an always-on container:

```bash
curl -fsSL https://raw.githubusercontent.com/SaeedCodez/mikrotik-xray-manager/main/docker-install.sh | sudo bash
```

What it does for you:

- Detects the architecture (incl. ARM for Raspberry Pi) automatically.
- **Bakes the matching `xray-core` into the image** — nothing else to install,
  no host binary to mount.
- Runs with `restart: unless-stopped`, so it survives crashes **and reboots**.
- Prints the panel URL (`http://<host-ip>:8080`) and the generated password when
  it finishes.

Manage it from the install dir (`/opt/xray-manager`):

```bash
docker compose logs -f                       # live logs
docker compose restart                       # restart
docker compose down                          # stop & remove
git pull && docker compose up -d --build     # update to the latest source
```

#### Manual Compose (already cloned the repo)

Copy `.env.example` to `.env` (set `APP_PASSWORD` and `SESSION_SECRET`), then:

```bash
docker compose up -d --build
```

`data/` is a bind-mount so your proxies/subscriptions survive restarts. To build
and run by hand instead:

```bash
docker build -t xray-manager .
docker run -d --name xray-manager --restart unless-stopped \
  -p 8080:8080 -p 10808:10808 -p 10809:10809 \
  -e APP_PASSWORD=secret123 \
  -e SESSION_SECRET=$(head -c 32 /dev/urandom | base64) \
  -v "$(pwd)/data:/app/data" \
  xray-manager
```

> The image already contains a matching `xray` binary, so no `-v /usr/local/bin/xray`
> mount is needed.

---

## Alternative — native install without Docker (systemd)

[`install.sh`](install.sh) is an all-in-one installer **and** service manager. It
detects your architecture (Pi 5 = `arm64`), downloads the latest prebuilt binary
from GitHub Releases (or builds from source if no release exists yet), installs
[xray-core](https://github.com/XTLS/Xray-core), writes a config + a `systemd`
service that auto-starts on boot and restarts on crash, and installs itself as the
`xray-managerctl` command.

```bash
curl -fsSL https://raw.githubusercontent.com/SaeedCodez/mikrotik-xray-manager/main/install.sh -o install.sh
sudo bash install.sh install
```

Then open `http://<pi-ip>:8080`. Manage it afterwards with:

```bash
sudo xray-managerctl status        # service state + panel URL
sudo xray-managerctl logs          # follow live logs
sudo xray-managerctl update        # pull the latest build from GitHub & restart
sudo xray-managerctl restart       # restart
sudo xray-managerctl password      # change the panel password
sudo xray-managerctl xray-update   # update the xray-core binary
sudo xray-managerctl self-update   # update the management script itself
sudo xray-managerctl uninstall     # remove (add --purge to also delete data)
```

Useful `install`/`update` flags: `--system-update` (also `apt upgrade` the Pi),
`--source` (build from source instead of downloading), `--port N`, `--password PASS`.

> **Prebuilt builds** come from the [`release` workflow](.github/workflows/release.yml):
> push a version tag and GitHub Actions builds the `arm64`/`amd64`/`armv7` binaries
> and attaches them to the Release. Until you cut the first tag, the script
> auto-falls-back to building from source on the Pi.
>
> ```bash
> git tag v0.1.0 && git push origin v0.1.0   # triggers the release build
> ```

---

## Using it

1. Open the UI and unlock with your password.
2. **Add proxies** — tap **Add proxy** and paste a share link, or go to
   **Subscriptions**, add a URL, and **Refresh** to pull a batch.
3. **Test** — tap a proxy to expand it and hit **Test**, or use **Test all**.
4. **Activate** — expand a proxy and tap **Set active**. This writes the xray
   config; if xray is already running it restarts to apply.
5. **Start** — go to **Status** and tap **Start Xray** (binary required).
6. Point your client / router at the **SOCKS5** port (`127.0.0.1:10808` locally,
   or `<host-ip>:10808` over the LAN).

---

## Routing MikroTik traffic through the proxy

Xray exposes a **SOCKS5** inbound on `XRAY_INBOUND_PORT` (default `10808`) and an
**HTTP** inbound on `XRAY_INBOUND_HTTP_PORT` (default `10809`), both on `0.0.0.0`.
RouterOS can't speak SOCKS natively, so the common patterns are:

**A. Point SOCKS-aware apps directly at the host.** Any client on the LAN can use
`<linux-machine-ip>:10808` as a SOCKS5 proxy — no router config needed.

**B. Route selected traffic via the Linux box as a gateway.** Send the traffic
you want proxied to the Linux machine and let a local redirector (e.g.
`redsocks`, or an xray `dokodemo-door` inbound) hand it to xray:

```
# On MikroTik — send a host's traffic to the Linux box as gateway
/ip route add dst-address=0.0.0.0/0 gateway=<linux-machine-ip> routing-mark=via-proxy
/ip firewall mangle add chain=prerouting src-address=<client-ip> \
    action=mark-routing new-routing-mark=via-proxy passthrough=yes
```

On the Linux box, run a transparent redirector that forwards to xray's SOCKS/HTTP
inbound. (A full transparent-proxy setup is beyond this tool's scope, but xray's
SOCKS/HTTP inbounds are standard and work with `redsocks`, PAC files, or any
SOCKS-aware client.)

---

## API reference

All `/api/*` routes except `/api/auth/*` require the session cookie.

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/api/auth/login` | `{ password }` → sets session cookie |
| `POST` | `/api/auth/logout` | Clears the session |
| `GET` | `/api/auth/status` | `{ authenticated }` |
| `GET` | `/api/proxies` | List proxies (with latency + active flag) |
| `POST` | `/api/proxies` | `{ raw_url }` → parse + store one proxy |
| `POST` | `/api/proxies/import` | `{ urls: [...] }` → batch import (skips bad ones) |
| `DELETE` | `/api/proxies/{id}` | Delete a proxy |
| `GET` | `/api/subscriptions` | List subscriptions |
| `POST` | `/api/subscriptions` | `{ name, url }` → add |
| `DELETE` | `/api/subscriptions/{id}` | Delete (`?keepProxies=false` removes its proxies) |
| `POST` | `/api/subscriptions/{id}/refresh` | Refresh one → `{ added, removed, count }` |
| `POST` | `/api/subscriptions/refresh-all` | Refresh all |
| `POST` | `/api/health/test/{id}` | Test one → `{ latency, error }` |
| `GET` | `/api/health/test-all` | **SSE** stream of results (409 if already running) |
| `GET` | `/api/xray/status` | Process + active-proxy status |
| `POST` | `/api/xray/activate/{id}` | Make a proxy active (restart if running) |
| `POST` | `/api/xray/start` | Start xray with the active proxy |
| `POST` | `/api/xray/restart` | Restart xray |
| `POST` | `/api/xray/stop` | Stop xray |
| `GET` | `/api/xray/logs` | **SSE** stream of xray logs |

The two streaming endpoints use Server-Sent Events; the front-end consumes them
with `EventSource` (logs) and a `fetch`-stream reader (test-all, so it can detect
the 409 "already running" case).

---

## Project layout

```
xray-manager/
├── main.go                  # HTTP server, embed, graceful shutdown
├── internal/
│   ├── config/              # env-var configuration
│   ├── models/              # Proxy, Subscription, ActiveProxy
│   ├── storage/             # mutex-guarded JSON persistence
│   ├── parser/              # vmess/vless/trojan/ss/hy2 share-link parsers
│   ├── subscription/        # fetch + parse subscription URLs
│   ├── health/              # TCP-ping latency tests
│   ├── xray/                # config generator + process manager
│   ├── auth/                # HMAC session cookies
│   └── handlers/            # HTTP handlers + routes
└── web/static/              # index.html + app.js (Alpine) + style.css (embedded)
```

### Implementation notes

- **No external Go dependencies.** Routing uses Go 1.22's method/pattern
  `net/http.ServeMux` (instead of gorilla/mux); UUIDs come from `crypto/rand`.
  This keeps it a true single static binary with no `go.sum`.
- **Front-end** is vanilla HTML + [Alpine.js](https://alpinejs.dev) +
  [Lucide](https://lucide.dev) icons (all via CDN, no build step). Styling is
  hand-written CSS built on the Ghab design tokens — the design is token-driven
  rather than utility-class driven, so it mirrors the hand-off exactly.
- **Health checks measure real delay.** Each test spins up a short-lived xray
  instance with that proxy as the outbound, fetches `HEALTH_TEST_URL`
  (`http://www.gstatic.com/generate_204` by default) through it, and reports the
  round-trip time — so the latency reflects the proxy actually working, not just
  its port being reachable. If the xray binary isn't available, it falls back to
  a TCP ping. (Real-delay tests take ~1–2s each, like v2rayN; the UI shows a
  "testing…" spinner while they run.)
