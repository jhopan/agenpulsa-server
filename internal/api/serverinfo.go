package api

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// serverInfo: alamat server untuk BERBAGAI jalur akses.
//
// Kenapa dua-duanya:
//   - LAN  : IP lokal (mis. http://192.168.10.21:8800) — dipakai bot/klien
//     yang jalan di jaringan yang sama. jauh lebih cepat, gak lewat internet.
//   - PUBLIK: URL Cloudflare Tunnel (mis. https://agenpulsa.jhopan.my.id) —
//     dipakai webhook paypan & client yang di luar jaringan.
//
// Keduanya dilayani server yang sama, cuma berbeda cara menjangkaunya.
type serverInfo struct {
	LANURL    string `json:"lan_url"`     // URL LAN (isi manual kalau auto-detect salah)
	PublicURL string `json:"public_url"`  // URL publik / CF tunnel
	Webhook   string `json:"webhook_url"`     // URL publik + /webhook (paypan dari luar)
	WebhookLAN string `json:"webhook_url_lan"` // URL LAN + /webhook (paypan di jaringan lokal)
	Port      string `json:"port"`
	Host      string `json:"host"`      // host CF tunnel (dari public URL)
	Detected  string `json:"detected_lan"` // IP LAN yang terdeteksi otomatis
	Same      bool   `json:"same"`       // true kalau public URL == LAN URL
}

// deteksiLANIP: IP IPv4 yang paling "ijdiah" (private LAN).
// Prioritas: interface yang punya DEFAULT ROUTE (jalur keluar internet) —
// itu interface yang dipakai server & paling stabil diakses dari jaringan.
// Fallback: 192.168.x, lalu 10.x, lalu 172.16-31.x.
// Loopback & link-local (169.254.x) dilewati.
func deteksiLANIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	ipByIface := map[string]string{}
	var kandidat []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil {
				continue
			}
			ip := ipn.IP.To4().String()
			if strings.HasPrefix(ip, "169.254.") {
				continue // link-local
			}
			kandidat = append(kandidat, ip)
			if _, ada := ipByIface[ifc.Name]; !ada {
				ipByIface[ifc.Name] = ip
			}
		}
	}
	if len(kandidat) == 0 {
		return ""
	}
	// 1) interface dengan default route (Linux: /proc/net/route)
	if ifc := ifaceDefaultRoute(); ifc != "" {
		if ip, ok := ipByIface[ifc]; ok {
			return ip
		}
	}
	// 2) fallback: skor private range
	skor := func(ip string) int {
		switch {
		case strings.HasPrefix(ip, "192.168."):
			return 3
		case strings.HasPrefix(ip, "10."):
			return 2
		case strings.HasPrefix(ip, "172."):
			return 1
		}
		return 0
	}
	best := ""
	skorBest := -1
	for _, ip := range kandidat {
		if s := skor(ip); s > skorBest {
			best, skorBest = ip, s
		}
	}
	return best
}

// ifaceDefaultRoute: nama interface yang jadi default route.
// Linux: baca /proc/net/route (kolom: iface destination gateway ... flag),
// default route = destination 00000000. Non-Linux: "" (fallback lain).
func ifaceDefaultRoute() string {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}
	bestIface := ""
	bestMetric := ^uint64(0)
	for _, line := range strings.Split(string(data), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 8 {
			continue
		}
		if f[1] != "00000000" { // bukan default route
			continue
		}
		var metric uint64
		if m, err := strconv.ParseUint(f[6], 16, 64); err == nil {
			metric = m
		}
		if metric < bestMetric {
			bestMetric, bestIface = metric, f[0]
		}
	}
	return bestIface
}

// portServer: port dari env AP_PORT (default 8081) — sama kayak main.go.
func portServer() string {
	p := os.Getenv("AP_PORT")
	if p == "" {
		p = "8081"
	}
	return p
}

// serverInfoPayload: susun data alamat server.
func (a *API) serverInfoPayload() serverInfo {
	port := portServer()
	deteksi := deteksiLANIP()

	// URL LAN: manual (setting) kalau diisi, else auto-deteksi.
	lan := strings.TrimSpace(a.store.GetSetting("server_url_lan", ""))
	if lan == "" && deteksi != "" {
		lan = "http://" + deteksi + ":" + port
	}
	lan = strings.TrimRight(lan, "/")

	// URL publik: prefer server_url_tunnel (kalau diisi), else server_url (lama).
	pub := strings.TrimSpace(a.store.GetSetting("server_url_tunnel", ""))
	if pub == "" {
		pub = strings.TrimSpace(a.store.GetSetting("server_url", ""))
	}
	pub = strings.TrimRight(pub, "/")

	info := serverInfo{
		LANURL:    lan,
		PublicURL: pub,
		Port:      port,
		Detected:  deteksi,
		Same:      lan != "" && lan == pub,
	}
	// Webhook: dua-duanya. LAN buat Paypan yg jalan di jaringan lokal (cepat),
	// publik (tunnel) buat Paypan di luar jaringan / cloud.
	if lan != "" {
		info.WebhookLAN = lan + "/webhook"
	}
	if pub != "" {
		info.Webhook = pub + "/webhook"
		// host CF tunnel untuk info tunnel (mis. agenpulsa.jhopan.my.id)
		trim := strings.TrimPrefix(strings.TrimPrefix(pub, "https://"), "http://")
		info.Host = strings.Split(trim, "/")[0]
	} else if lan != "" {
		info.Webhook = info.WebhookLAN
	}
	return info
}

// handleServerInfo: GET /api/v1/server-info (admin) — alamat LAN & publik.
func (a *API) handleServerInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.serverInfoPayload())
}

// handleTesWebhook: POST /api/v1/tes-webhook — server yang memanggil webhook
// (bukan browser). alasan: browser di halaman HTTPS gak boleh fetch ke
// http://LAN (mixed content), jadi tes harus lewat server.
//
// Body: {"target":"lan"|"publik"} — server pilih URL dari serverInfoPayload
// (allowlist internal, bukan URL bebas) lalu POST signature palsu.
// Hasil 403 = webhook hidup & HMAC aktif (itu healthy state).
func (a *API) handleTesWebhook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target string `json:"target"` // "lan" | "publik"
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req)

	info := a.serverInfoPayload()
	var url string
	switch req.Target {
	case "lan":
		url = info.WebhookLAN
	case "publik":
		url = info.Webhook
	default:
		url = info.Webhook
	}
	if url == "" {
		writeJSON(w, 400, map[string]string{"error": "alamat webhook belum diisi"})
		return
	}
	payload := `{"event":"order.paid","order":{"id":"tes","price":1,"code":1,"total":2}}`
	resp, err := http.Post(url, "application/json", strings.NewReader(payload))
	if err != nil {
		writeJSON(w, 502, map[string]any{"ok": false, "target": req.Target, "url": url, "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	writeJSON(w, 200, map[string]any{
		"ok":         resp.StatusCode == 200 || resp.StatusCode == 403,
		"target":     req.Target,
		"url":        url,
		"http_status": resp.StatusCode,
		"body":       strings.TrimSpace(string(body)),
	})
}
