#!/usr/bin/env bash
#
# xray-manager — all-in-one installer & service manager (Raspberry Pi / Linux).
#
# Quick start:
#   curl -fsSL https://raw.githubusercontent.com/SaeedCodez/mikrotik-xray-manager/main/install.sh -o install.sh
#   sudo bash install.sh install
#
# After install, manage it with:
#   sudo xray-managerctl <command>
#
# Commands:
#   install            Install/upgrade everything and start the service
#   update             Pull the latest app build from GitHub and restart
#   start|stop|restart Manage the service
#   status             Show service status + the panel URL
#   logs [-n N]        Follow live logs (Ctrl-C to quit). -n shows last N lines and exits
#   config             Edit the env file, then restart
#   password [NEW]     Set the panel password and restart
#   xray-update        Update the xray-core binary to the latest release
#   self-update        Update this management script itself
#   uninstall          Remove the service (keeps /var/lib data unless --purge)
#   version            Show installed app version info
#
# Flags for `install`/`update`:
#   --system-update    Also run `apt-get upgrade` (updates the whole Pi)
#   --source           Build from source instead of downloading a release
#   --port N           Web UI port (default 8080)
#   --password PASS    Set the panel password non-interactively

set -euo pipefail

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------
APP_NAME="xray-manager"
REPO="SaeedCodez/mikrotik-xray-manager"
BIN_PATH="/usr/local/bin/xray-manager"
CTL_PATH="/usr/local/bin/xray-managerctl"
XRAY_PATH="/usr/local/bin/xray"
ENV_FILE="/etc/xray-manager.env"
DATA_DIR="/var/lib/xray-manager"
UNIT_FILE="/etc/systemd/system/${APP_NAME}.service"
SVC_USER="xray-manager"
GO_VERSION="1.23.4"   # used only for the --source fallback

# Defaults / flags (overridable)
APP_PORT="${APP_PORT:-8080}"
APP_PASSWORD="${APP_PASSWORD:-}"
DO_SYSTEM_UPDATE=0
FROM_SOURCE=0
PURGE=0
GENERATED_PASS=""

# ---------------------------------------------------------------------------
# Pretty output
# ---------------------------------------------------------------------------
if [ -t 1 ]; then
  C_OK=$'\033[32m'; C_WARN=$'\033[33m'; C_ERR=$'\033[31m'; C_DIM=$'\033[2m'; C_OFF=$'\033[0m'
else
  C_OK=""; C_WARN=""; C_ERR=""; C_DIM=""; C_OFF=""
fi
log()  { printf '%s==>%s %s\n' "$C_OK"   "$C_OFF" "$*"; }
warn() { printf '%s[!]%s %s\n' "$C_WARN" "$C_OFF" "$*" >&2; }
die()  { printf '%s[x]%s %s\n' "$C_ERR"  "$C_OFF" "$*" >&2; exit 1; }

need_root() {
  [ "$(id -u)" -eq 0 ] || die "Run as root (use: sudo $0 $*)"
}

# ---------------------------------------------------------------------------
# Architecture detection -> GitHub asset names
# ---------------------------------------------------------------------------
detect_arch() {
  case "$(uname -m)" in
    aarch64|arm64)  APP_ARCH="arm64"; XRAY_ASSET="Xray-linux-arm64-v8a.zip"; GO_ARCH="arm64" ;;
    x86_64|amd64)   APP_ARCH="amd64"; XRAY_ASSET="Xray-linux-64.zip";        GO_ARCH="amd64" ;;
    armv7l|armv6l)  APP_ARCH="armv7"; XRAY_ASSET="Xray-linux-arm32-v7a.zip"; GO_ARCH="armv6l" ;;
    *) die "Unsupported architecture: $(uname -m)" ;;
  esac
  APP_ASSET="xray-manager-linux-${APP_ARCH}"
  APP_URL="https://github.com/${REPO}/releases/latest/download/${APP_ASSET}"
  XRAY_URL="https://github.com/XTLS/Xray-core/releases/latest/download/${XRAY_ASSET}"
}

