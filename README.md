# AgenPulsa Server

Server order pulsa / paket kuota **isipulsa.web.id** — HTTP API + web admin + scheduler, ditulis Go (stdlib + SQLite, tanpa dependency runtime).

Dipakai oleh bot Telegram, bot WhatsApp, dan web admin. Pembayaran lewat **QRIS PayPan**, notifikasi lewat **bot Telegram**.

---

## Fitur

- **Katalog** paket isipulsa (label bebas, voucher, provider untuk guard nomor)
- **Order manual** (admin langsung beli) & **order terjadwal** (sekali jalan)
- **Alur QRIS**: order → invoice PayPan → bayar → webhook `order.paid` → beli otomatis
- **Expire 5 menit**: order tak dibayar otomatis `failed` (murni server, tak bergantung PayPan)
- **Guard provider**: nomor harus cocok operator paket, dicek sebelum saldo kepotong
- **Guard jadwal**: tanggal masa lalu ditolak di semua jalur
- **Limit 3 order aktif per user** (`client_id` bot, bukan per IP)
- **Notifikasi 2 channel**: admin (lengkap, whitelist) + publik/testimoni (tanpa nomor HP)
- **Kontak pelanggan** tersimpan, tinggal pilih saat beli
- **Watchdog**: order nyangkut → `failed` + notif (tanpa retry)
- **Health check** `/health`, rate limit, WAL checkpoint, backup Google Sheet

---

## Install / Update (via GitHub Releases)

Tidak perlu Go di server — binary di-build otomatis oleh GitHub Actions.

### Install baru

```bash
curl -fsSL https://raw.githubusercontent.com/jhopan/agenpulsa-server/master/install.sh | bash
```

### Update ke versi terbaru (`latest`)

```bash
curl -fsSL https://raw.githubusercontent.com/jhopan/agenpulsa-server/master/install.sh | bash -s -- update
```

### Update / install versi tertentu

```bash
curl -fsSL https://raw.githubusercontent.com/jhopan/agenpulsa-server/master/install.sh | bash -s -- update v1.2.0
```

Script akan: deteksi arsitektur (amd64/arm64) → download paket release → backup binary lama → pasang systemd service (kalau belum ada) → restart → cek `/health`. Kalau gagal start, **otomatis rollback** ke binary sebelumnya.

### Setelah terpasang — CLI `ap`

`install.sh` memasang perintah **`ap`** ke `/usr/local/bin/ap`:

```bash
ap                 # menu interaktif
ap status          # status + versi + health
ap start           # nyalakan
ap stop            # matikan
ap restart         # restart
ap log             # log terakhir (follow)
ap update          # update ke latest
ap update v1.2.0   # update/pasang versi tertentu
ap version         # versi terpasang
ap health          # cek /health
ap backup          # backup DB + binary sekarang
ap restore <file>  # restore DB dari backup
```

Service sudah **enabled** (auto-start saat boot). Jadi setelah reboot tinggal jalan sendiri.

### Halaman release

- Semua versi: https://github.com/jhopan/agenpulsa-server/releases
- **Latest** (selalu versi terbaru): https://github.com/jhopan/agenpulsa-server/releases/latest

> Catatan: GitHub tidak membuat entri release terpisah bernama "latest". Yang ada:
> **tag terbaru ditandai badge `Latest`**, dan URL `/releases/latest` selalu
> redirect ke tag itu. Semua link download di bawah pakai `/latest/download/...`
> sehingga otomatis ikut versi terbaru.

### Download manual

```bash
# Linux x86_64 (debian/ubuntu)
curl -fLO https://github.com/jhopan/agenpulsa-server/releases/latest/download/agenpulsa-server-linux-amd64
chmod +x agenpulsa-server-linux-amd64
./agenpulsa-server-linux-amd64 --version

# Linux ARM64 (armbian/raspberry)
curl -fLO https://github.com/jhopan/agenpulsa-server/releases/latest/download/agenpulsa-server-linux-arm64
```

Paket lengkap (binary + `web/` + `helper/`):

```
https://github.com/jhopan/agenpulsa-server/releases/latest/download/agenpulsa-server_<versi>_linux_amd64.tar.gz
https://github.com/jhopan/agenpulsa-server/releases/latest/download/agenpulsa-server_<versi>_linux_arm64.tar.gz
```

Cek integritas: `checksums.txt` (sha256) di halaman release.

---

## Jalankan dari source

```bash
go build -o agenpulsa-server .
./agenpulsa-server            # default :8081, DB data/agenpulsa.db
```

Env (infra saja):

| Env | Default | Keterangan |
|---|---|---|
| `AP_PORT` | `8081` | port HTTP |
| `AP_DB` | `data/agenpulsa.db` | path SQLite |
| `AP_PROFILE` | `profile` | user-data-dir Chromium (cookies isipulsa) |

> Config runtime (PayPan, bot Telegram, ambang saldo, channel publik) **diatur di web admin**, bukan env — bisa diubah tanpa restart.

---

## Setup awal

1. Buka `http://<ip>:<port>/admin` → login (default `admin` / `admin123`, **ganti** di Pengaturan)
2. **Pengaturan → Integrasi**: URL server publik, PayPan base URL + token + secret
3. **Pengaturan → Bot & Notifikasi**: bot token, whitelist chat ID admin
4. **Pengaturan → Channel Publik (Testimoni)**: chat ID supergrup + topik, token publik (kosong = pakai bot admin)
5. **Akun Login**: inject cookies isipulsa (export dari Cookie-Editor)
6. **Katalog**: tambah paket (search dari isipulsa, provider terisi otomatis)
7. Daftarkan webhook di admin PayPan: `https://<domain>/webhook`

---

## API singkat

Semua endpoint butuh header `X-API-Key` (kecuali `/health`, `/api/login`, `/webhook`).

| Method | Path | Fungsi |
|---|---|---|
| GET | `/health` | health check publik (uptime, versi, ukuran DB) |
| GET | `/api/v1/catalog` | daftar katalog aktif |
| POST | `/api/v1/orders/pending` | order user → invoice QRIS |
| POST | `/api/v1/langganan` | order terjadwal sekali-jalan → invoice QRIS |
| GET | `/api/v1/orders/{ref}` | status order |
| GET | `/api/v1/order-status` | status antrian user (buat bot) |
| POST | `/api/v1/notify-public` | push notif ke channel publik (bot lain) |
| POST | `/webhook` | webhook PayPan (HMAC) |

---

## Rilis versi baru (maintainer)

```bash
git tag v1.2.0
git push origin v1.2.0
```

GitHub Actions akan: jalankan test → build `amd64` + `arm64` → buat paket tar.gz → publikasikan release. Release terbaru otomatis ditandai **Latest**.

---

## Lisensi

Private — JhopanStore.
