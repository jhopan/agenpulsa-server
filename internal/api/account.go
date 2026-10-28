// Package api: akun isipulsa (status/login fresh/ganti akun) + pengaturan.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jhopan/agenpulsa-server/internal/db"
)

type akunStatus struct {
	Login    bool   `json:"login"`
	Saldo    string `json:"saldo"`
	Nama     string `json:"nama"`
	Masalah  string `json:"masalah,omitempty"`
	Profile  string `json:"profile"`
	Username string `json:"username"`
	WinOpen  bool   `json:"window_login_open"`
	// umur cookies sejak inject terakhir + flag reminder (>= 3 hari).
	CookieUmurJam  float64 `json:"cookie_umur_jam"`
	CookieReminder bool    `json:"cookie_reminder"`
}

const cookieReminderJam = 72.0 // 3 hari

// cookieUmurJam hitung umur cookies dari settings.cookies_updated_at.
// Return -1 kalau belum pernah inject / sudah direset.
func (a *API) cookieUmurJam() float64 {
	raw := a.store.GetSetting("cookies_updated_at", "")
	if raw == "" {
		return -1
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", raw, db.WIBLoc())
	if err != nil {
		return -1
	}
	return db.NowWIB().Sub(t).Hours()
}

// account: status login isipulsa + info akun.
func (a *API) account(w http.ResponseWriter, r *http.Request) {
	umur := a.cookieUmurJam()
	st := akunStatus{
		Saldo:          "-",
		Profile:        a.eng.ProfileDir(),
		Username:       a.store.GetSetting("isipulsa_username", "-"),
		CookieUmurJam:  umur,
		CookieReminder: umur >= cookieReminderJam,
	}
	ok, saldo := a.eng.CekStatus()
	st.Login = ok
	if saldo != "" && saldo != "Error" && saldo != "-" {
		st.Saldo = saldo
	}
	if !ok {
		st.Masalah = "Sesi login habis atau chromium gagal dibuka. Cek log server; pakai 'Login Manual' untuk login ulang."
	}
	writeJSON(w, 200, st)
}

// gantiAkun: hapus folder profile (cookie isipulsa) -> login fresh akun baru.
func (a *API) gantiAkun(w http.ResponseWriter, r *http.Request) {
	dir := a.eng.ProfileDir()
	a.eng.ResetLoginForce()
	if dir != "" && dir != "." && dir != "/" {
		if err := os.RemoveAll(dir); err != nil {
			jsonErr(w, 500, "gagal hapus profile: "+err.Error())
			return
		}
	}
	_ = a.store.SetSetting("isipulsa_username", "-")
	_ = a.store.SetSetting("cookies_updated_at", "") // hapus profile = cookies hilang
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// loginManual: mulai sesi login — window Chrome langsung (Windows) atau

func (a *API) importCookies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cookies string `json:"cookies"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<22)).Decode(&req); err != nil { // max 4MB
		jsonErr(w, 400, "json tidak valid")
		return
	}
	n, err := a.eng.ImportCookies(req.Cookies)
	if err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok":     true,
		"jumlah": n,
		"pesan":  fmt.Sprintf("%d cookie diinject ke profile — cek status login.", n),
	})
}

// simpanUsername: catat nama akun isipulsa (opsional, untuk ditampilkan).
func (a *API) simpanUsername(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		jsonErr(w, 400, "json tidak valid")
		return
	}
	_ = a.store.SetSetting("isipulsa_username", strings.TrimSpace(req.Username))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------- pengaturan (settings) ----------

var settingKeys = map[string]bool{
	"paypan_secret":        true,
	"paypan_base_url":      true, // base URL API paypan (create/get invoice)
	"paypan_token":         true, // Bearer token scope order
	"admin_user":           true,
	"admin_pass":           true,
	"isipulsa_username":    true,
	"server_url":           true, // URL publik server (CF tunnel), dipakai client & webhook
	"server_url_lan":       true, // URL LAN (opsional) — buat bot/klien lokal, lebih cepat
	"server_url_tunnel":    true, // URL CF tunnel (opsional) — sama dengan server_url, buat clarity
	"bot_tg_token":         true, // token bot Telegram — server kirim notif via Bot API
	"bot_admin_id":         true, // WHITELIST chat ID penerima notif lengkap (pisah koma)
	"bot_admin_thread_id":  true, // topik (message_thread_id) buat notif admin di supergroup
	"saldo_min":            true, // ambang alert saldo isipulsa (default 20000)
	"saldo_interval_menit": true, // jeda cek saldo (menit, default 30, 5-1440)
	"saldo_notif_jam":      true, // ulang notif saldo tiap N jam (default 1)
	"sheet_backup_url":     true, // URL Apps Script untuk backup order ke Google Sheet
	"sheet_backup_token":   true, // token rahasia biar sheet cuma bisa diisi server ini
	// Channel notifikasi PUBLIK (testimoni): token bot + chat/channel tujuan.
	// Isi order di channel ini TANPA nomor HP (bukti transaksi, bukan data pribadi).
	"bot_public_token":    true,
	"bot_public_chat_id":  true,
	"bot_public_thread_id": true, // topik (message_thread_id) di supergroup forum
	"notif_public_status": true, // status mana yang dikirim ke channel publik (default: success)
}

// tambahKey: buat/upsert API key client (admin only).
func (a *API) tambahKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key        string `json:"key"`
		Nama       string `json:"nama"`
		BolehOrder bool   `json:"boleh_order"`
		BolehAdmin bool   `json:"boleh_admin"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		jsonErr(w, 400, "json tidak valid")
		return
	}
	req.Nama = strings.TrimSpace(req.Nama)
	if req.Nama == "" {
		jsonErr(w, 400, "nama wajib diisi")
		return
	}
	req.Key = strings.TrimSpace(req.Key)
	if req.Key == "" {
		// auto-generate: AP-<24 hex>
		b := make([]byte, 12)
		if _, err := rand.Read(b); err != nil {
			jsonErr(w, 500, "gagal generate key")
			return
		}
		req.Key = "AP-" + hex.EncodeToString(b)
	}
	if err := a.store.UpsertAPIKey(&db.APIKey{
		Key: req.Key, Nama: req.Nama, BolehOrder: req.BolehOrder, BolehAdmin: req.BolehAdmin,
	}); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "key": req.Key, "nama": req.Nama})
}

