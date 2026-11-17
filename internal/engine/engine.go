// Package engine: eksekusi order isipulsa dari queue — HTTP murni via
// helper/isip_api.py (requests + CSRF). Tanpa Chromium/CDP.
package engine

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jhopan/agenpulsa-server/internal/db"
)

// Jam rekap/pembukuan isipulsa: 23:40 - 00:35 WIB, order dibatalkan.
var maintStart = 23*60 + 40 // menit
var maintEnd = 0*60 + 35

func IsMaintenance(now time.Time) bool {
	wib := now.In(db.WIBLoc())
	m := wib.Hour()*60 + wib.Minute()
	return m >= maintStart || m < maintEnd
}

func MaintenanceMessage() string {
	return "ORDER DIBATALKAN: transaksi ditutup untuk rekap dan pembukuan pukul 23:40-00:35 WIB. Silakan ulangi setelah 00:35 WIB."
}

var reNonDigit = regexp.MustCompile(`[^\d]`)

func ParseHarga(s string) int64 {
	d := reNonDigit.ReplaceAllString(s, "")
	if d == "" {
		return 0
	}
	v, _ := strconv.ParseInt(d, 10, 64)
	return v
}

type Engine struct {
	store      *db.Store
	cookieFile string // jar cookie isipulsa (JSON export Cookie-Editor)
	wake       chan struct{}
	closed     bool
	// Notifier dipanggil tiap order status final (success/failed/cancelled) —
	// di-set oleh api (TgNotify). Nil = silent.
	Notifier func(order db.Order, status, pesan string)
	// LoginInvalidNotifier dipanggil saat cookie isipulsa turns out invalid
	// (login gagal) — di-set oleh api. Nil = silent.
	// Dipanggil BANYAK tempat: monitor saldo, monitor cookie, order gagal.
	// Implementasi di api yang ngatur rate-limit (max 1x / 3 hari).
	LoginInvalidNotifier func(alasan string)
	// CookieExpiryNotifier dipanggil saat cookie mau expired (<= N hari).
	CookieExpiryNotifier func(sisaHari int, expireWIB string, jumlahCookie int)
}

func New(store *db.Store) (*Engine, error) {
	jar := os.Getenv("AP_COOKIES")
	if jar == "" {
		dbPath := os.Getenv("AP_DB")
		if dbPath == "" {
			dbPath = filepath.Join("data", "agenpulsa.db")
		}
		jar = filepath.Join(filepath.Dir(dbPath), "cookies.json")
	}
	if dir := filepath.Dir(jar); dir != "." && dir != "" {
		_ = os.MkdirAll(dir, 0o700)
	}
	return &Engine{store: store, cookieFile: jar, wake: make(chan struct{}, 1)}, nil
}

// CookieFile: path jar cookie isipulsa (JSON export Cookie-Editor).
func (e *Engine) CookieFile() string { return e.cookieFile }

// Close menghentikan worker (tidak ada browser/proses latar yang perlu ditutup).
func (e *Engine) Close() { e.closed = true }

// Enqueue memasukkan order ke antri (status queued) dan bangunkan worker.
func (e *Engine) Enqueue(o *db.Order) error {
	if _, err := e.store.CreateOrder(o); err != nil {
		return err
	}
	select {
	case e.wake <- struct{}{}:
	default:
	}
	return nil
}

