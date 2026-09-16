// Package api: HTTP API + web admin (stdlib net/http, frontend embed).
package api

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jhopan/agenpulsa-server/internal/db"
	"github.com/jhopan/agenpulsa-server/internal/engine"
)

type API struct {
	store    *db.Store
	eng      *engine.Engine
	web      fs.FS
	ppCfg    PaypanConfig
	ppClient *PaypanClient
}

func New(store *db.Store, eng *engine.Engine, webFS embed.FS) *API {
	sub, _ := fs.Sub(webFS, "web")
	// ENV = level infrastruktur saja (port, path). Config runtime (paypan dll)
	// murni dari settings DB via web admin — live, tanpa rebuild/restart.
	cfg := PaypanConfig{Timeout: 30 * time.Second}
	a := &API{store: store, eng: eng, web: sub, ppCfg: cfg, ppClient: NewPaypanClient(cfg)}
	// notif hasil order berbayar -> Telegram admin (kalau bot dikonfigurasi).
	eng.Notifier = func(o db.Order, status, pesan string) {
		a.TgNotify(formatOrderNotif(&o, status, pesan))
	}
	return a
}

// formatOrderNotif pesan Telegram utk hasil order berbayar.
func formatOrderNotif(o *db.Order, status, pesan string) string {
	emoji := map[string]string{
		"success": "✅", "failed": "❌", "cancelled": "🚫", "scheduled": "📅",
	}[status]
	nomor := o.Nomor
	if nomor == "" {
		nomor = "-"
	}
	msg := fmt.Sprintf(
		"%s *Order %s*\n\n📦 %s\n📱 %s\n💵 Harga: %s\n🔖 Ref: `%s`",
		emoji, strings.ToUpper(status), o.Label, nomor,
		"Rp "+strconv.FormatInt(o.HargaJual, 10), o.Ref,
	)
	if o.HargaJual > 0 && o.Modal > 0 && status == "success" {
		msg += fmt.Sprintf("\n💰 Modal: Rp %s · Profit: Rp %s",
			strconv.FormatInt(o.Modal, 10),
			strconv.FormatInt(o.HargaJual-o.Modal, 10))
	}
	if pesan != "" {
		msg += "\n\n" + pesan
	}
	return msg
}

// StartReconcile jalankan loop rekonsiliasi order pending_payment (goroutine).
// Webhook bisa hilang (firewall/retry habis) — polling ke Paypan jamin order
// gak nyangkut: paid -> proses, expired -> failed.
func (a *API) StartReconcile() {
	if !a.ppCfg.Enabled() {
		return
	}
	go func() {
		for {
			a.reconcileOnce()
			time.Sleep(60 * time.Second)
		}
	}()
}

const pendingGrace = 7 * time.Minute // invoice expired 5 menit + buffer webhook retry

func (a *API) reconcileOnce() {
	pending, err := a.store.ListOrders("pending_payment", 200)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-pendingGrace)
	for _, o := range pending {
		if o.CreatedAt == "" {
			continue
		}
		created, err := time.ParseInLocation("2006-01-02 15:04:05", o.CreatedAt, db.WIBLoc())
		if err != nil || created.After(cutoff) {
			continue // masih dalam masa bayar/webhook retry
		}
		inv, err := a.ppClient.GetInvoice(a.store, o.InvoiceID)
		if err != nil {
			continue // paypan gak bisa dihubungi — coba lagi tick berikutnya
		}
		switch inv.Status {
		case "paid":
			a.terimaPembayaran(o, inv.Total)
		case "expired":
			_ = a.store.UpdateOrderStatus(o.ID, "failed",
				"invoice expired (tidak dibayar dalam 5 menit) — silakan buat order baru", "")
		default: // pending: biarkan, mungkin webhook tertunda; cek lagi nanti
		}
	}
}

// terimaPembayaran proses order yang dibayar (dari webhook ATAU reconcile).
// Langganan -> buat jadwal sekali-jalan; lainnya -> queue untuk dibeli.
func (a *API) terimaPembayaran(o *db.Order, total int64) {
	if o.Sumber == "langganan" {
		if err := a.langgananAktifkan(o); err != nil {
			_ = a.store.UpdateOrderStatus(o.ID, "pending_payment",
				"paid tapi gagal buat jadwal: "+err.Error()+" - butuh cek manual", "")
			return
		}
		return
	}
	_ = a.store.UpdateOrderStatus(o.ID, "queued",
		"pembayaran diterima (paypan invoice "+o.InvoiceID+", total "+strconv.FormatInt(total, 10)+")", "")
	a.eng.Wake()
}

