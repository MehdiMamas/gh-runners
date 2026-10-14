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
