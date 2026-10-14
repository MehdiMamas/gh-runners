//go:build darwin

package ops

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// launchdPlists are the launchd jobs whose plist names a file in dir:
// svc.sh's actions.runner.* agent, or one a script wrote.
func launchdPlists(dir string) []foreignService {
	home, _ := os.UserHomeDir()
	var out []foreignService
	for _, folder := range []string{filepath.Join(home, "Library", "LaunchAgents"), "/Library/LaunchAgents", "/Library/LaunchDaemons"} {
		files, _ := filepath.Glob(filepath.Join(folder, "*.plist"))
		for _, p := range files {
			b, err := os.ReadFile(p)
			if err != nil || !containsPath(string(b), dir) {
				continue
			}
			out = append(out, foreignService{
				Name:       "launchd " + strings.TrimSuffix(filepath.Base(p), ".plist"),
				Path:       p,
				NeedsAdmin: !strings.HasPrefix(p, home),
			})
		}
	}
	return out
}

func foreignServices(dir string) []foreignService { return launchdPlists(dir) }

// stopForeign unloads the launchd agents that start the runner and keeps
// their plists in the runner folder (gh-runners-old-<name>.plist), so
// nothing starts the old copy again.
func stopForeign(dir string, log io.Writer) error {
	var needRoot []string
	for _, s := range launchdPlists(dir) {
		if s.NeedsAdmin {
			needRoot = append(needRoot, s.Path)
			continue
		}
		fmt.Fprintf(log, "Unloading %s ...\n", s.Path)
		_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d", os.Getuid()), s.Path).Run()
		keep := filepath.Join(dir, "gh-runners-old-"+filepath.Base(s.Path))
		if err := os.Rename(s.Path, keep); err != nil {
			return fmt.Errorf("could not move %s out of LaunchAgents: %w", s.Path, err)
		}
	}
	if len(needRoot) > 0 {
		var cmds []string
		for _, p := range needRoot {
			cmds = append(cmds, fmt.Sprintf("sudo launchctl bootout system %q; sudo rm %q", p, p))
		}
		return fmt.Errorf("a system launchd job starts this runner and removing it needs your password. Run this in Terminal, then try again:\n%s", strings.Join(cmds, "\n"))
	}
	return nil
}
