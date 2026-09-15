// Package api: notifikasi Telegram — server kirim chat ke admin via Bot API
// (settings bot_tg_token + bot_admin_id, diisi di web admin -> Pengaturan).
package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jhopan/agenpulsa-server/internal/db"
)

// TgNotify kirim pesan ke admin via Bot API. No-op kalau token/ID belum diset.
// Gagal = log saja, jangan bikin caller error (notif best effort).
func (a *API) TgNotify(pesan string) {
	token := a.store.GetSetting("bot_tg_token", "")
	chatID := a.store.GetSetting("bot_admin_id", "")
	if token == "" || chatID == "" {
		return // belum dikonfigurasi — diam
	}
	go func() {
		defer func() { _ = recover() }()
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.PostForm(
			"https://api.telegram.org/bot"+token+"/sendMessage",
			url.Values{
				"chat_id":    {chatID},
				"text":       {pesan},
				"parse_mode": {"Markdown"},
			},
		)
		if err != nil {
			log.Printf("[TG NOTIFY] gagal kirim: %v", err)
			return
		}
		defer resp.Body.Close()
		var out struct {
			OK          bool   `json:"ok"`
			Description string `json:"description"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if !out.OK {
			log.Printf("[TG NOTIFY] telegram tolak: %s", out.Description)
		}
	}()
}

// TgNotifyBlock kirim dan tunggu hasil (dipakai tes koneksi di Pengaturan).
func (a *API) TgNotifyBlock(pesan string) error {
	token := a.store.GetSetting("bot_tg_token", "")
	chatID := a.store.GetSetting("bot_admin_id", "")
	if token == "" || chatID == "" {
		return fmt.Errorf("bot_tg_token / bot_admin_id belum diset")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.PostForm(
		"https://api.telegram.org/bot"+token+"/sendMessage",
		url.Values{"chat_id": {chatID}, "text": {pesan}, "parse_mode": {"Markdown"}},
	)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if !out.OK {
		if strings.Contains(out.Description, "chat not found") {
			return fmt.Errorf("chat tidak ketemu — bot perlu didiag conversation dulu (kirim /start ke bot)")
		}
		return fmt.Errorf("telegram: %s", out.Description)
	}
	return nil
}

// tesBot: handler tombol "Tes Notif" di Pengaturan — kirim pesan test ke admin.
func (a *API) tesBot(w http.ResponseWriter, r *http.Request) {
	pesan := fmt.Sprintf("✅ Notifikasi agenpulsa aktif (tes %s WIB).", db.NowWIB().Format("02/01 15:04"))
	if err := a.TgNotifyBlock(pesan); err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
