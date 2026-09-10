// Package api: login admin username/password + session cookie (in-memory).
package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"
)

const sessionCookie = "ap_session"
const sessionTTL = 24 * time.Hour

type sessions struct {
	mu sync.Mutex
	m  map[string]time.Time
}

var sess = &sessions{m: map[string]time.Time{}}

func (s *sessions) new() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	tok := hex.EncodeToString(b)
	s.mu.Lock()
	s.m[tok] = time.Now().Add(sessionTTL)
	s.mu.Unlock()
	return tok
}

func (s *sessions) check(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.m[tok]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.m, tok)
		return false
	}
	return true
}

func (s *sessions) del(tok string) {
	s.mu.Lock()
	delete(s.m, tok)
	s.mu.Unlock()
}

func (a *API) sessionAdmin(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	return sess.check(c.Value)
}

func (a *API) checkLogin(username, password string) bool {
	user := a.store.GetSetting("admin_user", "admin")
	pass := a.store.GetSetting("admin_pass", "admin123")
	okU := subtle.ConstantTimeCompare([]byte(username), []byte(user))
	okP := subtle.ConstantTimeCompare([]byte(password), []byte(pass))
	return okU == 1 && okP == 1
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		jsonErr(w, 400, "json tidak valid")
		return
	}
	if !a.checkLogin(req.Username, req.Password) {
		time.Sleep(500 * time.Millisecond) // rem brute force
		jsonErr(w, 401, "username atau password salah")
		return
	}
	tok := sess.new()
	if tok == "" {
		jsonErr(w, 500, "gagal buat session")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL / time.Second),
	})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		sess.del(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// me: cek apakah request ini punya akses admin (session atau API key admin).
func (a *API) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]bool{"admin": a.sessionAdmin(r) || a.adminKey(r)})
}
