// Package doctor checks this machine: token, platform, dependencies,
// daemon and runners. The CLI prints the checks; the dashboard shows them.
package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/ops"
	"github.com/MehdiMamas/gh-runners/internal/runner"
	"github.com/MehdiMamas/gh-runners/internal/service"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// Levels of a check.
const (
	OK   = "ok"
	Fix  = "fix"
	Note = "note"
)

// Check is one line of the report.
type Check struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

// Run returns every check. st is the daemon status when the caller already
// has it (the daemon itself), or nil to ask the daemon.
func Run(ctx context.Context, st *control.Status) []Check {
	var out []Check
	add := func(level, format string, a ...any) { out = append(out, Check{level, fmt.Sprintf(format, a...)}) }

	if osName, arch, err := runner.Platform(); err != nil {
		add(Fix, "%v", err)
	} else {
		add(OK, "runner package: %s-%s", osName, arch)
	}
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		add(Fix, "running as root: the runner refuses to configure as root; use a normal user")
	}

	ts, err := github.ResolveToken(store.TokenPath())
	if err != nil {
		add(Fix, "%v", err)
	} else {
		me, scopes, err := github.New(ts.Token).Me(ctx)
		switch {
		case err != nil:
			add(Fix, "token from %s does not work: %v", ts.Source, err)
		case scopes == "":
			add(OK, "signed in as %s (token from %s, fine-grained: needs Administration read/write on the repos)", me.Login, ts.Source)
		default:
			add(OK, "signed in as %s (token from %s, scopes: %s)", me.Login, ts.Source, scopes)
			if !HasScope(scopes, "repo") {
				add(Fix, "the token lacks the `repo` scope: sign in again, or gh auth refresh -s repo")
			}
			if !HasScope(scopes, "admin:org") {
				add(Note, "no `admin:org` scope: fine for repo runners; organization runners need it")
			}
		}
	}

	if runtime.GOOS == "linux" {
		if out, err := exec.Command("ldconfig", "-p").Output(); err == nil && !strings.Contains(string(out), "libicu") {
			add(Fix, "libicu not found; the runner needs it. Install it with your package manager (for example: sudo apt install libicu-dev)")
		}
	}

	s := service.Get()
	switch {
	case s.Running && s.Installed:
		add(OK, "background service running (pid %d), starts at login (%s)", s.PID, s.Where)
	case s.Running:
		add(Note, "background service running (pid %d) but not set to start at login", s.PID)
	case s.Installed:
		add(Fix, "background service set to start at login but not running: gh-runners service start")
	default:
		add(Fix, "background service not installed: gh-runners service install")
	}

	if p := store.LoadPause(); p.Paused {
		add(Note, "this machine is paused: no runner, on-demand runner, sync or update runs; gh-runners resume turns them back on")
	}

	cfg, _ := store.LoadConfig()
	add(OK, "labels this machine answers to: %s", strings.Join(ops.MachineLabels(cfg), ", "))
	add(OK, "runners folder: %s", store.RunnersDir())
	if cfg.Sync.Auto {
		add(OK, "auto-sync on (every %s)", cfg.Sync.Interval.Or(time.Hour))
	}
	if cfg.OnDemand.Enabled {
		if st == nil {
			if x, err := control.GetStatus(); err == nil {
				st = &x
			}
		}
		if st != nil {
			add(OK, "on-demand: %s", st.OnDemand)
		} else {
			add(OK, "on-demand on")
		}
	}

	runners, _ := store.List()
	unregistered := 0
	for _, r := range runners {
		if !r.Configured() {
			unregistered++
		}
	}
	add(OK, "%d runners on this machine", len(runners))
	if unregistered > 0 {
		add(Fix, "%d runner folders were never registered (an add failed); add them again or remove them", unregistered)
	}
	return out
}

// HasScope reports whether a comma separated X-OAuth-Scopes list has want.
func HasScope(scopes, want string) bool {
	for _, s := range strings.Split(scopes, ",") {
		if strings.TrimSpace(s) == want {
			return true
		}
	}
	return false
}
