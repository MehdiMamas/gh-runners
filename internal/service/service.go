// Package service makes the daemon start at login: a launchd agent on
// macOS, a systemd user unit on Linux, and a Run registry entry on Windows.
// None of them need admin rights.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/proc"
)

// Name is used for the unit and registry value, Label for the launchd agent.
const (
	Name  = "gh-runners"
	Label = "io.github.mehdimamas.gh-runners"
)

// Status of the login service.
type Status struct {
	Installed bool
	Running   bool
	PID       int
	Where     string // file or registry key that makes it start
}

// Executable is the absolute path the service will run.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	return filepath.Abs(exe)
}

// Install registers the daemon to start at login and starts it now.
func Install() (string, error) { return install() }

// Uninstall stops the daemon and removes the login entry.
func Uninstall() error { return uninstall() }

// UninstallKeepRunning removes the login entry but leaves a daemon running
// until logout, so the dashboard that asked for it stays up.
func UninstallKeepRunning() error { return uninstallKeepRunning() }

// Restart stops and starts the daemon (to load a new binary, for example).
func Restart() error {
	if err := stop(); err != nil {
		return err
	}
	return start()
}

// Installed reports whether the login entry exists, without asking the
// daemon (the daemon itself uses it).
func Installed() Status { return status() }

// Start starts the daemon now.
func Start() error { return start() }

// Stop stops the daemon (and every runner) until the next Start or login.
func Stop() error { return stop() }

// Get reports whether the login entry exists and the daemon answers.
func Get() Status {
	s := status()
	if st, err := control.GetStatus(); err == nil {
		s.Running, s.PID = true, st.PID
	}
	return s
}

// StartDetached runs `<exe> daemon` in the background, detached from this
// terminal, and waits for it to answer.
func StartDetached() error {
	if _, err := control.GetStatus(); err == nil {
		return nil
	}
	exe, err := Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "daemon")
	proc.Detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return waitRunning(15 * time.Second)
}

func waitRunning(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := control.GetStatus(); err == nil {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("the daemon did not start within %s; see `gh-runners logs --daemon`", timeout)
}

func waitStopped(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := control.GetStatus(); err != nil {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %v: %s", name, args, err, out)
	}
	return nil
}
