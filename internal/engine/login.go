// Package engine: management cookie isipulsa — jar JSON (export Cookie-Editor).
//
// Login isipulsa = inject cookies. TIDAK ada Chromium/CDP/Playwright: hasil
// export disimpan sebagai file JSON (0600) dan helper/isip_api.py memakainya
// langsung untuk request HTTP.
package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jhopan/agenpulsa-server/internal/db"
)

// jarCookie: satu cookie di jar (format yang sama dengan export Cookie-Editor,
// jadi file ini bisa di-copy balik ke ekstensi kalau perlu).
type jarCookie struct {
	Name           string  `json:"name"`
	Value          string  `json:"value"`
	Domain         string  `json:"domain,omitempty"`
	Path           string  `json:"path,omitempty"`
	HTTPOnly       bool    `json:"httpOnly,omitempty"`
	Secure         bool    `json:"secure,omitempty"`
	SameSite       string  `json:"sameSite,omitempty"`
	ExpirationDate float64 `json:"expirationDate,omitempty"`
	// Session: cookie tidak punya tanggal dari isipulsa; ExpirationDate di atas
	// adalah masa berlaku sintetis (30 hari) supaya tidak hilang saat dihitung.
	Session bool `json:"session,omitempty"`
}

// CookieImportResult: hasil inject cookies ke jar.
type CookieImportResult struct {
	Jumlah int    `json:"jumlah"`
	File   string `json:"file"`
	// SessionCookie: cookie tanpa tanggal expiry (atau tanggalnya sudah lewat)
	// — diberi masa berlaku 30 hari.
	SessionCookie int      `json:"session_cookie"`
	Ditolak       []string `json:"ditolak,omitempty"` // entri tanpa name/value
	Login         bool     `json:"login"`
	Saldo         string   `json:"saldo"`
	Pesan         string   `json:"pesan"`
}

// cookieSessionExpiry: masa berlaku pengganti untuk cookie tanpa tanggal.
const cookieSessionExpiry = 30 * 24 * time.Hour

// cookieSliding: cookie yang diputar/diperpanjang isipulsa tiap request
// (±2 jam). Tanggalnya BUKAN tanda sesi mau mati, jadi dikeluarkan dari
// perhitungan "cookie mau expired" — kalau ikut dihitung, monitor akan
// lapor "cookie sudah expired" terus padahal login normal.
var cookieSliding = map[string]bool{"sid": true, "csrf_cookie": true}

// cookieExpiryUnix: expiry (unix detik) satu cookie export Cookie-Editor.
// diperpanjang=true kalau cookie tidak punya tanggal (session cookie) atau
// tanggalnya sudah lewat — dua-duanya diberi 30 hari.
func cookieExpiryUnix(c map[string]any, now time.Time) (exp int64, diperpanjang bool) {
	raw := 0.0
	for _, key := range []string{"expirationDate", "expires", "expiry"} {
		if v, ok := c[key].(float64); ok && v > 0 {
			raw = v
			break
		}
	}
	if raw <= 0 || int64(raw) <= now.Unix() {
		return now.Add(cookieSessionExpiry).Unix(), true
	}
	return int64(raw), false
}

// normalizeCookie: satu entri export Cookie-Editor -> jarCookie. name/value
// kosong = bukan cookie valid (caller mencatatnya sebagai ditolak).
func normalizeCookie(c map[string]any, now time.Time) (jarCookie, bool) {
	ck := jarCookie{}
	ck.Name, _ = c["name"].(string)
	ck.Value, _ = c["value"].(string)
	ck.Domain, _ = c["domain"].(string)
	if ck.Domain == "" {
		ck.Domain = ".isipulsa.web.id"
	}
	ck.Path, _ = c["path"].(string)
	if ck.Path == "" {
		ck.Path = "/"
	}
	ck.HTTPOnly = boolAny(c["httpOnly"])
	ck.Secure = boolAny(c["secure"])
	if ss, ok := c["sameSite"].(string); ok {
		switch strings.ToLower(ss) {
		case "strict":
			ck.SameSite = "strict"
		case "lax":
			ck.SameSite = "lax"
		case "no_restriction", "none":
			ck.SameSite = "no_restriction"
		}
	}
	exp, diperpanjang := cookieExpiryUnix(c, now)
	ck.ExpirationDate = float64(exp)
	ck.Session = diperpanjang
	return ck, diperpanjang
}

// readCookieJar: baca jar dari disk. File belum ada = jar kosong (bukan error).
func readCookieJar(path string) ([]jarCookie, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var jar []jarCookie
	if err := json.Unmarshal(raw, &jar); err != nil {
		return nil, fmt.Errorf("jar cookie rusak (%s): %w", path, err)
	}
	return jar, nil
}

