#!/usr/bin/env python3
"""migrate_profile_cookies.py — profile Chromium lama -> jar cookie baru.

Sekali pakai saat upgrade ke rilis tanpa-Chromium (jar cookie). Membaca
cookie isipulsa dari user-data-dir lama via Playwright (masih terpasang di
server ini), lalu menulis jar JSON yang dibaca helper/isip_api.py.

Pakai: python3 migrate_profile_cookies.py <profile-dir> <jar-out>
"""
import json
import os
import sys
import time

from playwright.sync_api import sync_playwright

BASE = "https://isipulsa.web.id/"
SAME_SITE = {"Strict": "strict", "Lax": "lax", "None": "no_restriction"}


def main():
    if len(sys.argv) != 3:
        sys.exit("usage: migrate_profile_cookies.py <profile-dir> <jar-out>")
    profile, out = sys.argv[1], sys.argv[2]

    with sync_playwright() as p:
        ctx = p.chromium.launch_persistent_context(profile, headless=True)
        try:
            cookies = ctx.cookies([BASE])
        finally:
            ctx.close()

    now = int(time.time())
    jar = []
    for c in cookies:
        exp = int(c.get("expires") or -1)
        session = exp <= 0 or exp <= now
        jar.append({
            "name": c["name"],
            "value": c["value"],
            "domain": c.get("domain") or ".isipulsa.web.id",
            "path": c.get("path") or "/",
            "httpOnly": bool(c.get("httpOnly")),
            "secure": bool(c.get("secure")),
            "sameSite": SAME_SITE.get(c.get("sameSite") or "", ""),
            # cookie session -> masa berlaku sintetis 30 hari
            "expirationDate": float(now + 30 * 24 * 3600 if session else exp),
            "session": session,
        })

    folder = os.path.dirname(os.path.abspath(out))
    os.makedirs(folder, exist_ok=True)
    tmp = out + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(jar, f, indent=1)
        f.write("\n")
    os.chmod(tmp, 0o600)
    os.replace(tmp, out)
    print(json.dumps({"ok": True, "jumlah": len(jar), "ajar": out}))


if __name__ == "__main__":
    main()