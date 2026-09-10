#!/usr/bin/env python3
"""pw_helper.py — jembatan Playwright untuk agenpulsa-server (Go).

Port dari bot.py lama (project AgenPulsa): selector & flow IDENTIK.
Setiap operasi = buka chromium -> kerja -> tutup (tanpa chrome nyangkut).

Pakai:
  python pw_helper.py cekstatus --profile profile
  python pw_helper.py order    --profile profile --nomor 0812 --tab "Paket Kuota" --cari "5GB" --harga-max 20000 [--voucher V1]
  python pw_helper.py cookies  --profile profile --json-file c.json   (inject + cek)
  python pw_helper.py inject   --profile profile --json-file c.json   (inject saja)

Output: satu baris JSON di stdout: {"ok":bool,"login":bool,"saldo":str,"pesan":str,...}
"""
import argparse
import base64
import json
import os
import re
import sys
import time

BASE = "https://isipulsa.web.id/"

RE_NONDIGIT = re.compile(r"[^\d]")


def parse_harga(s):
    d = RE_NONDIGIT.sub("", s or "")
    return int(d) if d else 0


def load_cookies(path):
    """Baca export Cookie-Editor (JSON array) -> format Playwright add_cookies."""
    with open(path, encoding="utf-8") as f:
        raw = json.load(f)
    cookies = []
    for c in raw:
        if not isinstance(c, dict) or not c.get("name") or "value" not in c:
            continue
        entry = {
            "name": c["name"],
            "value": c["value"],
            "domain": c.get("domain") or ".isipulsa.web.id",
            "path": c.get("path") or "/",
            "httpOnly": bool(c.get("httpOnly", False)),
            "secure": bool(c.get("secure", False)),
        }
        exp = c.get("expirationDate")
        if exp:
            entry["expires"] = int(exp)
        ss = str(c.get("sameSite") or "").lower()
        entry["sameSite"] = {"strict": "Strict", "lax": "Lax", "no_restriction": "None"}.get(ss, "Lax")
        cookies.append(entry)
    return cookies


def out(ok=False, **kw):
    kw["ok"] = ok
    print(json.dumps(kw, ensure_ascii=False))
    sys.exit(0 if ok else 1)


def run(profile, fn, headed=True):
    """Buka chromium persistent context, jalankan fn(page), tutup selalu.
    headed=True default: Cloudflare menolak headless (Just a moment)."""
    from playwright.sync_api import sync_playwright

    with sync_playwright() as p:
        ctx = p.chromium.launch_persistent_context(
            profile,
            headless=not headed,
            viewport={"width": 1366, "height": 900},
        )
        page = ctx.pages[0] if ctx.pages else ctx.new_page()
        try:
            return fn(page)
        finally:
            ctx.close()


def op_cekstatus(args):
    def fn(page):
        page.goto(BASE, wait_until="domcontentloaded", timeout=60000)
        # tunggu Cloudflare challenge selesai (title bukan "Just a moment")
        for _ in range(20):
            page.wait_for_timeout(2000)
            t = (page.title() or "").lower()
            if "moment" not in t:
                break
        # header bisa lambat; ambil apa adanya, jangan hard-wait 30s
        try:
            header = page.locator("header").inner_text(timeout=8000)
        except Exception:
            header = page.title()  # fallback: halaman lambat/beda
        if "Masuk" in header and "Saldo" not in header:
            return {"login": False, "saldo": "-", "pesan": "sesi login habis"}
        if "Saldo" not in header:
            return {"login": False, "saldo": "-", "pesan": "halaman tidak utuh (timeout)"}
        saldo = header.replace("Saldo Deposit", "").strip() or "Rp 0"
        return {"login": True, "saldo": saldo}

    r = run(args.profile, fn)
    out(True, **r)


def op_inject(args):
    cookies = load_cookies(args.json_file)
    if not cookies:
        out(False, pesan="tidak ada cookie valid di file")

    def fn(page):
        ctx = page.context
        ctx.add_cookies(cookies)
        return len(cookies)

    n = run(args.profile, fn)
    out(True, jumlah=n, pesan=f"{n} cookie diinject ke profile")


def op_cookies(args):
    """Inject lalu langsung cek login — satu pembukaan browser saja."""
    cookies = load_cookies(args.json_file)
    if not cookies:
        out(False, pesan="tidak ada cookie valid di file")

    def fn(page):
        page.context.add_cookies(cookies)
        page.goto(BASE, wait_until="domcontentloaded", timeout=60000)
        page.wait_for_timeout(2000)
        header = page.locator("header").inner_text()
        if "Masuk" in header and "Saldo" not in header:
            return {"login": False, "saldo": "-", "jumlah": len(cookies),
                    "pesan": "cookie terpasang TAPI sesi ditolak isipulsa (sid terikat perangkat?)"}
        saldo = header.replace("Saldo Deposit", "").strip() or "Rp 0"
        return {"login": True, "saldo": saldo, "jumlah": len(cookies),
                "pesan": f"LOGIN BERHASIL, saldo {saldo}"}

    r = run(args.profile, fn)
    out(r.get("login") is True, **r)