# ---------------------------------------------------------------------------
# Dependencies
# ---------------------------------------------------------------------------
ensure_deps() {
  local missing=()
  for c in curl unzip; do command -v "$c" >/dev/null 2>&1 || missing+=("$c"); done
  if command -v apt-get >/dev/null 2>&1; then
    log "Updating package lists…"
    apt-get update -qq
    if [ "$DO_SYSTEM_UPDATE" -eq 1 ]; then
      log "Upgrading system packages (this can take a while)…"
      DEBIAN_FRONTEND=noninteractive apt-get upgrade -y -qq
    fi
    if [ "${#missing[@]}" -gt 0 ]; then
      log "Installing dependencies: ${missing[*]}"
      DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ca-certificates "${missing[@]}"
    fi
  elif [ "${#missing[@]}" -gt 0 ]; then
    die "Missing tools (${missing[*]}) and no apt-get found. Install them and retry."
  fi
}

# ---------------------------------------------------------------------------
# Service user + data dir
# ---------------------------------------------------------------------------
ensure_user() {
  if ! id "$SVC_USER" >/dev/null 2>&1; then
    log "Creating system user '$SVC_USER'…"
    useradd --system --no-create-home --shell /usr/sbin/nologin "$SVC_USER"
  fi
  mkdir -p "$DATA_DIR/xray"
  chown -R "$SVC_USER:$SVC_USER" "$DATA_DIR"
}

# ---------------------------------------------------------------------------
# App binary: download a release, or build from source
# ---------------------------------------------------------------------------
download_app() {
  local tmp; tmp="$(mktemp)"
  log "Downloading latest app build ($APP_ASSET)…"
  if curl -fL --retry 3 -o "$tmp" "$APP_URL"; then
    # Guard against a 404 HTML page sneaking through.
    if head -c4 "$tmp" | grep -q $'\x7fELF'; then
      install -m 0755 "$tmp" "$BIN_PATH"
      rm -f "$tmp"
      return 0
    fi
  fi
  rm -f "$tmp"
  warn "No prebuilt release found for '$APP_ARCH'."
  warn "Falling back to building from source (needs ~150MB to fetch the Go toolchain)."
  build_from_source
}

build_from_source() {
  local work go_root
  work="$(mktemp -d)"; go_root="$work/go"
  log "Fetching Go ${GO_VERSION} toolchain ($GO_ARCH)…"
  curl -fL --retry 3 -o "$work/go.tgz" \
    "https://go.dev/dl/go${GO_VERSION}.linux-${GO_ARCH}.tar.gz" \
    || die "Failed to download the Go toolchain."
  tar -C "$work" -xzf "$work/go.tgz"
  log "Cloning source and building…"
  command -v git >/dev/null 2>&1 || { apt-get install -y -qq git || die "git required"; }
  git clone --depth 1 "https://github.com/${REPO}.git" "$work/src"
  ( cd "$work/src" && CGO_ENABLED=0 "$go_root/bin/go" build -trimpath -ldflags="-s -w" -o "$BIN_PATH" . )
  chmod 0755 "$BIN_PATH"
  rm -rf "$work"
}

# ---------------------------------------------------------------------------
# xray-core binary
# ---------------------------------------------------------------------------
install_xray() {
  if [ -x "$XRAY_PATH" ] && [ "${1:-}" != "force" ]; then
    log "xray-core already present at $XRAY_PATH (skipping; use 'xray-update' to refresh)."
    return
  fi
  local tmp; tmp="$(mktemp -d)"
  log "Downloading xray-core ($XRAY_ASSET)…"
  curl -fL --retry 3 -o "$tmp/x.zip" "$XRAY_URL" || die "Failed to download xray-core."
  unzip -o -q "$tmp/x.zip" xray -d "$tmp"
  install -m 0755 "$tmp/xray" "$XRAY_PATH"
  rm -rf "$tmp"
  "$XRAY_PATH" version | head -n1 || true
}

