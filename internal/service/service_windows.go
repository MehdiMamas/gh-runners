package service

import (
	"os"
	"time"

	"golang.org/x/sys/windows/registry"

	"github.com/MehdiMamas/gh-runners/internal/control"
)

// The per-user Run key starts the daemon at login without admin rights.
// (A Windows service would need admin and a stored password.)
const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func install() (string, error) {
	exe, err := Executable()
	if err != nil {
		return "", err
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	cmdline := `"` + exe + `" daemon --detach`
	if h := os.Getenv("GH_RUNNERS_HOME"); h != "" {
		cmdline = `"` + exe + `" --home "` + h + `" daemon --detach`
	}
	if err := k.SetStringValue(name(), cmdline); err != nil {
		return "", err
	}
	return `HKCU\` + runKey + `\` + name(), StartDetached()
}

func uninstall() error {
	if k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE); err == nil {
		_ = k.DeleteValue(name())
		k.Close()
	}
	return stop()
}

func start() error { return StartDetached() }

func stop() error {
	_ = control.Shutdown()
	waitStopped(90 * time.Second)
	return nil
}

func status() Status {
	s := Status{Where: `HKCU\` + runKey + `\` + name()}
	if k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE); err == nil {
		_, _, err := k.GetStringValue(name())
		s.Installed = err == nil
		k.Close()
	}
	return s
}

func uninstallKeepRunning() error {
	if k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE); err == nil {
		_ = k.DeleteValue(name())
		k.Close()
	}
	return nil
}
