package service

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/control"
)

func unitPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "systemd", "user", name()+".service")
}

func hasSystemd() bool {
	return exec.Command("systemctl", "--user", "show-environment").Run() == nil
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"` }

// Unit renders the systemd user unit.
func Unit(exe, path, home string) string {
	env := "Environment=" + quote("PATH="+path) + "\n"
	if home != "" {
		env += "Environment=" + quote("GH_RUNNERS_HOME="+home) + "\n"
	}
	return fmt.Sprintf(`[Unit]
Description=gh-runners: GitHub Actions self-hosted runner supervisor
Wants=network-online.target
After=network-online.target

[Service]
ExecStart=%s daemon
Restart=on-failure
RestartSec=10
%sKillMode=mixed
TimeoutStopSec=90

[Install]
WantedBy=default.target
`, quote(exe), env)
}

func install() (string, error) {
	exe, err := Executable()
	if err != nil {
		return "", err
	}
	if !hasSystemd() {
		return installCron(exe)
	}
	if err := os.MkdirAll(filepath.Dir(unitPath()), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(unitPath(), []byte(Unit(exe, os.Getenv("PATH"), os.Getenv("GH_RUNNERS_HOME"))), 0o644); err != nil {
		return "", err
	}
	// A daemon started by hand would make the unit exit right away.
	_ = run("systemctl", "--user", "stop", name()+".service")
	_ = control.Shutdown()
	waitStopped(60 * time.Second)
	if err := run("systemctl", "--user", "daemon-reload"); err != nil {
		return "", err
	}
	if err := run("systemctl", "--user", "enable", "--now", name()+".service"); err != nil {
		return "", err
	}
	// Lingering keeps user services running without an open login session
	// (headless box, after a reboot). Best effort: some distros need sudo.
	if u, err := user.Current(); err == nil {
		if err := run("loginctl", "enable-linger", u.Username); err != nil {
			fmt.Fprintf(os.Stderr, "note: could not enable lingering, so runners only run while you are logged in.\nTo fix: sudo loginctl enable-linger %s\n", u.Username)
		}
	}
	return unitPath(), waitRunning(15 * time.Second)
}

// cronTag ends the crontab line of this copy of gh-runners.
func cronTag() string { return "# " + name() }

// hasCron tells whether crontab text has this copy's line. The tag must end
// the line: "# gh-runners" is also the start of another copy's tag.
func hasCron(text string) bool {
	for _, l := range strings.Split(text, "\n") {
		if strings.HasSuffix(strings.TrimSpace(l), cronTag()) {
			return true
		}
	}
	return false
}

// installCron is the fallback without a systemd user session (WSL, some
// containers): an @reboot crontab line, and start the daemon now.
func installCron(exe string) (string, error) {
	existing, _ := exec.Command("crontab", "-l").Output()
	if !hasCron(string(existing)) {
		next := strings.TrimRight(string(existing), "\n")
		if next != "" {
			next += "\n"
		}
		args := ""
		if h := os.Getenv("GH_RUNNERS_HOME"); h != "" {
			args = " --home " + quote(h)
		}
		next += "@reboot " + quote(exe) + args + " daemon >/dev/null 2>&1 " + cronTag() + "\n"
		cmd := exec.Command("crontab", "-")
		cmd.Stdin = strings.NewReader(next)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("no systemd user session and crontab failed (%v: %s); run `gh-runners daemon` from your own startup script", err, out)
		}
	}
	return "crontab @reboot", StartDetached()
}

func uninstall() error {
	if _, err := os.Stat(unitPath()); err == nil {
		_ = run("systemctl", "--user", "disable", "--now", name()+".service")
		if err := os.Remove(unitPath()); err != nil {
			return err
		}
		_ = run("systemctl", "--user", "daemon-reload")
	}
	if existing, err := exec.Command("crontab", "-l").Output(); err == nil && hasCron(string(existing)) {
		var keep []string
		for _, l := range strings.Split(string(existing), "\n") {
			if l != "" && !hasCron(l) {
				keep = append(keep, l)
			}
		}
		cmd := exec.Command("crontab", "-")
		cmd.Stdin = strings.NewReader(strings.Join(keep, "\n") + "\n")
		_ = cmd.Run()
	}
	_ = control.Shutdown()
	waitStopped(60 * time.Second)
	return nil
}

func start() error {
	if _, err := os.Stat(unitPath()); err == nil && hasSystemd() {
		if err := run("systemctl", "--user", "start", name()+".service"); err != nil {
			return err
		}
		return waitRunning(15 * time.Second)
	}
	return StartDetached()
}

func stop() error {
	if _, err := os.Stat(unitPath()); err == nil && hasSystemd() {
		_ = run("systemctl", "--user", "stop", name()+".service")
	}
	_ = control.Shutdown()
	waitStopped(90 * time.Second)
	return nil
}

func status() Status {
	if _, err := os.Stat(unitPath()); err == nil {
		return Status{Installed: true, Where: unitPath()}
	}
	if existing, err := exec.Command("crontab", "-l").Output(); err == nil && hasCron(string(existing)) {
		return Status{Installed: true, Where: "crontab @reboot"}
	}
	return Status{Where: unitPath()}
}

func uninstallKeepRunning() error {
	if err := uninstall(); err != nil {
		return err
	}
	return StartDetached()
}
