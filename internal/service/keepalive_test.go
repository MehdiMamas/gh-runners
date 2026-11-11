package service

import (
	"errors"
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

func TestKeepalivePlan(t *testing.T) {
	inter := `<LogonType>InteractiveToken</LogonType>`
	s4u := `<LogonType>S4U</LogonType>`
	for _, c := range []struct {
		name              string
		installed, exists bool
		xml               string
		err               error
		want              keepaliveStep
	}{
		{"not installed", false, false, "", nil, planNothing},
		{"not installed, stray task", false, true, inter, nil, planNothing},
		{"installed, no task", true, false, "", nil, planCreate},
		{"installed, interactive task", true, true, inter, nil, planMigrate},
		{"installed, S4U task", true, true, s4u, nil, planNothing},
		{"installed, task unreadable", true, true, "", errors.New("x"), planNothing},
	} {
		if got := keepalivePlan(c.installed, c.exists, c.xml, c.err); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
