package service

import (
	"os"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// schtasks runs schtasks.exe with no window of its own.
func schtasks(args ...string) ([]byte, error) {
	cmd := exec.Command("schtasks.exe", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW, HideWindow: true}
	return cmd.CombinedOutput()
}

func installKeepalive(exe string) error {
	home := os.Getenv("GH_RUNNERS_HOME")
	return createTask(keepaliveArgs(exe, home), func(args []string) error {
		out, err := schtasks(args...)
		if err != nil {
			return &keepaliveError{err, out}
		}
		return nil
	})
}

type keepaliveError struct {
	err error
	out []byte
}

func (e *keepaliveError) Error() string {
	return "create the keepalive task: " + e.err.Error() + ": " + strings.TrimSpace(string(e.out))
}

// removeKeepalive deletes the task; a missing one is fine.
func removeKeepalive() {
	_, _ = schtasks("/Delete", "/F", "/TN", keepaliveTask(os.Getenv("GH_RUNNERS_HOME")))
}

func keepaliveExists() bool {
	_, err := schtasks("/Query", "/TN", keepaliveTask(os.Getenv("GH_RUNNERS_HOME")))
	return err == nil
}

func ensureKeepalive() (bool, error) {
	s := status()
	migrate := false
	if s.Keepalive {
		out, err := schtasks("/Query", "/TN", keepaliveTask(os.Getenv("GH_RUNNERS_HOME")), "/V", "/FO", "LIST")
		migrate = err == nil && interactiveOnly(string(out)) == logonInteractive
	}
	if !migrate && !needKeepalive(s.Installed, s.Keepalive) {
		return false, nil
	}
	exe, err := Executable()
	if err != nil {
		return false, err
	}
	if err := installKeepalive(exe); err != nil {
		return false, err
	}
	return true, nil
}
