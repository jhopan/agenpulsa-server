//go:build windows

package engine

import "os/exec"

// setProcGroup: di Windows tak perlu (CommandContext kill process-nya).
func setProcGroup(cmd *exec.Cmd) {}