// Wake bangunkan worker (misal webhook paypan mengubah order jadi queued).
func (e *Engine) Wake() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Worker loop: ambil order queued satu-satu, eksekusi, ulangi.
func (e *Engine) Worker() {
	for {
		if e.closed {
			return
		}
		o, err := e.store.PopNextQueued()
		if err != nil {
			log.Printf("worker pop: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		if o == nil {
			select {
			case <-e.wake:
			case <-time.After(10 * time.Second):
			}
			continue
		}
		e.Execute(o)
	}
}

// Execute jalankan satu order end-to-end.
func (e *Engine) Execute(o *db.Order) {
	now := db.NowWIB()
	if IsMaintenance(now) {
		_ = e.store.UpdateOrderStatus(o.ID, "cancelled", MaintenanceMessage(), "")
		e.Callback(o, "cancelled", MaintenanceMessage())
		return
	}
	_ = e.store.UpdateOrderStatus(o.ID, "running", "memproses order...", "")

	// P10: ukur durasi eksekusi — log latency biar bisa lihat Lambat/CEPAt.
	mulai := time.Now()
	pesan, modal, orderID, ok := e.runOrder(o)
	durasi := time.Since(mulai)
	status := "failed"
	if ok {
		status = "success"
	}
	_ = e.store.UpdateOrderStatus(o.ID, status, pesan, orderID)
	e.Callback(o, status, pesan)
	e.notifyOrder(*o, status, pesan)
	// Order gagal karena cookie/sesi login invalid -> kabari admin SEKARANG
	// (bukan nunggu monitor 60 menit). Notif rate-limit diurus di sisi api.
	if status == "failed" && looksLikeLoginProblem(pesan) {
		e.NotifyLoginInvalid("order gagal: " + pesan)
	}
	log.Printf("[ORDER #%d] %s %s durasi=%s ref=%s nomor=%s", o.ID, status, o.Label, durasi.Round(time.Millisecond), o.Ref, o.Nomor)
	_ = modal
}

// looksLikeLoginProblem: deteksi pesan error yang disebabkan session login
// isipulsa invalid (bukan masalah lain seperti saldo kurang / paket hilang).
func looksLikeLoginProblem(pesan string) bool {
	p := strings.ToLower(pesan)
	// HATI-HATI: jangan pakai kata umum kayak "expired" — "invoice expired"
	// (order kadaluarsa) itu BUKAN masalah cookie.
	kata := []string{
		"tidak ada cookie", "sesi login habis", "belum login", "login habis",
		"harus login", "gagal login", "unauthorized", "not logged in",
		"cookie tidak valid", "sesi habis", "session expired",
		"login expired", "cookie expired", "cookies expired",
		"kena cloudflare",
	}
	for _, k := range kata {
		if strings.Contains(p, k) {
			return true
		}
	}
	return false
}

// NotifyLoginInvalid: kabari ke api bahwa cookie/sesi login invalid.
func (e *Engine) NotifyLoginInvalid(alasan string) {
	if e.LoginInvalidNotifier == nil {
		return
	}
	e.LoginInvalidNotifier(alasan)
}

// NotifyCookieExpiry: kabari ke api bahwa cookie mau expired.
func (e *Engine) NotifyCookieExpiry(sisaHari int, expireWIB string, jumlah int) {
	if e.CookieExpiryNotifier == nil {
		return
	}
	e.CookieExpiryNotifier(sisaHari, expireWIB, jumlah)
}

// notifyOrder push hasil order ke Notifier (Telegram) kalau terpasang.
// Hanya order BERBAYAR (sumber api/paypan/langganan — bukan scheduler admin
// harian) supaya jadwal gagal berulang gak banjir chat.
func (e *Engine) notifyOrder(o db.Order, status, pesan string) {
	if e.Notifier == nil {
		return
	}
	if o.Sumber == "scheduler" {
		return // jadwal admin — silent (bisa dicek web admin)
	}
	e.Notifier(o, status, pesan)
}

// Callback kirim hasil ke callback_url order (jika ada), best effort.
func (e *Engine) Callback(o *db.Order, status, pesan string) {
	if o.CallbackURL == "" {
		return
	}
	go func() {
		defer func() { _ = recover() }()
		body := fmt.Sprintf(`{"order_id":%d,"ref":%q,"status":%q,"pesan":%q}`, o.ID, o.Ref, status, pesan)
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Post(o.CallbackURL, "application/json", strings.NewReader(body))
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
}

// runOrder: order via HTTP murni (isip_api.py) — tanpa Chromium/Turnstile.
// Return (pesan, modal, orderID_isipulsa, sukses).
func (e *Engine) runOrder(o *db.Order) (string, int64, string, bool) {
	// Ambil katalog item bila ada (untuk voucher/cari/operator/harga_max/produk).
	item := &db.CatalogItem{Tab: "Paket Kuota", Cari: o.Label}
	if o.CatalogID > 0 {
		if it, err := e.store.GetCatalog(o.CatalogID); err == nil {
			item = it
		}
	}
	produk := strings.ToLower(item.Tab)
	produk = strings.ReplaceAll(produk, "paket kuota", "paket_kuota")
	produk = strings.ReplaceAll(produk, "paket internet", "paket_internet")
	produk = strings.ReplaceAll(produk, " ", "_")

	// item.Cari = nama asli paket di isipulsa (anti-drift + fallback kalau voucher hilang)
	// item.Operator = provider paket (guard: nomor user harus cocok provider)
	sukses, pesan, orderID, modal := e.IsipOrder(o.Nomor, produk, item.Voucher, item.Cari, item.Operator, item.HargaMax)
	return pesan, modal, orderID, sukses
}

// CekStatus cek login & saldo via HTTP murni (tanpa Chromium).
func (e *Engine) CekStatus() (bool, string) {
	ok, saldo, err := e.IsipCekStatus()
	if err != nil {
		return false, "Error"
	}
	return ok, saldo
}

// LooksLikeLoginProblemForTest: export untuk test.
func LooksLikeLoginProblemForTest(pesan string) bool {
	return looksLikeLoginProblem(pesan)
}
