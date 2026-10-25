# AgenPulsa Server

Core order pulsa/paket kuota isipulsa.web.id sebagai HTTP server + web admin. Dibangun ulang dengan Go (rod + SQLite + stdlib), terpisah dari client (bot Telegram lama di `../AgenPulsa/`, bot WA, Paypan).

## Konsep

```
agenpulsa-server (repo ini)
├── HTTP API      <- client: bot TG lama, bot WA, Paypan
├── Web admin     <- browser (embed, stdlib net/http)
├── Worker rod    <- Chromium, queue 1 jalur, order isipulsa
├── Scheduler     <- jadwal harian/interval/sekali (WIB)
└── SQLite        <- catalog, orders, schedules, contacts, settings, api_keys
```

Server satu-satunya yang buka browser. Client hanya bicara API.

## Build & run

```bash
go build -o agenpulsa-server .
./agenpulsa-server          # default :8081, DB data/agenpulsa.db, profile/ (user-data-dir rod)
```

Env:
- `AP_PORT` (default 8081)
- `AP_DB` (default data/agenpulsa.db)
- `AP_PROFILE` (default profile/) — user-data-dir Chromium; cookie login isipulsa di sini

Chromium: rod auto-download; fallback `/usr/bin/chromium`, `/usr/bin/chromium-browser`, atau `chromium` di PATH.

## Setup awal

1. Jalankan server, buka web admin → isi API key di kolom atas (tersimpan localStorage).
2. Insert key admin pertama lewat SQLite:

```sql
INSERT INTO api_keys(key,nama,boleh_order,boleh_admin) VALUES('KEY-ADMIN','admin',1,1);
INSERT INTO settings(key,value) VALUES('admin_key','KEY-ADMIN');
INSERT INTO settings(key,value) VALUES('paypan_secret','SECRET-SHARED');
```

3. Tambah katalog (POST /api/v1/catalog dengan key admin).

Auth admin: key cocok dengan settings `admin_key` (constant-time) ATAU ada di `api_keys` dengan `boleh_admin=1`.

## API v1

Header: `X-API-Key: <key>`

| Method | Path | Ket |
|---|---|---|
| GET | `/api/v1/catalog?all=1` | daftar katalog (all=1 termasuk nonaktif) |
| POST | `/api/v1/catalog` | upsert katalog (admin) |
| DELETE | `/api/v1/catalog/{id}` | hapus katalog (admin) |
| POST | `/api/v1/orders` | order langsung → antri. Body: `{catalog_id\|voucher, nomor, ref?, callback_url?, chat_id?}` |
| POST | `/api/v1/orders/pending` | order status `pending_payment` (flow paypan) |
| GET | `/api/v1/orders/{id}` | status order |
| GET | `/api/v1/orders?status=&limit=` | daftar order |
| GET | `/api/v1/status` | login + saldo + WIB + flag maintenance |
| GET | `/api/v1/report?days=1\|7\|30` | laporan sukses/gagal/modal/omzet/profit |
| GET | `/api/v1/maintenance` | flag jam rekap 23:40-00:35 WIB |
| POST | `/api/v1/beli` | pembelian manual admin (QRIS paypan, admin session) |
| GET/POST | `/api/v1/jadwal` | jadwal admin CRUD (harian/sekali/interval) |
| POST/DELETE | `/api/v1/jadwal/{id}/toggle`, `/api/v1/jadwal/{id}` | toggle/hapus jadwal |
| POST | `/api/v1/langganan` | pembelian terjadwal customer (publik, sekali-jalan, QRIS) |
| GET/POST/DELETE | `/api/v1/keys` (+ `/{key}`) | CRUD API key client |
| POST | `/webhook` | webhook Paypan `order.paid` (HMAC, tanpa API key) — PATH FIXUS, jangan pindah |

`ref` = idempotency key. Kirim ulang request dengan ref sama → balas order lama, tidak dobel order.

Contoh order:

```bash
curl -X POST http://localhost:8081/api/v1/orders \
  -H 'X-API-Key: KEY-CLIENT' -H 'Content-Type: application/json' \
  -d '{"catalog_id":1,"nomor":"62 877-1234 5678","ref":"wa-628123-1719999"}'
```

## Flow Paypan (QRIS)

