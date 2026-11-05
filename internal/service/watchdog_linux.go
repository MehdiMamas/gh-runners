package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	watchdogTask    = "gh-runners WSL watchdog"
	watchdogScript  = "wsl-watchdog.ps1"
	watchdogTimeout = 90000 // ms the start command may take before it counts as stuck
	watchdogLimit   = 2     // stuck runs in a row before the distro is terminated
)

// watchdogTemplate is the Windows-side script. It runs outside the WSL VM,
// so it can still act when the VM is frozen: it starts the user manager of
// the distro (which brings the daemon back) and, when that hangs twice in a
// row, terminates the distro and tries again.
const watchdogTemplate = `$distro = '@DISTRO@'
$uid = @UID@
$dir = Split-Path -Parent $MyInvocation.MyCommand.Path
$log = Join-Path $dir 'wsl-watchdog.log'
$counter = Join-Path $dir 'wsl-watchdog.count'

function Log($msg) {
  if ((Test-Path $log) -and (Get-Item $log).Length -gt 1MB) {
    $tail = Get-Content $log -Tail 200
    Set-Content $log $tail
  }
  Add-Content $log ("{0} {1}" -f (Get-Date -Format 's'), $msg)
}

# Runs wsl.exe and returns 'ok', 'fail' or 'timeout'.
function Run-Wsl($wslArgs) {
  $p = Start-Process -FilePath 'wsl.exe' -ArgumentList $wslArgs -PassThru -WindowStyle Hidden
  if (-not $p.WaitForExit(@TIMEOUT@)) {
    try { $p.Kill() } catch {}
    return 'timeout'
  }
  if ($p.ExitCode -eq 0) { return 'ok' }
  return 'fail'
}

function Start-UserManager {
  return Run-Wsl @('-d', $distro, '-u', 'root', '--', 'systemctl', 'start', "user@$uid.service")
}

$count = 0
if (Test-Path $counter) {
  try { $count = [int](Get-Content $counter -Raw).Trim() } catch { $count = 0 }
}

$result = Start-UserManager
if ($result -eq 'ok') {
  Set-Content $counter 0
  Log 'ok'
  exit 0
}
if ($result -eq 'fail') {
  Log 'start command failed'
  exit 1
}

$count++
Log "start command timed out ($count in a row)"
if ($count -lt @LIMIT@) {
  Set-Content $counter $count
  exit 1
}
Log "terminating $distro"
Run-Wsl @('--terminate', $distro) | Out-Null
Set-Content $counter 0
$result = Start-UserManager
Log "start after terminate: $result"
if ($result -eq 'ok') { exit 0 }
exit 1
`

// renderWatchdog is the script for the given distro and user id.
func renderWatchdog(distro string, uid int) string {
	return strings.NewReplacer(
		"@DISTRO@", strings.ReplaceAll(distro, "'", "''"),
		"@UID@", fmt.Sprint(uid),
		"@TIMEOUT@", fmt.Sprint(watchdogTimeout),
		"@LIMIT@", fmt.Sprint(watchdogLimit),
	).Replace(watchdogTemplate)
}

// schtasksArgs creates the per-user task that runs the script every 5
// minutes: no admin, no SYSTEM, and no window (conhost --headless).
func schtasksArgs(scriptWinPath string) []string {
	return []string{
		"/Create", "/F", "/SC", "MINUTE", "/MO", "5", "/TN", watchdogTask,
		"/TR", `conhost.exe --headless powershell.exe -NoProfile -ExecutionPolicy Bypass -File "` + scriptWinPath + `"`,
	}
}

// watchdogWanted tells whether this is WSL with Windows interop, the only
// place the watchdog applies.
func watchdogWanted() bool {
	if os.Getenv("WSL_DISTRO_NAME") == "" {
		return false
	}
	_, err := exec.LookPath("schtasks.exe")
	return err == nil
}

// watchdogDir is the folder of the script, as a Windows path and as a path
// seen from WSL.
func watchdogDir() (win, unix string, err error) {
	cmd := exec.Command("cmd.exe", "/c", "echo %LOCALAPPDATA%")
	cmd.Dir = "/mnt/c"
	out, err := cmd.Output()
	if err != nil {
		return "", "", err
	}
	local := strings.TrimSpace(string(out))
	if local == "" || strings.Contains(local, "%") {
		return "", "", fmt.Errorf("could not read LOCALAPPDATA from Windows")
	}
	u, err := exec.Command("wslpath", "-u", local).Output()
	if err != nil {
		return "", "", err
	}
	return local + `\gh-runners`, filepath.Join(strings.TrimSpace(string(u)), "gh-runners"), nil
}

// installWatchdog writes the script and registers the task.
func installWatchdog() error {
	win, unix, err := watchdogDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(unix, 0o755); err != nil {
		return err
	}
	script := renderWatchdog(os.Getenv("WSL_DISTRO_NAME"), os.Getuid())
	if err := os.WriteFile(filepath.Join(unix, watchdogScript), []byte(script), 0o644); err != nil {
		return err
	}
	return run("schtasks.exe", schtasksArgs(win+`\`+watchdogScript)...)
}

// removeWatchdog deletes the task and the script. Best effort.
func removeWatchdog() {
	if !watchdogWanted() {
		return
	}
	_ = run("schtasks.exe", "/Delete", "/F", "/TN", watchdogTask)
	if _, unix, err := watchdogDir(); err == nil {
		_ = os.Remove(filepath.Join(unix, watchdogScript))
	}
}
