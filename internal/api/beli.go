// Package api: pembelian manual + jadwal (CRUD schedules).
package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/jhopan/agenpulsa-server/internal/db"
)

type beliReq struct {
	CatalogID int64  `json:"catalog_id"`
	Nomor     string `json:"nomor"`
	Ref       string `json:"ref"`
}

// beliManual: order dari web admin — LANGSUNG BUY (queued), pakai deposit isipulsa.
// Hanya USER (bot WA / web publik) yang lewat QRIS paypan (pending_payment -> bayar).
func (a *API) beliManual(w http.ResponseWriter, r *http.Request) {
	var req beliReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		jsonErr(w, 400, "json tidak valid")
		return
	}
	nomor := NormalizeNomor(req.Nomor)
	if nomor == "" {
		jsonErr(w, 400, "nomor tidak valid (butuh 08xxx / 62xxx)")
		return
	}
	if req.CatalogID <= 0 {
		jsonErr(w, 400, "catalog_id wajib")
		return
	}
	it, err := a.store.GetCatalog(req.CatalogID)
	if err != nil {
		jsonErr(w, 400, "katalog tidak ada")
		return
	}

	o := &db.Order{
		Ref:       req.Ref,
		Nomor:     nomor,
		CatalogID: req.CatalogID,
		Label:     it.Label,
		Modal:     it.HargaMax,
		HargaJual: it.HargaJual,
		Status:    "queued",
		Sumber:    "admin",
	}
	if o.Ref == "" {
		o.Ref = "adm-" + randomHex(8)
	}
	if err := a.eng.Enqueue(o); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{
		"ref":    o.Ref,
		"status": "queued",
		"pesan":  "order masuk queue — dibeli via saldo isipulsa",
	})
}

type jadwalReq struct {
	Tipe      string `json:"tipe"` // harian|sekali|interval
	Label     string `json:"label"`
	CatalogID int64  `json:"catalog_id"`
	Nomor     string `json:"nomor"`
	Jam       string `json:"jam"`           // HH:MM | DD/MM/YYYY HH:MM
	Interval  int    `json:"interval_hari"` // untuk tipe interval
}

func validJadwal(req *jadwalReq) error {
	req.Tipe = strings.TrimSpace(req.Tipe)
	req.Label = strings.TrimSpace(req.Label)
	req.Nomor = NormalizeNomor(req.Nomor)
	req.Jam = strings.TrimSpace(req.Jam)
	if req.Label == "" {
		return fmt.Errorf("label wajib")
	}
	if req.Nomor == "" {
		return fmt.Errorf("nomor tidak valid")
	}
	if req.CatalogID <= 0 {
		return fmt.Errorf("catalog_id wajib")
	}
	switch req.Tipe {
	case "harian":
		if !reHHMM.MatchString(req.Jam) {
			return fmt.Errorf("jam harian format HH:MM (mis. 07:30)")
		}
	case "sekali":
		if !reOnce.MatchString(req.Jam) {
			return fmt.Errorf("jam sekali format DD/MM/YYYY HH:MM (mis. 22/08/2026 00:00)")
		}
	case "interval":
		if req.Interval < 1 {
			return fmt.Errorf("interval minimal 1 hari")
		}
		if !reHHMM.MatchString(req.Jam) {
			return fmt.Errorf("jam interval format HH:MM (mis. 00:00)")
		}
	default:
		return fmt.Errorf("tipe harus harian|sekali|interval")
	}
	return nil
}

var (
	reHHMM = regexp.MustCompile(`^([01]?\d|2[0-3]):[0-5]\d$`)
	reOnce = regexp.MustCompile(`^\d{2}/\d{2}/\d{4} (?:[01]\d|2[0-3]):[0-5]\d$`)
)

// jadwalList: daftar semua jadwal.
func (a *API) jadwalList(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListSchedules()
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, items)
}

// jadwalAdd: tambah jadwal.
func (a *API) jadwalAdd(w http.ResponseWriter, r *http.Request) {
	var req jadwalReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		jsonErr(w, 400, "json tidak valid")
		return
	}
	if err := validJadwal(&req); err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	// pastikan katalog ada
	if _, err := a.store.GetCatalog(req.CatalogID); err != nil {
		jsonErr(w, 400, "katalog tidak ada")
		return
	}
	sc := &db.Schedule{
		Tipe:      req.Tipe,
		Label:     req.Label,
		CatalogID: req.CatalogID,
		Nomor:     req.Nomor,
		Jam:       req.Jam,
		Aktif:     true,
	}
	if req.Tipe == "interval" {
		sc.IntervalHari = req.Interval
	}
	id, err := a.store.UpsertSchedule(sc)
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	sc.ID = id
	writeJSON(w, 200, sc)
}

// jadwalToggle: aktif/nonaktif.
func (a *API) jadwalToggle(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	items, err := a.store.ListSchedules()
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	for _, sc := range items {
		if sc.ID == id {
			sc.Aktif = !sc.Aktif
			if _, err := a.store.UpsertSchedule(&sc); err != nil {
				jsonErr(w, 500, err.Error())
				return
			}
			writeJSON(w, 200, map[string]bool{"ok": true, "aktif": sc.Aktif})
			return
		}
	}
	jsonErr(w, 404, "jadwal tidak ada")
}

// jadwalDelete: hapus jadwal.
func (a *API) jadwalDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := a.store.DeleteSchedule(id); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
