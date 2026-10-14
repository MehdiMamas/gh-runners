// Package service makes the daemon start at login: a launchd agent on
// macOS, a systemd user unit on Linux, and a Run registry entry on Windows.
// None of them need admin rights.
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/proc"
)

// Name is used for the unit and registry value, Label for the launchd agent,
// both followed by Suffix.
const (
	Name  = "gh-runners"
	Label = "io.github.mehdimamas.gh-runners"
)

// Suffix tells a copy with its own state directory ($GH_RUNNERS_HOME or
// --home) apart from the main one in ~/.gh-runners: "" for the main one,
// "-" and 8 hex characters of the directory otherwise. Login entries and
// shortcuts carry it, so installing or removing a second copy (a test, say)
// never replaces or removes the main one's.
func Suffix() string { return SuffixFor(os.Getenv("GH_RUNNERS_HOME")) }

// SuffixFor is Suffix for the state directory home ("" is the main one).
func SuffixFor(home string) string {
	if home == "" {
		return ""
	}
	norm := func(p string) string {
		if a, err := filepath.Abs(p); err == nil {
			p = a
		}
		p = filepath.Clean(p)
		if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
			p = strings.ToLower(p)
		}
		return p
	}
	if u, err := os.UserHomeDir(); err == nil && norm(home) == norm(filepath.Join(u, ".gh-runners")) {
		return ""
	}
	sum := sha256.Sum256([]byte(norm(home)))
	return "-" + hex.EncodeToString(sum[:4])
}

func name() string  { return Name + Suffix() }
func label() string { return Label + Suffix() }

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

// Spawn runs this executable with args, detached, writing its output to
// logPath. The dashboard and the updater use it to restart the daemon they
// run in.
func Spawn(logPath string, args ...string) error {
	exe, err := Executable()
	if err != nil {
		return err
	}
	f, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := helperCommand(exe, args...)
	cmd.Stdout, cmd.Stderr = f, f
	proc.Detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
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
