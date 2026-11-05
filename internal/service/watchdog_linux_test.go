package service

import (
	"reflect"
	"strings"
	"testing"
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
