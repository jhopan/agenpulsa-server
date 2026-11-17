package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestCookieExpiryUnix: expiry dari export Cookie-Editor.
//   - expirationDate / expires / expiry diterima
//   - session cookie (tanpa tanggal) & tanggal lampau -> 30 hari
func TestCookieExpiryUnix(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	fallback := now.Add(cookieSessionExpiry).Unix()

	exp, diperpanjang := cookieExpiryUnix(map[string]any{"expirationDate": float64(now.Unix() + 86400)}, now)
	if exp != now.Unix()+86400 || diperpanjang {
		t.Errorf("expirationDate valid: exp=%d diperpanjang=%v", exp, diperpanjang)
	}
	for _, key := range []string{"expires", "expiry"} {
		exp, diperpanjang := cookieExpiryUnix(map[string]any{key: float64(now.Unix() + 3600)}, now)
		if exp != now.Unix()+3600 || diperpanjang {
			t.Errorf("key %q: exp=%d diperpanjang=%v", key, exp, diperpanjang)
		}
	}

	// Session cookie: Cookie-Editor menulis session=true tanpa expirationDate.
	for _, c := range []map[string]any{
		{"name": "sid", "session": true},
		{"name": "sid", "expirationDate": nil},
		{"name": "sid", "expirationDate": float64(now.Unix() - 60)},
	} {
		exp, diperpanjang := cookieExpiryUnix(c, now)
		if exp != fallback || !diperpanjang {
			t.Errorf("cookie %v harus diberi 30 hari: exp=%d diperpanjang=%v", c, exp, diperpanjang)
		}
	}
}

// TestNormalizeCookie: name+value wajib; domain/path default; sameSite
// dipetakan ke bentuk yang dibaca helper; cookie tanpa tanggal ditandai.
func TestNormalizeCookie(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	if ck, _ := normalizeCookie(map[string]any{"name": "x"}, now); ck.Value == "" {
		// nilai kosong tetap dianggap cookie (isi kosong) — validasi name/value
		// dilakukan di ImportCookies, bukan di sini.
		t.Log("cookie tanpa value tetap dinormalisasi (divalidasi ImportCookies)")
	}

	ck, diperpanjang := normalizeCookie(map[string]any{
		"name": "sid", "value": "abc", "httpOnly": true,
		"expirationDate": float64(now.Add(48 * time.Hour).Unix()),
	}, now)
	if ck.Domain != ".isipulsa.web.id" || ck.Path != "/" {
		t.Errorf("default domain/path salah: %q %q", ck.Domain, ck.Path)
	}
	if !ck.HTTPOnly || ck.SameSite != "" {
		t.Errorf("httpOnly/sameSite salah: %v %q", ck.HTTPOnly, ck.SameSite)
	}
	if diperpanjang || ck.Session {
		t.Error("expiry valid tidak boleh ditandai session/diperpanjang")
	}
	if int64(ck.ExpirationDate) != now.Unix()+48*3600 {
		t.Errorf("expiry tidak tersimpan: %v", ck.ExpirationDate)
	}

	session, diperpanjang := normalizeCookie(map[string]any{"name": "x", "value": "y"}, now)
	if !diperpanjang || !session.Session {
		t.Error("cookie tanpa tanggal harus ditandai session")
	}

	for in, want := range map[string]string{
		"Strict":         "strict",
		"Lax":            "lax",
		"no_restriction": "no_restriction",
		"none":           "no_restriction",
		"":               "",
	} {
		ck, _ := normalizeCookie(map[string]any{"name": "a", "value": "b", "sameSite": in}, now)
		if ck.SameSite != want {
			t.Errorf("sameSite %q -> %q, mau %q", in, ck.SameSite, want)
		}
	}
}

// TestCookieJarRoundTrip: jar ditulis 0600 dan terbaca balik.
func TestCookieJarRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "cookies.json")
	jar := []jarCookie{
		{Name: "user_key", Value: "abc", Domain: "isipulsa.web.id", Path: "/", ExpirationDate: 1_800_000_000},
		{Name: "sid", Value: "xyz", Domain: "isipulsa.web.id", Path: "/", ExpirationDate: 1_700_000_000, Session: true},
	}
	if err := writeCookieJar(path, jar); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows tidak menegakkan permission POSIX; Linux (production) harus 0600.
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("permission jar = %v, mau 0600", fi.Mode().Perm())
	}
	got, err := readCookieJar(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "user_key" || got[1].Value != "xyz" || !got[1].Session {
		t.Fatalf("isi jar tidak sesuai: %+v", got)
	}

	// Jar belum ada -> kosong, bukan error.
	if got, err := readCookieJar(filepath.Join(dir, "nope.json")); err != nil || got != nil {
		t.Errorf("jar belum ada harus (nil,nil), dapat (%v,%v)", got, err)
	}
}

// TestCookieExpiryJar: expiry paling awal dari cookie identitas; cookie
// sliding (sid/csrf_cookie) diabaikan supaya monitor tidak lapor "expired"
// terus (isipulsa memutar keduanya tiap request). Cookie session memakai
// expiry sintetis 30 hari dan tetap dihitung.
func TestCookieExpiryJar(t *testing.T) {
	e := &Engine{cookieFile: filepath.Join(t.TempDir(), "cookies.json")}
	if exp, n := e.CookieExpiry(); exp != 0 || n != 0 {
		t.Fatalf("jar kosong harus 0,0: dapat %d,%d", exp, n)
	}

	const (
		sliding   = int64(1_700_000_000) // lebih awal, tapi harus diabaikan
		identitas = int64(1_790_000_000)
		sintetis  = int64(1_795_000_000)
	)
	jar := []jarCookie{
		{Name: "sid", Value: "a", ExpirationDate: float64(sliding)},
		{Name: "csrf_cookie", Value: "b", ExpirationDate: float64(sliding)},
		{Name: "user_key", Value: "c", ExpirationDate: float64(identitas)},
		{Name: "session_only", Value: "d", Session: true, ExpirationDate: float64(sintetis)},
	}
	if err := writeCookieJar(e.cookieFile, jar); err != nil {
		t.Fatal(err)
	}
	if exp, n := e.CookieExpiry(); exp != identitas || n != 4 {
		t.Errorf("CookieExpiry = %d,%d mau %d,4", exp, n, identitas)
	}

	// Kalau isinya hanya cookie sliding -> fallback ke sliding.
	if err := writeCookieJar(e.cookieFile, jar[:2]); err != nil {
		t.Fatal(err)
	}
	if exp, n := e.CookieExpiry(); exp != sliding || n != 2 {
		t.Errorf("fallback sliding = %d,%d mau %d,2", exp, n, sliding)
	}

	// Cookie session saja -> pakai expiry sintetisnya.
	if err := writeCookieJar(e.cookieFile, jar[3:]); err != nil {
		t.Fatal(err)
	}
	if exp, n := e.CookieExpiry(); exp != sintetis || n != 1 {
		t.Errorf("session-only = %d,%d mau %d,1", exp, n, sintetis)
	}

	// Tanpa tanggal sama sekali -> 0.
	if err := writeCookieJar(e.cookieFile, []jarCookie{{Name: "x", Value: "y", Session: true}}); err != nil {
		t.Fatal(err)
	}
	if exp, n := e.CookieExpiry(); exp != 0 || n != 1 {
		t.Errorf("tanpa tanggal = %d,%d mau 0,1", exp, n)
	}
}
