#!/usr/bin/env bash
# ============================================================
#  AgenPulsa Server — installer & updater (GitHub Releases)
#
#  Pakai:
#    Install baru:
#      curl -fsSL https://raw.githubusercontent.com/jhopan/agenpulsa-server/master/install.sh | bash
#
#    Update (ambil release terbaru = "latest"):
#      curl -fsSL https://raw.githubusercontent.com/jhopan/agenpulsa-server/master/install.sh | bash -s -- update
#
#    Versi tertentu:
#      curl -fsSL ... | bash -s -- update v1.2.0
#
#  Yang dilakukan:
#   - deteksi OS/arsitektur (amd64 / arm64)
#   - download binary + web + helper dari GitHub Releases (tanpa perlu Go)
#   - backup binary & DB lama sebelum menimpa (rollback kalau gagal start)
#   - pasang systemd service + .env kalau belum ada
#   - restart & cek /health
# ============================================================
set -euo pipefail

REPO="jhopan/agenpulsa-server"
DIR="/opt/agenpulsa"
DATA_DIR="$DIR/data"
BIN="$DIR/agenpulsa-server"
SERVICE="agenpulsa-server"
PORT="${AP_PORT:-8081}"
API="https://api.github.com/repos/$REPO/releases"

R="\033[0m"; G="\033[1;32m"; Y="\033[1;33m"; RED="\033[1;31m"; C="\033[1;36m"
say()  { echo -e "${G}==>${R} $*"; }
warn() { echo -e "${Y}!>${R} $*"; }
die()  { echo -e "${RED}x>${R} $*" >&2; exit 1; }

need_root() { [[ $EUID -eq 0 ]] || die "jalankan sebagai root (sudo bash)"; }

# ---------- deteksi arsitektur ----------
detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    aarch64|arm64) echo arm64 ;;
    *) die "arsitektur $(uname -m) belum didukung (hanya amd64/arm64)" ;;
  esac
}

# ---------- tools ----------
ensure_tools() {
  for t in curl tar; do
    command -v "$t" >/dev/null 2>&1 || {
      say "pasang $t"
      (apt-get update -qq && apt-get install -y -qq "$t") >/dev/null 2>&1 || die "gagal pasang $t"
    }
  done
}

# ---------- ambil versi ----------
# latest_version: tag dari release terbaru (bukan draft/prerelease)
latest_version() {
  curl -fsSL "$API/latest" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1
}

# ---------- download paket ----------
download_release() {
  local ver="$1" arch="$2"
  local plain="${ver#v}"
  local pkg="agenpulsa-server_${plain}_linux_${arch}.tar.gz"
  local url="https://github.com/$REPO/releases/download/$ver/$pkg"
  local tmp
  tmp="$(mktemp -d)"

  say "download $pkg ($ver)"
  curl -fsSL --retry 3 --retry-delay 2 -o "$tmp/$pkg" "$url" \
    || die "gagal download $url — cek apakah release $ver punya file $pkg"

  tar -xzf "$tmp/$pkg" -C "$tmp"
  local extracted
  extracted="$(find "$tmp" -maxdepth 2 -name agenpulsa-server -type f | head -1)"
  [[ -n "$extracted" ]] || die "paket tidak berisi binary agenpulsa-server"

  mkdir -p "$DIR" "$DATA_DIR"
  # backup binary lama (buat rollback)
  if [[ -f "$BIN" ]]; then
    cp "$BIN" "$BIN.bak-$(date +%Y%m%d-%H%M%S)"
    say "backup binary lama"
  fi
  install -m 0755 "$extracted" "$BIN"

  # web + helper (aset statis ikut paket)
  local rootdir
  rootdir="$(dirname "$extracted")"
  [[ -d "$rootdir/web" ]] && cp -r "$rootdir/web/." "$DIR/web/" 2>/dev/null || true
  [[ -d "$rootdir/helper" ]] && cp -r "$rootdir/helper/." "$DIR/helper/" 2>/dev/null || true

  rm -rf "$tmp"
  say "binary terpasang: $BIN ($("$BIN" --version 2>/dev/null || echo "$ver"))"
}

# ---------- systemd + env (hanya kalau belum ada) ----------
ensure_service() {
  if [[ ! -f "$DIR/.env" ]]; then
    cat > "$DIR/.env" <<EOF
# infra-only (sebelum server jalan) — runtime config via web admin
AP_PORT=$PORT
AP_DB=$DATA_DIR/agenpulsa.db
AP_PROFILE=$DIR/profile
EOF
    chmod 600 "$DIR/.env"
    say ".env dibuat (port $PORT)"
  fi

  if [[ ! -f "/etc/systemd/system/$SERVICE.service" ]]; then
    cat > "/etc/systemd/system/$SERVICE.service" <<EOF
[Unit]
Description=AgenPulsa Server (order pulsa isipulsa + web admin)
After=network.target

[Service]
Type=simple
WorkingDirectory=$DIR
EnvironmentFile=$DIR/.env
ExecStart=$BIN
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    systemctl enable "$SERVICE" >/dev/null 2>&1 || true
    say "systemd service dibuat"
  fi
}

# ---------- restart + health check (rollback kalau gagal) ----------
restart_and_check() {
  systemctl restart "$SERVICE"
  sleep 3
  if systemctl is-active --quiet "$SERVICE"; then
    local code
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "http://127.0.0.1:$PORT/health" || echo 000)"
    say "service aktif (health: $code)"
    if [[ "$code" != "200" ]]; then
      warn "health belum 200 — cek log: journalctl -u $SERVICE -n 50"
    fi
    return 0
  fi

  warn "service GAGAL start — rollback"
  local bak
  bak="$(ls -1t "$BIN".bak-* 2>/dev/null | head -1 || true)"
  if [[ -n "$bak" ]]; then
    cp "$bak" "$BIN"
    systemctl restart "$SERVICE" || true
    die "rollback ke $bak — versi baru bermasalah"
  fi
  die "gagal start & tidak ada backup. Cek: journalctl -u $SERVICE -n 80"
}

# ---------- main ----------
main() {
  local mode="${1:-install}"
  local want_ver="${2:-}"
  need_root
  ensure_tools
  local arch; arch="$(detect_arch)"

  case "$mode" in
    install|update)
      local ver="${want_ver:-$(latest_version)}"
      [[ -n "$ver" ]] || die "tidak bisa ambil versi terbaru dari GitHub (cek koneksi/rilis)"
      say "target: $ver ($arch)"
      download_release "$ver" "$arch"
      ensure_service
      restart_and_check
      echo
      say "selesai. Web admin: http://$(hostname -I | awk '{print $1}'):$PORT/admin"
      ;;
    *)
      die "mode tidak dikenal: $mode (pakai: install|update [versi])"
      ;;
  esac
}

main "$@"