# ---------------------------------------------------------------------------
# Env file (created once; never clobbered)
# ---------------------------------------------------------------------------
write_env() {
  if [ -f "$ENV_FILE" ]; then
    log "Keeping existing config: $ENV_FILE"
    return
  fi
  local pass="$APP_PASSWORD" secret
  if [ -z "$pass" ] && [ -t 0 ]; then
    read -rsp "Set a panel password (leave blank to auto-generate): " pass; echo
  fi
  if [ -z "$pass" ]; then
    pass="$(head -c 18 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-16)"
    GENERATED_PASS="$pass"
  fi
  secret="$(head -c 32 /dev/urandom | base64)"
  ( umask 077
    cat >"$ENV_FILE" <<EOF
# xray-manager configuration — see README for all options.
APP_PASSWORD=$pass
APP_PORT=$APP_PORT
DATA_DIR=$DATA_DIR
XRAY_BINARY=$XRAY_PATH
SESSION_SECRET=$secret
EOF
  )
  chmod 600 "$ENV_FILE"
  log "Wrote config to $ENV_FILE"
}

# ---------------------------------------------------------------------------
# systemd unit
# ---------------------------------------------------------------------------
write_unit() {
  log "Installing systemd service…"
  cat >"$UNIT_FILE" <<EOF
[Unit]
Description=Xray Manager — web panel for Xray-core
Documentation=https://github.com/${REPO}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${SVC_USER}
Group=${SVC_USER}
EnvironmentFile=${ENV_FILE}
ExecStart=${BIN_PATH}
WorkingDirectory=${DATA_DIR}
Restart=always
RestartSec=3
LimitNOFILE=65536
# --- basic hardening ---
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=true
PrivateTmp=true
ReadWritePaths=${DATA_DIR}

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
}

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
lan_ip() { hostname -I 2>/dev/null | awk '{print $1}'; }
port_now() { awk -F= '/^APP_PORT=/{print $2}' "$ENV_FILE" 2>/dev/null | tr -d ' '; }

print_access() {
  local ip port; ip="$(lan_ip)"; port="$(port_now)"; port="${port:-$APP_PORT}"
  echo
  log "Panel is up:  ${C_OK}http://${ip:-<pi-ip>}:${port}${C_OFF}"
  if [ -n "$GENERATED_PASS" ]; then
    warn "Auto-generated password: ${GENERATED_PASS}  (change it: sudo xray-managerctl password)"
  fi
  echo "${C_DIM}Manage it:  sudo xray-managerctl {status|logs|update|restart}${C_OFF}"
}

install_ctl() {
  # Copy this script so it's runnable as `xray-managerctl`.
  if [ "${BASH_SOURCE[0]}" != "$CTL_PATH" ]; then
    install -m 0755 "${BASH_SOURCE[0]}" "$CTL_PATH" 2>/dev/null || \
      curl -fsSL "https://raw.githubusercontent.com/${REPO}/main/install.sh" -o "$CTL_PATH" && chmod 0755 "$CTL_PATH"
  fi
}

# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------
cmd_install() {
  need_root install
  detect_arch
  ensure_deps
  ensure_user
  if [ "$FROM_SOURCE" -eq 1 ]; then build_from_source; else download_app; fi
  install_xray
  write_env
  write_unit
  install_ctl
  log "Enabling and starting the service…"
  systemctl enable --now "$APP_NAME"
  sleep 1
  systemctl is-active --quiet "$APP_NAME" || { systemctl status "$APP_NAME" --no-pager -l || true; die "Service failed to start — see logs above."; }
  print_access
}

cmd_update() {
  need_root update
  detect_arch
  [ "$DO_SYSTEM_UPDATE" -eq 1 ] && ensure_deps
  if [ "$FROM_SOURCE" -eq 1 ]; then build_from_source; else download_app; fi
  install_ctl
  systemctl restart "$APP_NAME"
  log "Updated and restarted."
  cmd_version
}

cmd_start()   { need_root; systemctl start   "$APP_NAME"; log "started"; }
cmd_stop()    { need_root; systemctl stop    "$APP_NAME"; log "stopped"; }
cmd_restart() { need_root; systemctl restart "$APP_NAME"; log "restarted"; }

