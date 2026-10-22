// Package engine: management cookies isipulsa (inject via export Cookie-Editor).
//
// Login interaktif (window/VNC/noVNC) DIHAPUS — satu-satunya cara login
// sekarang: inject cookies dari browser asli. Chromium profile dipakai
// headless saja untuk operasi HTTP + inject cookie.
package engine

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

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
		if exp, ok := c["expirationDate"].(float64); ok && exp > 0 {
			ck.Expires = proto.TimeSinceEpoch(exp)
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
	log.Printf("[LOGIN] %d cookie diinject ke profile", len(cookies))
	// catat waktu inject — dasar reminder "inject cookies baru tiap 3 hari".
	_ = e.store.SetSetting("cookies_updated_at", db.NowWIB().Format("2006-01-02 15:04:05"))
	return len(cookies), nil
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
