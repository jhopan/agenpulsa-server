// Package engine: management cookies isipulsa (inject via export Cookie-Editor).
//
// Login interaktif (window/VNC/noVNC) DIHAPUS — satu-satunya cara login
// sekarang: inject cookies dari browser asli. Chromium profile dipakai
// headless saja untuk operasi HTTP + inject cookie.
package engine

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/jhopan/agenpulsa-server/internal/db"
)

// ImportCookies inject cookies (format export Cookie-Editor: JSON array) ke
// profile — buka chrome headless sebentar via CDP, tutup lagi. Cookie nempel
// permanen di user-data-dir.
func (e *Engine) ImportCookies(raw string) (int, error) {
	var list []map[string]any
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return 0, fmt.Errorf("JSON tidak valid: %w", err)
	}
	cookies := []*proto.NetworkCookieParam{}
	for _, c := range list {
		name, _ := c["name"].(string)
		val, _ := c["value"].(string)
		if name == "" || val == "" {
			continue
		}
		domain, _ := c["domain"].(string)
		if domain == "" {
			domain = ".isipulsa.web.id"
		}
		path, _ := c["path"].(string)
		if path == "" {
			path = "/"
		}
		ck := &proto.NetworkCookieParam{
			Name: name, Value: val, Domain: domain, Path: path,
			HTTPOnly: boolAny(c["httpOnly"]), Secure: boolAny(c["secure"]),
		}
		// PENTING: Cookie-Editor export pakai key "expirationDate", TAPI
		// CDP NetworkCookieParam BUTUH "expires" (unix detik). Kalau key-nya
		// cuma "expirationDate", ck.Expires = 0 -> cookie jadi SESSION
		// cookie -> Chromium langsung HAPUS saat profile dibuka lagi ->
		// "tidak ada cookie di profile" padahal inject reported sukses.
		//
		// Terima kedua nama key + fallback ke field "expires".
		exp := 0.0
		for _, key := range []string{"expirationDate", "expires", "expiry"} {
			if v, ok := c[key].(float64); ok && v > 0 {
				exp = v
				break
			}
		}
		if exp > 0 {
			// Pasti ke depan: cookie yang upcoming, jangan kelewat.
			if exp < float64(time.Now().Unix()) {
				exp = float64(time.Now().Add(30 * 24 * time.Hour).Unix())
			}
			ck.Expires = proto.TimeSinceEpoch(exp)
		} else {
			// Tanpa expirationDate -> session cookie (EPHEMERAL, hilang saat
			// chromium restart). Set expire 30 hari biar awet.
			ck.Expires = proto.TimeSinceEpoch(time.Now().Add(30 * 24 * time.Hour).Unix())
		}
		switch strings.ToLower(fmt.Sprint(c["sameSite"])) {
		case "strict":
			ck.SameSite = proto.NetworkCookieSameSiteStrict
		case "no_restriction", "none":
			ck.SameSite = proto.NetworkCookieSameSiteNone
		default:
			ck.SameSite = proto.NetworkCookieSameSiteLax
		}
		cookies = append(cookies, ck)
	}
	if len(cookies) == 0 {
		return 0, fmt.Errorf("tidak ada cookie valid (butuh name+value)")
	}

	// Tutup browser headless yang sedang jalan supaya profile tidak terkunci
	// dua proses.
	e.ResetLoginForce()

	url, err := launcher.New().
		UserDataDir(e.userData).
		Headless(true).
		Leakless(false).
		Set("no-sandbox").
		Launch()
	if err != nil {
		if bin := findChromium(); bin != "" {
			url, err = launcher.New().
				Bin(bin).
				UserDataDir(e.userData).
				Headless(true).
				Leakless(false).
				Set("no-sandbox").
				Launch()
		}
		if err != nil {
			return 0, fmt.Errorf("launch chromium: %w", err)
		}
	}
	b := rod.New().ControlURL(url)
	if err := b.Connect(); err != nil {
		return 0, err
	}
	defer b.Close()
	page, err := b.Page(proto.TargetCreateTarget{URL: baseURL})
	if err != nil {
		return 0, err
	}
	_ = page.WaitLoad()
	if err := b.SetCookies(cookies); err != nil {
		return 0, err
	}
	// VERIFIKASI: cookie harus benar-benar nempel di profile. Kalau expired/
	// session cookie, Chromium HAPUS pas profile dibuka lagi -> inject
	// "sukses" tapi cookie 0. Cek beneran sebelum lapor sukses.
	tersisa := e.hitungCookieProfile()
	log.Printf("[LOGIN] %d cookie diinject ke profile (tersisa di profile: %d)", len(cookies), tersisa)
	if tersisa == 0 {
		_ = e.store.SetSetting("cookies_updated_at", "")
		return len(cookies), fmt.Errorf("cookie terinject %d tapi TIDAK nempel di profile — kemungkinan cookie-nya expired/session. Export ulang dari browser yang SEDANG login (bukan dari cache lama)", len(cookies))
	}
	// catat waktu inject — dasar reminder "inject cookies baru tiap 3 hari".
	_ = e.store.SetSetting("cookies_updated_at", db.NowWIB().Format("2006-01-02 15:04:05"))
	return len(cookies), nil
}