Paypan production: `https://paypan.jhopan.my.id` (BLS token scope order di settings `paypan_token`, webhook secret di `paypan_secret`). Config bisa dari env `PAYPAN_BASE_URL/PAYPAN_TOKEN/PAYPAN_WEBHOOK_SECRET`.

1. Pembelian (manual admin `/api/v1/beli`, langganan customer `/api/v1/langganan`, atau client `POST /api/v1/orders/pending`) → order `pending_payment` + server panggil `POST /api/invoice` ke Paypan → simpan `invoice_id`, balas `pay_url`/`qr_url`/`total` ke pembayar.
2. Customer scan QR (total = price + kode unik 3 digit, expired 5 menit) → NotifListen di HP merchant tangkap notif GoPay → Paypan match by total → invoice `paid`.
3. Paypan kirim webhook:

```
POST /webhook
X-Paypan-Event: order.paid
X-Paypan-Signature: hex(hmac_sha256(raw_body, webhook_secret))
{"event":"order.paid","order":{"id":"...","price":...,"code":...,"total":...,"paid_at":...}}
```

4. Server verifikasi HMAC → cari order by `invoice_id` (idempoten: status bukan pending = ok) →
   - order manual/client: status → `queued`, worker eksekusi (buy ke isipulsa, saldo isipulsa kepotong)
   - order langganan: buat schedule sekali-jalan (tanggal+jam pilihan customer), status → `scheduled`; scheduler eksekusi 1x lalu auto-hapus
5. Invoice expired 5 menit tanpa bayar → order tetap pending_payment (bisa dibuat invoice baru).

Order gagal setelah bayar (maintenance/saldo agen habis): status failed + pesan jelas; refund via admin Paypan (catatan administratif, transfer manual).

## Guard bawaan

- Maintenance 23:40-00:35 WIB: order di periode itu → `cancelled` + pesan.
- Harga naik: bila harga real-time > `harga_max` katalog → order batal.
- Login habis: order gagal dengan pesan jelas; cek `GET /api/v1/status`.
- Nomor dinormalisasi (`62 877-` → `0877...`) sebelum masuk order.
- **Provider**: prefix nomor harus cocok operator paket (katalog kolom `operator`);
  beda → order dibatalkan sebelum saldo kepotong.
- **Expire 5 menit**: order `pending_payment` lewat `created_at + 5m` → `failed`
  ("expired — tidak dibayar dalam 5 menit"). Murni server, TIDAK bergantung
  Paypan. Reconcile loop tiap 15 detik. Webhook `order.paid` yang datang
  setelah expire → order diproses ulang (bayar terlambat, jangan diamkan).
- **Limit order aktif**: maksimal 3 order aktif (`pending_payment`/`queued`/`running`)
  per **identitas user** (`client_id`: `tg:<chat_id>` / `wa:<jid>`) — BUKAN per IP
  (banyak user satu WiFi/NAT). Penuh → 429 "kamu masih punya 3 order aktif".
  Bot cek dulu: `GET /api/v1/order-status` → `{aktif, maks, bisa_order, order_pending}`.
- **Watchdog**: order `running` > 10 menit (server crash) → `failed` + notif
  Telegram. **TIDAK** di-retry (biar aman, admin beli manual).

## Channel notifikasi (2 channel)

| Channel | Tujuan | Isi | Setting |
|---|---|---|---|
| **Admin** | owner (privat) | lengkap: nomor, ref, modal, profit, pesan error | `bot_tg_token` + `bot_admin_id` (**whitelist**, pisah koma) + `bot_admin_thread_id` (topik, opsional) |
| **Publik** | testimoni/voice (bisa dibaca orang lain) | **tanpa nomor HP, ref, data pribadi** — cuma paket + harga + waktu | `bot_public_token` + `bot_public_chat_id` + `bot_public_thread_id` (topik, opsional) |

- **Whitelist admin**: notif lengkap hanya dikirim ke chat ID yang terdaftar di
  `bot_admin_id` (bisa banyak, pisah koma). Kalau bot admin kena add ke grup lain,
  chat itu TIDAK menerima notif karena bukan whitelist.

- Channel publik = bot token TERPISAH (bot testimoni). Default: **hanya
  transaksi selesai (success)** — bukan saat order masuk. Ganti via
  `notif_public_status` (pisah koma, mis. `success,failed`).
- **Supergrup = channel publik** (topik "Testimoni"). **Admin = chat pribadi**
  (`bot_admin_id` whitelist) — TIDAK kirim ke grup.
