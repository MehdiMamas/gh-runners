//go:build linux

package ops

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// systemdUnits are the systemd units whose unit file names a file in dir:
// svc.sh's actions.runner.* system unit, or a user unit.
func systemdUnits(dir string) []foreignService {
	home, _ := os.UserHomeDir()
	var out []foreignService
	for _, folder := range []string{filepath.Join(home, ".config", "systemd", "user"), "/etc/systemd/system"} {
		files, _ := filepath.Glob(filepath.Join(folder, "*.service"))
		for _, p := range files {
			b, err := os.ReadFile(p)
			if err != nil || !containsPath(string(b), dir) {
				continue
			}
			out = append(out, foreignService{
				Name:       "systemd " + filepath.Base(p),
				Path:       p,
				NeedsAdmin: !strings.HasPrefix(p, home),
			})
		}
	}
	return out
}

func foreignServices(dir string) []foreignService { return systemdUnits(dir) }

// stopForeign disables the units that start the runner. A system unit
// needs root: it is tried with sudo -n (works when sudo needs no password)
// and otherwise the error says what to run.
func stopForeign(dir string, log io.Writer) error {
	var manual []string
	for _, s := range systemdUnits(dir) {
		unit := filepath.Base(s.Path)
		fmt.Fprintf(log, "Disabling %s ...\n", unit)
		if !s.NeedsAdmin {
			_ = exec.Command("systemctl", "--user", "disable", "--now", unit).Run()
			if err := os.Rename(s.Path, filepath.Join(dir, "gh-runners-old-"+unit)); err != nil {
				return err
			}
			_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
			continue
		}
		err := exec.Command("sudo", "-n", "systemctl", "disable", "--now", unit).Run()
		if err == nil {
			err = exec.Command("sudo", "-n", "mv", s.Path, filepath.Join(dir, "gh-runners-old-"+unit)).Run()
		}
		if err != nil {
			manual = append(manual, fmt.Sprintf("sudo systemctl disable --now %s && sudo rm %q", unit, s.Path))
			continue
		}
		_ = exec.Command("sudo", "-n", "systemctl", "daemon-reload").Run()
	}
	if len(manual) > 0 {
		return fmt.Errorf("a system service starts this runner and removing it needs your password. Run this in a terminal, then try again:\n%s", strings.Join(manual, "\n"))
	}
	return nil
}
