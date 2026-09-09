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
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jhopan/agenpulsa-server/internal/db"
	"github.com/jhopan/agenpulsa-server/internal/engine"
)

type API struct {
	store *db.Store
	eng   *engine.Engine
	web   fs.FS
}

func New(store *db.Store, eng *engine.Engine, webFS embed.FS) *API {
	sub, _ := fs.Sub(webFS, "web")
	return &API{store: store, eng: eng, web: sub}
}

func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()

	// Web admin (static).
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

	// Paypan webhook (HMAC, tanpa API key).
	mux.HandleFunc("POST /api/webhooks/paypan", a.paypanWebhook)

	return mux
}

// ---------- auth ----------

type keyPerm struct {
	key  *db.APIKey
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
		if a.adminKey(r) {
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
	o.Status = "pending_payment"
	if err := a.eng.Enqueue(o); err != nil { // simpan row (status pending, worker skip)
		jsonErr(w, 500, err.Error())
		return
	}
	fresh, _ := a.store.GetOrderByRef(o.Ref)
	writeJSON(w, 202, fresh)
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
		"login": ok,
		"saldo": saldo,
		"wib":   db.NowWIB().Format("2006-01-02 15:04:05 WIB"),
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

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

var _ = time.Now
