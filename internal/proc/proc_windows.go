//go:build windows

package proc

import (
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

const createNoWindow = 0x08000000

func supervise(cmd *exec.Cmd) {
	// The child shares the daemon's console (hidden when the daemon was
	// detached), so no window opens and CTRL_BREAK can reach its group.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | createNoWindow,
		HideWindow:    true,
	}
}

func interrupt(cmd *exec.Cmd) {
	_ = windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid))
}

func kill(cmd *exec.Cmd) {
	// cmd.exe -> Runner.Listener -> Runner.Worker: kill the whole tree.
	k := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	k.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
	if err := k.Run(); err != nil {
		_ = cmd.Process.Kill()
	}
}
