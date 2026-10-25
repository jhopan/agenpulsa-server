#!/usr/bin/env bash
# ============================================================
#  ap — CLI manajemen AgenPulsa Server
#
#  Dipasang otomatis oleh install.sh ke /usr/local/bin/ap
#
#  Pakai:
#    ap                  menu interaktif
#    ap start            nyalakan service
#    ap stop             matikan
#    ap restart          restart
#    ap status           status + versi + health
#    ap log              log terakhir (follow)
#    ap update           update ke release terbaru (latest)
#    ap update v1.2.0    update ke versi tertentu
#    ap version          versi binary terpasang
#    ap health           cek /health
#    ap backup           backup DB + binary sekarang
#    ap restore <file>   restore DB dari backup
# ============================================================
set -uo pipefail

REPO="jhopan/agenpulsa-server"
DIR="/opt/agenpulsa"
BIN="$DIR/agenpulsa-server"
SERVICE="agenpulsa-server"
BACKUP_DIR="$DIR/backup"
INSTALL_URL="https://raw.githubusercontent.com/$REPO/master/install.sh"

R="\033[0m"; G="\033[1;32m"; Y="\033[1;33m"; RED="\033[1;31m"; C="\033[1;36m"; B="\033[1m"
say()  { echo -e "${G}==>${R} $*"; }
warn() { echo -e "${Y}!>${R} $*"; }
die()  { echo -e "${RED}x>${R} $*" >&2; exit 1; }

need_root() { [[ $EUID -eq 0 ]] || die "jalankan sebagai root (sudo ap $*)"; }

port() {
  local p
  p="$(grep -E '^AP_PORT=' "$DIR/.env" 2>/dev/null | cut -d= -f2 | tr -d '[:space:]')"
  echo "${p:-8081}"
}

do_version() {
  if [[ -x "$BIN" ]]; then "$BIN" --version 2>/dev/null || echo "?"; else echo "(belum terpasang)"; fi
}

do_health() {
  local p code body
  p="$(port)"
  body="$(curl -s --max-time 5 "http://127.0.0.1:$p/health" 2>/dev/null || true)"
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "http://127.0.0.1:$p/health" 2>/dev/null || echo 000)"
  if [[ "$code" == "200" ]]; then
    echo -e "${G}OK${R} $body"
    return 0
  fi
  echo -e "${RED}DOWN${R} (HTTP $code)"
  return 1
}

do_status() {
  local p state ver
  p="$(port)"
  state="$(systemctl is-active "$SERVICE" 2>/dev/null || echo unknown)"
  ver="$(do_version)"
  echo -e "${B}AgenPulsa Server${R}"
  echo "  service : $state"
  echo "  versi   : $ver"
  echo "  port    : $p"
  echo "  folder  : $DIR"
  if [[ "$state" == "active" ]]; then
    printf "  health  : "; do_health
  fi
  echo "  autostart: $(systemctl is-enabled "$SERVICE" 2>/dev/null || echo disabled)"
}

do_start()   { need_root start;   say "start $SERVICE";   systemctl start   "$SERVICE"; sleep 2; do_status; }
do_stop()    { need_root stop;    say "stop $SERVICE";    systemctl stop    "$SERVICE"; systemctl is-active "$SERVICE" 2>/dev/null || echo "  (berhenti)"; }
do_restart() { need_root restart; say "restart $SERVICE"; systemctl restart "$SERVICE"; sleep 3; do_status; }

do_log() {
  local n="${1:-60}"
  journalctl -u "$SERVICE" -n "$n" --no-pager 2>/dev/null || die "tidak bisa baca log"
  echo
  warn "ikuti log langsung? tekan Ctrl+C untuk keluar"
  sleep 1
  journalctl -u "$SERVICE" -f
}

do_update() {
  need_root update
  local ver="${1:-}"
  say "update ${ver:-(latest)}"
  # delegate ke install.sh (sudah ada backup + rollback + health check)
  if command -v curl >/dev/null 2>&1; then
    if [[ -n "$ver" ]]; then
      curl -fsSL "$INSTALL_URL" | bash -s -- update "$ver"
    else
      curl -fsSL "$INSTALL_URL" | bash -s -- update
    fi
  else
    die "curl tidak ada — pasang dulu: apt install curl"
  fi
  echo
  do_status
}

do_backup() {
  need_root backup
  local ts f
  ts="$(date +%Y%m%d-%H%M%S)"
  mkdir -p "$BACKUP_DIR"
  if [[ -f "$DIR/data/agenpulsa.db" ]]; then
    f="$BACKUP_DIR/agenpulsa-$ts.db"
    # pakai .backup biar konsisten walau WAL aktif
    sqlite3 "$DIR/data/agenpulsa.db" ".backup '$f'" 2>/dev/null \
      || cp "$DIR/data/agenpulsa.db" "$f"
    say "DB: $f ($(du -h "$f" | cut -f1))"
  fi
  [[ -f "$BIN" ]] && cp "$BIN" "$BACKUP_DIR/agenpulsa-server-$ts" && say "binary: $BACKUP_DIR/agenpulsa-server-$ts"
  say "selesai"
}

do_restore() {
  need_root restore
  local f="${1:-}"
  [[ -n "$f" ]] || die "pakai: ap restore /opt/agenpulsa/backup/agenpulsa-XXXX.db"
  [[ -f "$f" ]] || die "file tidak ada: $f"
  say "backup DB sekarang dulu (aman)"
  do_backup >/dev/null 2>&1 || true
  systemctl stop "$SERVICE" || true
  cp "$f" "$DIR/data/agenpulsa.db"
  rm -f "$DIR/data/agenpulsa.db-wal" "$DIR/data/agenpulsa.db-shm"
  systemctl start "$SERVICE"
  sleep 3
  say "restore selesai dari $f"
  do_status
}

menu() {
  echo -e "${B}AgenPulsa Server — menu${R}"
  echo "  1) Status"
  echo "  2) Start"
  echo "  3) Stop"
  echo "  4) Restart"
  echo "  5) Log (follow)"
  echo "  6) Update ke latest"
  echo "  7) Backup sekarang"
  echo "  8) Health check"
  echo "  0) Keluar"
  echo
  read -rp "Pilih: " c
  case "$c" in
    1) do_status ;;
    2) do_start ;;
    3) do_stop ;;
    4) do_restart ;;
    5) do_log 100 ;;
    6) do_update ;;
    7) do_backup ;;
    8) do_health ;;
    0|*) exit 0 ;;
  esac
}

case "${1:-menu}" in
  menu|"")    menu ;;
  start)      do_start ;;
  stop)       do_stop ;;
  restart)    do_restart ;;
  status)     do_status ;;
  log|logs)   do_log "${2:-60}" ;;
  update|up)  do_update "${2:-}" ;;
  version|ver) do_version ;;
  health)     do_health ;;
  backup)     do_backup ;;
  restore)    do_restore "${2:-}" ;;
  *) die "perintah tidak dikenal: $1 (pakai: ap [start|stop|restart|status|log|update|version|health|backup|restore])" ;;
esac
