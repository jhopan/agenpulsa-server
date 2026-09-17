#!/usr/bin/env bash
# Deploy agenpulsa-server. Build -> scp -> restart -> verify.
#   ./deploy.sh staging   -> armbian-jhosua1 (ARM64, :8081)  lokasi uji
#   ./deploy.sh prod      -> laptop-debian   (AMD64, :8082)  production
set -euo pipefail

case "${1:-}" in
  staging) HOST=armbian-jhosua1; GOARCH=arm64; PORT=8081; IP=192.168.11.23 ;;
  prod)    HOST=laptop-debian;   GOARCH=amd64; PORT=8082; IP=127.0.0.1 ;;
  *) echo "usage: $0 {staging|prod}"; exit 1 ;;
esac
DIR=/opt/agenpulsa
BIN=agenpulsa-server-linux-$GOARCH

echo "== build linux/$GOARCH ($1)"
GOOS=linux GOARCH=$GOARCH go build -o "$BIN" .

echo "== copy ke $HOST"
scp -q "$BIN"        "$HOST:$DIR/agenpulsa-server"
scp -q web/index.html web/login.html web/logo_agenpulsa_server.png \
       web/favicon-32.png web/favicon-48.png web/apple-touch-icon.png "$HOST:$DIR/web/"
scp -q helper/isip_api.py "$HOST:$DIR/helper/isip_api.py"

echo "== restart service"
ssh "$HOST" "chmod +x $DIR/agenpulsa-server && systemctl restart agenpulsa-server && sleep 2 && systemctl is-active agenpulsa-server"

echo "== verify"
curl -s --max-time 8 -o /dev/null -w "local  :$PORT -> %{http_code}\n" "http://$IP:$PORT/"
