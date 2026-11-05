package daemon

import (
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

type healthEnv struct {
	t       *testing.T
	d       *Daemon
	now     time.Time
	status  string
	listErr error
	missing bool
	busy    bool
	kills   []int
}

func newHealthEnv(t *testing.T) *healthEnv {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	e := &healthEnv{t: t, now: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC), status: "online"}
	e.d = &Daemon{Log: log.New(io.Discard, "", 0), strays: map[string]int{}}
	e.d.children = map[string]*child{"a": {
		cmd: &exec.Cmd{}, dir: "dir-a",
		state: control.ProcState{ID: "a", State: "running", PID: 99, Since: e.now.Add(-time.Hour)},
	}}
	r := cfgRunner("a", false)
	r.Kind, r.Owner, r.Repo, r.Name = store.KindRepo, "o", "r", "a"
	if err := store.Save(r); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(r.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir(), ".runner"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *healthEnv) tick(advance time.Duration) {
	e.t.Helper()
	e.now = e.now.Add(advance)
	rs, _ := store.List()
	list := func(github.Scope) ([]github.Runner, error) {
		if e.listErr != nil {
			return nil, e.listErr
		}
		if e.missing {
			return nil, nil
		}
		return []github.Runner{{Name: "a", Status: e.status}}, nil
	}
	e.d.checkGitHub(e.now, rs, list, func(string) bool { return e.busy }, func(pid int) error {
		e.kills = append(e.kills, pid)
		return nil
	})
}

func (e *healthEnv) state() control.ProcState { return e.d.children["a"].state }

func TestHealthOnlineNoFlag(t *testing.T) {
	e := newHealthEnv(t)
	e.tick(0)
	if e.state().GitHubOffline || len(e.kills) != 0 {
		t.Fatal("online runner must not be flagged or killed")
	}
	if e.d.ghCheck.At != e.now || e.d.ghCheck.Err != "" {
		t.Fatalf("ghCheck = %+v", e.d.ghCheck)
	}
}

func TestHealthOfflineFlagThenKillOnce(t *testing.T) {
	e := newHealthEnv(t)
	e.status = "offline"
	e.tick(0)
	if !e.state().GitHubOffline || !e.state().GitHubOfflineSince.Equal(e.now) || len(e.kills) != 0 {
		t.Fatalf("first sighting: %+v kills %v", e.state(), e.kills)
	}
	first := e.state().GitHubOfflineSince
	e.tick(2 * time.Minute)
	if len(e.kills) != 0 {
		t.Fatal("killed before 5 minutes")
	}
	e.tick(3 * time.Minute)
	if len(e.kills) != 1 || e.kills[0] != 99 {
		t.Fatalf("kills = %v", e.kills)
	}
	if !e.state().GitHubOfflineSince.Equal(first) || !e.d.children["a"].healthKill {
		t.Fatal("since must stay and healthKill must be set")
	}
	e.tick(2 * time.Minute)
	e.tick(2 * time.Minute)
	if len(e.kills) != 1 {
		t.Fatalf("second kill within 15 minutes: %v", e.kills)
	}
	e.tick(15 * time.Minute)
	if len(e.kills) != 2 {
		t.Fatalf("no kill after 15 minutes: %v", e.kills)
	}
}

func TestHealthListErrorKeepsFlags(t *testing.T) {
	e := newHealthEnv(t)
	e.status = "offline"
	e.tick(0)
	since := e.state().GitHubOfflineSince
	e.listErr = errors.New("rate limited")
	e.tick(10 * time.Minute)
	if len(e.kills) != 0 || !e.state().GitHubOffline || !e.state().GitHubOfflineSince.Equal(since) {
		t.Fatalf("flags changed on error: %+v kills %v", e.state(), e.kills)
	}
	if e.d.ghCheck.Err != "rate limited" || !e.d.ghCheck.At.Equal(e.now) {
		t.Fatalf("ghCheck = %+v", e.d.ghCheck)
	}
	e.listErr = nil
	e.tick(time.Minute)
	if len(e.kills) != 1 {
		t.Fatal("the 5 minute clock must keep counting from the first sighting")
	}
}

func TestHealthSkips(t *testing.T) {
	for name, mod := range map[string]func(e *healthEnv){
		"young":    func(e *healthEnv) { e.d.children["a"].state.Since = e.now.Add(-time.Minute) },
		"paused":   func(e *healthEnv) { e.d.paused.Store(true) },
		"backoff":  func(e *healthEnv) { e.d.children["a"].state.State = "backoff" },
		"stopping": func(e *healthEnv) { e.d.children["a"].stopping = true },
		"missing":  func(e *healthEnv) { e.missing = true },
	} {
		t.Run(name, func(t *testing.T) {
			e := newHealthEnv(t)
			e.status = "offline"
			mod(e)
			e.tick(0)
			if e.state().GitHubOffline || len(e.kills) != 0 {
				t.Fatalf("must be skipped: %+v kills %v", e.state(), e.kills)
			}
		})
	}
}

func TestHealthBusyNotKilled(t *testing.T) {
	e := newHealthEnv(t)
	e.status = "offline"
	e.busy = true
	e.tick(0)
	e.tick(10 * time.Minute)
	if len(e.kills) != 0 || !e.state().GitHubOffline {
		t.Fatalf("busy runner: kills %v state %+v", e.kills, e.state())
	}
	e.busy = false
	e.tick(time.Minute)
	if len(e.kills) != 1 {
		t.Fatal("must restart once the job is done")
	}
}

func TestHealthComingBackOnlineClears(t *testing.T) {
	e := newHealthEnv(t)
	e.status = "offline"
	e.tick(0)
	e.status = "online"
	e.tick(time.Minute)
	if e.state().GitHubOffline || !e.state().GitHubOfflineSince.IsZero() {
		t.Fatalf("flag not cleared: %+v", e.state())
	}
}

func TestHealthRecheckPIDAndSince(t *testing.T) {
	e := newHealthEnv(t)
	e.status = "offline"
	e.tick(0)
	// The runner restarted between collect and kill: the new process must not be hit.
	rs, _ := store.List()
	list := func(github.Scope) ([]github.Runner, error) {
		e.d.mu.Lock()
		e.d.children["a"].state.PID = 123
		e.d.mu.Unlock()
		return []github.Runner{{Name: "a", Status: "offline"}}, nil
	}
	e.now = e.now.Add(10 * time.Minute)
	e.d.checkGitHub(e.now, rs, list, func(string) bool { return false }, func(pid int) error {
		e.kills = append(e.kills, pid)
		return nil
	})
	if len(e.kills) != 0 {
		t.Fatalf("killed a changed process: %v", e.kills)
	}
}

func TestHealthFlagClearedOnBackoff(t *testing.T) {
	e := newHealthEnv(t)
	c := e.d.children["a"]
	c.state.GitHubOffline, c.state.GitHubOfflineSince = true, e.now
	e.d.backoff(c, "x")
	if c.state.GitHubOffline || !c.state.GitHubOfflineSince.IsZero() {
		t.Fatal("backoff must clear the flag")
	}
}

func TestHealthKillIsNotACrash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stand-in listener is a shell script")
	}
	e := newHealthEnv(t)
	e.d.reload = make(chan struct{}, 1)
	r := cfgRunner("a", false)
	if err := os.WriteFile(filepath.Join(r.Dir(), "run.sh"), []byte("#!/bin/sh\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &child{state: control.ProcState{ID: "a", Restarts: 4, GitHubOffline: true, GitHubOfflineSince: e.now}}
	e.d.mu.Lock()
	e.d.children["a"] = c
	err := e.d.start(r, c)
	c.healthKill = true
	cmd, done := c.cmd, c.done
	if c.state.GitHubOffline {
		t.Error("start must clear the GitHub offline flag")
	}
	e.d.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	_ = cmd.Process.Kill()
	<-done
	for i := 0; i < 200; i++ {
		e.d.mu.Lock()
		st := c.state.State
		e.d.mu.Unlock()
		if st == "backoff" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.d.mu.Lock()
	defer e.d.mu.Unlock()
	if c.state.State != "backoff" || c.state.Restarts != 1 || c.state.LastExit != "restarted: GitHub showed it offline" {
		t.Fatalf("state = %+v", c.state)
	}
	if wait := time.Until(c.state.NextStart); wait > minBackoff {
		t.Fatalf("next start in %v, want at most %v", wait, minBackoff)
	}
}
