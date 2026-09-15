#!/usr/bin/env bash
# Installs the LabNote Device Connector as a systemd service.
# Usage: sudo ./install.sh ./labnote-connector_linux_amd64
set -euo pipefail

BINARY="${1:-./labnote-connector_linux_amd64}"
[ -f "$BINARY" ] || { echo "binary not found: $BINARY" >&2; exit 1; }

id -u labnote-connector >/dev/null 2>&1 || \
  useradd --system --home /var/lib/labnote-connector --shell /usr/sbin/nologin labnote-connector

install -m 0755 "$BINARY" /usr/local/bin/labnote-connector
install -d -o labnote-connector -g labnote-connector -m 0750 /var/lib/labnote-connector
install -m 0644 "$(dirname "$0")/labnote-connector.service" /etc/systemd/system/labnote-connector.service

systemctl daemon-reload
systemctl enable --now labnote-connector

echo
echo "Installed. Finish the setup at http://127.0.0.1:8420"
echo "Logs: journalctl -u labnote-connector -f"
