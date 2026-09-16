// Package api: monitor saldo isipulsa — kalau turun di bawah ambang
// (default Rp 20.000), notif ke admin via TgNotify. 1x per penurunan:
// gak spam ulang selama saldo masih di bawah & belum pernah naik di atas.
package api

import (
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/jhopan/agenpulsa-server/internal/db"
)

const saldoMinDefault = 20000  // Rp
const saldoNotifJamDefault = 1 // ulang notif tiap N jam selama masih di bawah ambang

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

// saldoIntervalDefault jeda antar cek saldo (menit). Bisa diubah live via
// settings `saldo_interval_menit` (web admin → Pengaturan).
const saldoIntervalDefault = 30

// StartSaldoMonitor loop cek saldo isipulsa, alert kalau < ambang.
// Interval dibaca dari settings tiap loop — ubah di web langsung kepakai.
func (a *API) StartSaldoMonitor() {
	go func() {
		// tunggu 2 menit pertama (biar login/browser siap, gak balapan saat boot)
		time.Sleep(2 * time.Minute)
		for {
			a.saldoTick()
			time.Sleep(time.Duration(a.saldoIntervalMenit()) * time.Minute)
		}
	}()
}

// saldoIntervalMenit baca settings saldo_interval_menit; invalid/0 = default.
func (a *API) saldoIntervalMenit() int {
	v := a.store.GetSetting("saldo_interval_menit", "")
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 5 || n > 1440 { // batas wajar: 5 menit - 24 jam
		return saldoIntervalDefault
	}
	return n
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
		// masih di bawah ambang & sudah pernah dinotif — tunggu naik.
		// TAPI: kalau terakhir dinotif sudah lewat `saldo_notif_jam` (default 1
		// jam), kirim ulang — saldo gak kunjung di-top up, remind lagi.
		ulangJam := int64(saldoNotifJamDefault)
		if v := a.store.GetSetting("saldo_notif_jam", ""); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				ulangJam = n
			}
		}
		if !sudahAlert {
			a.kirimSaldoAlert(saldoStr, ambang)
			return
		}
		if last := a.store.GetSetting("saldo_alert_at", ""); last != "" {
			if t, err := time.ParseInLocation("2006-01-02 15:04:05", last, db.WIBLoc()); err == nil {
				if db.NowWIB().Sub(t) >= time.Duration(ulangJam)*time.Hour {
					a.kirimSaldoAlert(saldoStr, ambang)
				}
			}
		} else {
			a.kirimSaldoAlert(saldoStr, ambang)
		}
		return
	}
	// saldo cukup — reset flag biar penurunan berikutnya dinotif lagi
	if sudahAlert {
		_ = a.store.SetSetting("saldo_alert_aktif", "0")
		_ = a.store.SetSetting("saldo_alert_at", "")
		log.Printf("[SALDO] pulih: %s (>= ambang Rp %d)", saldoStr, ambang)
	}
}

// kirimSaldoAlert kirim notif + set flag (dipanggil 1x per penurunan, ulang 24 jam).
func (a *API) kirimSaldoAlert(saldoStr string, ambang int64) {
	pesan := "💸 *Saldo isipulsa menipis*\n\n" +
		"Saldo: *" + saldoStr + "*\n" +
		"Ambang: Rp " + strconv.FormatInt(ambang, 10) + "\n\n" +
		"Top up deposit isipulsa sebelum order customer gagal."
	log.Printf("[SALDO] %s", strings.ReplaceAll(pesan, "\n", " | "))
	_ = a.store.SetSetting("saldo_alert_aktif", "1")
	_ = a.store.SetSetting("saldo_alert_at", db.NowWIB().Format("2006-01-02 15:04:05"))
	a.TgNotify(pesan)
}
