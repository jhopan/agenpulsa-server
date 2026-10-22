// Package api: notifikasi Telegram — server kirim chat ke admin via Bot API
// (settings bot_tg_token + bot_admin_id, diisi di web admin -> Pengaturan).
package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
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


// ---------- Channel publik (testimoni) ----------

// TgNotifyPublic kirim notifikasi ke channel PUBLIK (bot + chat id terpisah
// dari admin). Isinya TANPA nomor HP, ref, atau data pribadi lain — cuma
// bukti transaksi (paket + harga + waktu).
//
// Pakai bot token berbeda: bot notifikasi publik = bot testimoni/voice yang
// SubscriberChannel-nya bisa dibaca orang lain.
func (a *API) TgNotifyPublic(pesan string) {
	token := strings.TrimSpace(a.store.GetSetting("bot_public_token", ""))
	chatID := strings.TrimSpace(a.store.GetSetting("bot_public_chat_id", ""))
	if token == "" || chatID == "" {
		return // channel publik belum diset — diam
	}
	// Topik di supergroup forum: message_thread_id (opsional). Kalau diisi,
	// pesan dikirim ke topik tsb, bukan general.
	form := url.Values{
		"chat_id":    {chatID},
		"text":       {pesan},
		"parse_mode": {"Markdown"},
	}
	if th := strings.TrimSpace(a.store.GetSetting("bot_public_thread_id", "")); th != "" {
		form.Set("message_thread_id", th)
	}
	go func() {
		defer func() { _ = recover() }()
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.PostForm(
			"https://api.telegram.org/bot"+token+"/sendMessage",
			form,
		)
		if err != nil {
			log.Printf("[TG PUBLIC] gagal kirim: %v", err)
			return
		}
		defer resp.Body.Close()
		var out struct {
			OK          bool   `json:"ok"`
			Description string `json:"description"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if !out.OK {
			log.Printf("[TG PUBLIC] telegram tolak: %s", out.Description)
		}
	}()
}

// formatOrderNotifPublic: notifikasi transaksi ke channel publik.
// SENGAJA TANPA: nomor HP, ref, chat_id, modal, profit, pesan error mentah.
//
// Pola nomor HP (08xx/62xx) disensor jadi "08xx-xxxx-xxxx" supaya kebetulan
// muncul di teks lain (mis. nama paket) gak bocor. Tapi lebih aman: field
// nomor memang gak pernah dimasukin.
func formatOrderNotifPublic(o *db.Order, status, pesan string) string {
	emoji := map[string]string{
		"success": "✅", "failed": "❌", "cancelled": "🚫", "scheduled": "📅",
	}[status]
	// label dibersihin: kalau label ikut nyimpen nomor (mis. "LANGGANAN
	// 0878... (ref)"), replace dengan "[paket]" supaya ga bocor.
	label := bersihkanNomor(o.Label)
	if label == "" {
		label = "Paket kuota"
	}
	msg := fmt.Sprintf("%s *Transaksi %s*\n\n📦 %s\n💵 %s",
		emoji, strings.ToUpper(status), label,
		"Rp "+strconv.FormatInt(o.HargaJual, 10))
	if o.CreatedAt != "" {
		msg += "\n🕐 " + o.CreatedAt + " WIB"
	}
	return msg
}

// bersihkanNomor: hapus nomor HP / ref dari teks label supaya aman untuk
// channel publik.
func bersihkanNomor(s string) string {
	out := s
	// ref scheduler: "sched-3-2026-09-20T07:00" / "sub-xxxx"
	out = regexp.MustCompile(`(?i)\b(sched|sub|adm|pend|exp)-[a-z0-9-]+`).ReplaceAllString(out, "")
	// nomor HP: 08xx / 62xx (+ spasi, tanda, atau tidak)
	out = regexp.MustCompile(`\b0?8\d{1,4}[\s-]?\d{3,4}[\s-]?\d{2,5}\b`).ReplaceAllString(out, "[nomor]")
	out = regexp.MustCompile(`\b62\d{1,4}[\s-]?\d{3,4}[\s-]?\d{2,5}\b`).ReplaceAllString(out, "[nomor]")
	// kurung/kurung senggang dobel
	out = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(out, "(", ""), ")", ""))
	out = strings.TrimSpace(strings.ReplaceAll(out, "  ", " "))
	return out
}

// FormatOrderNotifPublicForTest: export formatOrderNotifPublic buat test.
func FormatOrderNotifPublicForTest(o *db.Order, status, pesan string) string {
	return formatOrderNotifPublic(o, status, pesan)
}
