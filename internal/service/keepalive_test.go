package service

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestKeepaliveTask(t *testing.T) {
	if got := keepaliveTask(""); got != "gh-runners keepalive" {
		t.Errorf("main copy: %q", got)
	}
	a := keepaliveTask(filepath.Join(t.TempDir(), "a"))
	b := keepaliveTask(filepath.Join(t.TempDir(), "b"))
	if !strings.HasPrefix(a, "gh-runners keepalive (gh-runners-") || !strings.HasSuffix(a, ")") {
		t.Errorf("copy with its own home: %q", a)
	}
	if a == b || a == keepaliveTask("") {
		t.Errorf("tasks must differ per home: %q %q", a, b)
	}
}

func TestKeepaliveArgs(t *testing.T) {
	got := keepaliveArgs(`C:\bin\gh-runners.exe`, "")
	want := []string{"/Create", "/F", "/SC", "MINUTE", "/MO", "5", "/TN", "gh-runners keepalive",
		"/TR", `conhost.exe --headless "C:\bin\gh-runners.exe" daemon --detach --keepalive`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
	home := filepath.Join(t.TempDir(), "h")
	got = keepaliveArgs(`C:\bin\gh-runners.exe`, home)
	if tr := got[len(got)-1]; !strings.Contains(tr, ` --home "`+home+`" daemon --detach --keepalive`) {
		t.Errorf("home missing: %q", tr)
	}
	for _, a := range got {
		if a == "/RL" || a == "/RU" || a == "/RP" {
			t.Errorf("elevation or account flag %q", a)
		}
	}
}