def op_order(args):
    def fn(page):
        page.goto(BASE, wait_until="domcontentloaded", timeout=60000)
        if page.locator("#header-signin", has_text="Masuk").count() > 0:
            return {"login": False, "pesan": "GAGAL: sesi login habis. Inject cookies ulang."}
        page.locator(".form-tabs a", has_text=args.tab).first.click()
        page.fill('input[name="nomor_hp"]', args.nomor)
        page.locator('input[name="nomor_hp"]').blur()
        page.wait_for_selector("#nominal .row button", state="attached", timeout=30000)
        page.locator("#nominal").wait_for(state="visible", timeout=10000)

        target = None
        voucher_gagal = False
        if args.voucher:
            t = page.locator(f'#nominal .row button[data-voucher="{args.voucher}"]')
            if t.count() > 0:
                target = t
            else:
                voucher_gagal = True
        if target is None and args.cari:
            items = page.locator("#nominal .row button")
            for i in range(items.count()):
                el = items.nth(i)
                if args.cari.lower() in (el.inner_text() or "").lower():
                    target = el
                    break
        if target is None:
            if voucher_gagal:
                return {"pesan": f"GAGAL: voucher {args.voucher} tidak ada di isipulsa, dan fallback cari '{args.cari}' tidak ketemu. Katalog perlu diupdate."}
            return {"pesan": f"GAGAL: paket tidak ditemukan di tab {args.tab} (voucher={args.voucher}, cari={args.cari})"}

        target.click()
        page.select_option("#pilihpembayaran", "balance")
        nama_paket = " ".join((target.inner_text() or "").split())
        harga = page.inner_text("#harga h3")

        if args.harga_max:
            harga_now = parse_harga(harga)
            if harga_now > args.harga_max:
                return {"pesan": (f"ORDER DIBATALKAN: harga naik. Sekarang {harga}, batas tersimpan "
                                  f"Rp {args.harga_max:,}".replace(",", "."))}

        result = page.evaluate(
            """() => new Promise((resolve) => {
                var url = "https://isipulsa.web.id/" + jQuery('input[name="produk"]').val();
                jQuery.post(url, jQuery("#order_form").serialize(), function (data) {
                    resolve(JSON.stringify(data));
                }).fail(function (xhr) {
                    resolve(JSON.stringify({success: false, errors: ["HTTP " + xhr.status]}));
                });
            })"""
        )
        data = json.loads(result)
        if data.get("success"):
            oid = data.get("id")
            return {"sukses": True, "order_id": str(oid), "harga": harga, "paket": nama_paket,
                    "pesan": f"ORDER SUKSES. Paket: {nama_paket} | Harga: {harga} | ID: {oid}"}
        errors = "; ".join(data.get("errors", ["tidak diketahui"]))
        return {"pesan": f"ORDER GAGAL. Paket: {nama_paket} | Harga: {harga} | Alasan: {errors}"}

    r = run(args.profile, fn)
    out(r.get("sukses") is True, **r)


def op_search(args):
    def fn(page):
        page.goto(BASE, wait_until="domcontentloaded", timeout=60000)
        page.locator(".form-tabs a", has_text=args.tab).first.click()
        page.fill('input[name="nomor_hp"]', args.nomor or "081234567890")
        page.locator('input[name="nomor_hp"]').blur()
        page.wait_for_selector("#nominal .row button", state="attached", timeout=30000)
        return page.evaluate(
            """(kw) => Array.from(document.querySelectorAll('#nominal .row button'))
                .filter(b => b.innerText.toLowerCase().includes(kw.toLowerCase()))
                .map(b => ({
                    voucher: b.getAttribute('data-voucher'),
                    operator: b.getAttribute('data-operator'),
                    nama: b.getAttribute('data-nominal'),
                    harga: b.getAttribute('data-harga'),
                    teks: b.innerText.replace(/\\n/g, ' ').trim()
                }))""",
            args.cari or "",
        )

    items = run(args.profile, fn)
    out(True, items=items)


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)

    def common(sp, need_profile=True):
        if need_profile:
            sp.add_argument("--profile", default="profile")

    s = sub.add_parser("cekstatus")
    common(s)
    s.set_defaults(fn=op_cekstatus)

    s = sub.add_parser("inject")
    common(s)
    s.add_argument("--json-file", required=True)
    s.set_defaults(fn=op_inject)

    s = sub.add_parser("cookies")
    common(s)
    s.add_argument("--json-file", required=True)
    s.set_defaults(fn=op_cookies)

    s = sub.add_parser("order")
    common(s)
    s.add_argument("--nomor", required=True)
    s.add_argument("--tab", default="Paket Kuota")
    s.add_argument("--cari")
    s.add_argument("--voucher")
    s.add_argument("--harga-max", type=int, default=0)
    s.set_defaults(fn=op_order)

    s = sub.add_parser("search")
    common(s)
    s.add_argument("--tab", default="Paket Kuota")
    s.add_argument("--cari", default="")
    s.add_argument("--nomor", default="081234567890")
    s.set_defaults(fn=op_search)

    args = ap.parse_args()
    args.fn(args)


if __name__ == "__main__":
    main()
