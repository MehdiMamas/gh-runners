package daemon

import (
	"io"
	"log"
	"os/exec"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/store"
)

func TestHoldIdle(t *testing.T) {
	for _, tc := range []struct {
		name    string
		busy    map[string]bool
		jitBusy int
		max     int
		want    []string
	}{
		{"no limit", map[string]bool{"a": false, "b": true}, 0, 0, nil},
		{"negative limit", map[string]bool{"a": false, "b": true}, 0, -1, nil},
		{"below the limit", map[string]bool{"a": false, "b": false}, 0, 1, nil},
		{"limit reached holds every idle runner", map[string]bool{"a": true, "b": false, "c": false}, 0, 1, []string{"b", "c"}},
		{"busy runners are never held", map[string]bool{"a": true, "b": true, "c": false}, 0, 2, []string{"c"}},
		{"jit busy counts toward the limit", map[string]bool{"a": false, "b": false}, 1, 1, []string{"a", "b"}},
		{"jit busy alone below the limit", map[string]bool{"a": false}, 1, 2, nil},
	} {
		if got := holdIdle(tc.busy, tc.jitBusy, tc.max, false); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestJitAllowed(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		max, maxJobs, busy, want int
	}{
		{"unset", 2, 0, 5, 2},
		{"cap above max", 2, 5, 0, 2},
		{"max below the cap", 1, 4, 1, 1},
		{"cap leaves room", 4, 3, 1, 2},
		{"floored at 0", 2, 1, 3, 0},
	} {
		if got := jitAllowed(tc.max, tc.maxJobs, tc.busy, false); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestHoldIdleInUse(t *testing.T) {
	busy := map[string]bool{"a": true, "b": false, "c": false}
	// In use, the limit counts as reached with no max_jobs and no jobs.
	if got := holdIdle(busy, 0, 0, true); !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Errorf("in use, busy runner kept: got %v", got)
	}
	if got := holdIdle(map[string]bool{"a": false}, 0, 5, true); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("in use, far below the limit: got %v", got)
	}
	if got := holdIdle(busy, 0, 0, false); got != nil {
		t.Errorf("free, no limit: got %v", got)
	}
}

func TestJitAllowedInUse(t *testing.T) {
	if got := jitAllowed(2, 0, 0, true); got != 0 {
		t.Errorf("in use: got %d, want 0", got)
	}
	if got := jitAllowed(2, 5, 0, true); got != 0 {
		t.Errorf("in use with a cap: got %d, want 0", got)
	}
}

func TestApplyLimitInUseHoldsThenLifts(t *testing.T) {
	d := &Daemon{Log: log.New(io.Discard, "", 0), children: map[string]*child{}, jit: newJITManager(nil)}
	// A runner that is held, nothing running: with the machine free again the
	// hold lifts only after holdTicks ticks, even without max_jobs.
	d.children["a"] = &child{held: true}
	for i := 1; i < holdTicks; i++ {
		if !d.applyLimit(0, false) {
			t.Fatalf("tick %d: hold lifted early", i)
		}
	}
	if d.applyLimit(0, false) {
		t.Fatal("hold did not lift after holdTicks free ticks")
	}
	if d.children["a"].held {
		t.Fatal("runner still held")
	}
	// In use again: the counter restarts, the hold stays.
	d.children["a"].held = true
	if !d.applyLimit(0, true) || d.below != 0 {
		t.Fatalf("in use must hold and reset the counter, below=%d", d.below)
	}
}

// fakeBusy is a busyFn driven by the test: dir -> busy, plus a call counter.
type fakeBusy struct {
	mu    sync.Mutex
	busy  map[string]bool
	calls int
}

func (f *fakeBusy) fn(dir string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.busy[dir]
}

func (f *fakeBusy) set(dir string, b bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.busy[dir] = b
}

func (f *fakeBusy) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// watchDaemon is a Daemon with running (fake) children named by ids, all busy,
// the busy state read from the returned fakeBusy, and holdPoll of 5 ms.
func watchDaemon(t *testing.T, maxJobs int, ids ...string) (*Daemon, *fakeBusy) {
	t.Helper()
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	cfg := store.DefaultConfig()
	cfg.MaxJobs = maxJobs
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	old := holdPoll
	holdPoll = 5 * time.Millisecond
	t.Cleanup(func() { holdPoll = old })
	f := &fakeBusy{busy: map[string]bool{}}
	d := &Daemon{
		Log: log.New(io.Discard, "", 0), children: map[string]*child{},
		jit: newJITManager(nil), strays: map[string]int{}, busyFn: f.fn,
	}
	for _, id := range ids {
		dir := t.TempDir()
		d.children[id] = &child{cmd: &exec.Cmd{}, dir: dir}
		f.set(dir, true)
	}
	return d, f
}

func (d *Daemon) applyLocked(maxJobs int, inUse bool) bool {
	if inUse {
		d.deskBusy.Store(true) // the watcher rechecks holdAll() live
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.applyLimit(maxJobs, inUse)
}

func (d *Daemon) childState(id string) (c *child, held, stopping, running bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c = d.children[id]
	return c, c.held, c.stopping, c.cmd != nil
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// quiet fails if f is called again within a few polls.
func quiet(t *testing.T, what string, f *fakeBusy) {
	t.Helper()
	n := f.count()
	time.Sleep(10 * holdPoll)
	if f.count() != n {
		t.Fatalf("%s: busy was still read", what)
	}
}

// A job ends and the next one would start before the next tick: the runner is
// stopped and held within holdPoll of the idle read, with no tick running.
func TestHoldWhenIdleStopsAtJobEnd(t *testing.T) {
	d, f := watchDaemon(t, 0, "a")
	c := d.children["a"]
	if !d.applyLocked(0, true) {
		t.Fatal("in use must hold")
	}
	if c.cmd == nil || c.held {
		t.Fatal("a busy runner must not be stopped by the tick")
	}
	f.set(c.dir, false)
	waitFor(t, "runner held", func() bool {
		_, held, stopping, running := d.childState("a")
		return held && !stopping && !running
	})
	d.mu.Lock()
	state, flag := c.state.State, c.holdAfterJob
	d.mu.Unlock()
	if state != "held" || flag {
		t.Fatalf("state %q, holdAfterJob %v", state, flag)
	}
	quiet(t, "after the stop", f)
}

// The runner is still busy at the read (a new job arrived): no stop, and the
// watcher keeps watching until the job ends.
func TestHoldWhenIdleKeepsWatchingWhileBusy(t *testing.T) {
	d, f := watchDaemon(t, 0, "a")
	c := d.children["a"]
	d.applyLocked(0, true)
	before := f.count()
	waitFor(t, "several busy reads", func() bool { return f.count() >= before+5 })
	if _, held, stopping, running := d.childState("a"); held || stopping || !running {
		t.Fatal("a busy runner was stopped")
	}
	d.mu.Lock()
	flag := c.holdAfterJob
	d.mu.Unlock()
	if !flag {
		t.Fatal("watcher gave up while the runner was busy")
	}
	f.set(c.dir, false)
	waitFor(t, "runner held", func() bool { _, held, _, running := d.childState("a"); return held && !running })
}

// The hold lifts while a watcher runs: it exits without stopping the runner.
func TestHoldWhenIdleExitsWhenHoldLifts(t *testing.T) {
	d, f := watchDaemon(t, 0, "a", "b")
	d.children["b"].held, d.children["b"].cmd = true, nil
	c := d.children["a"]
	d.applyLocked(0, true)
	d.deskBusy.Store(false)
	for i := 0; i < holdTicks; i++ {
		d.applyLocked(0, false)
	}
	if d.applyLocked(0, false) {
		t.Fatal("hold did not lift")
	}
	f.set(c.dir, false)
	quiet(t, "after the lift", f)
	if _, held, stopping, running := d.childState("a"); held || stopping || !running {
		t.Fatal("watcher stopped a runner after the hold lifted")
	}
}

// The hold stops being wanted without applyLimit lifting it (the update
// drain ends): the live recheck wins and the idle runner is not stopped.
func TestHoldWhenIdleRechecksLiveState(t *testing.T) {
	d, f := watchDaemon(t, 0, "a")
	c := d.children["a"]
	d.draining.Store(true)
	d.mu.Lock()
	d.applyLimit(0, true)
	d.mu.Unlock()
	d.draining.Store(false)
	f.set(c.dir, false)
	waitFor(t, "watcher to exit", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return !c.holdAfterJob
	})
	quiet(t, "after the recheck", f)
	if _, held, stopping, running := d.childState("a"); held || stopping || !running {
		t.Fatal("runner stopped though no hold is wanted")
	}
}

// The child is replaced or stopped by something else: the watcher exits and
// leaves the new child alone.
func TestHoldWhenIdleExitsWhenChildChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(d *Daemon, c *child)
	}{
		{"replaced", func(d *Daemon, c *child) { d.children["a"] = &child{cmd: &exec.Cmd{}, dir: c.dir} }},
		{"stopping", func(d *Daemon, c *child) { c.stopping = true }},
		{"stopped", func(d *Daemon, c *child) { c.cmd = nil }},
		{"removed", func(d *Daemon, c *child) { delete(d.children, "a") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, f := watchDaemon(t, 0, "a")
			c := d.children["a"]
			d.applyLocked(0, true)
			d.mu.Lock()
			tc.change(d, c)
			d.mu.Unlock()
			f.set(c.dir, false)
			quiet(t, "after the change", f)
			d.mu.Lock()
			defer d.mu.Unlock()
			if n := d.children["a"]; n != nil && n != c && (n.held || n.stopping || n.cmd == nil || n.holdAfterJob) {
				t.Fatal("the new child was touched")
			}
			if c.held && tc.name != "stopping" {
				t.Fatal("old child held")
			}
			if c.holdAfterJob {
				t.Fatal("watcher left its flag set")
			}
		})
	}
}