// hitungCookieProfile: jumlah cookie isipulsa yang benar-benar ada di
// profile Chromium (bukan session/expired yg udah dibersih Chromium).
//
// DB cookie Chromium = SQLite. Copy dulu ke file temp biar gak lock /
// ganggu Chromium yg mungkin lagi jalan.
func (e *Engine) hitungCookieProfile() int {
	for _, p := range []string{
		filepath.Join(e.userData, "Default", "Cookies"),
		filepath.Join(e.userData, "Default", "Network", "Cookies"),
	} {
		n := e.countCookieRows(p)
		if n > 0 {
			return n
		}
	}
	return 0
}

// CookieExpiry: tanggal kedaluwarsa cookie isipulsa paling awal (unix detik)
// + jumlah cookie isipulsa yang ada di profile.
//
// Dipakai buat notif "cookie mau expired" (7 hari sebelum) — jauh lebih
// akurat daripadaumur inject, karena cookie asli punya masa berlaku
// sendiri (biasanya 30 hari, atau session cookie = 0).
func (e *Engine) CookieExpiry() (expUnix int64, jumlah int) {
	for _, p := range []string{
		filepath.Join(e.userData, "Default", "Cookies"),
		filepath.Join(e.userData, "Default", "Network", "Cookies"),
	} {
		exp, n := e.readCookieExpiry(p)
		if n > 0 {
			return exp, n
		}
	}
	return 0, 0
}

// readCookieExpiry: baca host_key + expires_utc cookie isipulsa dari DB Chromium.
// expires_utc = microseconds since epoch (format Chrome).
func (e *Engine) readCookieExpiry(path string) (int64, int) {
	if _, err := os.Stat(path); err != nil {
		return 0, 0
	}
	tmp, err := os.CreateTemp("", "ckx-*.db")
	if err != nil {
		return 0, 0
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpName) }()

	src, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer src.Close()
	dst, err := os.OpenFile(tmpName, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, 0
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return 0, 0
	}
	_ = dst.Close()

	d, err := sql.Open("sqlite", tmpName)
	if err != nil {
		return 0, 0
	}
	defer d.Close()
	rows, err := d.Query(`SELECT expires_utc FROM cookies WHERE host_key LIKE '%isipulsa.web.id'`)
	if err != nil {
		return 0, 0
	}
	defer rows.Close()
	var (
		jumlah int
		expMin int64
	)
	for rows.Next() {
		var e int64
		if err := rows.Scan(&e); err != nil {
			continue
		}
		jumlah++
		if e <= 0 {
			// session cookie (expired 0) -> paling cepat, tapi berarti tak ada
			// tanggal expired. Jangan dipakai sebagai "expired" — skip.
			continue
		}
		// Chrome: microseconds since epoch
		sec := e / 1_000_000
		if expMin == 0 || sec < expMin {
			expMin = sec
		}
	}
	return expMin, jumlah
}

// countCookieRows: hitung baris cookie host isipulsa.web.id di DB Chromium.
func (e *Engine) countCookieRows(path string) int {
	if _, err := os.Stat(path); err != nil {
		return 0
	}
	// copy ke temp: DB mungkin sedang dipakai Chromium (lock).
	tmp, err := os.CreateTemp("", "ck-*.db")
	if err != nil {
		return 0
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpName) }()

	src, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer src.Close()
	dst, err := os.OpenFile(tmpName, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return 0
	}
	_ = dst.Close()

	d, err := sql.Open("sqlite", tmpName)
	if err != nil {
		return 0
	}
	defer d.Close()
	var n int
	err = d.QueryRow(`SELECT COUNT(*) FROM cookies WHERE host_key LIKE '%isipulsa.web.id'`).Scan(&n)
	if err != nil {
		return 0
	}
	return n
}

func boolAny(v any) bool {
	b, _ := v.(bool)
	return b
}

// ResetLoginForce: tutup browser headless (dipakai handler hapus akun /
// sebelum inject cookie baru). Tidak ada lagi sesi login interaktif.
func (e *Engine) ResetLoginForce() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.browser != nil {
		_ = e.browser.Close()
		e.browser = nil
	}
}
