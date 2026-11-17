// Package engine: jalur HTTP murni ke isipulsa via isip_api.py (requests +
// cookies jar). TANPA Chromium — bebas Cloudflare/Turnstile.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// isipTimeout: batas waktu panggil helper isip_api.py. Tanpa timeout, satu
// order yang nge-hang (mis. Cloudflare challenge) memblokir SELURUH worker —
// queue tertahan, order lain menumpuk.
const isipTimeout = 120 * time.Second

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

// helperPath: cari isip_api.py. Dicek relatif ke CWD, ke binary, lalu naik
// dari CWD (go run / go test jalan di subfolder, bukan root repo).
func helperPath() string {
	cands := []string{filepath.Join("helper", "isip_api.py")}
	if exe, err := os.Executable(); err == nil {
		cands = append(cands, filepath.Join(filepath.Dir(exe), "helper", "isip_api.py"))
	}
	if wd, err := os.Getwd(); err == nil {
		for d := wd; ; d = filepath.Dir(d) {
			cands = append(cands, filepath.Join(d, "helper", "isip_api.py"))
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	for _, c := range cands {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "isip_api.py"
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
	helper := helperPath()
	full := append([]string{helper}, args...) // subcommand dulu, opsi sesudahnya
	// Context + timeout + kill process group: python/requests yg nge-hang
	// harus dibunuh, bukan nunggu selamanya.
	ctx, cancel := context.WithTimeout(context.Background(), isipTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, full...)
	setProcGroup(cmd)
	outBytes, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		log.Printf("[ISIP %v] TIMEOUT %s — helper dibunuh", args, isipTimeout)
		return nil, fmt.Errorf("isip_api timeout %s (helper macet, sudah dibunuh)", isipTimeout)
	}
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
	login, saldo, _, err := e.IsipCekStatusDetail()
	return login, saldo, err
}

// IsipCekStatusDetail: seperti IsipCekStatus + alasan dari helper (mis.
// "tidak ada cookie di profile", "kena Cloudflare challenge") supaya bisa
// ditampilkan ke admin saat inject cookies gagal login.
func (e *Engine) IsipCekStatusDetail() (login bool, saldo, pesan string, err error) {
	r, err := e.runIsip("cekstatus", "--cookies", e.cookieFile)
	if err != nil {
		return false, "Error", err.Error(), err
	}
	return r.Login, r.Saldo, r.Pesan, nil
}

// IsipSearch cari paket dari daftar voucher halaman produk.
func (e *Engine) IsipSearch(tab, cari string, limit int) ([]map[string]string, error) {
	r, err := e.runIsip("search", "--cookies", e.cookieFile, "--tab", tab, "--cari", cari, "--limit", fmt.Sprint(limit))
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
// operatorPaket = provider paket katalog (guard: prefix nomor harus cocok).
func (e *Engine) IsipOrder(nomor, produk, voucher, namaAsli, operatorPaket string, hargaMax int64) (sukses bool, pesan string, orderID string, modal int64) {
	args := []string{"order", "--cookies", e.cookieFile, "--nomor", nomor}
	if produk != "" {
		args = append(args, "--produk", produk)
	}
	if voucher != "" {
		args = append(args, "--voucher", voucher)
	}
	if namaAsli != "" {
		args = append(args, "--nama-asli", namaAsli)
	}
	if operatorPaket != "" {
		args = append(args, "--operator", operatorPaket)
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

// IsipTimeoutSeconds: batas waktu helper dalam detik (untuk monitoring/test).
func IsipTimeoutSeconds() int { return int(isipTimeout.Seconds()) }
