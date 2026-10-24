package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/proc"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

func gone(t *testing.T, what string, pids ...int) {
	t.Helper()
	until(t, what, func() bool {
		for _, p := range pids {
			if proc.Alive(p) {
				return false
			}
		}
		return true
	})
}

// strayAt starts the test binary as <folder>/bin/Runner.Listener with nothing
// supervising it: what an older version left behind.
func strayAt(t *testing.T, folder string) int {
	t.Helper()
	exe := "Runner.Listener"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.MkdirAll(filepath.Join(folder, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(folder, "bin", exe)
	if err := os.WriteFile(path, src, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path)
	cmd.Env = append(os.Environ(), "FAKE_RUNNER=child")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd.Process.Pid
}

func fakeHome(t *testing.T, mode string) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_BIN", bin)
	t.Setenv("FAKE_RUNNER", mode)
	if err := store.SaveConfig(store.Config{DashboardPort: freePort(t)}); err != nil {
		t.Fatal(err)
	}
}

// The Mac bug: stopping a runner whose script exits on the interrupt while its
// listener keeps running. After the stop both are gone and the state is stopped.
func TestStopKillsListenerThatOutlivesTheScript(t *testing.T) {
	old := stopGrace
	stopGrace = time.Second
	defer func() { stopGrace = old }()
	fakeHome(t, "leader")
	a := fakeRunner(t, "a", false)
	d, stop := startDaemon(t)
	defer stop()
	lead, child := pids(t, a)

	a.Disabled = true
	if err := store.Save(a); err != nil {
		t.Fatal(err)
	}
	nudge(d.reload)
	gone(t, "script and listener after the stop", lead, child)
	// A stopped runner has no entry; the dashboard derives "stopped" from it.
	until(t, "no process state left", func() bool { s := state(d, "a"); return s.State != "stopping" && s.PID == 0 && s.Strays == 0 })
}

func TestSweepKillsStraysOfRunnersThatShouldNotRun(t *testing.T) {
	old := sweepEvery
	sweepEvery = 100 * time.Millisecond
	defer func() { sweepEvery = old }()
	fakeHome(t, "parent")
	a := fakeRunner(t, "a", true) // stopped by hand
	ab := fakeRunner(t, "ab", true)
	root := filepath.Dir(a.Dir())
	other := strayAt(t, a.Dir()+"-other")
	inAB := strayAt(t, ab.Dir())
	stray := strayAt(t, a.Dir())
	_ = root

	d, stop := startDaemon(t)
	defer stop()
	gone(t, "the stray of a", stray)
	gone(t, "the stray of ab", inAB)
	if !proc.Alive(other) {
		t.Fatal("a process of a folder next to the runner's was killed")
	}
	until(t, "no leftover state", func() bool { return state(d, "a").Strays == 0 })
}

func TestStartKillsStraysFirst(t *testing.T) {
	fakeHome(t, "parent")
	a := fakeRunner(t, "a", false)
	stray := strayAt(t, a.Dir())
	_, stop := startDaemon(t)
	defer stop()
	gone(t, "the stray before the start", stray)
	pids(t, a)
}

func TestStrayShowsAsStopping(t *testing.T) {
	old := sweepEvery
	sweepEvery = time.Hour
	defer func() { sweepEvery = old }()
	fakeHome(t, "parent")
	a := fakeRunner(t, "a", true)
	stray := strayAt(t, a.Dir())
	d, stop := startDaemon(t)
	defer stop()
	d.mu.Lock()
	d.strays["a"] = 1
	d.mu.Unlock()
	if s := state(d, "a"); s.State != "stopping" || s.Strays != 1 {
		t.Fatalf("a runner with a leftover process: %+v", s)
	}
	if !proc.Alive(stray) {
		t.Fatal("the stray was killed by a status call")
	}
}

func TestStopResult(t *testing.T) {
	cases := []struct {
		updated, signalled bool
		want               error
	}{
		{true, true, ErrUpdated},
		{true, false, nil},
		{false, true, nil},
		{false, false, nil},
	}
	for _, c := range cases {
		if got := stopResult(c.updated, c.signalled); got != c.want {
			t.Errorf("stopResult(%v,%v) = %v, want %v", c.updated, c.signalled, got, c.want)
		}
	}
}