cmd_status() {
  systemctl status "$APP_NAME" --no-pager -l || true
  local ip port; ip="$(lan_ip)"; port="$(port_now)"
  echo
  log "Panel: http://${ip:-<pi-ip>}:${port:-8080}"
}

cmd_logs() {
  if [ "${1:-}" = "-n" ]; then
    journalctl -u "$APP_NAME" -n "${2:-100}" --no-pager
  else
    echo "${C_DIM}(Ctrl-C to stop following)${C_OFF}"
    journalctl -u "$APP_NAME" -f -n 50
  fi
}

cmd_config() {
  need_root config
  "${EDITOR:-nano}" "$ENV_FILE"
  systemctl restart "$APP_NAME"
  log "Config saved and service restarted."
}

cmd_password() {
  need_root password
  local new="${1:-}"
  if [ -z "$new" ]; then read -rsp "New panel password: " new; echo; fi
  [ -n "$new" ] || die "Empty password."
  case "$new" in *'|'*) die "Password can't contain the '|' character (limitation of this script).";; esac
  if grep -q '^APP_PASSWORD=' "$ENV_FILE"; then
    sed -i "s|^APP_PASSWORD=.*|APP_PASSWORD=$new|" "$ENV_FILE"
  else
    echo "APP_PASSWORD=$new" >>"$ENV_FILE"
  fi
  systemctl restart "$APP_NAME"
  log "Password updated and service restarted."
}

cmd_xray_update() { need_root; detect_arch; ensure_deps; install_xray force; systemctl restart "$APP_NAME"; log "xray-core updated."; }

cmd_self_update() {
  need_root
  log "Updating management script…"
  curl -fsSL "https://raw.githubusercontent.com/${REPO}/main/install.sh" -o "$CTL_PATH"
  chmod 0755 "$CTL_PATH"
  log "Done. Re-run your command."
}

cmd_uninstall() {
  need_root uninstall
  systemctl disable --now "$APP_NAME" 2>/dev/null || true
  rm -f "$UNIT_FILE"; systemctl daemon-reload
  rm -f "$BIN_PATH" "$CTL_PATH"
  if [ "$PURGE" -eq 1 ]; then
    rm -rf "$DATA_DIR" "$ENV_FILE"
    userdel "$SVC_USER" 2>/dev/null || true
    log "Uninstalled and purged all data."
  else
    log "Uninstalled. Kept data in $DATA_DIR and config $ENV_FILE (use --purge to remove)."
    log "Left xray-core at $XRAY_PATH."
  fi
}

cmd_version() {
  if [ -x "$BIN_PATH" ]; then
    log "App binary: $BIN_PATH ($(stat -c '%y' "$BIN_PATH" 2>/dev/null | cut -d. -f1))"
  else
    warn "App not installed."
  fi
  [ -x "$XRAY_PATH" ] && "$XRAY_PATH" version | head -n1 || true
}

usage() { sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^#\{0,1\} \{0,1\}//'; }

# ---------------------------------------------------------------------------
# Arg parsing
# ---------------------------------------------------------------------------
CMD="${1:-help}"; shift || true
POS=()
while [ "$#" -gt 0 ]; do
  case "$1" in
    --system-update) DO_SYSTEM_UPDATE=1 ;;
    --source)        FROM_SOURCE=1 ;;
    --purge)         PURGE=1 ;;
    --port)          APP_PORT="$2"; shift ;;
    --password)      APP_PASSWORD="$2"; shift ;;
    *)               POS+=("$1") ;;
  esac
  shift
done
set -- "${POS[@]:-}"

case "$CMD" in
  install)      cmd_install ;;
  update)       cmd_update ;;
  start)        cmd_start ;;
  stop)         cmd_stop ;;
  restart)      cmd_restart ;;
  status)       cmd_status ;;
  logs)         cmd_logs "$@" ;;
  config)       cmd_config ;;
  password)     cmd_password "$@" ;;
  xray-update)  cmd_xray_update ;;
  self-update)  cmd_self_update ;;
  uninstall)    cmd_uninstall ;;
  version)      cmd_version ;;
  help|-h|--help) usage ;;
  *) warn "Unknown command: $CMD"; usage; exit 1 ;;
esac
