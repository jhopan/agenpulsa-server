// Package engine: buka Chromium via rod, eksekusi order isipulsa dari queue.
// Port dari bot.py (Playwright) — selektor & flow identik:
//   - tab: .form-tabs a (text)
//   - nomor: input[name="nomor_hp"] + blur
//   - paket: #nominal .row button[data-voucher=...]
//   - submit: jQuery $.post AJAX (bukan form.submit)
package engine

import (
	"fmt"
	"net/http"
	"log"
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
	store    *db.Store
	browser  *rod.Browser
	userData string
	mu       sync.Mutex // queue: satu eksekusi browser pada satu waktu
	wake     chan struct{}
	closed   bool
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

// browser lazy: start sekali, dipakai ulang semua order.
func (e *Engine) getBrowser() (*rod.Browser, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.browser != nil {
		return e.browser, nil
	}
	url, err := launcher.New().
		UserDataDir(e.userData).
		Headless(true).
		Set("no-sandbox").
		Launch()
	if err != nil {
		// Fallback: pakai chromium binary dari playwright cache (Armbian).
		if bin := findChromium(); bin != "" {
			url, err = launcher.New().
				Bin(bin).
				UserDataDir(e.userData).
				Headless(true).
				Set("no-sandbox").
				Launch()
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
	_ = modal
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

// runOrder: alur order sebenarnya di browser. Return (pesan, modal, orderID_isipulsa, sukses).
func (e *Engine) runOrder(o *db.Order) (string, int64, string, bool) {
	// Ambil katalog item bila ada (untuk voucher/cari/harga_max).
	item := &db.CatalogItem{Tab: "Paket Kuota", Cari: o.Label}
	if o.CatalogID > 0 {
		if it, err := e.store.GetCatalog(o.CatalogID); err == nil {
			item = it
		}
	}

	browser, err := e.getBrowser()
	if err != nil {
		return "ERROR: " + err.Error(), 0, "", false
	}

	page, err := browser.Page(proto.TargetCreateTarget{URL: baseURL})
	if err != nil {
		return "ERROR: " + err.Error(), 0, "", false
	}
	defer func() {
		_ = page.Close()
	}()

	if err := page.WaitLoad(); err != nil {
		return "ERROR: timeout buka isipulsa: " + err.Error(), 0, "", false
	}

	// Cek login: #header-signin bertuliskan Masuk = sesi habis.
	signin, _ := page.Element("#header-signin")
	if signin != nil {
		if txt, _ := signin.Text(); strings.Contains(txt, "Masuk") {
			return "GAGAL: sesi login habis. Login ulang via VNC / inject cookies.", 0, "", false
		}
	}

	// Klik tab produk.
	tab, err := page.ElementR(".form-tabs a", item.Tab)
	if err != nil {
		return fmt.Sprintf("GAGAL: tab %q tidak ada", item.Tab), 0, "", false
	}
	if err := tab.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return "ERROR klik tab: " + err.Error(), 0, "", false
	}

	// Isi nomor + blur agar daftar paket termuat.
	nomorInput, err := page.Element(`input[name="nomor_hp"]`)
	if err != nil {
		return "ERROR: input nomor tidak ada", 0, "", false
	}
	if err := nomorInput.SelectAllText(); err == nil {
		_ = nomorInput.Input(o.Nomor)
	}
	_ = nomorInput.Blur()

	if _, err := page.Elements("#nominal .row button"); err != nil {
		return "ERROR: daftar paket tidak muncul", 0, "", false
	}

	// Pilih paket: voucher dulu, fallback cari.
	var target *rod.Element
	if item.Voucher != "" {
		sel := fmt.Sprintf(`#nominal .row button[data-voucher="%s"]`, item.Voucher)
		if el, err := page.Element(sel); err == nil {
			target = el
		}
	}
	if target == nil && item.Cari != "" {
		btns, _ := page.Elements("#nominal .row button")
		for _, b := range btns {
			t, _ := b.Text()
			if strings.Contains(strings.ToLower(t), strings.ToLower(item.Cari)) {
				target = b
				break
			}
		}
	}
	if target == nil {
		return fmt.Sprintf("GAGAL: paket tidak ditemukan (voucher=%s, cari=%s)", item.Voucher, item.Cari), 0, "", false
	}
	if err := target.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return "ERROR klik paket: " + err.Error(), 0, "", false
	}

	// Payment method: saldo.
	pay, err := page.Element("#pilihpembayaran")
	if err == nil {
	_ = pay.Select([]string{"balance"}, true, rod.SelectorTypeCSSSector)
	}

	namaPaket, _ := target.Text()
	namaPaket = strings.Join(strings.Fields(namaPaket), " ")
	hargaEl, _ := page.Element("#harga h3")
	hargaStr := ""
	if hargaEl != nil {
		hargaStr, _ = hargaEl.Text()
	}
	modal := ParseHarga(hargaStr)

	// Guard harga naik.
	if item.HargaMax > 0 && modal > item.HargaMax {
		msg := fmt.Sprintf("ORDER DIBATALKAN: harga naik. Sekarang Rp %s, batas Rp %d. Perbarui katalog jika harga baru wajar.",
			formatRp(modal), item.HargaMax)
		return msg, modal, "", false
	}

	// Submit order via jQuery $.post (bukan form.submit — tombol submit men-shadow).
	res, err := page.Eval(`() => new Promise((resolve) => {
		var url = "https://isipulsa.web.id/" + jQuery('input[name="produk"]').val();
		jQuery.post(url, jQuery("#order_form").serialize(), function (data) {
			resolve(JSON.stringify(data));
		}).fail(function (xhr) {
			resolve(JSON.stringify({success: false, errors: ["HTTP " + xhr.status]}));
		});
	})`)
	if err != nil {
		return "ERROR submit AJAX: " + err.Error(), modal, "", false
	}
	raw := res.Value.String()

	if strings.Contains(raw, `"success":true`) {
		reID := regexp.MustCompile(`"id":\s*"?(\d+)"?`)
		id := ""
		if m := reID.FindStringSubmatch(raw); m != nil {
			id = m[1]
		}
		msg := fmt.Sprintf("ORDER SUKSES. Paket: %s | Harga: %s | ID: %s | https://isipulsa.web.id/history/view/%s",
			namaPaket, hargaStr, id, id)
		return msg, modal, id, true
	}
	// Ambil pesan error dari isipulsa.
	reErr := regexp.MustCompile(`"errors":\s*\[(.*?)\]`)
	pesan := "tidak diketahui"
	if m := reErr.FindStringSubmatch(raw); m != nil {
		pesan = strings.Trim(m[1], `"`)
	}
	return fmt.Sprintf("ORDER GAGAL. Paket: %s | Harga: %s | Alasan: %s", namaPaket, hargaStr, pesan), modal, "", false
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

// CekStatus cek login & saldo isipulsa. Port dari cek_status(). Return (loginOK, saldoStr).
func (e *Engine) CekStatus() (bool, string) {
	browser, err := e.getBrowser()
	if err != nil {
		return false, "Error"
	}
	page, err := browser.Page(proto.TargetCreateTarget{URL: baseURL})
	if err != nil {
		return false, "Error"
	}
	defer func() { _ = page.Close() }()
	if err := page.WaitLoad(); err != nil {
		return false, "Error"
	}
	header, err := page.Element("header")
	if err != nil {
		return false, "Error"
	}
	txt, _ := header.Text()
	if strings.Contains(txt, "Masuk") && !strings.Contains(txt, "Saldo") {
		return false, "-"
	}
	saldo := strings.TrimSpace(strings.ReplaceAll(txt, "Saldo Deposit", ""))
	if saldo == "" {
		saldo = "Rp 0"
	}
	return true, saldo
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
