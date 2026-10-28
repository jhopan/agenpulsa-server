package api

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/jhopan/agenpulsa-server/internal/db"
)

// hargaDalamPesan: pola "Harga: Rp 4.550" yg nyisip di pesan order.
// Ini harga MODAL (harga beli isipulsa) — JANGAN keluar ke client.
var hargaDalamPesan = regexp.MustCompile(`(?i)\|?\s*Harga\s*:?\s*Rp\s*[\d.,]+`)
var pipeBersih = regexp.MustCompile(`\s*\|\s*\|\s*|\s*\|\s*$`)

// bersihkanPesanOrder: buang harga dari pesan order (defensif, buat order lama
// yang pesannya masih nyimpan harga modal). Dipakai di view client.
func bersihkanPesanOrder(pesan string) string {
	if pesan == "" {
		return ""
	}
	out := hargaDalamPesan.ReplaceAllString(pesan, "")
	out = pipeBersih.ReplaceAllString(out, "| ")
	out = strings.TrimSpace(out)
	out = strings.TrimSuffix(out, "|")
	return strings.TrimSpace(out)
}

// orderView: Order yang dikirim ke CLIENT (bot WA/Telegram).
//
// PENTING: field "modal" (harga modal / harga beli dari isipulsa) TIDAK
// boleh keluar ke client — itu rafel profit. Client cuma butuh:
// harga JUAL, status, dan pesan.
//
// Admin (session web atau API key admin) tetap dapat order lengkap
// (modal + profit) — buat laporan di web admin.
type orderView struct {
	Ref         string `json:"ref"`
	Nomor       string `json:"nomor"`
	Label       string `json:"label"`
	HargaJual   int64  `json:"harga_jual"`
	Status      string `json:"status"`
	OrderIDIsip string `json:"order_id_isipulsa"`
	Pesan       string `json:"pesan"`
	InvoiceID   string `json:"invoice_id"`
	ClientID    string `json:"client_id"`
	ChatID      string `json:"chat_id"`
	Sumber      string `json:"sumber"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// orderToView: konversi Order -> orderView (tanpa modal).
func orderToView(o *db.Order) orderView {
	return orderView{
		Ref:         o.Ref,
		Nomor:       o.Nomor,
		Label:       o.Label,
		HargaJual:   o.HargaJual,
		Status:      o.Status,
		OrderIDIsip: o.OrderIDIsip,
		Pesan:       bersihkanPesanOrder(o.Pesan),
		InvoiceID:   o.InvoiceID,
		ClientID:    o.ClientID,
		ChatID:      o.ChatID,
		Sumber:      o.Sumber,
		CreatedAt:   o.CreatedAt,
		UpdatedAt:   o.UpdatedAt,
	}
}

// ordersToViews: slice order -> slice view.
func ordersToViews(os []*db.Order) []orderView {
	out := make([]orderView, 0, len(os))
	for _, o := range os {
		out = append(out, orderToView(o))
	}
	return out
}

// RequesterBolehLihatModal: true kalau requester admin (session web atau
// API key dengan boleh_admin). Client biasa (bot) cuma boleh lihat view.
func (a *API) RequesterBolehLihatModal(r *http.Request) bool {
	if a.sessionAdmin(r) || a.adminKey(r) {
		return true
	}
	k, err := a.store.CheckAPIKey(r.Header.Get("X-API-Key"))
	if err != nil {
		return false
	}
	return k.BolehAdmin
}
