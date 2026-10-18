package service

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", label()+".plist")
}

func domain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// Plist renders the launchd agent.
func Plist(exe, path, home string) string {
	env := fmt.Sprintf("\t\t<key>PATH</key><string>%s</string>\n", esc(path))
	if home != "" {
		env += fmt.Sprintf("\t\t<key>GH_RUNNERS_HOME</key><string>%s</string>\n", esc(home))
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>daemon</string>
	</array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key>
	<dict><key>SuccessfulExit</key><false/></dict>
	<key>ThrottleInterval</key><integer>30</integer>
	<key>EnvironmentVariables</key>
	<dict>
%s	</dict>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, Label+SuffixFor(home), esc(exe), env, esc(filepath.Join(store.Home(), "launchd.err.log")))
}

func install() (string, error) {
	exe, err := Executable()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath()), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(plistPath(), []byte(Plist(exe, os.Getenv("PATH"), os.Getenv("GH_RUNNERS_HOME"))), 0o644); err != nil {
		return "", err
	}
	_ = run("launchctl", "bootout", domain()+"/"+label())
	// A daemon started by hand would make the agent exit right away.
	_ = control.Shutdown()
	waitStopped(60 * time.Second)
	if err := run("launchctl", "bootstrap", domain(), plistPath()); err != nil {
		return "", err
	}
	return plistPath(), waitRunning(15 * time.Second)
}

func uninstall() error {
	_ = run("launchctl", "bootout", domain()+"/"+label())
	_ = control.Shutdown()
	waitStopped(60 * time.Second)
	if err := os.Remove(plistPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func start() error {
	if _, err := os.Stat(plistPath()); err != nil {
		return StartDetached()
	}
	if err := run("launchctl", "kickstart", domain()+"/"+label()); err != nil {
		if err := run("launchctl", "bootstrap", domain(), plistPath()); err != nil {
			return err
		}
	}
	return waitRunning(15 * time.Second)
}

// restart has launchd stop and start the job in one call. The helper that
// runs it was spawned by the daemon; if it booted the job out itself, it
// could go down with it and leave nothing to start the daemon again.
func restart() error {
	if _, err := os.Stat(plistPath()); err != nil {
		return stopStart()
	}
	if err := run("launchctl", "kickstart", "-k", domain()+"/"+label()); err != nil {
		if err := run("launchctl", "bootstrap", domain(), plistPath()); err != nil {
			return err
		}
	}
	return waitRunning(90 * time.Second)
}

func stop() error {
	if _, err := os.Stat(plistPath()); err == nil {
		// bootout, not kill: KeepAlive would restart a killed daemon.
		_ = run("launchctl", "bootout", domain()+"/"+label())
	}
	_ = control.Shutdown()
	waitStopped(90 * time.Second)
	return nil
}

func status() Status {
	_, err := os.Stat(plistPath())
	return Status{Installed: err == nil, Where: plistPath()}
}

func uninstallKeepRunning() error {
	if err := uninstall(); err != nil {
		return err
	}
	return StartDetached()
}
