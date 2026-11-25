package proc

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const jobHookVar = "ACTIONS_RUNNER_HOOK_JOB_COMPLETED"

// Both scripts run inside Runner.Worker, so one worker is this job; with
// more, another job may use the work folder or the simulators and nothing is
// touched. They empty the job's checkout only when it is under this runner
// folder's _work (never _work itself); the script sits in the runner folder.
const jobHookSh = `#!/bin/sh
# Written by gh-runners (#197, #202). Empties the job's checkout; on macOS shuts down iOS simulators.
[ "$(pgrep -f 'Runner.Worker' | wc -l)" -le 1 ] || exit 0
W="$(cd "$(dirname "$0")" && pwd)/_work/"
case "$GITHUB_WORKSPACE" in
  *..*) ;;
  "$W"?*) find "$GITHUB_WORKSPACE" -mindepth 1 -maxdepth 1 -exec rm -rf {} + >/dev/null 2>&1 ;;
esac
%s
exit 0
`

const jobHookPs1 = `# Written by gh-runners (#202). Empties the job's checkout.
if ((Get-Process Runner.Worker -ErrorAction SilentlyContinue | Measure-Object).Count -gt 1) { exit 0 }
$w = Join-Path $PSScriptRoot '_work'
$ws = $env:GITHUB_WORKSPACE
if ($ws -and $ws -notlike '*..*' -and [IO.Path]::GetFullPath($ws).StartsWith($w + '\', [StringComparison]::OrdinalIgnoreCase)) {
  Get-ChildItem -LiteralPath $ws -Force -ErrorAction SilentlyContinue | Remove-Item -Recurse -Force -ErrorAction SilentlyContinue
}
exit 0
`

// jobHook returns the hook file name and script for an OS.
func jobHook(goos string) (name, script string) {
	switch goos {
	case "windows":
		return "job-completed.ps1", jobHookPs1
	case "darwin":
		return "job-completed.sh", strings.Replace(jobHookSh, "%s", "xcrun simctl shutdown all >/dev/null 2>&1", 1)
	}
	return "job-completed.sh", strings.Replace(jobHookSh, "%s\n", "", 1)
}

// withJobHook sets the runner's job-completed hook, so a job's checkout is
// emptied when it ends (and, on macOS, simulators it booted stop). An
// existing hook (the user's own .env or environment) is left alone.
func withJobHook(base []string, dir string) []string {
	if base == nil {
		base = os.Environ()
	}
	for _, kv := range base {
		if envKey(kv) == jobHookVar {
			return base
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, ".env")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), jobHookVar+"=") {
				return base
			}
		}
	}
	name, script := jobHook(runtime.GOOS)
	path := filepath.Join(dir, name)
	if b, err := os.ReadFile(path); err != nil || string(b) != script {
		tmp := path + ".tmp"
		if os.WriteFile(tmp, []byte(script), 0o755) != nil || os.Rename(tmp, path) != nil {
			return base
		}
	}
	return append(base, jobHookVar+"="+path)
}
