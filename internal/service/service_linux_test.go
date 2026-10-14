package service

import (
	"strings"
	"testing"
)

func TestUnit(t *testing.T) {
	u := Unit("/home/me/.local/bin/gh-runners", "/usr/bin:/bin", "")
	for _, want := range []string{`ExecStart="/home/me/.local/bin/gh-runners" daemon`, `Environment="PATH=/usr/bin:/bin"`, "WantedBy=default.target", "KillMode=mixed"} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q:\n%s", want, u)
		}
	}
	if strings.Contains(u, "GH_RUNNERS_HOME") {
		t.Error("unit sets GH_RUNNERS_HOME without a home")
	}
}

func TestHasCron(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", "")
	other := "@reboot \"/x/gh-runners\" --home \"/tmp/t\" daemon >/dev/null 2>&1 # gh-runners-0a1b2c3d"
	if hasCron(other) {
		t.Error("main copy claims another copy's line")
	}
	if !hasCron(other + "\n@reboot \"/x/gh-runners\" daemon >/dev/null 2>&1 # gh-runners") {
		t.Error("main copy misses its own line")
	}
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	if hasCron("@reboot \"/x/gh-runners\" daemon >/dev/null 2>&1 # gh-runners") {
		t.Error("another copy claims the main line")
	}
	if !hasCron("@reboot x daemon " + cronTag()) {
		t.Error("another copy misses its own line")
	}
}
