package doctor

import (
	"strings"
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/service"
	"github.com/MehdiMamas/gh-runners/internal/store"
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

func TestHasScope(t *testing.T) {
	// HasScope drives the token-scope lines in Run: a missing "repo" scope
	// is a Fix ("the token lacks the `repo` scope..."), a missing
	// "admin:org" scope is a Note ("no `admin:org` scope...").
	cases := []struct {
		scopes string
		want   string
		has    bool
	}{
		// Ok: the token carries the scope.
		{"repo, admin:org", "repo", true},
		{"repo", "repo", true},
		{" repo , admin:org", "repo", true},
		{"repo, admin:org", "admin:org", true},
		{"workflow, repo", "repo", true},
		// Fail: the scope is absent.
		{"admin:org", "repo", false},
		{"repo", "admin:org", false},
		{"", "repo", false},
		{"", "admin:org", false},
		// Fail: a longer scope name is not a match.
		{"repo2, workflow", "repo", false},
		{"repo:status", "repo", false},
	}
	for _, c := range cases {
		if got := HasScope(c.scopes, c.want); got != c.has {
			t.Errorf("HasScope(%q, %q) = %v, want %v", c.scopes, c.want, got, c.has)
		}
	}
}

func TestUpdateLine(t *testing.T) {
	next := time.Date(2026, 11, 6, 9, 30, 0, 0, time.UTC)
	withNext := &control.Status{Update: control.UpdateState{Next: next}}
	cases := []struct {
		name string
		cfg  store.Config
		st   *control.Status
		want string
	}{
		{
			name: "default interval, no daemon status",
			cfg:  store.Config{},
			want: "update check every 6h (default)",
		},
		{
			name: "custom interval, no daemon status",
			cfg:  store.Config{UpdateInterval: store.Duration{Duration: 30 * time.Minute}},
			want: "update check every 30m",
		},
		{
			name: "custom interval over an hour",
			cfg:  store.Config{UpdateInterval: store.Duration{Duration: 90 * time.Minute}},
			want: "update check every 1h30m",
		},
		{
			name: "default interval with next check due",
			cfg:  store.Config{},
			st:   withNext,
			want: "update check every 6h (default); next " + next.Local().Format("15:04 Jan 2"),
		},
		{
			name: "custom interval with next check due",
			cfg:  store.Config{UpdateInterval: store.Duration{Duration: 2 * time.Hour}},
			st:   withNext,
			want: "update check every 2h; next " + next.Local().Format("15:04 Jan 2"),
		},
		{
			name: "daemon status without a next check",
			cfg:  store.Config{},
			st:   &control.Status{},
			want: "update check every 6h (default)",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := UpdateLine(c.cfg, c.st); got != c.want {
				t.Errorf("UpdateLine() = %q, want %q", got, c.want)
			}
		})
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
