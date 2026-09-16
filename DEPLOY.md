# Deploy laptop-debian

Lokasi: /opt/agenpulsa (root). Semua config via ENV — gak perlu rebuild buat ganti config.

## File

```
/opt/agenpulsa/
  .env                    # SEMUA config di sini (chmod 600)
  agenpulsa-server        # binary linux amd64
  helper/isip_api.py      # helper HTTP isipulsa (butuh python3 + requests)
  web/                    # embedded UI (perlu ada saat build, runtime dibaca dari binary)
  data/agenpulsa.db       # SQLite (WAL)
  profile/                # cookies isipulsa (inject via web admin)
```

## Service systemd

- `agenpulsa-server.service` — server HTTP (EnvironmentFile=/opt/agenpulsa/.env)
- `cloudflare-agenpulsa.service` — tunnel agenpulsa.jhopan.my.id -> 127.0.0.1:8082
  (config /etc/cloudflared/agenpulsa.yml, credentials /root/.cloudflared/41d64cd0-*.json)

## .env (semua opsional kecuali dinyatakan)

```
AP_PORT=8082                          # port HTTP
AP_DB=/opt/agenpulsa/data/agenpulsa.db
AP_PROFILE=/opt/agenpulsa/profile
PAYPAN_BASE_URL=                      # kosong = pakai settings dari web admin
PAYPAN_TOKEN=                         # (web admin menang kalau env kosong)
PAYPAN_WEBHOOK_SECRET=
PAYPAN_WEBHOOK_URL=
```

## Perintah

```bash
systemctl restart agenpulsa-server      # setelah edit .env
journalctl -u agenpulsa-server -f       # log live
systemctl status cloudflare-agenpulsa   # tunnel
```

## Update binary

```bash
# di Windows:
GOOS=linux GOARCH=amd64 go build -o agenpulsa-server-linux-amd64 .
scp agenpulsa-server-linux-amd64 laptop-debian:/opt/agenpulsa/agenpulsa-server
# di debian:
systemctl restart agenpulsa-server
```

## Web admin

https://agenpulsa.jhopan.my.id  (login admin — password ada di settings DB admin_pass)
Webhook untuk Paypan: https://agenpulsa.jhopan.my.id/webhook
