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

// TgNotify kirim pesan lengkap (nomor, ref, modal, profit) ke chat admin.
//
// WHITELIST: bot_admin_id = daftar chat ID Telegram yang boleh menerima
// notif lengkap (pisah koma). Hanya chat di daftar itu yang dikirimi —
// kalau bot ini gak sengaja masuk grup lain, chat itu TIDAK akan menerima
// notif (karena bukan di whitelist).
//
// Gagal = log saja, jangan bikin caller error (notif best effort).
func (a *API) TgNotify(pesan string) {
	token := a.store.GetSetting("bot_tg_token", "")
	if token == "" {
		return // belum dikonfigurasi — diam
	}
	chats := a.adminChats()
	if len(chats) == 0 {
		return // whitelist kosong — diam
	}
	// Topik admin (opsional): kalau diisi, notif masuk topik tsb.
	form := url.Values{
		"text":       {pesan},
		"parse_mode": {"Markdown"},
	}
	if th := strings.TrimSpace(a.store.GetSetting("bot_admin_thread_id", "")); th != "" {
		form.Set("message_thread_id", th)
	}
	go func() {
		defer func() { _ = recover() }()
		client := &http.Client{Timeout: 10 * time.Second}
		for _, chatID := range chats {
			f := url.Values{}
			for k, v := range form {
				f[k] = v
			}
			f.Set("chat_id", chatID)
			resp, err := client.PostForm(
				"https://api.telegram.org/bot"+token+"/sendMessage", f)
			if err != nil {
				log.Printf("[TG NOTIFY] gagal kirim ke %s: %v", chatID, err)
				continue
			}
			var out struct {
				OK          bool   `json:"ok"`
				Description string `json:"description"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&out)
			resp.Body.Close()
			if !out.OK {
				log.Printf("[TG NOTIFY] telegram tolak (%s): %s", chatID, out.Description)
			}
		}
	}()
}

// adminChats: whitelist chat ID penerima notif lengkap (setting bot_admin_id).
// Format: satu atau lebih ID dipisah koma. Nilai "-100..." = supergroup/channel.
func (a *API) adminChats() []string {
	raw := a.store.GetSetting("bot_admin_id", "")
	var out []string
	for _, v := range strings.Split(raw, ",") {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// TgNotifyBlock kirim dan tunggu hasil (dipakai tes koneksi di Pengaturan).
func (a *API) TgNotifyBlock(pesan string) error {
	token := a.store.GetSetting("bot_tg_token", "")
	chats := a.adminChats()
	if token == "" || len(chats) == 0 {
		return fmt.Errorf("bot_tg_token / bot_admin_id (whitelist) belum diset")
	}
	chatID := chats[0]
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
	// Satu bot bisa dipakai untuk admin + publik. Token publik opsional:
	// kalau kosong -> pakai token bot admin (bot yang sama, tujuan beda).
	token := a.publicBotToken()
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

// AdminChatsForTest: export adminChats buat test.
func (a *API) AdminChatsForTest() []string { return a.adminChats() }


// TgNotifyPublicCustom: kirim pesan ke channel publik dari bot MANA pun.
// Dipakai endpoint /api/v1/notify-public — bot lain (WA, script, dashboard)
// bisa push notif transaksi ke topik testimoni tanpa lewat agenpulsa-server.
//
// Pengaman: pesan disensor (nomor HP / ref dibuang) sebelum dikirim, dan
// panjangnya dibatasi. Jadi bot pengirim gak bisa bocorin data pelanggan
// ke channel publik.
func (a *API) TgNotifyPublicCustom(pesan string) bool {
	token := a.publicBotToken()
	chatID := strings.TrimSpace(a.store.GetSetting("bot_public_chat_id", ""))
	if token == "" || chatID == "" {
		return false
	}
	pesan = bersihkanNomor(pesan)
	if len(pesan) > 900 {
		pesan = pesan[:900] + "…"
	}
	form := url.Values{
		"chat_id":    {chatID},
		"text":       {pesan},
		"parse_mode": {"Markdown"},
	}
	if th := strings.TrimSpace(a.store.GetSetting("bot_public_thread_id", "")); th != "" {
		form.Set("message_thread_id", th)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.PostForm("https://api.telegram.org/bot"+token+"/sendMessage", form)
	if err != nil {
		log.Printf("[TG PUBLIC] gagal push custom: %v", err)
		return false
	}
	defer resp.Body.Close()
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if !out.OK {
		log.Printf("[TG PUBLIC] telegram tolak: %s", out.Description)
		return false
	}
	return true
}


// publicBotToken: token bot untuk channel publik. Kalau bot_public_token
// kosong -> pakai bot_tg_token (SATU bot untuk admin + publik).
// Bot yang sama bisa kirim ke chat pribadi admin DAN topik supergrup —
// cukup beda chat_id + message_thread_id.
func (a *API) publicBotToken() string {
	if t := strings.TrimSpace(a.store.GetSetting("bot_public_token", "")); t != "" {
		return t
	}
	return strings.TrimSpace(a.store.GetSetting("bot_tg_token", ""))
}

// PublicBotTokenForTest: export publicBotToken buat test.
func (a *API) PublicBotTokenForTest() string { return a.publicBotToken() }
