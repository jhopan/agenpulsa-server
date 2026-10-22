// Package api: backup order ke Google Sheet via Apps Script webhook (P8).
package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jhopan/agenpulsa-server/internal/db"
)

// backupSheetURL: URL Apps Script (doPost) yang menerima JSON order lalu
// append ke Google Sheet. Diset di web admin -> Pengaturan.
const settingBackupURL = "sheet_backup_url"
const settingBackupToken = "sheet_backup_token"

// StartBackupLoop: backup order ke Google Sheet tiap jam (hanya order baru
// sejak backup terakhir). Kalau URL belum diset -> diam.
func (a *API) StartBackupLoop() {
	go func() {
		for {
			time.Sleep(10 * time.Minute)
			a.backupTick()
		}
	}()
}

func (a *API) backupTick() {
	url := strings.TrimSpace(a.store.GetSetting(settingBackupURL, ""))
	if url == "" {
		return // belum diset
	}
	since := a.store.GetSetting("sheet_backup_last", "")
	if since == "" {
		since = db.NowWIB().AddDate(0, 0, -1).Format("2006-01-02 15:04:05")
	}
	orders, err := a.store.ListOrdersSince(since, 200)
	if err != nil {
		log.Printf("[BACKUP] ambil order: %v", err)
		return
	}
	if len(orders) == 0 {
		return
	}
	payload := map[string]any{
		"rows":  backupRows(orders),
		"token": a.store.GetSetting(settingBackupToken, ""),
		"wib":   db.NowWIB().Format("2006-01-02 15:04:05"),
	}
	body, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("[BACKUP] gagal kirim sheet: %v", err)
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("[BACKUP] sheet menolak (HTTP %d)", resp.StatusCode)
		return
	}
	// tandai sudah ter-backup (pakai created_at terakhir)
	last := orders[len(orders)-1].CreatedAt
	_ = a.store.SetSetting("sheet_backup_last", last)
	log.Printf("[BACKUP] %d order terkirim ke Google Sheet (sampai %s)", len(orders), last)
}

// backupRows: ubah order jadi baris CSV-ish untuk sheet.
func backupRows(orders []*db.Order) [][]string {
	out := make([][]string, 0, len(orders))
	for _, o := range orders {
		out = append(out, []string{
			o.CreatedAt, o.Ref, o.Nomor, o.Label,
			strconv.FormatInt(o.Modal, 10), strconv.FormatInt(o.HargaJual, 10),
			o.Status, o.Pesan, o.Sumber, o.InvoiceID,
		})
	}
	return out
}
