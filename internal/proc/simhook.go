package proc

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const simHookVar = "ACTIONS_RUNNER_HOOK_JOB_COMPLETED"

// simHookScript shuts the iOS simulators down when a job ends, unless
// another job still runs on this machine (its simulator would die with ours).
// The runner runs it inside Runner.Worker, so one worker is this job.
const simHookScript = `#!/bin/sh
# Written by gh-runners (#197). Shuts down iOS simulators a job left booted.
[ "$(pgrep -f 'Runner.Worker' | wc -l)" -le 1 ] || exit 0
xcrun simctl shutdown all >/dev/null 2>&1
exit 0
`

// withSimHook sets the runner's job-completed hook on macOS, so simulators a
// job booted stop crashing in the login session after it ends. An existing
// hook (the user's own .env or environment) is left alone.
func withSimHook(base []string, dir string) []string {
	if runtime.GOOS != "darwin" {
		return base
	}
	if base == nil {
		base = os.Environ()
	}
	for _, kv := range base {
		if envKey(kv) == simHookVar {
			return base
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, ".env")); err == nil && strings.Contains(string(b), simHookVar+"=") {
		return base
	}
	path := filepath.Join(dir, "job-completed.sh")
	if b, err := os.ReadFile(path); err != nil || string(b) != simHookScript {
		if os.WriteFile(path, []byte(simHookScript), 0o755) != nil {
			return base
		}
	}
	return append(base, simHookVar+"="+path)
}