// StartCookieReminder loop per 30 menit: kalau umur cookies >= 3 hari, catat
// peringatan (log) MAKS 1x per hari — key notif = tanggal WIB. Bot Telegram
// yang baca & kirim notif ke admin. Reset otomatis saat inject cookies baru.
func (a *API) StartCookieReminder() {
	go func() {
		for {
			a.cookieReminderTick()
			time.Sleep(30 * time.Minute)
		}
	}()
}

func (a *API) cookieReminderTick() {
	umur := a.cookieUmurJam()
	if umur < cookieReminderJam {
		return
	}
	hari := int(umur / 24)
	hariIni := db.NowWIB().Format("2006-01-02")
	if a.store.GetSetting("cookie_notif_sent", "") == hariIni {
		return // sudah dikirim hari ini — jangan spam
	}
	pesan := fmt.Sprintf(
		"Cookies isipulsa sudah %d hari (umur >= 3 hari). Inject cookies baru di web admin -> Akun Login supaya sesi gak mati mendadak.",
		hari)
	log.Printf("[COOKIE REMINDER] %s", pesan)
	_ = a.store.SetSetting("cookie_notif_sent", hariIni)
	a.TgNotify("🍪 *Reminder Cookies*\n\n" + pesan)
}

// cookieStatus status reminder cookies untuk bot Telegram (poll).
func (a *API) cookieStatus(w http.ResponseWriter, r *http.Request) {
	umur := a.cookieUmurJam()
	sudahKirim := a.store.GetSetting("cookie_notif_sent", "") == db.NowWIB().Format("2006-01-02")
	writeJSON(w, 200, map[string]any{
		"umur_jam":               umur,
		"umur_hari":              int(umur / 24),
		"reminder":               umur >= cookieReminderJam,
		"sudah_dikirim_hari_ini": sudahKirim,
		"pesan":                  cookiePesan(umur),
	})
}

func cookiePesan(umur float64) string {
	if umur < 0 {
		return "Belum ada cookies tercatat — inject cookies di web admin -> Akun Login."
	}
	hari := int(umur / 24)
	if umur >= cookieReminderJam {
		return fmt.Sprintf("⚠️ Cookies isipulsa sudah %d hari (>= 3 hari). Inject cookies baru SEKARANG di web admin -> Akun Login sebelum sesi mati mendadak.", hari)
	}
	sisa := int(cookieReminderJam-umur) / 24
	return fmt.Sprintf("Cookies isipulsa %d hari. Sisa %d hari sebelum perlu inject baru.", hari, sisa+1)
}

func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()

	// Web admin: dashboard (perlu admin) + login page (publik).
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		if !a.adminKey(r) && !a.sessionAdmin(r) {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		a.serveFile(w, "index.html")
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		a.serveFile(w, "login.html")
	})
	mux.Handle("GET /", http.FileServerFS(a.web))

	// API v1 (API key). needAdmin=true untuk operasi ubah katalog.
	mux.HandleFunc("GET /api/v1/catalog", a.auth(false, a.listCatalog))
	mux.HandleFunc("POST /api/v1/catalog", a.auth(true, a.upsertCatalog))
	mux.HandleFunc("DELETE /api/v1/catalog/{id}", a.auth(true, a.deleteCatalog))

	mux.HandleFunc("POST /api/v1/orders", a.auth(false, a.createOrder))
	mux.HandleFunc("GET /api/v1/orders/{id}", a.auth(false, a.getOrder))
	mux.HandleFunc("GET /api/v1/orders", a.auth(false, a.listOrders))
	mux.HandleFunc("POST /api/v1/orders/pending", a.auth(false, a.createPending)) // flow paypan

	mux.HandleFunc("GET /api/v1/status", a.auth(false, a.status))
	mux.HandleFunc("GET /api/v1/report", a.auth(false, a.report))
	mux.HandleFunc("GET /api/v1/maintenance", a.auth(false, a.maintenance))
	mux.HandleFunc("GET /api/v1/cookie-status", a.auth(false, a.cookieStatus)) // bot TG poll
	mux.HandleFunc("POST /api/v1/tes-bot", a.auth(true, a.tesBot))            // tes notif (admin)

	// Akun isipulsa + pengaturan (admin via session/API key, gate di handler).
	mux.HandleFunc("GET /api/v1/account", a.auth(false, a.account))
	mux.HandleFunc("POST /api/v1/account/relogin", a.auth(true, a.gantiAkun))
	mux.HandleFunc("POST /api/v1/account/login-manual", a.auth(true, a.loginManual))
	mux.HandleFunc("POST /api/v1/account/cookies", a.auth(true, a.importCookies))
	mux.HandleFunc("GET /api/v1/account/vnc", a.auth(true, a.loginVNC))
	mux.HandleFunc("POST /api/v1/account/username", a.auth(true, a.simpanUsername))
	mux.HandleFunc("GET /api/v1/settings", a.auth(true, a.getSettings))
	mux.HandleFunc("POST /api/v1/settings", a.auth(true, a.setSettings))
	mux.HandleFunc("GET /api/v1/keys", a.auth(true, a.listKeys))
	mux.HandleFunc("POST /api/v1/keys", a.auth(true, a.tambahKey))
	mux.HandleFunc("DELETE /api/v1/keys/{key}", a.auth(true, a.hapusKey))
	mux.HandleFunc("GET /api/v1/search", a.auth(true, a.searchPaket))

	// Pembelian manual + jadwal (admin web).
	mux.HandleFunc("POST /api/v1/beli", a.auth(true, a.beliManual))
	mux.HandleFunc("GET /api/v1/jadwal", a.auth(true, a.jadwalList))
	mux.HandleFunc("POST /api/v1/jadwal", a.auth(true, a.jadwalAdd))
	mux.HandleFunc("POST /api/v1/jadwal/{id}/toggle", a.auth(true, a.jadwalToggle))
	mux.HandleFunc("DELETE /api/v1/jadwal/{id}", a.auth(true, a.jadwalDelete))

	// Langganan customer: set jadwal sendiri -> bayar -> jadwal aktif (publik).
	mux.HandleFunc("POST /api/v1/langganan", a.langgananBaru)

	// Paypan webhook (HMAC, tanpa API key).
	mux.HandleFunc("POST /webhook", a.paypanWebhook)

	// Login admin (username/password -> session cookie).
	mux.HandleFunc("POST /api/login", a.login)
	mux.HandleFunc("POST /api/logout", a.logout)
	mux.HandleFunc("GET /api/me", a.me)

	return mux
}

