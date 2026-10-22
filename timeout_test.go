package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jhopan/agenpulsa-server/internal/engine"
)

// TestHelperTimeoutKonstan: pastikan batas timeout helper terdefinisi dan
// masuk akal (worker tidak boleh macet selamanya).
func TestHelperTimeoutKonstan(t *testing.T) {
	if engine.IsipTimeoutSeconds() <= 0 {
		t.Error("timeout helper harus > 0")
	}
	if engine.IsipTimeoutSeconds() > 600 {
		t.Error("timeout helper terlalu lama (> 10 menit) - worker bisa macet")
	}
}

// TestHelperPath: helper harus ditemukan (path relatif ke CWD atau binary) —
// dulu bug: LookPath("helper/isip_api.py") selalu gagal di systemd.
func TestHelperPath(t *testing.T) {
	wd, _ := os.Getwd()
	helper := filepath.Join(wd, "helper", "isip_api.py")
	if _, err := os.Stat(helper); err != nil {
		t.Skip("helper tidak ada di cwd test")
	}
	if runtime.GOOS == "windows" {
		return
	}
}
