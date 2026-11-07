#!/bin/sh
# install og-relay as a systemd service: sudo ./install.sh RELAY_ID [PORT]
set -eu
id="${1:?usage: sudo ./install.sh RELAY_ID [PORT]}"
port="${2:-47160}"
echo "$id" | grep -Eq '^[A-Z][A-Z0-9]{1,5}$' || { echo "relay id: 2-6 capital letters/digits, a letter first" >&2; exit 2; }
[ -x ./og-relay ] || { echo "og-relay binary not found here (download it from Releases)" >&2; exit 2; }
id ogrelay >/dev/null 2>&1 || useradd --system --user-group --no-create-home --shell /usr/sbin/nologin ogrelay
install -d /opt/og-relay
install -m 0755 ./og-relay /opt/og-relay/og-relay
cat > /etc/systemd/system/og-relay.service <<UNIT
[Unit]
Description=Open Ground co-op relay ($id)
After=network-online.target
Wants=network-online.target

[Service]
User=ogrelay
ExecStart=/opt/og-relay/og-relay -addr :$port -region $id
Restart=always
RestartSec=3
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
LimitNOFILE=8192

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now og-relay
sleep 1
systemctl is-active og-relay
echo "check from outside: printf 'OGR1 PROBE\\n' | nc -w 3 <this ip> $port   (expect: PROBE $id)"
