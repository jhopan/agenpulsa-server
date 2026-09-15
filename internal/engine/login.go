// Package engine: login flow (window langsung di Windows, Xvfb+VNC+noVNC di
// Linux headless) + teardown + deteksi login sukses.
package engine

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
	"github.com/jhopan/agenpulsa-server/internal/db"
)

const (
	vncDisplay  = ":99"
	vncPort     = 5900
	vncWebPort  = 6080
	vncTimeout  = 15 * time.Minute
	vncPassword = "agenpulsa"
)

type vncStack struct {
	display  *exec.Cmd
	x11vnc   *exec.Cmd
	websock  *exec.Cmd
	passFile string
	deadline time.Time
}

type LoginSession struct {
	e        *Engine
	page     *rod.Page
	vnc      *vncStack
	visible  bool // window langsung (bukan VNC)
	deadline time.Time
}

// StartLogin mulai sesi login: window langsung di Windows, Xvfb+VNC di Linux.
func (e *Engine) StartLogin() error {
	e.mu.Lock()
	if e.loginOpen {
		e.mu.Unlock()
		return fmt.Errorf("sesi login sudah berjalan")
	}
	e.loginOpen = true
	e.mu.Unlock()

	// tutup browser headless biar profile tidak terkunci dua proses.
	e.ResetLoginForce()

	sess := &LoginSession{e: e, deadline: time.Now().Add(vncTimeout)}
	if runtime.GOOS != "windows" {
		vs, err := startVNCStack(e.userData)
		if err != nil {
			e.mu.Lock()
			e.loginOpen = false
			e.mu.Unlock()
			return err
		}
		sess.vnc = vs
		sess.deadline = vs.deadline
	}

	b, page, err := e.launchVisible(sess.vnc != nil)
	if err != nil {
		sess.cleanup()
		e.mu.Lock()
		e.loginOpen = false
		e.mu.Unlock()
		return err
	}
	sess.page = page
	sess.visible = sess.vnc == nil

	e.mu.Lock()
	e.browser = b
	e.sess = sess
	e.mu.Unlock()

	go sess.watch()
	return nil
}

// launchVisible buka chrome visible (window langsung atau di dalam Xvfb).
func (e *Engine) launchVisible(inXvfb bool) (*rod.Browser, *rod.Page, error) {
	url, err := launchChrome(e.userData, "", inXvfb)
	if err != nil {
		if bin := findChromium(); bin != "" {
			url, err = launchChrome(e.userData, bin, inXvfb)
		}
	}
	if err != nil {
		return nil, nil, fmt.Errorf("launch chromium visible: %w", err)
	}
	b := rod.New().ControlURL(url)
	if err := b.Connect(); err != nil {
		return nil, nil, err
	}
	page, err := b.Page(proto.TargetCreateTarget{URL: baseURL})
	if err != nil {
		_ = b.Close()
		return nil, nil, err
	}
	_ = page.WaitLoad()
	return b, page, nil
}

var reDevtools = regexp.MustCompile(`(ws://[^\s]+)`)

// launchChrome jalankan chrome visible; kalau inXvfb, eksekusi manual dengan
// DISPLAY=:99 karena launcher.New() tidak bisa set env proses.
func launchChrome(profile, bin string, inXvfb bool) (string, error) {
	l := launcher.New().
		UserDataDir(profile).
		Headless(false).
		Leakless(false). // leakless.exe sering kena blokir Defender di Windows
		Set("no-sandbox")
	if bin != "" {
		l = l.Bin(bin)
	}
	if !inXvfb {
		return l.Launch()
	}
	// eksekusi manual dgn DISPLAY; ambil ws URL dari output DevTools.
	exe := l.Get(flags.Bin)
	if exe == "" {
		var err error
		exe, err = launcher.NewBrowser().Get() // unduh/pakai chrome rod bila perlu
		if err != nil {
			return "", err
		}
	}
	args := []string{"--remote-debugging-port=0", "--no-sandbox", "--user-data-dir=" + profile, "--no-first-run", "--headless=false", baseURL}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), "DISPLAY="+vncDisplay)
	out := &strings.Builder{}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		return "", err
	}
	go func() { _ = cmd.Wait() }() // reap zombie
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if m := reDevtools.FindStringSubmatch(out.String()); m != nil {
			return m[1], nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	return "", fmt.Errorf("devtools ws tidak ditemukan: %s", strings.TrimSpace(out.String()))
}

// watch pantau login sukses di background; selesai -> cleanup + lepas lock.
func (s *LoginSession) watch() {
	defer func() { _ = recover() }()
	ok := false
	for i := 0; i < int(vncTimeout/time.Second); i++ {
		time.Sleep(time.Second)
		header, err := s.page.Element("header")
		if err != nil {
			// halaman mungkin reload; cek page masih hidup
			if _, infoErr := s.page.Info(); infoErr != nil {
				break // browser ditutup user
			}
			continue
		}
		txt, _ := header.Text()
		if strings.Contains(txt, "Saldo") {
			ok = true
			break
		}
		if s.vnc != nil && time.Now().After(s.vnc.deadline) {
			break
		}
	}
	_ = s.page.Close()
	_ = s.cleanupBrowser()
	s.cleanup()
	s.e.mu.Lock()
	s.e.browser = nil
	s.e.sess = nil
	s.e.loginOpen = false
	s.e.mu.Unlock()
	log.Printf("[LOGIN] sesi login selesai, login sukses=%v", ok)
}

