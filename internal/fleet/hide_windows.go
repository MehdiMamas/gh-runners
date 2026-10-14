//go:build windows

package fleet

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps `tailscale status` from flashing a console window when the
// daemon (which has no visible console) runs it.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
}
