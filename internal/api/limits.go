// Package api: rate limit endpoint order + health check untuk monitoring.
package api

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jhopan/agenpulsa-server/internal/db"
)

// ---------- Rate limit (P6) ----------

// rateLimiter: sliding window per kunci (API key / IP). Mencegah flood order
// dari bot yang salah / key yang bocor — queue penuh + DB membengkak.
type rateLimiter struct {
	mu      sync.Mutex
	hits    map[string][]time.Time
	max     int
	window  time.Duration
	stopped bool
}

// newRateLimiter: max request per window per kunci.
func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{hits: make(map[string][]time.Time), max: max, window: window}
}

// allow: true kalau request ini boleh lewat. Sisa quota dikembalikan.
func (rl *rateLimiter) allow(key string) (bool, int) {
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	cut := now.Add(-rl.window)
	list := rl.hits[key]
	keep := list[:0]
	for _, t := range list {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	if len(keep) >= rl.max {
		rl.hits[key] = keep
		return false, 0
	}
	keep = append(keep, now)
	rl.hits[key] = keep
	return true, rl.max - len(keep)
}

// sweep: buang kunci yang sudah kedaluwarsa ( biar map gak tumbuh).
func (rl *rateLimiter) sweep() {
	cut := time.Now().Add(-rl.window)
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for k, list := range rl.hits {
		keep := list[:0]
		for _, t := range list {
			if t.After(cut) {
				keep = append(keep, t)
			}
		}
		if len(keep) == 0 {
			delete(rl.hits, k)
		} else {
			rl.hits[k] = keep
		}
	}
}

// maxActiveOrderPerIP: maksimal order AKTIF (pending_payment/queued/running)
// per lokasi. Kalau sudah penuh -> order baru DITOLAK dan user diminta nunggu
// salah satu order yang sedang jalan selesai. Ini yang bikin bot tinggal
// "hit API buat cek" tanpa bikin antrian numpuk.
const maxActiveOrderPerIP = 3

// limitActiveOrder: middleware sebelum buat order. Kalau lokasi sudah punya
// maxActiveOrderPerIP order aktif -> 429 (dengan sisa/status biar bot tahu).
func (a *API) limitActiveOrder(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := a.clientIP(r)
		n, err := a.store.CountActiveByIP(ip)
		if err != nil {
			next(w, r) // DB error -> jangan blokir order
			return
		}
		if n >= maxActiveOrderPerIP {
			w.Header().Set("Retry-After", "60")
			w.Header().Set("X-Active-Orders", strconv.Itoa(n))
			jsonErr(w, 429, "lokasi kamu masih punya "+strconv.Itoa(n)+" order aktif (maks "+strconv.Itoa(maxActiveOrderPerIP)+") — tunggu salah satu selesai, lalu ulangi")
			return
		}
		w.Header().Set("X-Active-Orders", strconv.Itoa(n))
		next(w, r)
	}
}

// clientIP: IP client. Kalau lewat proxy (CF tunnel) pakai X-Forwarded-For.
func (a *API) clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	if r.Header.Get("CF-Connecting-IP") != "" {
		return strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))
	}
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	return ip
}

// orderStatus: status antrian untuk bot (cek via API). Menjawab: berapa order
// aktif dari lokasi ini, dan daftar statusnya.
func (a *API) orderStatus(w http.ResponseWriter, r *http.Request) {
	ip := a.clientIP(r)
	n, err := a.store.CountActiveByIP(ip)
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	pending, err := a.store.ListOrders("pending_payment", 20)
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	// filter hanya order milik lokasi ini
	var mine []*db.Order
	for _, o := range pending {
		if o.ClientIP == ip {
			mine = append(mine, o)
		}
	}
	out := make([]map[string]any, 0, len(mine))
	for _, o := range mine {
		out = append(out, map[string]any{
			"ref": o.Ref, "status": o.Status, "nomor": o.Nomor,
			"label": o.Label, "total": o.HargaJual, "created_at": o.CreatedAt,
		})
	}
	writeJSON(w, 200, map[string]any{
		"ip":            ip,
		"aktif":         n,
		"maks":          maxActiveOrderPerIP,
		"bisa_order":    n < maxActiveOrderPerIP,
		"order_pending": out,
	})
}

// rateKey: API key kalau ada, else IPRemote (proxy: pakai X-Forwarded-For).
func (a *API) rateKey(r *http.Request) string {
	if k := strings.TrimSpace(r.Header.Get("X-API-Key")); k != "" {
		return "key:" + k
	}
	ip := r.RemoteAddr
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		ip = strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	return "ip:" + ip
}

// StartRateLimitSweep: bersihkan peta rate limit tiap 5 menit.
func (a *API) StartRateLimitSweep() {
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			a.orderLimiter.sweep()
		}
	}()
}

// ---------- Health check (P7) ----------

// health: endpoint publik tanpa API key — buat auto-ping / monitoring.
// Jangan bocorkan data sensitif: hanya status hidup + waktu.
func (a *API) health(w http.ResponseWriter, r *http.Request) {
	dbSize, walSize := a.store.DBSize()
	writeJSON(w, 200, map[string]any{
		"ok":       true,
		"service":  "agenpulsa-server",
		"wib":      db.NowWIB().Format("2006-01-02 15:04:05"),
		"uptime":   time.Since(a.startedAt).Round(time.Second).String(),
		"db_bytes": dbSize,
		"wal_bytes": walSize,
		"version":  a.version,
	})
}

// OrderLimiterForTest: akses limiter buat unit test.
func (a *API) OrderLimiterForTest() *rateLimiter { return a.orderLimiter }

// Allow: exported wrapper utk test (allow unexported).
func (rl *rateLimiter) Allow(key string) (bool, int) { return rl.allow(key) }
