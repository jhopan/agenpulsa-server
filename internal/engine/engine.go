// Package engine: buka Chromium via rod, eksekusi order isipulsa dari queue.
// Port dari bot.py (Playwright) — selektor & flow identik:
//   - tab: .form-tabs a (text)
//   - nomor: input[name="nomor_hp"] + blur
//   - paket: #nominal .row button[data-voucher=...]
//   - submit: jQuery $.post AJAX (bukan form.submit)
package engine

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
	"github.com/jhopan/agenpulsa-server/internal/db"
)

const baseURL = "https://isipulsa.web.id/"

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
	store     *db.Store
	browser   *rod.Browser
	userData  string
	mu        sync.Mutex // queue: satu eksekusi browser pada satu waktu
	wake      chan struct{}
	closed    bool
	loginOpen bool          // sesi login (window/VNC) sedang berjalan
	sess      *LoginSession // sesi login aktif, nil kalau tidak ada
	// Notifier dipanggil tiap order status final (success/failed/cancelled) —
	// di-set oleh api (TgNotify). Nil = silent.
	Notifier func(order db.Order, status, pesan string)
}

func New(store *db.Store) (*Engine, error) {
	userData := os.Getenv("AP_PROFILE")
	if userData == "" {
		userData = "profile"
	}
	_ = os.MkdirAll(userData, 0o755)
	e := &Engine{store: store, userData: userData, wake: make(chan struct{}, 1)}
	return e, nil
}

// lowmemFlags flag chromium hemat RAM/CPU (server low-spec).
// Pasangan [nama, nilai]; nilai kosong = flag boolean.
var lowmemFlags = [][2]string{
	{"disable-gpu", ""},                   // tanpa GPU render
	{"disable-dev-shm-usage", ""},         // /dev/shm kecil di VPS
	{"disable-extensions", ""},            // tanpa ekstensi
	{"disable-sync", ""},                  // tanpa sync
	{"disable-translate", ""},             // tanpa translate UI
	{"disable-background-networking", ""}, // tanpa traffic background
	{"disable-default-apps", ""},
	{"disable-plugins", ""},
	{"no-first-run", ""},
	{"mute-audio", ""},
	{"blink-settings", "imagesEnabled=false"}, // jangan load gambar (isipulsa cukup DOM teks)
}

func applyLowmem(l *launcher.Launcher) *launcher.Launcher {
	for _, f := range lowmemFlags {
		if f[1] == "" {
			l = l.Set(flags.Flag(f[0]))
		} else {
			l = l.Set(flags.Flag(f[0]), f[1])
		}
	}
	return l
}

// browser lazy: start sekali, dipakai ulang semua order.
func (e *Engine) getBrowser() (*rod.Browser, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.browser != nil {
		return e.browser, nil
	}
	l := applyLowmem(launcher.New().
		UserDataDir(e.userData).
		Headless(true).
		Set("no-sandbox"))
	url, err := l.Launch()
	if err != nil {
		// Fallback: pakai chromium binary dari sistem/playwright cache.
		if bin := findChromium(); bin != "" {
			l2 := applyLowmem(launcher.New().
				Bin(bin).
				UserDataDir(e.userData).
				Headless(true).
				Set("no-sandbox"))
			url, err = l2.Launch()
		}
		if err != nil {
			return nil, fmt.Errorf("launch chromium: %w", err)
		}
	}
	e.browser = rod.New().ControlURL(url)
	if err := e.browser.Connect(); err != nil {
		e.browser = nil
		return nil, err
	}
	return e.browser, nil
}

func findChromium() string {
	patterns := []string{
		"/root/.cache/ms-playwright/chromium-*/chrome-linux/chrome",
		"/root/.cache/ms-playwright/chromium_headless_shell-*/chrome-linux/headless_shell",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
	}
	for _, p := range patterns {
		if matches, _ := filepath.Glob(p); len(matches) > 0 {
			return matches[0]
		}
	}
	if p, err := exec.LookPath("chromium"); err == nil {
		return p
	}
	return ""
}

// ProfileDir path folder user-data-dir (cookie isipulsa).
func (e *Engine) ProfileDir() string { return e.userData }

