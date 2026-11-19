#!/usr/bin/env python3
"""isip_api.py — klien isipulsa.web.id via HTTP murni (requests).

TANPA Chromium/Turnstile: semua pakai cookies sesi + CSRF + UA yang sama.
Cookies dibaca dari jar JSON (hasil inject di web admin, format Cookie-Editor).

Pakai:
  python isip_api.py cekstatus --cookies cookies.json
  python isip_api.py search    --cookies cookies.json --tab "Paket Kuota" --cari "5GB"
  python isip_api.py order     --cookies cookies.json --nomor 0812... --voucher 701 [--harga-max 60000]

Output: satu baris JSON {"ok":..,"login":..,"saldo":..,"items":..,"pesan":..}
"""
import argparse
import ast
import atexit
import json
import os
import re
import sys
import time
import tempfile

import requests

BASE = "https://isipulsa.web.id/"
UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"

RE_NONDIGIT = re.compile(r"[^\d]")

# Prefix nomor Indonesia -> nama operator (dipakai guard operator sebelum order).
# Kelengkapan: seluruh prefix Seluler Indonesia (Telkomsel, XL, Axis, Indosat
# Ooredoo+H3I, Three, Smartfren, By.U, dan blok lama).
PREFIX_OPERATOR = [
    # Telkomsel (simPATI, Kartu As, Halo, By.U juga di 62851/6285x sebagian)
    ("62811", "Telkomsel"), ("62812", "Telkomsel"), ("62813", "Telkomsel"),
    ("62821", "Telkomsel"), ("62822", "Telkomsel"), ("62823", "Telkomsel"),
    ("62852", "Telkomsel"), ("62853", "Telkomsel"),
    # XL Axiata (XLSmart)
    ("62817", "XL"), ("62818", "XL"), ("62819", "XL"), ("62859", "XL"),
    ("62877", "XL"), ("62878", "XL"), ("62851", "XL"),
    # Axis (jaringan XL)
    ("62831", "Axis"), ("62832", "Axis"), ("62833", "Axis"), ("62838", "Axis"),
    # Indosat Ooredoo (IM3 + Ooredoo)
    ("62814", "Indosat"), ("62815", "Indosat"), ("62816", "Indosat"),
    ("62855", "Indosat"), ("62856", "Indosat"), ("62857", "Indosat"), ("62858", "Indosat"),
    # Three (H3I)
    ("62895", "Three"), ("62896", "Three"), ("62897", "Three"), ("62898", "Three"), ("62899", "Three"),
    # Smartfren
    ("62881", "Smartfren"), ("62882", "Smartfren"), ("62883", "Smartfren"),
    ("62888", "Smartfren"), ("62889", "Smartfren"),
    # Blok lama (jarang, tapi biar lengkap)
    ("62861", "Telkom"), ("62864", "Telkom"), ("62865", "Telkom"),   # Telkom Flexi
    ("62868", "Telkom"),                                              # Telkom
    ("62827", "Smartfren"),                                           # Smart (Mobile-8/Smartfren lama)
    ("62829", "Smartfren"),                                           # Fren/Hepi (Smartfren lama)
]


def detect_operator(nomor: str) -> str:
    """08xxx/62xxx -> nama operator. '' kalau gak dikenali."""
    d = RE_NONDIGIT.sub("", nomor or "")
    if d.startswith("0"):
        d = "62" + d[1:]
    if not d.startswith("62"):
        return ""
    for prefix, op in PREFIX_OPERATOR:
        if d.startswith(prefix):
            return op
    return ""


# Operator isipulsa yang TIDAK terikat nomor HP (produk non-seluler /
# voucher fisik) — guard nomor dilewati utk operator2 ini.
# CATATAN: dicek dgn word-boundary — 'telkomsel' gak boleh kena 'telkom'.
OPERATOR_NON_NOMOR = (
    "pln", "bpjs", "garena", "gemscool", "megaxus",
    "telkom flexi", "pln pasca", "voucher game", "etoll", "e-money",
    "voucher internet", "token",
)


def operator_matches(nomor: str, operator_nama: str) -> bool:
    """True kalau operator nomor cocok dgn operator paket (fuzzy: Telkomsel
    cocok dgn 'Telkomsel (Ilmupedia)', XL dgn 'XL (Xtra Combo)', dst).

    Nomor gak dikenali ATAU katalog tanpa info operator ATAU produk non-seluler
    (PLN/game/dll) -> True (skip guard).
    """
    op_paket = (operator_nama or "").lower()
    if not op_paket:
        return True  # katalog gak ada info operator -> skip guard
    for non in OPERATOR_NON_NOMOR:
        if non in op_paket:
            return True  # PLN/game/token dll — gak terikat nomor seluler
    op_num = detect_operator(nomor).lower()
    if not op_num:
        return True  # nomor gak dikenali -> jangan blokir (biarkan isipulsa validasi)
    # 'telkomsel (ilmupedia)' -> 'telkomsel'
    op_paket_base = re.split(r"[\s(/-]", op_paket.strip())[0]
    return op_num == op_paket_base or op_num in op_paket or op_paket_base in op_num


