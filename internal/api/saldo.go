// Package api: monitor saldo isipulsa — kalau turun di bawah ambang
// (default Rp 20.000), notif ke admin via TgNotify. 1x per penurunan:
// gak spam ulang selama saldo masih di bawah & belum pernah naik di atas.
package api

import (
	"log"
	"strconv"
	"strings"
	"time"
)

const saldoMinDefault = 20000 // Rp

// parseSaldo "Rp 17.233" -> 17233. Gagal = -1.
func parseSaldo(s string) int64 {
	d := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	if d == "" {
		return -1
	}
	v, err := strconv.ParseInt(d, 10, 64)
	if err != nil {
		return -1
	}
	return v
}

// StartSaldoMonitor loop per 30 menit: cek saldo isipulsa, alert kalau < ambang.
func (a *API) StartSaldoMonitor() {
	go func() {
		// tunggu 2 menit pertama (biar login/browser siap, gak balapan saat boot)
		time.Sleep(2 * time.Minute)
		for {
			a.saldoTick()
			time.Sleep(30 * time.Minute)
		}
	}()
}

func (a *API) saldoTick() {
	ambang := int64(saldoMinDefault)
	if v := a.store.GetSetting("saldo_min", ""); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			ambang = n
		}
	}
	ok, saldoStr := a.eng.CekStatus()
	if !ok {
		return // login bermasalah — cookie reminder yg handle itu, jangan dobel notif
	}
	saldo := parseSaldo(saldoStr)
	if saldo < 0 {
		return // parse gagal — skip
	}
	sudahAlert := a.store.GetSetting("saldo_alert_aktif", "") == "1"
	if saldo < ambang {
		if sudahAlert {
			return // masih di bawah ambang & sudah pernah dinotif — tunggu naik
		}
		pesan := "💸 *Saldo isipulsa menipis*\n\n" +
			"Saldo: *" + saldoStr + "*\n" +
			"Ambang: Rp " + strconv.FormatInt(ambang, 10) + "\n\n" +
			"Top up deposit isipulsa sebelum order customer gagal."
		log.Printf("[SALDO] %s", strings.ReplaceAll(pesan, "\n", " | "))
		_ = a.store.SetSetting("saldo_alert_aktif", "1")
		a.TgNotify(pesan)
		return
	}
	// saldo cukup — reset flag biar penurunan berikutnya dinotif lagi
	if sudahAlert {
		_ = a.store.SetSetting("saldo_alert_aktif", "0")
		log.Printf("[SALDO] pulih: %s (>= ambang Rp %s)", saldoStr, strconv.FormatInt(ambang, 10))
	}
}
