// Package api: kontak pelanggan (nomor yang sering dipakai admin saat membelikan).
package api

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
)

// listContacts: daftar kontak pelanggan (nama -> nomor).
func (a *API) listContacts(w http.ResponseWriter, r *http.Request) {
	m, err := a.store.ListContacts()
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	// map -> array stabil (nama urut abjad)
	out := make([]map[string]string, 0, len(m))
	namas := make([]string, 0, len(m))
	for n := range m {
		namas = append(namas, n)
	}
	sort.Strings(namas)
	for _, n := range namas {
		out = append(out, map[string]string{"nama": n, "nomor": m[n]})
	}
	writeJSON(w, 200, out)
}

// simpanContact: upsert kontak {nama, nomor} (nomor dinormalisasi).
func (a *API) simpanContact(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Nama  string `json:"nama"`
		Nomor string `json:"nomor"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<14)).Decode(&req); err != nil {
		jsonErr(w, 400, "json tidak valid")
		return
	}
	req.Nama = strings.TrimSpace(req.Nama)
	req.Nomor = NormalizeNomor(req.Nomor)
	if req.Nama == "" || req.Nomor == "" {
		jsonErr(w, 400, "nama dan nomor wajib (nomor 08xxx / 62xxx)")
		return
	}
	if len(req.Nama) > 80 {
		req.Nama = req.Nama[:80]
	}
	if err := a.store.UpsertContact(req.Nama, req.Nomor); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1", "nama": req.Nama, "nomor": req.Nomor})
}

// hapusContact: hapus kontak by nama (path /api/v1/contacts/{nama}).
func (a *API) hapusContact(w http.ResponseWriter, r *http.Request) {
	nama := r.PathValue("nama")
	if nama == "" {
		jsonErr(w, 400, "nama wajib")
		return
	}
	if err := a.store.DeleteContact(nama); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}
