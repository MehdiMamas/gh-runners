package service

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/store"
)

const (
	watchdogTask    = "gh-runners WSL watchdog"
	watchdogScript  = "wsl-watchdog.ps1"
	watchdogBackup  = "wsl-watchdog.ps1.bak"
	watchdogState   = "wsl-interop.state"
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
$state = Join-Path $dir 'wsl-interop.state'

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

# Checks that Windows programs can start from inside the distro (WSL
# interop). Only records and logs a change; it never touches the counter and
# never terminates the distro. A timeout counts as broken. A distro without
# Windows programs on its PATH reads as ok.
function Test-Interop {
  $r = Run-Wsl @('-d', $distro, '--', 'sh', '-c', '"command -v cmd.exe >/dev/null || exit 0; cmd.exe /c exit 0"')
  $prev = ''
  if (Test-Path $state) {
    try { $prev = (Get-Content $state -Raw).Trim() } catch { $prev = '' }
  }
  if ($r -eq 'ok') {
    if ($prev -like 'broken*') { Log 'interop ok again' }
    Set-Content $state 'ok' -Encoding ascii
  } elseif ($prev -notlike 'broken*') {
    Log 'interop broken'
    Set-Content $state ("broken {0}" -f (Get-Date -Format 's')) -Encoding ascii
  }
}

$count = 0
if (Test-Path $counter) {
  try { $count = [int](Get-Content $counter -Raw).Trim() } catch { $count = 0 }
}

$result = Start-UserManager
if ($result -eq 'ok') {
  Set-Content $counter 0
  Log 'ok'
  Test-Interop
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

// watchdogCommand is the program and arguments the task runs: no window
// (conhost --headless).
func watchdogCommand(scriptWinPath string) (command, args string) {
	return "conhost.exe", `--headless powershell.exe -NoProfile -ExecutionPolicy Bypass -File "` + scriptWinPath + `"`
}

// schtasksArgs is the plain schtasks command line that runs the script every
// 5 minutes (no admin, no SYSTEM). It is the fallback when the S4U task
// cannot be created; it runs only while the user is signed in.
func schtasksArgs(scriptWinPath string) []string {
	c, a := watchdogCommand(scriptWinPath)
	return []string{"/Create", "/F", "/SC", "MINUTE", "/MO", "5", "/TN", watchdogTask, "/TR", c + " " + a}
}

// watchdogXML is the S4U task definition: it runs before anybody signs in.
func watchdogXML(scriptWinPath, user string) string {
	c, a := watchdogCommand(scriptWinPath)
	return taskXML(watchdogTask, user, c, a, "PT5M")
}

// windowsUser is DOMAIN\user of the Windows account, read through interop.
func windowsUser() (string, error) {
	cmd := exec.Command("cmd.exe", "/c", `echo %USERDOMAIN%\%USERNAME%`)
	cmd.Dir = "/mnt/c"
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	u := strings.TrimSpace(string(out))
	if u == "" || strings.Contains(u, "%") || strings.HasPrefix(u, `\`) {
		return "", fmt.Errorf("could not read the Windows user name: %q", u)
	}
	return u, nil
}

// registerWatchdogTask creates the S4U task, or the sign-in-only one when
// that fails (logged).
func registerWatchdogTask(winDir, unixDir string) error {
	script := winDir + `\` + watchdogScript
	user, err := windowsUser()
	if err == nil {
		err = registerTaskXML(func(args ...string) error { return run("schtasks.exe", args...) },
			watchdogTask, watchdogXML(script, user), unixDir,
			func(base string) string { return winDir + `\` + base })
	}
	if err == nil {
		return nil
	}
	log.Printf("WSL watchdog task: S4U logon failed, using the sign-in-only task: %v", err)
	return run("schtasks.exe", schtasksArgs(script)...)
}

// migrateWatchdogTask recreates an existing task that still logs on with an
// interactive token. A missing task is never created here. It reports
// whether it recreated the task.
func migrateWatchdogTask(winDir, unixDir string) (bool, error) {
	out, err := exec.Command("schtasks.exe", "/Query", "/TN", watchdogTask, "/XML").Output()
	if !needS4U(string(out), err) {
		return false, nil
	}
	if err := registerWatchdogTask(winDir, unixDir); err != nil {
		return false, err
	}
	return true, nil
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

// recordedWatchdogDir is the unix path of the script folder saved at
// install, for when cmd.exe cannot be started to find it.
func recordedWatchdogDir() string {
	b, err := os.ReadFile(store.WatchdogDirPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func recordWatchdogDir(unix string) error {
	if recordedWatchdogDir() == unix {
		return nil
	}
	if err := os.MkdirAll(store.Home(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(store.WatchdogDirPath(), []byte(unix+"\n"), 0o644)
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
	if err := recordWatchdogDir(unix); err != nil {
		return err
	}
	return registerWatchdogTask(win, unix)
}

// removeWatchdog deletes the task, the script and what the probe left. Best
// effort; with broken interop the folder comes from the recorded path.
func removeWatchdog() {
	if !watchdogWanted() {
		return
	}
	_ = run("schtasks.exe", "/Delete", "/F", "/TN", watchdogTask)
	unix := ""
	if _, u, err := watchdogDir(); err == nil {
		unix = u
	} else {
		unix = recordedWatchdogDir()
	}
	if unix != "" {
		for _, f := range []string{watchdogScript, watchdogBackup, watchdogState} {
			_ = os.Remove(filepath.Join(unix, f))
		}
	}
	_ = os.Remove(store.WatchdogDirPath())
}

// EnsureWatchdog rewrites the script of an install that already has one when
// it differs from the current template, and recreates its task as S4U when it
// still logs on interactively. It never creates a script or a missing task.
// It reports whether it changed anything.
func EnsureWatchdog() (bool, error) {
	if !watchdogWanted() {
		return false, nil
	}
	win, unix, derr := watchdogDir()
	if derr != nil {
		unix = recordedWatchdogDir()
	}
	if unix == "" {
		return false, nil
	}
	if _, err := os.Stat(filepath.Join(unix, watchdogScript)); err != nil {
		return false, nil
	}
	if err := recordWatchdogDir(unix); err != nil {
		return false, err
	}
	changed, err := rewriteWatchdog(unix, renderWatchdog(os.Getenv("WSL_DISTRO_NAME"), os.Getuid()))
	if err != nil {
		return changed, err
	}
	if derr != nil {
		return changed, nil // no interop: the task cannot be read or made
	}
	moved, err := migrateWatchdogTask(win, unix)
	return changed || moved, err
}

// rewriteWatchdog replaces the script in dir with content when it exists and
// differs: the old one is kept as .bak, the new one goes in by rename.
func rewriteWatchdog(dir, content string) (bool, error) {
	path := filepath.Join(dir, watchdogScript)
	old, err := os.ReadFile(path)
	if err != nil || string(old) == content {
		return false, nil
	}
	if err := os.WriteFile(filepath.Join(dir, watchdogBackup), old, 0o644); err != nil {
		return false, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return true, nil
}

// InteropBroken tells whether the watchdog found that Windows programs
// cannot start from WSL, and since when.
func InteropBroken() (since time.Time, broken bool) {
	dir := recordedWatchdogDir()
	if dir == "" {
		return time.Time{}, false
	}
	b, err := os.ReadFile(filepath.Join(dir, watchdogState))
	if err != nil {
		return time.Time{}, false
	}
	return parseInteropState(string(b))
}

// parseInteropState reads the state file: "ok" or "broken <ISO time>", with
// a possible BOM and CRLF.
func parseInteropState(s string) (time.Time, bool) {
	s = strings.TrimSpace(strings.TrimPrefix(s, "\ufeff"))
	rest, ok := strings.CutPrefix(s, "broken")
	if !ok {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006-01-02T15:04:05", strings.TrimSpace(rest), time.Local)
	if err != nil {
		return time.Time{}, true
	}
	return t, true
}
