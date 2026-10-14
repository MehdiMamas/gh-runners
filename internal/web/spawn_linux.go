package web

import (
	"os"
	"os/exec"
)

// helperCommand keeps a helper alive while it restarts or stops the
// daemon: under systemd the unit's processes are all killed when it stops,
// so the helper (and any daemon it starts) gets a scope of its own.
func helperCommand(exe string, args ...string) *exec.Cmd {
	if os.Getenv("INVOCATION_ID") != "" {
		if run, err := exec.LookPath("systemd-run"); err == nil {
			return exec.Command(run, append([]string{"--user", "--scope", "--quiet", "--collect", "--", exe}, args...)...)
		}
	}
	return exec.Command(exe, args...)
}
