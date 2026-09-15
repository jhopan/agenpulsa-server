// Package scheduler: jadwal order (WIB) + cek saldo periodik.
package scheduler

import (
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/jhopan/agenpulsa-server/internal/db"
	"github.com/jhopan/agenpulsa-server/internal/engine"
)

type Scheduler struct {
	store *db.Store
	eng   *engine.Engine
}

func New(store *db.Store, eng *engine.Engine) *Scheduler {
	return &Scheduler{store: store, eng: eng}
}

func (s *Scheduler) NowWIB() time.Time { return db.NowWIB() }

// Run loop tiap 30 detik: cek jadwal yang jatuh waktu.
func (s *Scheduler) Run() {
	for {
		s.tick()
		time.Sleep(30 * time.Second)
	}
}

func (s *Scheduler) tick() {
	now := db.NowWIB()
	scheds, err := s.store.ListSchedules()
	if err != nil {
		log.Printf("scheduler list: %v", err)
		return
	}
	for _, sc := range scheds {
		if !sc.Aktif {
			continue
		}
		switch sc.Tipe {
		case "harian":
			if s.matchHHMM(sc.Jam, now) {
				s.fire(&sc, now)
			}
		case "interval":
			if s.matchHHMM(sc.Jam, now) && s.intervalDue(&sc, now) {
				s.fire(&sc, now)
			}
		case "sekali":
			if s.matchOnce(&sc, now) {
				s.fire(&sc, now)
				_ = s.store.DeleteSchedule(sc.ID) // sekali pakai
			}
		}
	}
}

func (s *Scheduler) matchHHMM(jam string, now time.Time) bool {
	parts := strings.Split(jam, ":")
	if len(parts) != 2 {
		return false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	return err1 == nil && err2 == nil && h == now.Hour() && m == now.Minute()
}

func (s *Scheduler) intervalDue(sc *db.Schedule, now time.Time) bool {
	if sc.TerakhirJalan == "" {
		return true
	}
	last, err := time.ParseInLocation("2006-01-02", sc.TerakhirJalan, db.WIBLoc())
	if err != nil {
		return true
	}
	days := int(now.Sub(last).Hours() / 24)
	return days >= sc.IntervalHari
}

func (s *Scheduler) matchOnce(sc *db.Schedule, now time.Time) bool {
	for _, layout := range []string{"02/01/2006 15:04", "02/01 15:04"} {
		t, err := time.ParseInLocation(layout, sc.Jam, db.WIBLoc())
		if err != nil {
			continue
		}
		if layout == "02/01 15:04" {
			t = t.AddDate(now.Year(), 0, 0)
			if t.Before(now) {
				t = t.AddDate(1, 0, 0)
			}
		}
		diff := now.Sub(t)
		return diff >= 0 && diff < 30*time.Second
	}
	return false
}

// fire buat order dari jadwal (masuk queue normal).
func (s *Scheduler) fire(sc *db.Schedule, now time.Time) {
	// Hindari dobel: kalau sudah jalan menit yang sama, skip.
	last := s.store.GetSetting("sched_last_"+strconv.FormatInt(sc.ID, 10), "")
	minuteKey := now.Format("2006-01-02 15:04")
	if last == minuteKey {
		return
	}
	_ = s.store.SetSetting("sched_last_"+strconv.FormatInt(sc.ID, 10), minuteKey)

	o := &db.Order{
		Ref:       fmtSched(sc.ID, minuteKey),
		Nomor:     sc.Nomor,
		CatalogID: sc.CatalogID,
		Label:     sc.Label,
		Status:    "queued",
		Sumber:    "scheduler",
		ChatID:    sc.ChatID,
	}
	// isi modal/harga dari katalog biar laporan (modal/omzet/profit) gak Rp 0.
	if it, err := s.store.GetCatalog(sc.CatalogID); err == nil {
		o.Label = it.Label
		o.Modal = it.HargaMax
		o.HargaJual = it.HargaJual
	}
	if o.Ref == "" {
		return
	}
	if err := s.eng.Enqueue(o); err != nil {
		log.Printf("scheduler enqueue: %v", err)
		return
	}
	if sc.Tipe == "interval" {
		_ = s.store.SetScheduleLastRun(sc.ID, now.Format("2006-01-02"))
	}
	log.Printf("[JADWAL %s WIB] %s -> order #%d antri", sc.Jam, sc.Label, o.ID)
}

func fmtSched(id int64, minute string) string {
	return "sched-" + strconv.FormatInt(id, 10) + "-" + strings.ReplaceAll(minute, " ", "T")
}
