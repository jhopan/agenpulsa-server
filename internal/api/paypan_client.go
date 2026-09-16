// Package api: klien Paypan (buat invoice, cek status) + config dari env.
// Ref: C:\Users\ACER\Documents\Project\PayPan\API.md
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jhopan/agenpulsa-server/internal/db"
)

// PaypanConfig konfigurasi dari env (bukan hardcode).
type PaypanConfig struct {
	BaseURL    string // PAYPAN_BASE_URL, mis. https://paypan.jhopan.my.id
	Token      string // PAYPAN_TOKEN (Bearer, scope order)
	Secret     string // PAYPAN_WEBHOOK_SECRET (verifikasi webhook, pp_...)
	WebhookURL string // PAYPAN_WEBHOOK_URL (didaftarkan manual di admin Paypan)
	Timeout    time.Duration
}

// LoadPaypanConfig baca dari env; kosong = fitur paypan mati (order manual tetap jalan).
func LoadPaypanConfig() PaypanConfig {
	return PaypanConfig{
		BaseURL:    strings.TrimRight(os.Getenv("PAYPAN_BASE_URL"), "/"),
		Token:      strings.TrimSpace(os.Getenv("PAYPAN_TOKEN")),
		Secret:     strings.TrimSpace(os.Getenv("PAYPAN_WEBHOOK_SECRET")),
		WebhookURL: strings.TrimSpace(os.Getenv("PAYPAN_WEBHOOK_URL")),
		Timeout:    30 * time.Second,
	}
}

// NewPaypanClientFromStore: token/base URL bisa dari settings DB (diisi via UI
// Pengaturan) — env menang kalau ada. Jadi rotate token gak perlu rebuild.
func NewPaypanClientFromStore(store *db.Store) *PaypanClient {
	cfg := LoadPaypanConfig()
	if cfg.BaseURL == "" {
		cfg.BaseURL = strings.TrimRight(store.GetSetting("paypan_base_url", ""), "/")
	}
	if cfg.Token == "" {
		cfg.Token = strings.TrimSpace(store.GetSetting("paypan_token", ""))
	}
	if cfg.Secret == "" {
		cfg.Secret = strings.TrimSpace(store.GetSetting("paypan_secret", ""))
	}
	return NewPaypanClient(cfg)
}

// Enabled true kalau base URL + token tersedia.
func (c PaypanConfig) Enabled() bool { return c.BaseURL != "" && c.Token != "" }

type paypanResp struct {
	OK    bool          `json:"ok"`
	Error string        `json:"error"`
	Data  PaypanInvoice `json:"data"`
}

// PaypanInvoice struktur invoice dari API Paypan.
type PaypanInvoice struct {
	ID        string `json:"id"`
	Price     int64  `json:"price"`
	Code      int64  `json:"code"`
	Total     int64  `json:"total"`
	Status    string `json:"status"` // pending|paid|expired|refunded
	ExpiresAt int64  `json:"expires_at"`
	PayURL    string `json:"pay_url"`
	QRURL     string `json:"qr_url"`
}

// PaypanClient klien HTTP Paypan. Config LIVE: base URL/token/secret dibaca
// dari settings DB tiap request (fallback env saat startup) — ubah di web
// admin -> Pengaturan langsung aktif, tanpa rebuild/restart.
type PaypanClient struct {
	store *db.Store
	http  *http.Client
}

func NewPaypanClient(cfg PaypanConfig) *PaypanClient {
	// cfg dipakai hanya utk timeout; nilai request dibaca live dari store.
	return &PaypanClient{http: &http.Client{Timeout: cfg.Timeout}}
}

// liveCfg: sumber kebenaran = settings DB (web admin). ENV hanya bootstrap
// awal: kalau DB masih kosong, seed dari env SEKALI lalu web admin yang pegang.
// Tidak ada overwrite dua arah — satu setting satu tempat edit.
func (c *PaypanClient) liveCfg(store *db.Store) PaypanConfig {
	seed := func(key, env string) {
		if strings.TrimSpace(store.GetSetting(key, "")) == "" && strings.TrimSpace(os.Getenv(env)) != "" {
			_ = store.SetSetting(key, strings.TrimSpace(os.Getenv(env)))
		}
	}
	seed("paypan_base_url", "PAYPAN_BASE_URL")
	seed("paypan_token", "PAYPAN_TOKEN")
	seed("paypan_secret", "PAYPAN_WEBHOOK_SECRET")
	return PaypanConfig{
		BaseURL: strings.TrimRight(store.GetSetting("paypan_base_url", ""), "/"),
		Token:   strings.TrimSpace(store.GetSetting("paypan_token", "")),
		Secret:  strings.TrimSpace(store.GetSetting("paypan_secret", "")),
		Timeout: c.http.Timeout,
	}
}

// CreateInvoice buat invoice QRIS. price min 1000, max 9000000.
// Return invoice + error. Total yang dibayar customer = price + code.
func (c *PaypanClient) CreateInvoice(store *db.Store, price int64, label string) (*PaypanInvoice, error) {
	cfg := c.liveCfg(store)
	if !cfg.Enabled() {
		return nil, fmt.Errorf("paypan belum dikonfigurasi (PAYPAN_BASE_URL/PAYPAN_TOKEN)")
	}
	if price < 1000 || price > 9000000 {
		return nil, fmt.Errorf("harga di luar batas paypan (1.000 - 9.000.000)")
	}
	body, _ := json.Marshal(map[string]any{"price": price, "label": label})
	req, err := http.NewRequest("POST", cfg.BaseURL+"/api/invoice", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	return c.do(req)
}

// GetInvoice cek status invoice (polling cadangan; utama = webhook).
func (c *PaypanClient) GetInvoice(store *db.Store, id string) (*PaypanInvoice, error) {
	cfg := c.liveCfg(store)
	req, err := http.NewRequest("GET", cfg.BaseURL+"/api/invoice/"+id, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	return c.do(req)
}

func (c *PaypanClient) do(req *http.Request) (*PaypanInvoice, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var pr paypanResp
	if err := json.Unmarshal(raw, &pr); err != nil {
		return nil, fmt.Errorf("paypan respons tidak JSON (HTTP %d): %s", resp.StatusCode, truncate(string(raw), 120))
	}
	if !pr.OK {
		return nil, fmt.Errorf("paypan: %s (HTTP %d)", pr.Error, resp.StatusCode)
	}
	return &pr.Data, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

var _ = strconv.Itoa