- Bot LAIN bisa push ke topik testimoni: `POST /api/v1/notify-public`
  `{"pesan":"..."}` — pesan otomatis disensor (nomor HP/ref dibuang).
- Keamanan: nomor HP/ref di-hardcode TIDAK dimasukin ke pesan publik. Label order
  yang kebetulan nyimpen nomor (mis. `LANGGANAN 0878... (sched-...)`) disensor
  `bersihkanNomor()`. Test: `TestNotifPublicTanpaNomor`.

## Stabilitas & monitoring

| Fitur | Detail |
|---|---|
| Timeout helper isipulsa | 120 detik + kill process group (order yg hang tak memblokir worker) |
| Claim order atomik | `PopNextQueued` claim `queued`→`running` dalam 1 transaksi (anti dobel-beli) |
| WAL checkpoint | `wal_autocheckpoint=1000` + checkpoint TRUNCATE tiap 5 menit (WAL tak membengkak) |
| Health check | `GET /health` (publik, tanpa API key) — status, uptime, ukuran DB/WAL, versi |
| Rate limit | 10 order / 5 menit per kunci; peta disapu tiap 5 menit |
| Backup Google Sheet | order dikirim ke Apps Script webhook tiap 10 menit (`sheet_backup_url`); template: `helper/sheet_backup_appsscript.js` |
| Log latency | `[ORDER #id] <status> durasi=...` tiap order selesai |
| Exec timeout | semua `exec.Command` punya batas waktu (goroutine leak) |

Endpoint health juga dipakai auto-ping: `https://agenpulsa.jhopan.my.id/health`.

## Rilis & update (GitHub Releases)

Repo punya **GitHub Actions** (`.github/workflows/release.yml`): push tag `v*`
→ test → build `linux/amd64` + `linux/arm64` → publish release.

```bash
git tag v1.2.0
git push origin v1.2.0
```

Artefak per release:
- `agenpulsa-server-linux-amd64` / `-arm64` (binary tunggal)
- `agenpulsa-server_<ver>_linux_amd64.tar.gz` / `_arm64.tar.gz` (binary + `web/` + `helper/`)
- `checksums.txt` (sha256)

**"Latest"** = release terbaru non-draft/non-prerelease (otomatis via `make_latest: true`).
Link stabil:
- semua versi: `https://github.com/jhopan/agenpulsa-server/releases`
- terbaru: `https://github.com/jhopan/agenpulsa-server/releases/latest`
- unduh langsung: `.../releases/latest/download/agenpulsa-server-linux-amd64`

**Install / update di server (tanpa Go):**

```bash
# install baru
curl -fsSL https://raw.githubusercontent.com/jhopan/agenpulsa-server/master/install.sh | bash
# update ke latest
curl -fsSL .../install.sh | bash -s -- update
# versi tertentu
curl -fsSL .../install.sh | bash -s -- update v1.2.0
```

`install.sh` = deteksi arsitektur → download paket → **backup binary lama** →
pasang systemd + `.env` (kalau belum ada) → pasang CLI `ap` → restart → cek
`/health` → **rollback otomatis** kalau gagal start.

**CLI `ap`** (`manage.sh` → `/usr/local/bin/ap`) — satu perintah untuk operasional:

| Perintah | Fungsi |
|---|---|
| `ap` | menu interaktif |
| `ap status` | service + versi + health + autostart |
| `ap start` / `stop` / `restart` | kontrol service |
| `ap log` | log terakhir + follow |
| `ap update [versi]` | update ke latest / versi tertentu (delegasi ke install.sh) |
| `ap version` / `ap health` | cek versi / `/health` |
| `ap backup` | backup DB (`.backup`, konsisten) + binary |
| `ap restore <file>` | restore DB (backup dulu otomatis) |

Service `enabled` = auto-start saat boot, jadi `ap start` hanya perlu kalau
sengaja dimatikan.

**Soal "latest"**: GitHub TIDAK bikin entri release terpisah bernama latest.
Mekanismenya: tag terbaru diberi badge `Latest`, dan `/releases/latest`
redirect ke tag itu. Semua URL `/releases/latest/download/<file>` otomatis
ikut versi terbaru — jadi jangan cari entri "latest" di daftar.

