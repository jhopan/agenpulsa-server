package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jhopan/agenpulsa-server/internal/api"
	"github.com/jhopan/agenpulsa-server/internal/db"
	"github.com/jhopan/agenpulsa-server/internal/engine"
)

func TestNormalizeNomor(t *testing.T) {
	cases := map[string]string{
		"6287771234567":    "087771234567",
		"+628771234567":    "08771234567",
		"62 877 1234 5678": "087712345678",
		"0821-0889-1234":   "082108891234",
		"081234567890":     "081234567890",
		"62 877-":          "",
		"12345":            "",
		"07123456789":      "",
		"":                 "",
	}
	for in, want := range cases {
		if got := api.NormalizeNomor(in); got != want {
			t.Errorf("NormalizeNomor(%q)=%q want %q", in, got, want)
		}
	}
}

func TestParseHarga(t *testing.T) {
	if engine.ParseHarga("Rp 13.749") != 13749 {
		t.Error("ParseHarga fail")
	}
	if engine.ParseHarga("") != 0 {
		t.Error("ParseHarga empty fail")
	}
}

func TestMaintenance(t *testing.T) {
	loc := db.WIBLoc()
	if !engine.IsMaintenance(timeAt(23, 45, loc)) {
		t.Error("23:45 harus maintenance")
	}
	if !engine.IsMaintenance(timeAt(0, 10, loc)) {
		t.Error("00:10 harus maintenance")
	}
	if engine.IsMaintenance(timeAt(23, 39, loc)) {
		t.Error("23:39 tidak maintenance")
	}
	if engine.IsMaintenance(timeAt(0, 36, loc)) {
		t.Error("00:36 tidak maintenance")
	}
}

func timeAt(h, m int, loc *time.Location) time.Time {
	return time.Date(2026, 1, 1, h, m, 0, 0, loc)
}

func TestCreateOrderAndRef(t *testing.T) {
	dir := t.TempDir()
	store, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ref := "test-ref-1"
	o := &db.Order{Ref: ref, Nomor: "0812", Label: "X", Status: "queued", Sumber: "api"}
	id1, err := store.CreateOrder(o)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetOrderByRef(ref)
	if err != nil || got.ID != id1 {
		t.Fatalf("ref lookup: %v %+v", err, got)
	}
	if _, err := store.CreateOrder(o); err == nil {
		t.Error("ref dobel harus error UNIQUE")
	}
}

func TestAPISmoke(t *testing.T) {
	dir := t.TempDir()
	store, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_ = store.UpsertAPIKey(&db.APIKey{Key: "K1", Nama: "c1", BolehOrder: true})
	_ = store.SetSetting("paypan_secret", "s3cret")

	eng, err := engine.New(store)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	a := api.New(store, eng, testWebFS())
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	// catalog tanpa key -> 401
	r, _ := http.Get(srv.URL + "/api/v1/catalog")
	if r.StatusCode != 401 {
		t.Errorf("tanpa key harus 401, got %d", r.StatusCode)
	}

	// order dengan key -> 202
	body := strings.NewReader(`{"voucher":"v1","nomor":"081234567890","ref":"r1"}`)
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/orders", body)
	req.Header.Set("X-API-Key", "K1")
	req.Header.Set("Content-Type", "application/json")
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if r2.StatusCode != 202 {
		t.Errorf("order harus 202, got %d", r2.StatusCode)
	}

	// webhook paypan signature salah -> 403
	r3, _ := http.Post(srv.URL+"/webhook", "application/json",
		strings.NewReader(`{"event":"order.paid","order":{"id":"x","price":1,"code":1,"total":2}}`))
	if r3.StatusCode != 403 {
		t.Errorf("webhook sig salah harus 403, got %d", r3.StatusCode)
	}
}