// writeCookieJar: tulis jar atomik + permission 0600 (isinya kredensial sesi).
func writeCookieJar(path string, jar []jarCookie) error {
	raw, err := json.MarshalIndent(jar, "", " ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".cookies-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// ImportCookies: simpan cookies (format export Cookie-Editor: JSON array) ke
// jar lalu VERIFIKASI sesi ke isipulsa. Tanpa browser: tidak ada CDP, tidak
// ada file DB Chromium yang perlu di-flush.
func (e *Engine) ImportCookies(raw string) (CookieImportResult, error) {
	res := CookieImportResult{Saldo: "-", File: e.cookieFile}
	var list []map[string]any
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return res, fmt.Errorf("JSON tidak valid: %w", err)
	}
	now := time.Now()
	jar := make([]jarCookie, 0, len(list))
	for i, c := range list {
		ck, diperpanjang := normalizeCookie(c, now)
		if ck.Name == "" || ck.Value == "" {
			nama, _ := c["name"].(string)
			if nama == "" {
				nama = fmt.Sprintf("entry #%d", i+1)
			}
			res.Ditolak = append(res.Ditolak, nama)
			continue
		}
		if diperpanjang {
			res.SessionCookie++
		}
		jar = append(jar, ck)
	}
	if len(jar) == 0 {
		return res, fmt.Errorf("tidak ada cookie valid (butuh name+value)")
	}
	if err := writeCookieJar(e.cookieFile, jar); err != nil {
		return res, fmt.Errorf("gagal tulis jar cookie: %w", err)
	}
	res.Jumlah = len(jar)

	// Verifikasi: apakah isipulsa benar-benar menerima sesi ini. Tanpa ini,
	// admin dapat "inject sukses" lalu bingung kenapa status akun "HABIS".
	login, saldo, pesan, verr := e.IsipCekStatusDetail()
	res.Login = login
	if saldo != "" && saldo != "Error" {
		res.Saldo = saldo
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d cookie disimpan ke %s", res.Jumlah, e.cookieFile)
	if res.SessionCookie > 0 {
		fmt.Fprintf(&sb, " (%d cookie tanpa tanggal/sudah lewat diberi masa berlaku 30 hari)", res.SessionCookie)
	}
	switch {
	case verr != nil:
		sb.WriteString(". Status login belum bisa dicek: " + verr.Error())
	case res.Login:
		sb.WriteString(", login isipulsa AKTIF")
		if res.Saldo != "-" {
			sb.WriteString(" (saldo " + res.Saldo + ")")
		}
	default:
		sb.WriteString(". TAPI isipulsa MENOLAK sesi ini")
		if pesan != "" {
			sb.WriteString(": " + pesan)
		}
		sb.WriteString(" — export ulang cookies dari browser yang SEDANG login (bukan cache lama)")
	}
	if len(res.Ditolak) > 0 {
		sb.WriteString(". Entri ditolak (tanpa name/value): " + strings.Join(res.Ditolak, ", "))
	}
	res.Pesan = sb.String()

	// catat waktu inject — dasar reminder "inject cookies baru tiap 3 hari".
	_ = e.store.SetSetting("cookies_updated_at", db.NowWIB().Format("2006-01-02 15:04:05"))
	return res, nil
}

// CookieExpiry: tanggal kedaluwarsa cookie isipulsa paling awal (unix detik)
// + jumlah cookie di jar. Cookie sliding (sid/csrf_cookie) diabaikan kecuali
// tidak ada cookie lain. expUnix=0 berarti tidak ada tanggal (session) /
// jar kosong.
func (e *Engine) CookieExpiry() (expUnix int64, jumlah int) {
	jar, err := readCookieJar(e.cookieFile)
	if err != nil {
		return 0, 0
	}
	jumlah = len(jar)

	// Prioritas: cookie identitas (user_key/user_id/save_browser/...).
	expUnix = minExpiry(jar, false)
	if expUnix == 0 {
		expUnix = minExpiry(jar, true)
	}
	return expUnix, jumlah
}

// minExpiry: expiry paling awal; includeSliding=false melewati cookie sliding.
// Cookie session (tanpa tanggal asli) memakai expiry sintetis 30 hari, jadi
// tetap ikut dihitung.
func minExpiry(jar []jarCookie, includeSliding bool) int64 {
	var min int64
	for _, c := range jar {
		if c.ExpirationDate <= 0 {
			continue
		}
		if !includeSliding && cookieSliding[c.Name] {
			continue
		}
		sec := int64(c.ExpirationDate)
		if min == 0 || sec < min {
			min = sec
		}
	}
	return min
}

func boolAny(v any) bool {
	b, _ := v.(bool)
	return b
}
