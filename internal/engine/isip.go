// Package engine: jalur HTTP murni ke isipulsa via isip_api.py (requests +
// cookies profile). TANPA Chromium di jalur utama — bebas Cloudflare/Turnstile.
package engine

import (
	"encoding/json"
	"log"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type isipResult struct {
	OK     bool   `json:"ok"`
	Login  bool   `json:"login"`
	Saldo  string `json:"saldo"`
	Pesan  string `json:"pesan"`
	Jumlah int    `json:"jumlah"`
	Items  []struct {
		Voucher  string `json:"voucher"`
		Produk   string `json:"produk"`
		Operator string `json:"operator"`
		Nama     string `json:"nama"`
		Harga    int64  `json:"harga"`
	} `json:"items"`
	Sukses  bool   `json:"sukses"`
	OrderID string `json:"order_id"`
	Nama    string `json:"paket"`
	Harga   int64  `json:"harga"`
}

// runIsip panggil isip_api.py <args...>, parse JSON output terakhir.
func (e *Engine) runIsip(args ...string) (*isipResult, error) {
	py := "python"
	if runtime.GOOS != "windows" {
		py = "python3"
		if _, err := exec.LookPath("python3"); err != nil {
			py = "python"
		}
	}
	helper := filepath.Join("helper", "isip_api.py")
	if _, err := exec.LookPath(helper); err != nil {
		helper = "isip_api.py"
	}
	full := append([]string{helper}, args...) // subcommand dulu, --profile sesudahnya
	cmd := exec.Command(py, full...)
	outBytes, err := cmd.Output()
	if err != nil {
		// helper exit 1 saat gagal, tapi stdout tetap JSON yang valid — parse itu.
		var res isipResult
		if len(outBytes) > 0 && json.Unmarshal(outBytes, &res) == nil {
			log.Printf("[ISIP %v] ok=%v login=%v saldo=%q pesan=%q", args, res.OK, res.Login, res.Saldo, res.Pesan)
			return &res, nil
		}
		// bukan JSON: traceback/kena kill — lempar error asli.
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			log.Printf("[ISIP %v] stderr: %s", args, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("isip_api %v: %w", args, err)
	}
	var res isipResult
	if err := json.Unmarshal(outBytes, &res); err != nil {
		log.Printf("[ISIP %v] output: %s", args, strings.TrimSpace(string(outBytes)))
		return nil, fmt.Errorf("isip_api output bukan JSON: %s", strings.TrimSpace(string(outBytes)))
	}
	log.Printf("[ISIP %v] ok=%v login=%v saldo=%q pesan=%q", args, res.OK, res.Login, res.Saldo, res.Pesan)
	return &res, nil
}

// IsipCekStatus login + saldo via HTTP murni.
func (e *Engine) IsipCekStatus() (bool, string, error) {
	r, err := e.runIsip("cekstatus", "--profile", e.userData)
	if err != nil {
		return false, "Error", err
	}
	return r.Login, r.Saldo, nil
}

// IsipSearch cari paket dari daftar voucher halaman produk.
func (e *Engine) IsipSearch(tab, cari string, limit int) ([]map[string]string, error) {
	r, err := e.runIsip("search", "--profile", e.userData, "--tab", tab, "--cari", cari, "--limit", fmt.Sprint(limit))
	if err != nil {
		return nil, err
	}
	out := []map[string]string{}
	for _, it := range r.Items {
		out = append(out, map[string]string{
			"voucher":  it.Voucher,
			"produk":   it.Produk,
			"operator": it.Operator,
			"nama":     it.Nama,
			"harga":    fmt.Sprint(it.Harga),
		})
	}
	return out, nil
}

// IsipOrder order via POST form + csrf (tanpa browser).
// namaAsli = nama asli paket di isipulsa waktu katalog dibuat (anti-drift:
// kalau isipulsa rename/hapus paket, order dibatalkan atau fallback by nama).
func (e *Engine) IsipOrder(nomor, produk, voucher, namaAsli string, hargaMax int64) (sukses bool, pesan string, orderID string, modal int64) {
	args := []string{"order", "--profile", e.userData, "--nomor", nomor}
	if produk != "" {
		args = append(args, "--produk", produk)
	}
	if voucher != "" {
		args = append(args, "--voucher", voucher)
	}
	if namaAsli != "" {
		args = append(args, "--nama-asli", namaAsli)
	}
	if hargaMax > 0 {
		args = append(args, "--harga-max", fmt.Sprint(hargaMax))
	}
	r, err := e.runIsip(args...)
	if err != nil {
		return false, "ERROR: " + err.Error(), "", 0
	}
	if !r.OK && strings.Contains(r.Pesan, "kena Cloudflare") {
		return false, r.Pesan + " — coba inject cookies ulang dari browser asli.", "", 0
	}
	return r.Sukses, r.Pesan, r.OrderID, r.Harga
}

// IsipInject simpan cookie file JSON ke profile (dipakai semua operasi HTTP).
func (e *Engine) IsipInject(jsonFile string) (int, error) {
	r, err := e.runIsip("inject", "--profile", e.userData, "--json-file", jsonFile)
	if err != nil {
		return 0, err
	}
	return r.Jumlah, nil
}
