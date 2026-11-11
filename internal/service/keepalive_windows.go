package service

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
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

// installKeepalive creates the S4U task. When that fails, a fresh install
// (fallback true) gets the sign-in-only task; a migration (fallback false)
// leaves the existing task alone and returns the error.
func installKeepalive(exe string, fallback bool) error {
	home := os.Getenv("GH_RUNNERS_HOME")
	var err error
	if u, uerr := user.Current(); uerr != nil || u.Username == "" {
		err = fmt.Errorf("no Windows user name: %v", uerr)
	} else {
		dir := os.TempDir()
		err = registerTaskXML(func(args ...string) error {
			out, err := schtasks(args...)
			if err != nil {
				return &keepaliveError{err, out}
			}
			return nil
		}, keepaliveTask(home), keepaliveXML(exe, home, u.Username), dir,
			func(base string) string { return filepath.Join(dir, base) })
	}
	if err == nil {
		return nil
	}
	if !fallback {
		return err
	}
	log.Printf("keepalive task: S4U logon failed, using the sign-in-only task: %v", err)
	out, err := schtasks(keepaliveArgs(exe, home)...)
	if err != nil {
		return &keepaliveError{err, out}
	}
	return nil
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
	var out []byte
	var qerr error
	if s.Installed && s.Keepalive {
		out, qerr = schtasks("/Query", "/TN", keepaliveTask(os.Getenv("GH_RUNNERS_HOME")), "/XML")
	}
	switch keepalivePlan(s.Installed, s.Keepalive, string(out), qerr) {
	case planCreate, planMigrate:
		exe, err := Executable()
		if err != nil {
			return false, err
		}
		if err := installKeepalive(exe, !s.Keepalive); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}
