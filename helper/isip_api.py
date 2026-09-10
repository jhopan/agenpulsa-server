#!/usr/bin/env python3
"""isip_api.py — klien isipulsa.web.id via HTTP murni (requests).

TANPA Chromium/Turnstile: semua pakai cookies sesi + CSRF + UA yang sama.
Cookies diambil dari profile Chromium (dari inject Cookie-Editor).

Pakai:
  python isip_api.py cekstatus --profile profile
  python isip_api.py search    --profile profile --tab "Paket Kuota" --cari "5GB"
  python isip_api.py order     --profile profile --nomor 0812... --voucher 701 [--harga-max 60000]
  python isip_api.py inject    --profile profile --json-file c.json

Output: satu baris JSON {"ok":..,"login":..,"saldo":..,"items":..,"pesan":..}
"""
import argparse
import ast
import glob
import json
import os
import re
import shutil
import sqlite3
import sys
import time
import tempfile

import requests

BASE = "https://isipulsa.web.id/"
UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"

RE_NONDIGIT = re.compile(r"[^\d]")


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


def chromium_cookies(profile):
    """Baca cookies isipulsa dari profil Chromium (decrypt via playwright context)."""
    from playwright.sync_api import sync_playwright

    with sync_playwright() as p:
        ctx = p.chromium.launch_persistent_context(profile, headless=True)
        page = ctx.pages[0] if ctx.pages else ctx.new_page()
        cookies = ctx.cookies(BASE)
        ctx.close()
    return cookies


def load_cookie_file(path):
    """Export Cookie-Editor -> list dict Playwright."""
    with open(path, encoding="utf-8") as f:
        raw = json.load(f)
    out = []
    for c in raw:
        if not isinstance(c, dict) or not c.get("name") or "value" not in c:
            continue
        out.append(c)
    return out


def new_session(cookies):
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
    # kolom: [id, produk, ?, operator, nama, harga, ?, desc]
    items = []
    for d in data:
        try:
            items.append({
                "voucher": str(d[0]),
                "produk": str(d[1]),
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
    cookies = chromium_cookies(args.profile)
    if not cookies:
        out(False, login=False, saldo="-", pesan="tidak ada cookie di profile")
    s = new_session(cookies)
    r = get_retry(s, BASE)
    if "Just a moment" in r.text:
        out(False, login=False, saldo="-", pesan="kena Cloudflare challenge")
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
    cookies = chromium_cookies(args.profile)
    s = new_session(cookies)
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
    cookies = chromium_cookies(args.profile)
    s = new_session(cookies)

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

    # 3. guard harga
    if args.harga_max and harga > args.harga_max:
        out(False, pesan=(f"ORDER DIBATALKAN: harga naik. Sekarang Rp {harga:,}. "
                          f"Batas Rp {args.harga_max:,}".replace(",", ".")))

    # 4. POST order (json_format=1, seperti jQuery.post #order_form)
    form = {
        "csrf_token": csrf,
        "produk": page,
        "voucher": voucher,
        "nomor_hp": args.nomor,
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
        out(True, sukses=True, order_id=str(oid), harga=harga, paket=nama_asli,
            pesan=(f"{catatan}ORDER SUKSES. Paket: {nama_asli} | Harga: Rp {harga:,} | ID: {oid}"
                   .replace(",", ".")))
    errors = "; ".join(data.get("errors", ["tidak diketahui"]))
    out(False, pesan=f"{catatan}ORDER GAGAL. Alasan: {errors}")


def op_inject(args):
    """Inject cookie file -> profile chromium (dipakai semua operasi HTTP)."""
    cookies = load_cookie_file(args.json_file)
    if not cookies:
        out(False, pesan="tidak ada cookie valid di file")
    from playwright.sync_api import sync_playwright

    with sync_playwright() as p:
        ctx = p.chromium.launch_persistent_context(args.profile, headless=True)
        page = ctx.pages[0] if ctx.pages else ctx.new_page()
        pw_cookies = []
        for c in cookies:
            e = {
                "name": c["name"], "value": c["value"],
                "domain": c.get("domain") or ".isipulsa.web.id",
                "path": c.get("path") or "/",
                "httpOnly": bool(c.get("httpOnly", False)),
                "secure": bool(c.get("secure", False)),
            }
            exp = c.get("expirationDate")
            if exp:
                e["expires"] = int(exp)
            ss = str(c.get("sameSite") or "").lower()
            e["sameSite"] = {"strict": "Strict", "lax": "Lax", "no_restriction": "None"}.get(ss, "Lax")
            pw_cookies.append(e)
        ctx.add_cookies(pw_cookies)
        ctx.close()
    out(True, jumlah=len(pw_cookies), pesan=f"{len(pw_cookies)} cookie diinject ke profile")


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)

    s = sub.add_parser("cekstatus")
    s.add_argument("--profile", default="profile")
    s.set_defaults(fn=op_cekstatus)

    s = sub.add_parser("search")
    s.add_argument("--profile", default="profile")
    s.add_argument("--tab", default="Paket Kuota")
    s.add_argument("--cari", default="")
    s.add_argument("--limit", type=int, default=20)
    s.set_defaults(fn=op_search)

    s = sub.add_parser("order")
    s.add_argument("--profile", default="profile")
    s.add_argument("--nomor", required=True)
    s.add_argument("--produk", default="pulsa")
    s.add_argument("--voucher")
    s.add_argument("--nama-asli", dest="nama_asli", default="")  # nama asli di katalog (anti-drift)
    s.add_argument("--cari")
    s.add_argument("--harga-max", type=int, default=0)
    s.set_defaults(fn=op_order)

    s = sub.add_parser("inject")
    s.add_argument("--profile", default="profile")
    s.add_argument("--json-file", required=True)
    s.set_defaults(fn=op_inject)

    args = ap.parse_args()
    args.fn(args)


if __name__ == "__main__":
    main()
