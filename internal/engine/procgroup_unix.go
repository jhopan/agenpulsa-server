//go:build !windows

package engine

import (
	"os/exec"
	"syscall"
)

// setProcGroup: pid anak jadi process group sendiri supaya Timeout kill
// seluruh subtree (python -> chromium/Playwright).
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