def get_retry(s, url, tries=3, timeout=30, **kw):
    """GET dengan retry — network flake / TLS timeout jangan bikin operasi gagal."""
    last = None
    for i in range(tries):
        try:
            return s.get(url, timeout=timeout, **kw)
        except Exception as e:
            last = e
            time.sleep(2 * (i + 1))
    raise last


def out(ok=False, **kw):
    kw["ok"] = ok
    print(json.dumps(kw, ensure_ascii=False))
    sys.exit(0 if ok else 1)


def load_cookie_file(path):
    """Jar cookie (format Cookie-Editor) -> list dict."""
    with open(path, encoding="utf-8") as f:
        raw = json.load(f)
    out = []
    for c in raw:
        if not isinstance(c, dict) or not c.get("name") or "value" not in c:
            continue
        out.append(c)
    return out


def load_cookie_jar(path):
    """Baca jar; kalau belum ada / rusak -> keluar dengan pesan jelas.

    login/saldo ikut diisi false/"-" supaya output tetap JSON valid untuk
    semua subcommand (search/order mengabaikannya).
    """
    try:
        return load_cookie_file(path)
    except FileNotFoundError:
        out(False, login=False, saldo="-",
            pesan=f"tidak ada cookie di jar ({path}) — inject cookies dulu di web admin -> Akun Login")
    except Exception as e:  # noqa: BLE001 — apa pun penyebabnya, jangan crash
        out(False, login=False, saldo="-",
            pesan=f"jar cookie tidak bisa dibaca ({path}): {e}")


def write_jar_atomic(path, jar):
    """Tulis jar atomik (file temp + rename) dengan permission 0600."""
    folder = os.path.dirname(os.path.abspath(path)) or "."
    os.makedirs(folder, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=folder, prefix=".cookies-", suffix=".json")
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(jar, f, indent=1, ensure_ascii=False)
            f.write("\n")
        try:
            os.chmod(tmp, 0o600)
        except OSError:
            pass  # windows
        os.replace(tmp, path)
    except Exception:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise


def persist_jar(path, session):
    """Best effort: simpan balik value/expiry cookie yang diperbarui isipulsa.

    isipulsa memutar `sid` & memperpanjang `csrf_cookie` tiap request; tanpa
    write-back, jar makin basi. Hanya cookie yang SUDAH ada di jar yang
    di-update (tidak menambah/menghapus entri).
    """
    if not path or not session:
        return
    try:
        with open(path, encoding="utf-8") as f:
            jar = json.load(f)
    except Exception:
        return
    fresh = {}
    for c in session.cookies:
        fresh[(c.name, (c.domain or "").lstrip("."), c.path or "/")] = c
    changed = False
    for entry in jar:
        if not isinstance(entry, dict):
            continue
        key = (entry.get("name"), (entry.get("domain") or "").lstrip("."),
               entry.get("path") or "/")
        c = fresh.get(key)
        if not c:
            continue
        if c.value and entry.get("value") != c.value:
            entry["value"] = c.value
            changed = True
        if c.expires and abs(float(entry.get("expirationDate") or 0) - float(c.expires)) > 1:
            entry["expirationDate"] = float(c.expires)
            entry["session"] = False
            changed = True
    if changed:
        try:
            write_jar_atomic(path, jar)
        except Exception:
            pass  # best effort: jar lama tetap valid


def new_session(cookies, jar_path=""):
    s = requests.Session()
    for c in cookies:
        s.cookies.set(c["name"], c["value"],
                      domain=(c.get("domain") or ".isipulsa.web.id").lstrip("."),
                      path=c.get("path") or "/")
    s.headers.update({
        "User-Agent": UA,
        "Referer": BASE,
        "Accept-Language": "id-ID,id;q=0.9",
    })
    # out() memakai sys.exit -> atexit selalu jalan, termasuk saat error.
    if jar_path:
        atexit.register(persist_jar, jar_path, s)
    return s