// max_jobs reached by two busy runners: both are watched. When one ends and
// the count falls below the limit it is not stopped; the other stays busy.
func TestHoldWhenIdleMaxJobsBelowLimitNotStopped(t *testing.T) {
	d, f := watchDaemon(t, 2, "a", "b")
	a, b := d.children["a"], d.children["b"]
	if !d.applyLocked(2, false) {
		t.Fatal("limit reached must hold")
	}
	d.mu.Lock()
	both := a.holdAfterJob && b.holdAfterJob
	d.mu.Unlock()
	if !both {
		t.Fatal("expected a watcher on both busy runners")
	}
	f.set(a.dir, false)
	waitFor(t, "watcher of a to exit", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return !a.holdAfterJob
	})
	if _, held, stopping, running := d.childState("a"); held || stopping || !running {
		t.Fatal("runner stopped though the count fell below the limit")
	}
	if _, held, _, running := d.childState("b"); held || !running {
		t.Fatal("busy runner touched")
	}
}

// max_jobs reached with a third runner still busy: the one whose job ends
// while the limit is still reached is held.
func TestHoldWhenIdleMaxJobsStillReachedStops(t *testing.T) {
	d, f := watchDaemon(t, 2, "a", "b", "c")
	d.applyLocked(2, false)
	f.set(d.children["a"].dir, false)
	waitFor(t, "a held", func() bool { _, held, _, running := d.childState("a"); return held && !running })
	if _, held, _, _ := d.childState("b"); held {
		t.Fatal("busy runner held")
	}
}

