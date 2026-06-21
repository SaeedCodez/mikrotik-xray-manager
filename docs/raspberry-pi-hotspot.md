# Raspberry Pi → XRouter WiFi Hotspot (transparent Xray proxy router)

Turn a Raspberry Pi (running the `xray-manager` container) into a WiFi access point
that transparently routes **all client traffic through the Xray socks5 proxy**.

- **SSID:** `XRouter`  •  **Password:** `xrouter20251`
- **Uplink:** `eth0` (DHCP, untouched)  •  **AP:** `wlan0` = `10.42.0.1/24`
- **Proxy target:** socks5 `127.0.0.1:10808` (exposed by the `xray-manager` Docker container)

> Verified on a Raspberry Pi 5, Ubuntu Server 24.04 (Noble), kernel 6.8 `raspi`,
> onboard Broadcom WiFi (`brcmfmac`). Network stack: **systemd-networkd / netplan**
> (NetworkManager not used).

---

## 1. How it works

```
                      ┌─────────────────────── Raspberry Pi ───────────────────────┐
 WiFi client          │  wlan0 = 10.42.0.1/24 (hostapd AP + dnsmasq DHCP/DNS)       │
 (10.42.0.50-200) ───►│                                                             │   eth0
                      │  • TCP   ─iptables REDIRECT─► redsocks :12345 ─► socks5 ─┐   │  (uplink)
                      │  • DNS   ─iptables REDIRECT─► dnsmasq :53 ─► dns-over-   │   │ ──────►
                      │                                socks :5354 ─► socks5 ───┤   │  internet
                      │  • other UDP / QUIC / IPv6 fwd ─► DROP (no leak)        │   │
                      │                                                         ▼   │
                      │                              127.0.0.1:10808  ►  Xray (Docker) ──┘
                      └─────────────────────────────────────────────────────────────┘
```

Why this shape:

