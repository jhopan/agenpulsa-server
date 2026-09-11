// Package api: pembelian terjadwal oleh customer.
// Flow: pilih katalog -> set jadwal sendiri -> pending_payment -> bayar QRIS
// -> webhook paypan -> jadwal dibuat otomatis -> scheduler eksekusi.
package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/jhopan/agenpulsa-server/internal/db"
)

type langgananReq struct {
	CatalogID int64  `json:"catalog_id"`
	Nomor     string `json:"nomor"`   // nomor HP tujuan kirim paket
	Tipe      string `json:"tipe"`    // harian|sekali|interval
	Jam       string `json:"jam"`     // HH:MM WIB
	Tanggal   string `json:"tanggal"` // DD/MM/YYYY (tipe sekali)
	Interval  int    `json:"interval_hari"`
	ChatID    string `json:"chat_id"` // opsional: receipt ke WA/TG customer
	Callback  string `json:"callback_url"`
}

// status jadwal langganan: simpan di schedules.aktif=1, dengan chat_id
// berisi ref order biar bisa dilacak asalnya.

// langgananBaru: customer set jadwal sendiri -> order pending_payment.
func (a *API) langgananBaru(w http.ResponseWriter, r *http.Request) {
	var req langgananReq
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
	if !it.Aktif {
		jsonErr(w, 400, "katalog tidak aktif")
		return
	}

	// validasi jadwal (pakai validator sama dengan admin)
	// LANGGANAN CUSTOMER HANYA SEKALI JALAN — tanpa harian/interval
	// (QRIS 1x bayar 1x jalan; berulang = ribet refund/expired)
	if req.Tipe != "sekali" {
		jsonErr(w, 400, "pembelian terjadwal hanya sekali jalan (tanggal+jam). Tidak ada berulang.")
		return
	}
	jr := &jadwalReq{
		Tipe: req.Tipe, Label: "langganan " + nomor, CatalogID: req.CatalogID,
		Nomor: nomor, Jam: req.Jam, Interval: req.Interval,
	}
	if req.Tipe == "sekali" {
		jr.Jam = strings.TrimSpace(req.Tanggal + " " + req.Jam)
	}
	if err := validJadwal(jr); err != nil {
		jsonErr(w, 400, err.Error())
		return
	}

	// ref unik untuk order + jadwal
	ref := "sub-" + randomHex(8)

	// simpan detail jadwal di pesan order (dipakai saat paid -> buat schedule)
	detail := map[string]string{
		"tipe": req.Tipe, "jam": jr.Jam,
		"interval_hari": strconv.Itoa(req.Interval),
	}
	detailJSON, _ := json.Marshal(detail)

	// invoice QRIS paypan (harga = harga_jual katalog)
	inv, err := a.ppClient.CreateInvoice(it.HargaJual, it.Label+" "+nomor+" (langganan)")
	if err != nil {
		jsonErr(w, 502, "gagal buat invoice paypan: "+err.Error())
		return
	}

	o := &db.Order{
		Ref:         ref,
		Nomor:       nomor,
		CatalogID:   req.CatalogID,
		Label:       it.Label,
		Modal:       it.HargaMax,
		HargaJual:   it.HargaJual,
		Status:      "pending_payment",
		InvoiceID:   inv.ID,
		Sumber:      "langganan",
		ChatID:      req.ChatID,
		CallbackURL: req.Callback,
		Pesan:       "jadwal: " + string(detailJSON),
	}
	if _, err := a.store.CreateOrder(o); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{
		"ref":        ref,
		"status":     "pending_payment",
		"invoice_id": inv.ID,
		"total":      inv.Total,
		"pay_url":    inv.PayURL,
		"qr_url":     inv.QRURL,
		"expires_at": inv.ExpiresAt,
		"pesan":      "bayar QRIS total " + strconv.FormatInt(inv.Total, 10) + " — jadwal aktif otomatis setelah paid",
	})
}

// langgananAktifkan: dipanggil dari paypan webhook saat payment.paid untuk
// order sumber "langganan" -> buat schedule dari detail di pesan order.
func (a *API) langgananAktifkan(o *db.Order) error {
	// parse detail jadwal dari pesan: jadwal: {"tipe":...,"jam":...}
	var detail map[string]string
	i := strings.Index(o.Pesan, "jadwal: ")
	if i < 0 {
		return errLanggananDetail
	}
	if err := json.Unmarshal([]byte(o.Pesan[i+8:]), &detail); err != nil {
		return errLanggananDetail
	}
	interval, _ := strconv.Atoi(detail["interval_hari"])
	sc := &db.Schedule{
		Tipe:      detail["tipe"],
		Label:     "LANGGANAN " + o.Nomor + " (" + o.Ref + ")",
		CatalogID: o.CatalogID,
		Nomor:     o.Nomor,
		Jam:       detail["jam"],
		Aktif:     true,
		ChatID:    o.ChatID,
	}
	if detail["tipe"] == "interval" {
		sc.IntervalHari = interval
		if sc.IntervalHari < 1 {
			sc.IntervalHari = 1
		}
	}
	_, err := a.store.UpsertSchedule(sc)
	if err != nil {
		return err
	}
	_ = a.store.UpdateOrderStatus(o.ID, "scheduled",
		"langganan aktif - jadwal "+detail["tipe"]+" @ "+detail["jam"]+" WIB", "")
	return nil
}

type langgananErr string

func (e langgananErr) Error() string { return string(e) }

const errLanggananDetail = langgananErr("detail jadwal tidak ditemukan di order")
