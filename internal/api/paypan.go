// Package api: webhook Paypan — format ASLI dari API.md:
//   POST <url>   X-Paypan-Event: order.paid   X-Paypan-Signature: hex HMAC-SHA256(raw body)
//   {"event":"order.paid","order":{"id":"...","price":25000,"code":12,"total":25012,"paid_at":...}}
// Cocokkan ke order agenpulsa via invoice_id (disimpan saat create invoice).
package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
)

type paypanOrder struct {
	ID     string `json:"id"`
	Price  int64  `json:"price"`
	Code   int64  `json:"code"`
	Total  int64  `json:"total"`
	PaidAt int64  `json:"paid_at"`
}

type paypanHookPayload struct {
	Event string      `json:"event"`
	Order paypanOrder `json:"order"`
}

func (a *API) paypanWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) == 0 {
		jsonErr(w, 400, "body kosong")
		return
	}
	// secret webhook: env dulu, fallback settings DB.
	secret := a.ppCfg.Secret
	if secret == "" {
		secret = a.store.GetSetting("paypan_secret", "")
	}
	if secret == "" {
		jsonErr(w, 503, "paypan webhook secret belum diset")
		return
	}
	sig := r.Header.Get("X-Paypan-Signature")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expect := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expect)) {
		jsonErr(w, 403, "signature tidak valid")
		return
	}

	var ev paypanHookPayload
	if err := json.Unmarshal(body, &ev); err != nil {
		jsonErr(w, 400, "json tidak valid")
		return
	}
	if ev.Event != "order.paid" {
		// event lain diterima tanpa aksi (jangan bikin paypan retry terus)
		writeJSON(w, 200, map[string]any{"ok": true, "ignored": ev.Event})
		return
	}
	if ev.Order.ID == "" {
		jsonErr(w, 400, "order.id kosong")
		return
	}

	o, err := a.store.GetOrderByInvoice(ev.Order.ID)
	if err != nil {
		jsonErr(w, 404, "order tidak ada untuk invoice "+ev.Order.ID)
		return
	}

	// Idempotensi: webhook dobel / order sudah diproses -> ok (paypan berhenti retry).
	if o.Status != "pending_payment" {
		writeJSON(w, 200, map[string]any{"ok": true, "status": o.Status})
		return
	}

	// Order langganan: paid -> buat jadwal sekali-jalan (BUKAN order langsung).
	if o.Sumber == "langganan" {
		if err := a.langgananAktifkan(o); err != nil {
			_ = a.store.UpdateOrderStatus(o.ID, "pending_payment",
				"paid tapi gagal buat jadwal: "+err.Error()+" - butuh cek manual", "")
			jsonErr(w, 500, "gagal aktifkan jadwal, order tetap pending")
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true, "jadwal_dibuat": true})
		return
	}

	// Order manual biasa: paid -> masuk queue, worker eksekusi.
	_ = a.store.UpdateOrderStatus(o.ID, "queued",
		"pembayaran diterima (paypan invoice "+ev.Order.ID+", total "+strconv.FormatInt(ev.Order.Total, 10)+")", "")
	a.eng.Wake()
	writeJSON(w, 200, map[string]bool{"ok": true})
}