**Versi di binary**: di-inject via ldflags dari `internal/version`
(`Version`, `Commit`, `BuildDate`). Build lokal = `dev`.
Cek: `agenpulsa-server --version` atau `GET /health` field `version`.

Catatan deploy manual (tanpa release) tetap ada di `DEPLOY.md`.

## Verifikasi

```bash
go test ./...     # normalisasi nomor, parse harga, boundary maintenance, ref idempoten, API smoke
go vet ./...
```

Test hermetic: SQLite di t.TempDir + httptest, browser rod tidak jalan (lazy). Butuh Go 1.25.

## Batas lingkup (WAJIB dipatuhi)

Repo ini = **hanya agenpulsa-server**. Agent TIDAK BOLEH mengubah file di luar
lingkup ini (project lain, deploy tooling milik service lain, dsb). Kalau
masalah terdeteksi di project lain:

1. **JANGAN edit langsung** — cukup LAPOR: apa yang salah + file/line-nya +
   saran perbaikan, lalu biarkan user putuskan (atau dikerjakan di sesi
   project yang tepat).
2. Contoh lintasan yang pernah terjadi: bug `helper/isip_api.py` butuh
   playwright di debian → itu masih dalam repo ini (di-scope OK). Bandingkan:
   bug di `jhopanwabot` / `PayPan` / `tempmail` → LAPOR saja, jangan sentuh.

Lingkup aman (boleh edit):
- Semua file di repo ini (`internal/`, `web/`, `helper/`, `main*.go`, `AGENTS.md`, `DEPLOY.md`)

### Target deploy — ATURAN WAJIB

| Target | Host | Arsitektur | Port | Peran |
|---|---|---|---|---|
| **Staging** | `ssh root@192.168.10.22` (host alias `armbian-jhosua`) | ARM64 | 8081 | **Tempat pengujian** — semua perubahan diuji di sini dulu, bebas iterasi |
| **Production** | `ssh laptop-debian` | AMD64 | 8082 | **Production** (`agenpulsa.jhopan.my.id`) — deploy **HANYA kalau user menyuruh** |

- Deploy rutin / pengujian / iterasi → **staging armbian** (`./deploy.sh staging`).
- Deploy ke **debian (production) HANYA** setelah user eksplisit bilang "deploy ke production" / "deploy ke debian". Jangan pernah auto-deploy ke production.
- Service di masing-masing host: `agenpulsa-server.service` (DB `/opt/agenpulsa/data/agenpulsa.db`, profile `/opt/agenpulsa/profile`).
- Service TAMBAHAN di debian (jangan tomper): `cloudflare-agenpulsa.service` (tunnel `agenpulsa.jhopan.my.id`).
- DB runtime debian `/opt/agenpulsa/data/agenpulsa.db` (settings/katalog — via API atau SQL hati-hati)

Lingkup TERLARANG (lapor saja, jangan edit):
- `~/Documents/project/jhopanwabot/**` (project WA bot)
- `~/Documents/project/PayPan/**` (server paypan — paypan jangan disentuh)
- `~/Documents/project/TempMail*` / service tempmail (:8081 di debian)
- Service systemd MILIK LAIN di debian (tempmail, paypan, vaultwarden, vpn, camofox, dsb) — gak boleh stop/edit/restart
- VPS neva — sepenuhnya off-limits

## Status migrasi

- Server core: SKELETON JALAN (order engine rod, queue, scheduler, API, web admin, webhook paypan).
- Bot Telegram lama (`../AgenPulsa/`): TIDAK diubah, masih dipakai produksi. Nanti tgbot.py jadi client API ini.
- Login isipulsa = inject cookies (export Cookie-Editor) — login interaktif/VNC DIHAPUS.
- Belum ada: order batch, retry callback, admin form UI (sekarang read-only). Paypan connector di sisi paypan belum dibuat.

## Layout

```
main.go              # bootstrap (embed web/, start worker + scheduler + API)
main_test.go         # test utama
web_test.go          # testWebFS embed + driver sqlite untuk test
web/index.html       # web admin (embed)
internal/db/         # db.go (Open, pragma WAL) + store.go (CRUD) + schema.sql (embed)
internal/engine/     # rod: order/search/cekstatus, queue worker, guard, cari chromium
internal/scheduler/  # jadwal WIB harian/interval/sekali, loop 30 detik
internal/api/        # api.go (routes, auth API key) + paypan.go (webhook HMAC)
```