// ---------- auth ----------

type keyPerm struct {
	key   *db.APIKey
	admin bool
}

func (a *API) adminKey(r *http.Request) bool {
	k := r.Header.Get("X-API-Key")
	if k == "" {
		return false
	}
	// Admin via env key sederhana (bootstrap).
	envKey := a.store.GetSetting("admin_key", "")
	if envKey == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(k), []byte(envKey)) == 1
}

func (a *API) auth(needAdmin bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.adminKey(r) || a.sessionAdmin(r) {
			next(w, r)
			return
		}
		k, err := a.store.CheckAPIKey(r.Header.Get("X-API-Key"))
		if err != nil {
			jsonErr(w, 401, "API key tidak valid")
			return
		}
		if needAdmin && !k.BolehAdmin {
			jsonErr(w, 403, "butuh key admin")
			return
		}
		if !needAdmin && !k.BolehOrder && !k.BolehAdmin {
			jsonErr(w, 403, "key tidak punya izin order")
			return
		}
		next(w, r)
	}
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readBody[T any](r *http.Request) (*T, error) {
	var v T
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		return nil, err
	}
	return &v, nil
}

// NormalizeNomor: 62/+62/spasi/strip -> 08...
func NormalizeNomor(text string) string {
	var b strings.Builder
	for _, c := range text {
		if c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	d := b.String()
	if strings.HasPrefix(d, "62") {
		d = "0" + d[2:]
	}
	if !strings.HasPrefix(d, "08") || len(d) < 10 || len(d) > 15 {
		return ""
	}
	return d
}

// ---------- handlers ----------

func (a *API) listCatalog(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListCatalog(r.URL.Query().Get("all") != "1")
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, items)
}

func (a *API) upsertCatalog(w http.ResponseWriter, r *http.Request) {
	it, err := readBody[db.CatalogItem](r)
	if err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	id, err := a.store.UpsertCatalog(it)
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	it.ID = id
	writeJSON(w, 200, it)
}

