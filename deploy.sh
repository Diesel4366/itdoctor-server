#!/bin/bash
# Deploy IT-Doctor Server to VPS
set -e

VPS="31.129.96.100"
VPS_USER="root"
REMOTE_DIR="/opt/itdoctor-server"

echo "→ Building for Linux amd64..."
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o itdoctor-server-linux .

echo "→ Uploading to VPS..."
sshpass -p 'Uvbuuvbu4366@' scp -o StrictHostKeyChecking=no \
  itdoctor-server-linux \
  itdoctor-server.service \
  ${VPS_USER}@${VPS}:/tmp/

echo "→ Installing on VPS..."
sshpass -p 'Uvbuuvbu4366@' ssh -o StrictHostKeyChecking=no ${VPS_USER}@${VPS} "
  set -e
  mkdir -p ${REMOTE_DIR}
  mv /tmp/itdoctor-server-linux ${REMOTE_DIR}/itdoctor-server
  chmod +x ${REMOTE_DIR}/itdoctor-server
  mv /tmp/itdoctor-server.service /etc/systemd/system/itdoctor-server.service
  systemctl daemon-reload
  systemctl enable itdoctor-server
  systemctl restart itdoctor-server
  sleep 2
  systemctl status itdoctor-server --no-pager
"

echo "✓ Deploy complete!"
echo "→ REST API: http://${VPS}:8765/api/agents"
echo "→ Agent WS: ws://${VPS}:8766/agent/ws"