func (e *Engine) Close() {
	e.closed = true
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.browser != nil {
		_ = e.browser.Close()
		e.browser = nil
	}
}

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
		// sesi login aktif -> browser/profile dipakai login, jangan rebutan.
		if e.LoginOpen() {
			time.Sleep(2 * time.Second)
			continue
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

	pesan, modal, orderID, ok := e.runOrder(o)
	status := "failed"
	if ok {
		status = "success"
	}
	_ = e.store.UpdateOrderStatus(o.ID, status, pesan, orderID)
	e.Callback(o, status, pesan)
	e.notifyOrder(*o, status, pesan)
	_ = modal
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
	// Ambil katalog item bila ada (untuk voucher/cari/harga_max/produk).
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
	sukses, pesan, orderID, modal := e.IsipOrder(o.Nomor, produk, item.Voucher, item.Cari, item.HargaMax)
	return pesan, modal, orderID, sukses
}

// SearchPackages cari paket di tab tertentu (untuk katalog admin). Port dari search_packages().
func (e *Engine) SearchPackages(tab, keyword, nomor string) ([]map[string]string, error) {
	if nomor == "" {
		nomor = "081234567890"
	}
	browser, err := e.getBrowser()
	if err != nil {
		return nil, err
	}
	page, err := browser.Page(proto.TargetCreateTarget{URL: baseURL})
	if err != nil {
		return nil, err
	}
	defer func() { _ = page.Close() }()
	if err := page.WaitLoad(); err != nil {
		return nil, err
	}
	t, err := page.ElementR(".form-tabs a", tab)
	if err != nil {
		return nil, fmt.Errorf("tab %q tidak ada", tab)
	}
	_ = t.Click(proto.InputMouseButtonLeft, 1)
	in, err := page.Element(`input[name="nomor_hp"]`)
	if err != nil {
		return nil, err
	}
	_ = in.Input(nomor)
	_ = in.Blur()
	_, _ = page.Elements("#nominal .row button")
	res, err := page.Eval(`(kw) => Array.from(document.querySelectorAll('#nominal .row button'))
		.filter(b => b.innerText.toLowerCase().includes(kw.toLowerCase()))
		.map(b => ({
			voucher: b.getAttribute('data-voucher'),
			operator: b.getAttribute('data-operator'),
			nama: b.getAttribute('data-nominal'),
			harga: b.getAttribute('data-harga'),
			teks: b.innerText.replace(/\n/g, ' ').trim()
		}))`, keyword)
	if err != nil {
		return nil, err
	}
	var out []map[string]string
	_ = res.Value.Unmarshal(&out)
	return out, nil
}

// CekStatus cek login & saldo via HTTP murni (tanpa Chromium).
func (e *Engine) CekStatus() (bool, string) {
	ok, saldo, err := e.IsipCekStatus()
	if err != nil {
		return false, "Error"
	}
	return ok, saldo
}

// withBrowser buka chromium baru (profile sama), jalankan fn, tutup browser.
// Chrome tidak pernah nyangkut pegang profile (pola launch_persistent_context
// + close di bot.py lama).
func (e *Engine) withBrowser(fn func(page *rod.Page) (bool, string, error)) (bool, string, error) {
	l := applyLowmem(launcher.New().
		UserDataDir(e.userData).
		Headless(true).
		Leakless(false).
		Set("no-sandbox"))
	url, err := l.Launch()
	if err != nil {
		if bin := findChromium(); bin != "" {
			l2 := applyLowmem(launcher.New().
				Bin(bin).
				UserDataDir(e.userData).
				Headless(true).
				Leakless(false).
				Set("no-sandbox"))
			url, err = l2.Launch()
		}
		if err != nil {
			return false, "", fmt.Errorf("launch chromium: %w", err)
		}
	}
	b := rod.New().ControlURL(url)
	if err := b.Connect(); err != nil {
		return false, "", err
	}
	defer b.Close()
	page, err := b.Page(proto.TargetCreateTarget{URL: baseURL})
	if err != nil {
		return false, "", err
	}
	defer func() { _ = page.Close() }()
	if err := page.WaitLoad(); err != nil {
		return false, "", err
	}
	return fn(page)
}

func formatRp(n int64) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	return b.String()
}