func (s *LoginSession) cleanupBrowser() error {
	s.e.mu.Lock()
	b := s.e.browser
	s.e.mu.Unlock()
	if b != nil {
		return b.Close()
	}
	return nil
}

// cleanup matikan chrome + stack VNC bila ada.
func (s *LoginSession) cleanup() {
	if s.vnc != nil {
		s.vnc.stop()
	}
}

// LoginOpen true kalau sesi login sedang berjalan.
func (e *Engine) LoginOpen() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.loginOpen
}

// VNCInfo URL noVNC untuk admin (Linux headless saja).
func (e *Engine) VNCInfo() (url, pass string, ok bool) {
	e.mu.Lock()
	open := e.loginOpen && e.sess != nil && e.sess.vnc != nil
	e.mu.Unlock()
	if !open {
		return "", "", false
	}
	ip := outboundIP()
	return fmt.Sprintf("http://%s:%d/vnc.html", ip, vncWebPort), vncPassword, true
}

// VNCActive true kalau sesi login jalan lewat stack VNC (Linux headless).
func (e *Engine) VNCActive() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.loginOpen && e.sess != nil && e.sess.vnc != nil
}

// ImportCookies inject cookies (format export Cookie-Editor: JSON array) ke
// profile — buka chrome headless sebentar via CDP, tutup lagi. Cookie nempel
// permanen di user-data-dir.
func (e *Engine) ImportCookies(raw string) (int, error) {
	var list []map[string]any
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return 0, fmt.Errorf("JSON tidak valid: %w", err)
	}
	cookies := []*proto.NetworkCookieParam{}
	for _, c := range list {
		name, _ := c["name"].(string)
		val, _ := c["value"].(string)
		if name == "" || val == "" {
			continue
		}
		domain, _ := c["domain"].(string)
		if domain == "" {
			domain = ".isipulsa.web.id"
		}
		path, _ := c["path"].(string)
		if path == "" {
			path = "/"
		}
		ck := &proto.NetworkCookieParam{
			Name: name, Value: val, Domain: domain, Path: path,
			HTTPOnly: boolAny(c["httpOnly"]), Secure: boolAny(c["secure"]),
		}
		if exp, ok := c["expirationDate"].(float64); ok && exp > 0 {
			ck.Expires = proto.TimeSinceEpoch(exp)
		}
		switch strings.ToLower(fmt.Sprint(c["sameSite"])) {
		case "strict":
			ck.SameSite = proto.NetworkCookieSameSiteStrict
		case "no_restriction", "none":
			ck.SameSite = proto.NetworkCookieSameSiteNone
		default:
			ck.SameSite = proto.NetworkCookieSameSiteLax
		}
		cookies = append(cookies, ck)
	}
	if len(cookies) == 0 {
		return 0, fmt.Errorf("tidak ada cookie valid (butuh name+value)")
	}

	// sesi login jangan diganggu.
	if e.LoginOpen() {
		return 0, fmt.Errorf("sesi login sedang berjalan — tunggu/stop dulu")
	}
	e.ResetLoginForce()

	url, err := launcher.New().
		UserDataDir(e.userData).
		Headless(true).
		Leakless(false).
		Set("no-sandbox").
		Launch()
	if err != nil {
		if bin := findChromium(); bin != "" {
			url, err = launcher.New().
				Bin(bin).
				UserDataDir(e.userData).
				Headless(true).
				Leakless(false).
				Set("no-sandbox").
				Launch()
		}
		if err != nil {
			return 0, fmt.Errorf("launch chromium: %w", err)
		}
	}
	b := rod.New().ControlURL(url)
	if err := b.Connect(); err != nil {
		return 0, err
	}
	defer b.Close()
	page, err := b.Page(proto.TargetCreateTarget{URL: baseURL})
	if err != nil {
		return 0, err
	}
	_ = page.WaitLoad()
	if err := b.SetCookies(cookies); err != nil {
		return 0, err
	}
	log.Printf("[LOGIN] %d cookie diinject ke profile", len(cookies))
	// catat waktu inject — dasar reminder "inject cookies baru tiap 3 hari".
	_ = e.store.SetSetting("cookies_updated_at", db.NowWIB().Format("2006-01-02 15:04:05"))
	return len(cookies), nil
}

func boolAny(v any) bool {
	b, _ := v.(bool)
	return b
}