def parse_vouchers(html):
    m = re.search(r"var\s+vouchers\s*=\s*(\[.*?\]);\s*\n", html, re.S)
    if not m:
        return []
    raw = m.group(1)
    try:
        data = json.loads(raw)
    except Exception:
        data = ast.literal_eval(raw)
    # kolom: [voucher, produk, operator_id, nama_operator, nama_paket, harga, ?, desc]
    # operator_id = ID internal isipulsa (WAJIB di form order: data-operator button)
    items = []
    for d in data:
        try:
            items.append({
                "voucher": str(d[0]),
                "produk": str(d[1]),
                "operator_id": str(d[2]),
                "operator": str(d[3]),
                "nama": re.sub(r"<[^>]+>", " ", str(d[4])).strip(),
                "harga": int(re.sub(r"[^\d]", "", str(d[5])) or 0),
            })
        except Exception:
            continue
    return items


def get_csrf(s, page_html=None):
    if page_html:
        m = re.search(r'name="csrf_token" value="([^"]+)"', page_html)
        if m:
            return m.group(1)
    r = s.get(BASE, timeout=30)
    m = re.search(r'name="csrf_token" value="([^"]+)"', r.text)
    return m.group(1) if m else None


def op_cekstatus(args):
    cookies = load_cookie_jar(args.cookies)
    if not cookies:
        out(False, login=False, saldo="-",
            pesan="jar cookie kosong — inject cookies di web admin -> Akun Login")
    s = new_session(cookies, args.cookies)
    r = get_retry(s, BASE)
    if r.status_code != 200:
        # 5xx/429 halaman error, bukan bukti sesi ditolak -> gagal cek.
        out(False, saldo="-", error=f"HTTP {r.status_code} dari isipulsa")
    if "Just a moment" in r.text:
        # Cloudflare = rate-limit/IP, bukan cookie mati -> gagal cek.
        out(False, saldo="-", error="kena Cloudflare challenge")
    if "Masuk" in r.text and "Saldo" not in r.text:
        out(True, login=False, saldo="-", pesan="sesi login habis")
    m = re.search(r'Saldo Deposit\s*</[^>]+>\s*<span class="total-kredit">([^<]+)', r.text)
    if not m:
        m = re.search(r'Saldo Deposit.*?<span class="total-kredit">([^<]+)', r.text, re.S)
    if not m:
        m = re.search(r'Rp\s[\d.,]+', r.text)
    saldo = m.group(1).strip() if m and m.lastindex else (m.group(0).strip() if m else "Rp 0")
    saldo = saldo.replace("<br>", "").strip()
    out(True, login=True, saldo=saldo)


def op_search(args):
    cookies = load_cookie_jar(args.cookies)
    s = new_session(cookies, args.cookies)
    page = args.tab.lower().replace("paket kuota", "paket_kuota").replace(" ", "_")
    r = get_retry(s, f"{BASE}{page}" if page in ("pulsa", "paket_kuota") else BASE)
    if "Just a moment" in r.text:
        out(False, pesan="kena Cloudflare challenge")
    items = parse_vouchers(r.text)
    kw = (args.cari or "").lower()
    if kw:
        items = [i for i in items if kw in i["nama"].lower() or kw in i["operator"].lower()]
    if args.limit:
        items = items[:args.limit]
    out(True, jumlah=len(items), items=items)