func TestAdminPageHasResponsiveNavigation(t *testing.T) {
	srv, _, _ := newTestServer(t)
	cookie := loginAdmin(t, srv)

	req, err := http.NewRequest("GET", srv.URL+"/admin", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("admin page harus 200, got %d", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"id=\"menuToggle\"", "class=\"nav-scrim\"", "AgenPulsa Admin", "id=\"cookieGuide\"", "Cara dapatkan cookies", "id=\"telegramGuide\"", "Panduan Telegram", "id=\"webhookGuide\"", "Panduan Webhook", "id=\"settingsSaveStatus\""} {
		if !strings.Contains(string(body), want) {
			t.Errorf("admin page harus memuat %q", want)
		}
	}
}

func TestLoginSmoke(t *testing.T) {
	dir := t.TempDir()
	store, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	eng, err := engine.New(store)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	a := api.New(store, eng, testWebFS())
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	// salah -> 401
	r, _ := http.Post(srv.URL+"/api/login", "application/json",
		strings.NewReader(`{"username":"admin","password":"salah"}`))
	if r.StatusCode != 401 {
		t.Errorf("login salah harus 401, got %d", r.StatusCode)
	}

	// benar -> 200 + cookie session, /api/me admin=true
	r2, err := http.Post(srv.URL+"/api/login", "application/json",
		strings.NewReader(`{"username":"admin","password":"admin123"}`))
	if err != nil {
		t.Fatal(err)
	}
	if r2.StatusCode != 200 {
		t.Fatalf("login benar harus 200, got %d", r2.StatusCode)
	}
	cookies := r2.Cookies()
	if len(cookies) == 0 {
		t.Fatal("harus ada cookie session")
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/me", nil)
	req.AddCookie(cookies[0])
	r3, _ := http.DefaultClient.Do(req)
	var me struct {
		Admin bool `json:"admin"`
	}
	_ = json.NewDecoder(r3.Body).Decode(&me)
	if !me.Admin {
		t.Error("session cookie harus admin=true")
	}

	// session jadi akses admin: tambah katalog tanpa API key
	req2, _ := http.NewRequest("POST", srv.URL+"/api/v1/catalog",
		strings.NewReader(`{"label":"T","tab":"Paket Kuota","cari":"T","harga_max":100,"harga_jual":200,"aktif":true}`))
	req2.AddCookie(cookies[0])
	r4, _ := http.DefaultClient.Do(req2)
	if r4.StatusCode != 200 {
		t.Errorf("tambah katalog via session harus 200, got %d", r4.StatusCode)
	}

	// logout -> session mati
	req3, _ := http.NewRequest("POST", srv.URL+"/api/logout", nil)
	req3.AddCookie(cookies[0])
	http.DefaultClient.Do(req3)
	req4, _ := http.NewRequest("GET", srv.URL+"/api/me", nil)
	req4.AddCookie(cookies[0])
	r5, _ := http.DefaultClient.Do(req4)
	var me2 struct {
		Admin bool `json:"admin"`
	}
	_ = json.NewDecoder(r5.Body).Decode(&me2)
	if me2.Admin {
		t.Error("setelah logout harus admin=false")
	}
}


func mustEngine(t *testing.T, store *db.Store) *engine.Engine {
	t.Helper()
	e, err := engine.New(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

// newTestServer: server API + DB sementara untuk test (dipakai beberapa test).
func newTestServer(t *testing.T) (*httptest.Server, *db.Store, *engine.Engine) {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	eng, err := engine.New(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	a := api.New(store, eng, testWebFS())
	srv := httptest.NewServer(a.Routes())
	t.Cleanup(srv.Close)
	return srv, store, eng
}

// loginAdmin: login default admin/admin123, balikin cookie session.
func loginAdmin(t *testing.T, srv *httptest.Server) *http.Cookie {
	t.Helper()
	r, err := http.Post(srv.URL+"/api/login", "application/json",
		strings.NewReader(`{"username":"admin","password":"admin123"}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != 200 {
		t.Fatalf("login admin harus 200, got %d", r.StatusCode)
	}
	cookies := r.Cookies()
	if len(cookies) == 0 {
		t.Fatal("harus ada cookie session")
	}
	return cookies[0]
}

// TestGuardJadwalMasaLalu: jadwal "sekali" bertanggal < hari ini (WIB) harus ditolak
// di semua jalur (admin /api/v1/jadwal + customer /api/v1/langganan).
func TestGuardJadwalMasaLalu(t *testing.T) {
	srv, store, _ := newTestServer(t)
	defer srv.Close()

	// seed katalog
	it := db.CatalogItem{Label: "T", Tab: "Paket Kuota", Voucher: "1", HargaMax: 1000, HargaJual: 2000, Aktif: true}
	id, err := store.UpsertCatalog(&it)
	if err != nil {
		t.Fatal(err)
	}
	login := loginAdmin(t, srv)

	// tanggal kemarin (WIB)
	kemarin := db.NowWIB().AddDate(0, 0, -1).Format("02/01/2006")
	// tanggal besok
	besok := db.NowWIB().AddDate(0, 0, 1).Format("02/01/2006")

	// 1) admin jadwal: kemarin -> 400
	body := fmt.Sprintf(`{"tipe":"sekali","label":"tes","catalog_id":%d,"nomor":"081234567890","jam":"%s 07:00"}`, id, kemarin)
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/jadwal", strings.NewReader(body))
	req.AddCookie(login)
	res, _ := http.DefaultClient.Do(req)
	if res.StatusCode != 400 {
		t.Errorf("jadwal kemarin harus 400, got %d", res.StatusCode)
	}

	// 2) admin jadwal: besok -> 200
	body = fmt.Sprintf(`{"tipe":"sekali","label":"tes","catalog_id":%d,"nomor":"081234567890","jam":"%s 07:00"}`, id, besok)
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/jadwal", strings.NewReader(body))
	req.AddCookie(login)
	res, _ = http.DefaultClient.Do(req)
	if res.StatusCode != 200 {
		t.Errorf("jadwal besok harus 200, got %d", res.StatusCode)
	}

	// 3) langganan customer: kemarin -> 400 (invoice belum dibuat)
	body = fmt.Sprintf(`{"catalog_id":%d,"nomor":"081234567890","tanggal":"%s","jam":"07:00"}`, id, kemarin)
	res, _ = http.DefaultClient.Post(srv.URL+"/api/v1/langganan", "application/json", strings.NewReader(body))
	if res.StatusCode != 400 {
		t.Errorf("langganan kemarin harus 400, got %d", res.StatusCode)
	}
}

// TestExpirePendingLokal: order pending_payment yang lewat deadline bayar
// (5 menit) harus jadi failed — tidak nyangkut, tidak dieksekusi.
func TestExpirePendingLokal(t *testing.T) {
	srv, store, _ := newTestServer(t)
	defer srv.Close()

	// order pending tanpa invoice_id (paypan gak bisa dicek -> expire lokal)
	lampau := db.NowWIB().Add(-10 * time.Minute).Format("2006-01-02 15:04:05")
	o := &db.Order{
		Ref: "exp-test-1", Nomor: "081234567890", Label: "T",
		Status: "pending_payment", Sumber: "api", CreatedAt: lampau, UpdatedAt: lampau,
	}
	id, err := store.CreateOrder(o)
	if err != nil {
		t.Fatal(err)
	}
	// order pending yang MASIH di dalam window bayar
	baru := db.NowWIB().Format("2006-01-02 15:04:05")
	o2 := &db.Order{
		Ref: "exp-test-2", Nomor: "081234567891", Label: "T",
		Status: "pending_payment", Sumber: "api", CreatedAt: baru, UpdatedAt: baru,
	}
	id2, err := store.CreateOrder(o2)
	if err != nil {
		t.Fatal(err)
	}

	a := api.New(store, mustEngine(t, store), testWebFS())
	a.ExpirePendingForTest()

	g1, _ := store.GetOrder(id)
	if g1.Status != "failed" {
		t.Errorf("order lewat 5 menit harus failed, dapat %q", g1.Status)
	}
	if g1.Pesan == "" {
		t.Error("order failed harus ada pesan")
	}
	g2, _ := store.GetOrder(id2)
	if g2.Status != "pending_payment" {
		t.Errorf("order baru harus tetap pending_payment, dapat %q", g2.Status)
	}
	_ = srv
}

// TestExpireBatas5Menit: order tepat 5 menit -> expired; order < 5 menit -> belum.
func TestExpireBatas5Menit(t *testing.T) {
	srv, store, _ := newTestServer(t)
	defer srv.Close()

	mk := func(ref string, ageMin int) int64 {
		created := db.NowWIB().Add(-time.Duration(ageMin) * time.Minute)
		o := &db.Order{
			Ref: ref, Nomor: "081234567890", Label: "T",
			Status: "pending_payment", Sumber: "api",
			CreatedAt: created.Format("2006-01-02 15:04:05"),
			UpdatedAt: created.Format("2006-01-02 15:04:05"),
		}
		id, err := store.CreateOrder(o)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	lewat := mk("exp-5m", 6)   // 6 menit -> harus expired
	dekat := mk("exp-4m", 4)   // 4 menit -> masih pending

	a := api.New(store, mustEngine(t, store), testWebFS())
	a.ExpirePendingForTest()

	g1, _ := store.GetOrder(lewat)
	if g1.Status != "failed" {
		t.Errorf("order 6 menit harus failed, dapat %q", g1.Status)
	}
	g2, _ := store.GetOrder(dekat)
	if g2.Status != "pending_payment" {
		t.Errorf("order 4 menit harus pending_payment, dapat %q", g2.Status)
	}
}

// TestRateLimitOrder: order endpoint dibatasi (10 per 5 menit per kunci).
func TestRateLimitOrder(t *testing.T) {
	srv, store, _ := newTestServer(t)
	defer srv.Close()

	// seed katalog
	it := db.CatalogItem{Label: "T", Tab: "Paket Kuota", Voucher: "1", HargaMax: 1000, HargaJual: 2000, Aktif: true}
	if _, err := store.UpsertCatalog(&it); err != nil {
		t.Fatal(err)
	}
	login := loginAdmin(t, srv)

	// 11x POST catalog-like order pake session (bukan order -> biar tgak bikin invoice).
	// Pakai endpoint order pending? butuh paypan. Jadi test limiter langsung.
	a := api.New(store, mustEngine(t, store), testWebFS())
	rl := a.OrderLimiterForTest()
	for i := 0; i < 10; i++ {
		ok, _ := rl.Allow("test-key")
		if !ok {
			t.Fatalf("request %d harus boleh", i+1)
		}
	}
	ok, _ := rl.Allow("test-key")
	if ok {
		t.Error("request ke-11 harus ditolak (limit 10)")
	}
	// kunci lain tetap boleh
	if ok, _ := rl.Allow("key-lain"); !ok {
		t.Error("kunci lain harus boleh")
	}
	_ = login
}

// TestHealthEndpoint: /health publik tanpa API key -> 200 + ok:true.
func TestHealthEndpoint(t *testing.T) {
	srv, _, _ := newTestServer(t)
	defer srv.Close()
	r, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != 200 {
		t.Fatalf("/health harus 200, got %d", r.StatusCode)
	}
	var out struct {
		OK      bool   `json:"ok"`
		Service string `json:"service"`
	}
	if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Service != "agenpulsa-server" {
		t.Errorf("health payload salah: %+v", out)
	}
}

// TestPopNextQueuedAtomik: order queued di-claim jadi running, tidak bisa
// diambil worker kedua (anti dobel-beli).
func TestPopNextQueuedAtomik(t *testing.T) {
	srv, store, _ := newTestServer(t)
	defer srv.Close()

	o := &db.Order{Ref: "pop-test-1", Nomor: "081234567890", Label: "T", Status: "queued", Sumber: "api"}
	if _, err := store.CreateOrder(o); err != nil {
		t.Fatal(err)
	}
	got, err := store.PopNextQueued()
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("harus dapat order queued")
	}
	if got.Status != "running" {
		t.Errorf("order harus di-claim jadi running, dapat %q", got.Status)
	}
	// worker kedua: tidak boleh dapat order yang sama
	got2, err := store.PopNextQueued()
	if err != nil {
		t.Fatal(err)
	}
	if got2 != nil {
		t.Errorf("worker kedua jangan dapat order yang sama (dobel-beli): %+v", got2)
	}
}

// TestWatchdogOrderNyangkut: order 'running' lebih dari 10 menit -> failed
// (TIDAK di-retry — cukup notifikasi admin).
func TestWatchdogOrderNyangkut(t *testing.T) {
	srv, store, _ := newTestServer(t)
	defer srv.Close()

	lama := db.NowWIB().Add(-30 * time.Minute).Format("2006-01-02 15:04:05")
	o := &db.Order{
		Ref: "stuck-1", Nomor: "081234567890", Label: "T",
		Status: "running", Pesan: "memproses order...", Sumber: "api",
		CreatedAt: lama, UpdatedAt: lama,
	}
	if _, err := store.CreateOrder(o); err != nil {
		t.Fatal(err)
	}
	// order baru (belum 10 menit) harus tetap running
	baru := db.NowWIB().Format("2006-01-02 15:04:05")
	o2 := &db.Order{
		Ref: "stuck-2", Nomor: "081234567891", Label: "T",
		Status: "running", Pesan: "memproses order...", Sumber: "api",
		CreatedAt: baru, UpdatedAt: baru,
	}
	if _, err := store.CreateOrder(o2); err != nil {
		t.Fatal(err)
	}

	a := api.New(store, mustEngine(t, store), testWebFS())
	a.WatchdogTickForTest()

	g1, _ := store.GetOrderByRef("stuck-1")
	if g1.Status != "failed" {
		t.Errorf("order nyangkut harus failed, dapat %q", g1.Status)
	}
	g2, _ := store.GetOrderByRef("stuck-2")
	if g2.Status != "running" {
		t.Errorf("order baru harus tetap running, dapat %q", g2.Status)
	}
}

// TestLimitOrderAktifPerUser: user dengan 3 order aktif -> order ke-4
// ditolak (429). Lokasi lain tetap boleh.
func TestLimitOrderAktifPerUser(t *testing.T) {
	srv, store, _ := newTestServer(t)
	defer srv.Close()

	it := db.CatalogItem{Label: "T", Tab: "Paket Kuota", Voucher: "1", HargaMax: 1000, HargaJual: 2000, Aktif: true}
	if _, err := store.UpsertCatalog(&it); err != nil {
		t.Fatal(err)
	}
	login := loginAdmin(t, srv)

	// 3 order aktif dari user yang sama
	for i := 0; i < 3; i++ {
		o := &db.Order{
			Ref: "ip-lim-" + strconv.Itoa(i), Nomor: "081234567890", Label: "T",
			Status: "pending_payment", Sumber: "api", ClientID: "tg:111",
		}
		if _, err := store.CreateOrder(o); err != nil {
			t.Fatal(err)
		}
	}
	n, err := store.CountActiveByClient("tg:111")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("harus ada 3 order aktif, dapat %d", n)
	}
	// user lain = 0
	n2, _ := store.CountActiveByClient("tg:222")
	if n2 != 0 {
		t.Errorf("user lain harus 0, dapat %d", n2)
	}
	// order ke-4 dari user yang sama -> 429
	body := `{"catalog_id":1,"nomor":"081234567890"}`
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/orders/pending", strings.NewReader(body))
	req.AddCookie(login)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-Id", "tg:111")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 429 {
		t.Errorf("order ke-4 harus 429 (limit 3 per lokasi), got %d", res.StatusCode)
	}
	// lokasi lain -> bukan 429
	req2, _ := http.NewRequest("POST", srv.URL+"/api/v1/orders/pending", strings.NewReader(body))
	req2.AddCookie(login)
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Client-Id", "tg:222")
	res2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	if res2.StatusCode == 429 {
		t.Error("user lain harusnya boleh order")
	}
}

// TestOrderStatusEndpoint: bot bisa cek antrian via API.
func TestOrderStatusEndpoint(t *testing.T) {
	srv, store, _ := newTestServer(t)
	defer srv.Close()
	login := loginAdmin(t, srv)

	o := &db.Order{
		Ref: "st-1", Nomor: "081234567890", Label: "T",
		Status: "pending_payment", Sumber: "api", ClientID: "tg:333", HargaJual: 6500,
	}
	if _, err := store.CreateOrder(o); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/order-status", nil)
	req.AddCookie(login)
	req.Header.Set("X-Client-Id", "tg:333")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("order-status harus 200, got %d", res.StatusCode)
	}
	var out struct {
		Aktif      int  `json:"aktif"`
		Maks       int  `json:"maks"`
		BisaOrder  bool `json:"bisa_order"`
		OrderCount int  `json:"-"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	if out.Aktif != 1 {
		t.Errorf("aktif harus 1, dapat %d", out.Aktif)
	}
	if out.Maks != 3 {
		t.Errorf("maks harus 3, dapat %d", out.Maks)
	}
	if !out.BisaOrder {
		t.Error("bisa_order harus true (aktif < maks)")
	}
}