// ResetLoginForce dipakai handler ganti akun: stop sesi login kalau ada.
func (e *Engine) ResetLoginForce() {
	e.mu.Lock()
	if e.sess != nil {
		sess := e.sess
		e.mu.Unlock()
		sess.forceStop()
		return
	}
	if e.browser != nil {
		_ = e.browser.Close()
		e.browser = nil
	}
	e.mu.Unlock()
}

func (s *LoginSession) forceStop() {
	_ = s.page.Close()
	_ = s.cleanupBrowser()
	s.cleanup()
	s.e.mu.Lock()
	s.e.browser = nil
	s.e.sess = nil
	s.e.loginOpen = false
	s.e.mu.Unlock()
}

// outboundIP IP LAN yang dipakai route default (ala menu.py lama).
func outboundIP() string {
	out, err := exec.Command("sh", "-c", "ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \\([^ ]*\\).*/\\1/p' | head -n 1").Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		// Windows fallback: hostname -I tidak ada; pakai ipconfig sederhana.
		if runtime.GOOS == "windows" {
			return "localhost"
		}
		out, _ = exec.Command("hostname", "-I").Output()
		if len(strings.TrimSpace(string(out))) == 0 {
			return "IP-SERVER"
		}
	}
	return strings.TrimSpace(strings.Fields(string(out))[0])
}

// ---------- VNC stack (Linux headless) ----------

func startVNCStack(profile string) (*vncStack, error) {
	if err := ensureTools(); err != nil {
		return nil, err
	}
	passFile, err := ensureVNCPass()
	if err != nil {
		return nil, err
	}
	vs := &vncStack{passFile: passFile, deadline: time.Now().Add(vncTimeout)}

	// 1. Xvfb :99 (matikan dulu kalau nyangkut dari run sebelumnya).
	_ = exec.Command("pkill", "-f", "Xvfb :99").Run()
	time.Sleep(300 * time.Millisecond)
	vs.display = exec.Command("Xvfb", vncDisplay, "-screen", "0", "1366x900x24", "-nolisten", "tcp")
	if err := vs.display.Start(); err != nil {
		return nil, fmt.Errorf("Xvfb gagal: %w", err)
	}
	time.Sleep(700 * time.Millisecond)

	// 2. x11vnc di display itu (localhost saja).
	vs.x11vnc = exec.Command("x11vnc",
		"-display", vncDisplay,
		"-rfbauth", passFile,
		"-localhost", "-forever", "-shared", "-quiet")
	if err := vs.x11vnc.Start(); err != nil {
		vs.stop()
		return nil, fmt.Errorf("x11vnc gagal: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// 3. websockify/noVNC 0.0.0.0:6080 -> localhost:5900.
	novncWeb := findNoVNCWeb()
	vs.websock = exec.Command("websockify", "--web", novncWeb, "0.0.0.0:6080", fmt.Sprintf("localhost:%d", vncPort))
	if err := vs.websock.Start(); err != nil {
		vs.stop()
		return nil, fmt.Errorf("websockify gagal: %w (pasang novnc + websockify)", err)
	}
	time.Sleep(500 * time.Millisecond)
	return vs, nil
}

// findNoVNCWeb cari folder web noVNC.
func findNoVNCWeb() string {
	for _, p := range []string{"/usr/share/novnc", "/usr/share/webapps/novnc", "/opt/novnc"} {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return "/usr/share/novnc"
}

// ensureTools cek binary; pandu install kalau belum ada.
func ensureTools() error {
	need := map[string]string{"Xvfb": "xvfb", "x11vnc": "x11vnc", "websockify": "websockify (noVNC)"}
	var missing []string
	for bin, pkg := range need {
		if _, err := exec.LookPath(bin); err != nil {
			missing = append(missing, pkg)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("paket belum terpasang: %s — jalankan: sudo apt-get install -y xvfb x11vnc novnc websockify", strings.Join(missing, ", "))
	}
	return nil
}

// ensureVNCPass bikin file password VNC kalau belum ada (x11vnc format).
func ensureVNCPass() (string, error) {
	dir := "/root/.vnc"
	if home, err := os.UserHomeDir(); err == nil && home != "/root" {
		dir = filepath.Join(home, ".vnc")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	passFile := filepath.Join(dir, "agenpulsa.pass")
	if _, err := os.Stat(passFile); err == nil {
		return passFile, nil
	}
	if _, err := exec.LookPath("x11vnc"); err != nil {
		return "", fmt.Errorf("x11vnc tidak ada untuk -storepasswd")
	}
	out, err := exec.Command("x11vnc", "-storepasswd", vncPassword, passFile).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("storepasswd gagal: %v: %s", err, strings.TrimSpace(string(out)))
	}
	_ = os.Chmod(passFile, 0o600)
	return passFile, nil
}

func (vs *vncStack) stop() {
	for _, c := range []*exec.Cmd{vs.websock, vs.x11vnc, vs.display} {
		if c != nil && c.Process != nil {
			_ = c.Process.Kill()
			_ = c.Wait()
		}
	}
	_ = exec.Command("pkill", "-f", "Xvfb :99").Run()
}
