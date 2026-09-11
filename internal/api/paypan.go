package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/jhopan/agenpulsa-server/internal/db"
)

// Paypan webhook: event payment.paid -> order pending_payment masuk queue.
// Secret disimpan di settings.key "paypan_secret". Signature header:
//
//	X-Paypan-Signature: hex(hmac_sha256(body, secret))
type paypanEvent struct {
	Event     string `json:"event"` // "payment.paid" | "payment.expired"
	InvoiceID string `json:"invoice_id"`
	Ref       string `json:"ref"` // ref order agenpulsa (= ref invoice paypan)
	Amount    int64  `json:"amount"`
	PaidAt    string `json:"paid_at"`
}

func (a *API) paypanWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) == 0 {
		jsonErr(w, 400, "body kosong")
		return
	}
	secret := a.store.GetSetting("paypan_secret", "")
	if secret == "" {
		jsonErr(w, 503, "paypan_secret belum diset (tambah di settings)")
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

	var ev paypanEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		jsonErr(w, 400, "json tidak valid")
		return
	}

	if ev.Ref == "" {
		jsonErr(w, 400, "ref kosong")
		return
	}
	o, err := a.store.GetOrderByRef(ev.Ref)
	if err != nil {
		jsonErr(w, 404, "order tidak ada untuk ref "+ev.Ref)
		return
	}

	switch ev.Event {
	case "payment.expired":
		if o.Status == "pending_payment" {
			_ = a.store.UpdateOrderStatus(o.ID, "cancelled", "pembayaran expired", "")
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
		return

	case "payment.paid":
	default:
		jsonErr(w, 400, "event tidak dikenal: "+ev.Event)
		return
	}

	// Idempotensi + validasi status.
	if o.Status != "pending_payment" {
		// Sudah diproses (webhook dobel): balas ok agar paypan berhenti retry.
		writeJSON(w, 200, map[string]any{"ok": true, "status": o.Status})
		return
	}

	// Validasi jumlah: harus sama dengan harga_jual (paypan pakai kode unik).
	if o.HargaJual > 0 && ev.Amount != o.HargaJual {
		_ = a.store.UpdateOrderStatus(o.ID, "pending_payment",
			"jumlah bayar "+strconv.FormatInt(ev.Amount, 10)+" != harga_jual "+strconv.FormatInt(o.HargaJual, 10)+" - butuh cek manual", "")
		jsonErr(w, 409, "jumlah tidak cocok, order tetap pending")
		return
	}

	// Order langganan: paid -> buat jadwal (BUKAN order langsung).
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

	_ = a.store.UpdateOrderStatus(o.ID, "queued", "pembayaran diterima (paypan invoice "+ev.InvoiceID+")", "")
	// Bangunkan worker.
	a.eng.Wake()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

var _ = db.NowWIB
