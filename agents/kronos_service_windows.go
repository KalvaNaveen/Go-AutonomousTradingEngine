//go:build windows

package agents

import (
	"os/exec"
	"syscall"
)

// setSysProcAttr makes the PowerShell process fully detached on Windows:
//   - CREATE_NEW_PROCESS_GROUP — own Ctrl-C group, won't die when the console closes
//   - DETACHED_PROCESS          — no console window; runs silently in the background
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x00000008, // 0x8 = DETACHED_PROCESS
		HideWindow:    true,
	}
}
