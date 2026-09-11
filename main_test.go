package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jhopan/agenpulsa-server/internal/db"
	"github.com/jhopan/agenpulsa-server/internal/engine"
	"github.com/jhopan/agenpulsa-server/internal/api"
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