def op_order(args):
    cookies = load_cookie_jar(args.cookies)
    s = new_session(cookies, args.cookies)

    # 1. halaman produk -> csrf + konfirmasi login
    page = args.produk or "pulsa"
    r = get_retry(s, f"{BASE}{page}")
    if "Just a moment" in r.text:
        out(False, pesan="kena Cloudflare challenge")
    if "Masuk" in r.text and "Saldo" not in r.text:
        out(False, pesan="GAGAL: sesi login habis. Inject cookies ulang.")
    csrf = get_csrf(s, r.text)
    if not csrf:
        out(False, pesan="csrf_token tidak ketemu")

    # 2. Resolusi voucher + verifikasi nama (anti-drift).
    #    Kasus yang di-cover:
    #    a) voucher ada, nama sama            -> order jalan
    #    b) voucher ada, nama BERUBAH          -> BATALKAN (isi/harga bisa beda)
    #    c) voucher hilang, ada --nama-asli    -> cari by nama, pakai voucher baru
    #    d) voucher hilang, nama gak ketemu    -> GAGAL + pesan update katalog
    items = parse_vouchers(r.text)
    voucher, nama_asli, harga = args.voucher, "", 0
    entri = next((i for i in items if i["voucher"] == voucher), None) if voucher else None

    if entri is not None:
        # (a)/(b): verifikasi nama sesuai katalog
        nama_asli, harga = entri["nama"], entri["harga"]
        catatan = ""
        if args.nama_asli and args.nama_asli.lower() not in nama_asli.lower():
            out(False, pesan=(f"ORDER DIBATALKAN: nama paket isipulsa berubah. "
                              f"Katalog: '{args.nama_asli}' | Sekarang: '{nama_asli}' (Rp {harga:,}). "
                              f"Periksa & update katalog.".replace(",", ".")))
    else:
        # (c)/(d): voucher hilang -> cari by nama asli
        kw = (args.nama_asli or args.cari or "").lower()
        entri = next((i for i in items if kw and kw in i["nama"].lower()), None)
        if not entri:
            out(False, pesan=(f"GAGAL: voucher {voucher or '-'} tidak ada di isipulsa "
                              f"dan nama '{args.nama_asli or args.cari or '-'}' tidak ketemu. "
                              f"Katalog perlu diupdate manual."))
        catatan = f"[voucher berubah {voucher} -> {entri['voucher']}] " if voucher else ""
        voucher, nama_asli, harga = entri["voucher"], entri["nama"], entri["harga"]

    # 3. guard operator: prefix nomor harus cocok dgn operator paket
    #    (ilmupedia = khusus telkomsel dll) — cegah salah order + saldo kepotong.
    operator_nama = entri.get("operator", "")
    if not operator_matches(args.nomor, operator_nama):
        out(False, pesan=(f"ORDER DIBATALKAN: paket '{nama_asli}' khusus {operator_nama}, "
                          f"tapi nomor {args.nomor} bukan {operator_nama}. "
                          f"Gunakan nomor {operator_nama} yang sesuai."))

    # 4. guard harga
    if args.harga_max and harga > args.harga_max:
        out(False, pesan=(f"ORDER DIBATALKAN: harga naik. Sekarang Rp {harga:,}. "
                          f"Batas Rp {args.harga_max:,}".replace(",", ".")))

    # 4. POST order (json_format=1, seperti jQuery.post #order_form)
    #    Field wajib isipulsa: csrf_token, nomor_hp, pembayaran=balance,
    #    produk, operator (ID internal!), voucher, id_plgn, json_format.
    operator_id = ""
    for i in items:
        if i["voucher"] == voucher:
            operator_id = i.get("operator_id", "")
            break
    form = {
        "csrf_token": csrf,
        "nomor_hp": args.nomor,
        "pembayaran": "balance",
        "produk": page,
        "operator": operator_id,
        "voucher": voucher,
        "id_plgn": "",
        "otp_gateway[]": "email",
        "json_format": "1",
    }
    r2 = s.post(f"{BASE}{page}", data=form, timeout=60,
                headers={"X-Requested-With": "XMLHttpRequest"})
    if "Just a moment" in r2.text:
        out(False, pesan="kena Cloudflare challenge saat order")
    try:
        data = r2.json()
    except Exception:
        out(False, pesan=f"respons tidak JSON: {r2.text[:150]}")
    if data.get("success"):
        oid = data.get("id")
        # PENTING: jangan ikut harga internal isipulsa (modal) di pesan —
        # pesan ini dikirim ke pembeli via bot, harga modal = rafel profit.
        # Harga JUAL determination ada di sisi server (katalog harga_jual).
        out(True, sukses=True, order_id=str(oid), harga=harga, paket=nama_asli,
            pesan=(f"{catatan}ORDER SUKSES. Paket: {nama_asli} | ID: {oid}"))
    errors = "; ".join(data.get("errors", ["tidak diketahui"]))
    out(False, pesan=f"{catatan}ORDER GAGAL. Alasan: {errors}")


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    default_jar = os.environ.get("AP_COOKIES") or "cookies.json"

    s = sub.add_parser("cekstatus")
    s.add_argument("--cookies", default=default_jar)
    s.set_defaults(fn=op_cekstatus)

    s = sub.add_parser("search")
    s.add_argument("--cookies", default=default_jar)
    s.add_argument("--tab", default="Paket Kuota")
    s.add_argument("--cari", default="")
    s.add_argument("--limit", type=int, default=20)
    s.set_defaults(fn=op_search)

    s = sub.add_parser("order")
    s.add_argument("--cookies", default=default_jar)
    s.add_argument("--nomor", required=True)
    s.add_argument("--produk", default="pulsa")
    s.add_argument("--voucher")
    s.add_argument("--nama-asli", dest="nama_asli", default="")  # nama asli di katalog (anti-drift)
    s.add_argument("--operator", default="")  # operator paket (guard nomor user)
    s.add_argument("--cari")
    s.add_argument("--harga-max", type=int, default=0)
    s.set_defaults(fn=op_order)

    args = ap.parse_args()
    try:
        args.fn(args)
    except SystemExit:
        raise
    except Exception as e:  # noqa: BLE001 — error jaringan harus jadi JSON
        # Jangan dump traceback: Go membacanya sebagai "helper crash" tanpa
        # info. JSON dengan key error -> monitor tahu ini GAGAL CEK, bukan
        # cookie invalid (lihat statusHasil di internal/engine/isip.go).
        out(False, error=f"{type(e).__name__}: {e}")


if __name__ == "__main__":
    main()
