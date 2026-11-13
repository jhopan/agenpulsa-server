// Package api: monitor cookies isipulsa — notif cookie mau expired (7 hari
// sebelum) + notif cookie invalid (max 1x per 3 hari, biar gak spam).
//
// Dua aturan (dari user):
//  1. Info aja kalau cookie mau expired — reminder 7 hari sebelum tanggal
//     expired (bukan tiap jam spam).
//  2. Kalau cookie invalid (login gagal) — notif max 1x per 3 hari.
//
// Semua timing pakai settings DB, live tanpa restart.
package api

import (
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/jhopan/agenpulsa-server/internal/db"
)

const (
	// cookieExpiryWarnHari: notif kalau cookie mau expired dalam N hari.
	cookieExpiryWarnHari = 7
	// cookieInvalidNotifHari: jeda minimal 2 notif "cookie invalid" (hari).
	// 3 hari = 1x per 3 hari.
	cookieInvalidNotifHari = 3
	// cookieMonitorIntervalMenit: frekuensi cek cookie (menit).
	cookieMonitorIntervalMenit = 60
)

// StartCookieMonitor loop: cek expiry cookie + status login.
// Notif expired (7 hari sebelum) + notif invalid (max 1x / 3 hari).
func (a *API) StartCookieMonitor() {
	go func() {
		time.Sleep(90 * time.Second) // biar boot selesai dulu
		for {
			a.cookieMonitorTick()
			time.Sleep(cookieMonitorIntervalMenit * time.Second)
		}
	}()
}

// cookieMonitorTick: 1 siklus monitor cookie.
func (a *API) cookieMonitorTick() {
	expUnix, jumlah := a.eng.CookieExpiry()

	// --- 1. Cookie expired? (7 hari sebelum) ---
	if expUnix > 0 {
		sisa := time.Until(time.Unix(expUnix, 0))
		if sisa <= 0 {
			// sudah expired — biarkan notif "invalid" yang handle
			a.kirimCookieInvalid("cookie isipulsa sudah EXPIRED (tenggat lewat)")
			return
		}
		hari := int(sisa.Hours() / 24)
		if hari <= cookieExpiryWarnHari {
			// notif 1x per hari (kunci tanggal) — biar gak spam tiap jam
			key := "cookie_expiry_notif_" + db.NowWIB().Format("2006-01-02")
			sudah := a.store.GetSetting(key, "") == "1"
			if !bolehNotifExpiry(hari, sudah) {
				return
			}
			_ = a.store.SetSetting(key, "1")
			// bersihkan key hari-hari sebelumnya biar settings gak numpuk
			for i := 1; i <= 3; i++ {
				_ = a.store.SetSetting("cookie_expiry_notif_"+db.NowWIB().AddDate(0, 0, -i).Format("2006-01-02"), "")
			}
			pesan := fmt.Sprintf(
				"⏳ *Cookies isipulsa mau expired*\n\n"+
					"Tinggal *%d hari* lagi (%s).\n"+
					"Total cookie di profile: %d\n\n"+
					"Segera inject cookies baru di web admin → Akun Login, "+
					"supaya order gak gagal tengah malam.",
				hari, db.NowWIB().Add(time.Duration(hari)*24*time.Hour).Format("2006-01-02"),
				jumlah,
			)
			log.Printf("[COOKIE EXPIRY] %d hari lagi", hari)
			a.TgNotify(pesan)
		}
		return
	}

	// --- 2. Cookie invalid / login gagal (max 1x per 3 hari) ---
	ok, _ := a.eng.CekStatus()
	if ok {
		// login normal — reset flag biar_notif invalid lagi bisa terkirim
		_ = a.store.SetSetting("cookie_invalid_notif_at", "")
		return
	}
	a.kirimCookieInvalid("login isipulsa gagal — cookie tidak valid / sesi habis")
}