// hapusKey: hapus API key client (admin only).
func (a *API) hapusKey(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		jsonErr(w, 400, "key kosong")
		return
	}
	if err := a.store.DeleteAPIKey(key); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// searchPaket: cari paket isipulsa (untuk form katalog) — admin only.
func (a *API) searchPaket(w http.ResponseWriter, r *http.Request) {
	cari := strings.TrimSpace(r.URL.Query().Get("q"))
	if cari == "" {
		jsonErr(w, 400, "parameter q kosong")
		return
	}
	items, err := a.eng.IsipSearch("Paket Kuota", cari, 30)
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, items)
}

// listKeys: daftar API key client (admin only).
func (a *API) listKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := a.store.ListAPIKeys()
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, keys)
}

// getSettings: baca settings terpilih (admin only). Token ditampilkan apa
// adanya — single-admin tool, copy-paste antar panel harus gampang.
func (a *API) getSettings(w http.ResponseWriter, r *http.Request) {
	out := map[string]string{}
	for k := range settingKeys {
		out[k] = a.store.GetSetting(k, "")
	}
	out["profile_dir"] = a.eng.ProfileDir()
	writeJSON(w, 200, out)
}

// setSettings: ubah settings (admin only). Kosong = tidak diubah.
func (a *API) setSettings(w http.ResponseWriter, r *http.Request) {
	var req map[string]string
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		jsonErr(w, 400, "json tidak valid")
		return
	}
	changed := []string{}
	for k, v := range req {
		if !settingKeys[k] || strings.TrimSpace(v) == "" {
			continue
		}
		if err := a.store.SetSetting(k, strings.TrimSpace(v)); err != nil {
			jsonErr(w, 500, err.Error())
			return
		}
		changed = append(changed, k)
	}
	// paypan config live — client baca settings tiap request, gak perlu rebuild.
	writeJSON(w, 200, map[string]any{"ok": true, "changed": changed})
}