func (a *API) deleteCatalog(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := a.store.DeleteCatalog(id); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

type orderReq struct {
	Voucher     string `json:"voucher"`
	CatalogID   int64  `json:"catalog_id"`
	Nomor       string `json:"nomor"`
	Ref         string `json:"ref"`
	Sumber      string `json:"sumber"`
	ChatID      string `json:"chat_id"`
	CallbackURL string `json:"callback_url"`
}

func (a *API) buildOrder(req *orderReq, defaultSumber string) (*db.Order, error) {
	nomor := NormalizeNomor(req.Nomor)
	if nomor == "" {
		return nil, fmt.Errorf("nomor tidak valid (butuh format 08xxx / 62xxx)")
	}
	o := &db.Order{
		Ref:         req.Ref,
		Nomor:       nomor,
		CatalogID:   req.CatalogID,
		Status:      "queued",
		Sumber:      defaultSumber,
		ChatID:      req.ChatID,
		CallbackURL: req.CallbackURL,
	}
	if req.Sumber != "" {
		o.Sumber = req.Sumber
	}
	// Resolusi dari voucher / catalog_id.
	if req.CatalogID > 0 {
		it, err := a.store.GetCatalog(req.CatalogID)
		if err != nil {
			return nil, fmt.Errorf("katalog #%d tidak ada", req.CatalogID)
		}
		o.Label = it.Label
		o.Modal = it.HargaMax
		o.HargaJual = it.HargaJual
	} else if req.Voucher != "" {
		// Cari di katalog by voucher.
		items, _ := a.store.ListCatalog(false)
		for _, it := range items {
			if it.Voucher == req.Voucher {
				o.CatalogID = it.ID
				o.Label = it.Label
				o.Modal = it.HargaMax
				o.HargaJual = it.HargaJual
				break
			}
		}
		if o.CatalogID == 0 {
			o.Label = "voucher " + req.Voucher
		}
	} else {
		return nil, fmt.Errorf("butuh catalog_id atau voucher")
	}
	return o, nil
}

func (a *API) createOrder(w http.ResponseWriter, r *http.Request) {
	req, err := readBody[orderReq](r)
	if err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	// Idempotensi: ref sama -> balas order lama.
	if req.Ref != "" {
		if exist, _ := a.store.GetOrderByRef(req.Ref); exist != nil {
			writeJSON(w, 200, exist)
			return
		}
	}
	o, err := a.buildOrder(req, "api")
	if err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	if o.Ref == "" {
		o.Ref = "api-" + randomHex(8)
	}
	if err := a.eng.Enqueue(o); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	fresh, _ := a.store.GetOrderByRef(o.Ref)
	writeJSON(w, 202, fresh)
}

// createPending: order status pending_payment (menunggu webhook paypan).
func (a *API) createPending(w http.ResponseWriter, r *http.Request) {
	req, err := readBody[orderReq](r)
	if err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	if req.Ref == "" {
		req.Ref = "pend-" + randomHex(8)
	}
	if exist, _ := a.store.GetOrderByRef(req.Ref); exist != nil {
		writeJSON(w, 200, exist)
		return
	}
	o, err := a.buildOrder(req, "paypan")
	if err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	// Buat invoice QRIS paypan (sama dengan langgananBaru) — tanpa ini order
	// pending_payment gak pernah punya invoice_id -> webhook/reconcile gak bisa match.
	if o.HargaJual < 1000 {
		jsonErr(w, 400, "harga katalog di luar batas paypan (min 1.000)")
		return
	}
	inv, err := a.ppClient.CreateInvoice(a.store, o.HargaJual, o.Label+" "+o.Nomor)
	if err != nil {
		jsonErr(w, 502, "gagal buat invoice paypan: "+err.Error())
		return
	}
	o.InvoiceID = inv.ID
	o.Status = "pending_payment"
	if err := a.eng.Enqueue(o); err != nil { // simpan row (status pending, worker skip)
		jsonErr(w, 500, err.Error())
		return
	}
	fresh, _ := a.store.GetOrderByRef(o.Ref)
	writeJSON(w, 202, map[string]any{
		"ref":        fresh.Ref,
		"status":     fresh.Status,
		"invoice_id": inv.ID,
		"total":      inv.Total,
		"pay_url":    inv.PayURL,
		"qr_url":     inv.QRURL,
		"expires_at": inv.ExpiresAt,
		"pesan":      "bayar QRIS total " + strconv.FormatInt(inv.Total, 10) + " — order jalan otomatis setelah paid",
	})
}

func (a *API) getOrder(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	o, err := a.store.GetOrder(id)
	if err != nil {
		jsonErr(w, 404, "order tidak ada")
		return
	}
	writeJSON(w, 200, o)
}

func (a *API) listOrders(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	status := r.URL.Query().Get("status")
	orders, err := a.store.ListOrders(status, limit)
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, orders)
}

func (a *API) status(w http.ResponseWriter, r *http.Request) {
	ok, saldo := a.eng.CekStatus()
	writeJSON(w, 200, map[string]any{
		"login":       ok,
		"saldo":       saldo,
		"wib":         db.NowWIB().Format("2006-01-02 15:04:05 WIB"),
		"maintenance": engine.IsMaintenance(db.NowWIB()),
	})
}

func (a *API) report(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	switch days {
	case 1, 7, 30:
	default:
		days = 1
	}
	lap, err := a.store.Report(days)
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, lap)
}

func (a *API) maintenance(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"maintenance": engine.IsMaintenance(db.NowWIB()),
		"pesan":       engine.MaintenanceMessage(),
	})
}

// serveFile baca dari embed FS (login.html / index.html).
func (a *API) serveFile(w http.ResponseWriter, name string) {
	b, err := fs.ReadFile(a.web, name)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

var _ = time.Now