- **redsocks** bridges plain TCP → socks5 (clients don't speak socks).
- **DNS is tunneled** through the proxy via a tiny DNS‑over‑socks forwarder, because
  the container maps the socks port **TCP-only** (so socks UDP / DNS-over-UDP can't traverse it).
- **Fail-closed:** non-tunnelable traffic (other UDP, QUIC on UDP/443, IPv6 forwarding)
  is dropped. Clients have internet **only while xray-core is egressing** — no plaintext leak path.
- `eth0` is never reconfigured, so remote SSH stays up during setup.

---

## 2. Components

| Service | Role | Key file |
|---|---|---|
| `hostapd` | WiFi access point on `wlan0` | `/etc/hostapd/hostapd.conf`, `/etc/default/hostapd` |
| `systemd-networkd` | static IP `10.42.0.1/24` on `wlan0` | `/etc/systemd/network/10-wlan0-ap.network` |
| `dnsmasq` | DHCP + DNS for clients | `/etc/dnsmasq.d/xrouter.conf` |
| `dns-over-socks` | tunnels DNS as DNS-over-TCP through socks5 | `/usr/local/sbin/dns-over-socks.py` + unit |
| `redsocks` | transparent TCP → socks5 | `/etc/redsocks.conf` + `LimitNOFILE` drop-in |
| `xrouter-firewall` | iptables redirect + leak prevention | `/usr/local/sbin/xrouter-firewall.sh` + unit |

All six are enabled at boot. `wpa_supplicant` is **disabled** (it was holding `wlan0`).

---

## 3. Prerequisites

- Pi reachable over SSH on `eth0` with sudo.
- `xray-manager` container running and publishing socks5 on `127.0.0.1:10808`
  (web UI on `:8080`). The proxy must be **egressing** (start/select a working
  server in the UI) for clients to get internet.
- Packages:

```bash
sudo apt-get update
sudo apt-get install -y hostapd dnsmasq redsocks iw iptables-persistent
```

---

## 4. Configuration files

### `/etc/hostapd/hostapd.conf`
```ini
interface=wlan0
driver=nl80211
ssid=XRouter
country_code=IR
ieee80211d=1
hw_mode=g
channel=6
ieee80211n=1
wmm_enabled=1
auth_algs=1
macaddr_acl=0
ignore_broadcast_ssid=0
wpa=2
wpa_key_mgmt=WPA-PSK
rsn_pairwise=CCMP
wpa_passphrase=xrouter20251
```
`/etc/default/hostapd`:
```ini
DAEMON_CONF="/etc/hostapd/hostapd.conf"
```

### `/etc/systemd/network/10-wlan0-ap.network`
```ini
[Match]
Name=wlan0

[Link]
RequiredForOnline=no

[Network]
Address=10.42.0.1/24
ConfigureWithoutCarrier=yes
LinkLocalAddressing=no
IPv6AcceptRA=no
DHCP=no
```

### `/etc/dnsmasq.d/xrouter.conf`
```ini
interface=wlan0
bind-dynamic
listen-address=10.42.0.1
no-resolv
no-hosts
# upstream = local DNS-over-SOCKS5 forwarder (tunnels DNS through Xray)
server=127.0.0.1#5354
dhcp-range=10.42.0.50,10.42.0.200,255.255.255.0,12h
dhcp-option=option:router,10.42.0.1
dhcp-option=option:dns-server,10.42.0.1
dhcp-authoritative
domain-needed
bogus-priv
cache-size=1000
```
> `dnsmasq`'s `conf-dir` must be enabled so this drop-in is read:
> ```bash
> sudo sed -i 's|^#conf-dir=/etc/dnsmasq.d/,\*.conf|conf-dir=/etc/dnsmasq.d/,*.conf|' /etc/dnsmasq.conf
> ```

### `/usr/local/sbin/dns-over-socks.py`
```python
#!/usr/bin/env python3
"""Minimal DNS-over-SOCKS5 forwarder for XRouter.

Listens for plain UDP DNS queries from dnsmasq on 127.0.0.1:5354 and forwards
each query as DNS-over-TCP through the local SOCKS5 proxy (Xray, 127.0.0.1:10808)
to an upstream resolver. Tunnels client DNS through the proxy without needing
UDP support on the SOCKS port (Docker maps it TCP-only). Pure stdlib.
"""
import socket
import struct
import threading

LISTEN = ("127.0.0.1", 5354)
SOCKS = ("127.0.0.1", 10808)
UPSTREAM = ("1.1.1.1", 53)
TIMEOUT = 8


def socks5_connect(dst_ip, dst_port):
    s = socket.create_connection(SOCKS, timeout=TIMEOUT)
    s.settimeout(TIMEOUT)
    s.sendall(b"\x05\x01\x00")  # VER=5, 1 method, NO AUTH
    if s.recv(2) != b"\x05\x00":
        s.close()
        raise OSError("socks5 method negotiation failed")
    req = b"\x05\x01\x00\x01" + socket.inet_aton(dst_ip) + struct.pack(">H", dst_port)
    s.sendall(req)
    rep = s.recv(10)
    if len(rep) < 2 or rep[1] != 0x00:
        s.close()
        raise OSError("socks5 connect failed")
    return s


def recv_exact(s, n):
    buf = b""
    while len(buf) < n:
        chunk = s.recv(n - len(buf))
        if not chunk:
            break
        buf += chunk
    return buf


def handle(data, addr, srv):
    try:
        s = socks5_connect(*UPSTREAM)
        try:
            s.sendall(struct.pack(">H", len(data)) + data)  # DNS-over-TCP length prefix
            hdr = recv_exact(s, 2)
            if len(hdr) < 2:
                return
            n = struct.unpack(">H", hdr)[0]
            resp = recv_exact(s, n)
            if resp:
                srv.sendto(resp, addr)
        finally:
            s.close()
    except Exception:
        pass  # drop on failure; client retries / times out


def main():
    srv = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(LISTEN)
    while True:
        data, addr = srv.recvfrom(4096)
        threading.Thread(target=handle, args=(data, addr, srv), daemon=True).start()


if __name__ == "__main__":
    main()
```
`/etc/systemd/system/dns-over-socks.service`:
```ini
[Unit]
Description=DNS over SOCKS5 forwarder (XRouter)
After=network.target

[Service]
ExecStart=/usr/bin/python3 /usr/local/sbin/dns-over-socks.py
Restart=always
RestartSec=2
User=nobody
NoNewPrivileges=yes

[Install]
WantedBy=multi-user.target
```

### `/etc/redsocks.conf`
```ini
base {
	log_debug = off;
	log_info = on;
	log = "syslog:daemon";
	daemon = on;
	user = redsocks;
	group = redsocks;
	redirector = iptables;
}

redsocks {
	local_ip = 0.0.0.0;
	local_port = 12345;
	ip = 127.0.0.1;
	port = 10808;
	type = socks5;
}
```
`/etc/systemd/system/redsocks.service.d/limits.conf` — **required**; the default 1024 fd
limit causes `accept: backing off` (stalls) under a busy client:
```ini
[Service]
LimitNOFILE=65536
```
> To use the **http proxy** instead of socks5: set `port = 10809` and
> `type = http-connect` here, then `sudo systemctl restart redsocks`.

### `/usr/local/sbin/xrouter-firewall.sh`
```bash
#!/bin/bash
# XRouter: redirect hotspot (wlan0) traffic into the Xray socks5 proxy.
#   - client TCP -> redsocks (12345) -> socks5 127.0.0.1:10808
#   - client DNS -> dnsmasq (10.42.0.1:53) -> dns-over-socks -> socks5
#   - other UDP  -> dropped (no leak; forces TCP fallback, e.g. QUIC->TLS)
# Idempotent: safe to re-run.
set -uo pipefail
WIFI=wlan0
RP=12345

ip addr show "$WIFI" 2>/dev/null | grep -q "10.42.0.1/24" \
  || ip addr replace 10.42.0.1/24 dev "$WIFI" 2>/dev/null || true
sysctl -qw net.ipv4.ip_forward=1

iptables -t nat -N XROUTER 2>/dev/null || true
iptables -t nat -F XROUTER
for net in 0.0.0.0/8 10.0.0.0/8 100.64.0.0/10 127.0.0.0/8 169.254.0.0/16 \
           172.16.0.0/12 192.168.0.0/16 224.0.0.0/4 240.0.0.0/4; do
  iptables -t nat -A XROUTER -d "$net" -j RETURN
done
iptables -t nat -A XROUTER -p tcp -j REDIRECT --to-ports "$RP"

iptables -t nat -C PREROUTING -i "$WIFI" -p udp --dport 53 -j REDIRECT --to-ports 53 2>/dev/null \
  || iptables -t nat -A PREROUTING -i "$WIFI" -p udp --dport 53 -j REDIRECT --to-ports 53
iptables -t nat -C PREROUTING -i "$WIFI" -p tcp --dport 53 -j REDIRECT --to-ports 53 2>/dev/null \
  || iptables -t nat -A PREROUTING -i "$WIFI" -p tcp --dport 53 -j REDIRECT --to-ports 53
iptables -t nat -C PREROUTING -i "$WIFI" -p tcp -j XROUTER 2>/dev/null \
  || iptables -t nat -A PREROUTING -i "$WIFI" -p tcp -j XROUTER

# FORWARD: allow clients to reach the Docker-published web panel, drop the rest.
# Published ports (e.g. panel :8080) are DNAT'd to the container, so they traverse
# FORWARD (in=wlan0, out=docker bridge) instead of INPUT. Allow that flow + its
# established return, then drop all other wlan0 forwarding (no direct/UDP leak).
PANEL_PORT=8080
DOCKER_SUBNETS="$(ip -o -4 addr show | awk '$2 ~ /^(docker0|br-)/ {print $4}')"
iptables -D FORWARD -o "$WIFI" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT 2>/dev/null || true
for sub in $DOCKER_SUBNETS; do
  iptables -D FORWARD -i "$WIFI" -p tcp -d "$sub" --dport "$PANEL_PORT" -j ACCEPT 2>/dev/null || true
done
iptables -D FORWARD -i "$WIFI" -j DROP 2>/dev/null || true
# Re-insert at top in reverse order -> final: [return] [client->panel] [drop rest]
iptables -I FORWARD 1 -i "$WIFI" -j DROP
for sub in $DOCKER_SUBNETS; do
  iptables -I FORWARD 1 -i "$WIFI" -p tcp -d "$sub" --dport "$PANEL_PORT" -j ACCEPT
done
iptables -I FORWARD 1 -o "$WIFI" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT

iptables -C INPUT -p tcp --dport "$RP" ! -i "$WIFI" -j DROP 2>/dev/null \
  || iptables -A INPUT -p tcp --dport "$RP" ! -i "$WIFI" -j DROP
ip6tables -C FORWARD -i "$WIFI" -j DROP 2>/dev/null \
  || ip6tables -I FORWARD 1 -i "$WIFI" -j DROP

echo "xrouter-firewall applied"
```
`/etc/systemd/system/xrouter-firewall.service`:
```ini
[Unit]
Description=XRouter transparent proxy firewall rules
After=network-online.target redsocks.service dnsmasq.service docker.service
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/local/sbin/xrouter-firewall.sh

[Install]
WantedBy=multi-user.target
```

### `/etc/sysctl.d/99-xrouter.conf`
```ini
net.ipv4.ip_forward=1
net.ipv6.conf.all.forwarding=0
```

---

## 5. Enable & start

```bash
# release wlan0 from the wifi client supplicant
sudo systemctl disable --now wpa_supplicant

# regulatory domain + forwarding
sudo iw reg set IR
sudo sysctl --system

# bring up the AP IP
sudo systemctl restart systemd-networkd
sudo networkctl reconfigure wlan0

# unmask hostapd (Ubuntu masks it until configured), enable everything
sudo systemctl unmask hostapd
sudo systemctl daemon-reload
sudo systemctl enable --now dns-over-socks hostapd dnsmasq redsocks xrouter-firewall
```

---

## 6. Verify

```bash
# all services up
systemctl is-active hostapd dnsmasq redsocks dns-over-socks xrouter-firewall

# AP is broadcasting
sudo iw dev wlan0 info | grep -E 'ssid|type|channel'

# proxy egress works (run on the Pi)
curl --socks5-hostname 127.0.0.1:10808 https://api.ipify.org   # -> proxy exit IP

# DNS tunneled through the proxy
dig @10.42.0.1 example.com +short

# connected clients
sudo cat /var/lib/misc/dnsmasq.leases

# live proxied sessions (client -> redsocks should equal redsocks -> 10808)
sudo ss -tnp state established '( sport = :12345 )' | grep -c 10.42.0
sudo ss -tnp state established '( dport = :10808 )' | grep -c redsocks
```

> **Note:** an `OUTPUT`-chain self-test from the Pi itself (redirecting local curl into
> redsocks) is unreliable — `SO_ORIGINAL_DST` can't recover the original destination for
> *locally-originated* redirected connections. Test from a **real WiFi client** instead.

---

## 7. Operate / troubleshoot

| Symptom | Check |
|---|---|
| Clients connect but no internet | Is xray-core egressing? `curl --socks5-hostname 127.0.0.1:10808 https://api.ipify.org`. If not, start/select a proxy in the `:8080` UI. |
| Web panel `http://10.42.0.1:8080` won't open from a client (but SSH works) | The panel is a **Docker** container: requests to `:8080` are DNAT'd to the container, so they hit `FORWARD` (not `INPUT` like SSH) and get caught by the `-i wlan0 -j DROP` leak guard. The firewall opens a hole for the panel — confirm `iptables -S FORWARD` shows the `--dport 8080 -j ACCEPT` rules **above** the `-i wlan0 -j DROP`. Re-run `sudo systemctl restart xrouter-firewall` if missing (e.g. after the Docker bridge subnet changed). |
| `redsocks` logs `accept: backing off` | fd exhaustion — confirm the `LimitNOFILE=65536` drop-in: `cat /proc/$(pidof redsocks)/limits \| grep 'open files'`. |
| AP not visible | `systemctl status hostapd`; ensure `wpa_supplicant` is disabled and `rfkill list` shows wlan not blocked. |
| DNS fails | `journalctl -u dns-over-socks`; verify it listens on `127.0.0.1:5354` and socks5 egresses. |
| Re-apply firewall | `sudo systemctl restart xrouter-firewall` (script is idempotent). |

Logs: `journalctl -u hostapd -u dnsmasq -u redsocks -u dns-over-socks -f`

---

## 8. Design notes & limitations

- **TCP-only tunneling.** Only TCP (and DNS-over-TCP-via-socks) is proxied. Other UDP —
  including **QUIC (UDP/443)** — is dropped, forcing browsers/apps to fall back to TCP/TLS.
  This prevents leaks but can affect UDP-only apps (some VoIP, game traffic).
- **No IPv6 egress** for clients (IPv6 forwarding off, no RA) — avoids an untunneled v6 leak path.
- **Fail-closed.** If the proxy stops, clients lose internet rather than falling back to direct.
- **Respects Xray routing.** All client TCP enters Xray's socks inbound, so Xray's own
  routing/rules/outbound selection apply unchanged.
- **Persistence** is via enabled systemd units + the idempotent firewall service (not
  `netfilter-persistent`), so Docker's volatile iptables chains aren't snapshotted.
