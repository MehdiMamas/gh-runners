package service

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRenderWatchdog(t *testing.T) {
	s := renderWatchdog("Ubuntu-24.04", 1000)
	for _, want := range []string{
		"$distro = 'Ubuntu-24.04'",
		"$uid = 1000",
		"WaitForExit(90000)",
		"$count -lt 2",
		"user@$uid.service",
		"'--terminate'",
		"wsl-watchdog.log",
		"function Test-Interop",
		"command -v cmd.exe >/dev/null || exit 0; cmd.exe /c exit 0",
		"wsl-interop.state",
		"interop broken",
		"interop ok again",
		"Set-Content $state 'ok' -Encoding ascii",
		"-Encoding ascii",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	if strings.Contains(s, "@DISTRO@") || strings.Contains(s, "@UID@") {
		t.Error("placeholder left in script")
	}
	if got := renderWatchdog("it's", 1); !strings.Contains(got, "$distro = 'it''s'") {
		t.Error("quote in distro name not escaped")
	}
}

func TestWatchdogProbeInOkBranch(t *testing.T) {
	s := renderWatchdog("Ubuntu", 1000)
	start := strings.Index(s, "if ($result -eq 'ok') {")
	if start < 0 {
		t.Fatal("no ok branch")
	}
	block := s[start:]
	end := strings.Index(block, "\n}\n")
	if end < 0 {
		t.Fatal("ok branch not closed")
	}
	block = block[:end]
	logOK := strings.Index(block, "Log 'ok'")
	probe := strings.Index(block, "Test-Interop")
	exit := strings.Index(block, "exit 0")
	if logOK < 0 || probe < 0 || exit < 0 || !(logOK < probe && probe < exit) {
		t.Errorf("want Log 'ok' < Test-Interop < exit 0 in the ok branch, got %d %d %d", logOK, probe, exit)
	}
	if strings.Contains(block, "terminate") {
		t.Error("ok branch must not terminate the distro")
	}
}

func TestParseInteropState(t *testing.T) {
	at := time.Date(2026, 11, 5, 10, 30, 0, 0, time.Local)
	cases := []struct {
		in     string
		since  time.Time
		broken bool
	}{
		{"ok", time.Time{}, false},
		{"ok\r\n", time.Time{}, false},
		{"", time.Time{}, false},
		{"broken 2026-11-05T10:30:00", at, true},
		{"broken 2026-11-05T10:30:00\r\n", at, true},
		{"\ufeffbroken 2026-11-05T10:30:00\r\n", at, true},
		{"\ufeffok\r\n", time.Time{}, false},
		{"broken", time.Time{}, true},
	}
	for _, c := range cases {
		since, broken := parseInteropState(c.in)
		if broken != c.broken || !since.Equal(c.since) {
			t.Errorf("%q: got %v %v, want %v %v", c.in, since, broken, c.since, c.broken)
		}
	}
}

func TestInteropBroken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GH_RUNNERS_HOME", home)
	if _, broken := InteropBroken(); broken {
		t.Fatal("broken without a recorded folder")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "watchdog-dir"), []byte(dir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, broken := InteropBroken(); broken {
		t.Fatal("broken without a state file")
	}
	state := filepath.Join(dir, watchdogState)
	os.WriteFile(state, []byte("ok\r\n"), 0o644)
	if _, broken := InteropBroken(); broken {
		t.Fatal("ok read as broken")
	}
	os.WriteFile(state, []byte("\ufeffbroken 2026-11-05T10:30:00\r\n"), 0o644)
	since, broken := InteropBroken()
	if !broken || since.Year() != 2026 || since.Hour() != 10 {
		t.Fatalf("got %v %v", since, broken)
	}
}

func TestRewriteWatchdog(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, watchdogScript)

	if ok, err := rewriteWatchdog(dir, "new"); ok || err != nil {
		t.Fatalf("no script: got %v %v", ok, err)
	}
	if _, err := os.Stat(script); err == nil {
		t.Fatal("script created where there was none")
	}

	os.WriteFile(script, []byte("old"), 0o644)
	if ok, err := rewriteWatchdog(dir, "old"); ok || err != nil {
		t.Fatalf("equal: got %v %v", ok, err)
	}
	if _, err := os.Stat(filepath.Join(dir, watchdogBackup)); err == nil {
		t.Fatal("backup written for an equal script")
	}

	ok, err := rewriteWatchdog(dir, "new")
	if !ok || err != nil {
		t.Fatalf("rewrite: got %v %v", ok, err)
	}
	if b, _ := os.ReadFile(script); string(b) != "new" {
		t.Errorf("script is %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, watchdogBackup)); string(b) != "old" {
		t.Errorf("backup is %q", b)
	}
	if _, err := os.Stat(script + ".tmp"); err == nil {
		t.Error("temp file left behind")
	}
}

func TestSchtasksArgs(t *testing.T) {
	got := schtasksArgs(`C:\Users\me\AppData\Local\gh-runners\wsl-watchdog.ps1`)
	want := []string{
		"/Create", "/F", "/SC", "MINUTE", "/MO", "5", "/TN", "gh-runners WSL watchdog",
		"/TR", `conhost.exe --headless powershell.exe -NoProfile -ExecutionPolicy Bypass -File "C:\Users\me\AppData\Local\gh-runners\wsl-watchdog.ps1"`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

const goldenWatchdog = `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>gh-runners WSL watchdog</Description></RegistrationInfo>
  <Triggers><TimeTrigger><Repetition><Interval>PT5M</Interval></Repetition><StartBoundary>2026-01-01T00:00:00</StartBoundary><Enabled>true</Enabled></TimeTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>PC\me</UserId><LogonType>S4U</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries><StopIfGoingOnBatteries>false</StopIfGoingOnBatteries><StartWhenAvailable>true</StartWhenAvailable><ExecutionTimeLimit>PT5M</ExecutionTimeLimit><Enabled>true</Enabled><Hidden>true</Hidden></Settings>
  <Actions Context="Author"><Exec><Command>conhost.exe</Command><Arguments>--headless powershell.exe -NoProfile -ExecutionPolicy Bypass -File &#34;C:\Users\me\AppData\Local\gh-runners\wsl-watchdog.ps1&#34;</Arguments></Exec></Actions>
</Task>
`

func TestWatchdogXMLGolden(t *testing.T) {
	got := watchdogXML(`C:\Users\me\AppData\Local\gh-runners\wsl-watchdog.ps1`, `PC\me`)
	if got != goldenWatchdog {
		t.Errorf("got:\n%s", got)
	}
}
