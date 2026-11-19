package engine

import (
	"encoding/json"
	"testing"
)

// TestStatusHasil: monitor hanya boleh lapor "cookie invalid" kalau helper
// SUKSES dan isipulsa bilang login=false. Error jaringan/SSL/timeout (helper
// membalas JSON {"error":...}) = gagal cek, bukan cookie mati.
func TestStatusHasil(t *testing.T) {
	// isipulsa menolak sesi -> definitif invalid (err nil).
	login, saldo, pesan, err := statusHasil(&isipResult{Login: false, Saldo: "-", Pesan: "sesi login habis"})
	if err != nil {
		t.Errorf("login=false tanpa error tidak boleh jadi err: %v", err)
	}
	if login || pesan != "sesi login habis" || saldo != "-" {
		t.Errorf("hasil login=false salah: %v %q %q", login, saldo, pesan)
	}

	// login normal.
	login, saldo, _, err = statusHasil(&isipResult{OK: true, Login: true, Saldo: "Rp 10.000"})
	if err != nil || !login || saldo != "Rp 10.000" {
		t.Errorf("hasil login=true salah: %v %q %v", login, saldo, err)
	}

	// helper crash / SSL reset -> error, BUKAN cookie invalid.
	var res isipResult
	raw := `{"ok":false,"error":"SSLError: UNEXPECTED_EOF_WHILE_READING"}`
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := statusHasil(&res); err == nil {
		t.Error("payload error helper harus jadi err (gagal cek)")
	}

	if _, _, _, err := statusHasil(nil); err == nil {
		t.Error("hasil nil harus jadi err")
	}
}