// bolehNotifInvalid: true kalau boleh kirim notif "cookie invalid".
// Aturan: max 1x per cookieInvalidNotifHari (3 hari).
// lastNotif = setting "cookie_invalid_notif_at" (format WIB), "" = belum pernah.
func bolehNotifInvalid(lastNotif string, now time.Time) bool {
	if lastNotif == "" {
		return true
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", lastNotif, db.WIBLoc())
	if err != nil {
		return true // format rusak -> boleh (mending spam daripada diam)
	}
	return now.Sub(t) >= cookieInvalidNotifHari*24*time.Hour
}

// bolehNotifExpiry: true kalau boleh kirim notif "cookie mau expired".
// Aturan: 1x per hari (kunci tanggal di settings), dan hanya kalau sisa <= 7 hari.
func bolehNotifExpiry(sisaHari int, sudahNotifHariIni bool) bool {
	if sisaHari > cookieExpiryWarnHari {
		return false // belum mendekati expired
	}
	return !sudahNotifHariIni
}

// kirimCookieInvalid: notif cookie invalid, max 1x per 3 hari.
func (a *API) kirimCookieInvalid(alasan string) {
	last := a.store.GetSetting("cookie_invalid_notif_at", "")
	if !bolehNotifInvalid(last, db.NowWIB()) {
		return // masih dalam 3 hari — jangan spam
	}
	_ = a.store.SetSetting("cookie_invalid_notif_at", db.NowWIB().Format("2006-01-02 15:04:05"))
	log.Printf("[COOKIE INVALID] %s", alasan)
	a.TgNotify("⚠️ *Cookies isipulsa bermasalah*\n\n" +
		"Alasan: " + alasan + "\n\n" +
		"Sesi login isipulsa gak bisa dipakai — order bakal GAGAL.\n" +
		"Segera inject cookies baru di web admin → Akun Login.\n" +
		"(Notifikasi ini max 1x per " + strconv.Itoa(cookieInvalidNotifHari) + " hari.)")
}

// CookieStatusExtra: info tambahan cookie buat web admin + bot poll.
type CookieStatusExtra struct {
	ExpireUnix   int64  `json:"expire_unix"`
	ExpireWIB    string `json:"expire_wib"`
	SisaHari     int    `json:"sisa_hari"`
	JumlahCookie int    `json:"jumlah_cookie"`
	InvalidSince string `json:"invalid_notif_at,omitempty"`
}

// cookieStatusExtra: kumpulkan info expiry cookie.
func (a *API) cookieStatusExtra() CookieStatusExtra {
	out := CookieStatusExtra{}
	exp, n := a.eng.CookieExpiry()
	out.ExpireUnix = exp
	out.JumlahCookie = n
	if exp > 0 {
		t := time.Unix(exp, 0).In(db.WIBLoc())
		out.ExpireWIB = t.Format("2006-01-02 15:04")
		out.SisaHari = int(time.Until(t).Hours() / 24)
	}
	out.InvalidSince = a.store.GetSetting("cookie_invalid_notif_at", "")
	return out
}

// cookiePesan: pesan status cookie (dipakai bot poll).
func (a *API) cookiePesan(umur float64) string {
	extra := a.cookieStatusExtra()
	if extra.ExpireUnix > 0 {
		if extra.SisaHari <= 0 {
			return "⚠️ Cookies isipulsa SUDAH EXPIRED (" + extra.ExpireWIB + "). Inject cookies baru sekarang."
		}
		return fmt.Sprintf("⏳ Cookies isipulsa expire %s (tinggal %d hari). Total %d cookie.",
			extra.ExpireWIB, extra.SisaHari, extra.JumlahCookie)
	}
	return cookiePesanLama(umur)
}

// cookiePesanLama: fallback kalau expiry gak bisa dibaca (mis. session cookie).
func cookiePesanLama(umur float64) string {
	if umur < 0 {
		return "Belum ada cookies tercatat — inject cookies di web admin -> Akun Login."
	}
	hari := int(umur / 24)
	if umur >= cookieReminderJam {
		return fmt.Sprintf("⚠️ Cookies isipulsa sudah %d hari (>= 3 hari). Inject cookies baru SEKARANG di web admin -> Akun Login sebelum sesi mati mendadak.", hari)
	}
	sisa := int(cookieReminderJam-umur) / 24
	return fmt.Sprintf("Cookies isipulsa %d hari. Sisa %d hari sebelum perlu inject baru.", hari, sisa+1)
}

// Export guard untuk test (bukan bagian API publik).
func BolehNotifInvalidForTest(lastNotif string, now time.Time) bool {
	return bolehNotifInvalid(lastNotif, now)
}

// BolehNotifExpiryForTest: true kalau boleh notif cookie expired.
func BolehNotifExpiryForTest(sisaHari int, sudahNotifHariIni bool) bool {
	return bolehNotifExpiry(sisaHari, sudahNotifHariIni)
}
