package doctor

import (
	"strings"
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/service"
)

func TestKeepaliveCheck(t *testing.T) {
	cases := []struct {
		goos string
		s    service.Status
		warn bool
	}{
		{"windows", service.Status{Installed: true}, true},
		{"windows", service.Status{Installed: true, Keepalive: true}, false},
		{"windows", service.Status{}, false},
		{"linux", service.Status{Installed: true}, false},
		{"darwin", service.Status{Installed: true}, false},
	}
	for _, c := range cases {
		if got := keepaliveCheck(c.goos, c.s) != ""; got != c.warn {
			t.Errorf("%s %+v: warn=%v, want %v", c.goos, c.s, got, c.warn)
		}
	}
}

func TestInteropCheck(t *testing.T) {
	at := time.Date(2026, 11, 5, 10, 30, 0, 0, time.UTC)
	if interopCheck(true, at, false) != "" {
		t.Error("warned while interop works")
	}
	if interopCheck(false, at, true) != "" {
		t.Error("warned outside WSL")
	}
	msg := interopCheck(true, at, true)
	for _, want := range []string{"2026-11-05 10:30", "wsl --shutdown", "not from inside WSL", "stops the daemon"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %s", want, msg)
		}
	}
	if m := interopCheck(true, time.Time{}, true); m == "" || strings.Contains(m, "since") {
		t.Errorf("unknown time: %q", m)
	}
}