// applyLimit runs again while a watcher is live: no second watcher.
func TestApplyLimitStartsOneWatcherPerChild(t *testing.T) {
	d, _ := watchDaemon(t, 0, "a")
	d.applyLocked(0, true)
	time.Sleep(10 * holdPoll)
	n := runtime.NumGoroutine()
	for i := 0; i < 5; i++ {
		d.applyLocked(0, true)
	}
	time.Sleep(10 * holdPoll)
	if got := runtime.NumGoroutine(); got > n {
		t.Fatalf("goroutines %d -> %d: a second watcher started", n, got)
	}
}

// No hold wanted: no watcher and no extra busy reads.
func TestApplyLimitNoWatcherWithoutHold(t *testing.T) {
	d, f := watchDaemon(t, 0, "a")
	n := runtime.NumGoroutine()
	d.applyLocked(0, false)
	time.Sleep(10 * holdPoll)
	if d.children["a"].holdAfterJob || f.count() != 0 || runtime.NumGoroutine() > n {
		t.Fatalf("watcher started with no hold wanted (reads %d)", f.count())
	}
	// A limit that is not reached starts none either.
	d.applyLocked(5, false)
	time.Sleep(10 * holdPoll)
	if d.children["a"].holdAfterJob {
		t.Fatal("watcher started below the limit")
	}
}
