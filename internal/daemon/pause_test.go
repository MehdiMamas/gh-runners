package daemon

import (
	"context"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/proc"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// TestMain doubles as the fake runner: with FAKE_RUNNER=parent the test
// binary starts a sleeping child, writes both PIDs to pids.txt in its
// directory and sleeps.
func TestMain(m *testing.M) {
	switch os.Getenv("FAKE_RUNNER") {
	case "parent", "leader":
		kind := "child"
		if os.Getenv("FAKE_RUNNER") == "leader" {
			// run.sh as on the Mac: it exits on the interrupt, its child ignores it.
			kind = "stubborn"
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, syscall.SIGINT)
			go func() { <-sig; os.Exit(0) }()
		}
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "FAKE_RUNNER="+kind)
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		_ = os.WriteFile("pids.txt", []byte(strconv.Itoa(os.Getpid())+" "+strconv.Itoa(child.Process.Pid)), 0o644)
		time.Sleep(time.Minute)
		os.Exit(0)
	case "stubborn":
		signal.Ignore(syscall.SIGINT)
		time.Sleep(time.Minute)
		os.Exit(0)
	case "child":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeRunner(t *testing.T, id string, disabled bool) store.Runner {
	t.Helper()
	r := store.Runner{ID: id, Kind: store.KindRepo, Owner: "octo", Repo: id, Name: id, Disabled: disabled}
	if err := store.Save(r); err != nil {
		t.Fatal(err)
	}
	var name, body string
	if runtime.GOOS == "windows" {
		name, body = "run.cmd", "@\"%FAKE_BIN%\"\r\n"
	} else {
		name, body = "run.sh", "#!/bin/sh\nexec \"$FAKE_BIN\"\n"
	}
	if err := os.WriteFile(filepath.Join(r.Dir(), name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir(), ".runner"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	return r
}

func pids(t *testing.T, r store.Runner) (int, int) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		b, err := os.ReadFile(filepath.Join(r.Dir(), "pids.txt"))
		if f := strings.Fields(string(b)); err == nil && len(f) == 2 {
			p, _ := strconv.Atoi(f[0])
			c, _ := strconv.Atoi(f[1])
			return p, c
		}
	}
	t.Fatal("the fake runner never started")
	return 0, 0
}

func until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out: %s", what)
}

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func state(d *Daemon, id string) control.ProcState {
	for _, p := range d.status().Runners {
		if p.ID == id {
			return p
		}
	}
	return control.ProcState{}
}

func startDaemon(t *testing.T) (*Daemon, func()) {
	t.Helper()
	d := &Daemon{Version: "test"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = d.Run(ctx); close(done) }()
	until(t, "daemon up", func() bool { _, err := control.GetStatus(); return err == nil })
	return d, func() { cancel(); <-done }
}

func TestPauseResume(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_BIN", bin)
	t.Setenv("FAKE_RUNNER", "parent")
	if err := store.SaveConfig(store.Config{DashboardPort: freePort(t), OnDemand: store.OnDemand{Enabled: true}, Sync: store.Sync{Auto: true}}); err != nil {
		t.Fatal(err)
	}
	a := fakeRunner(t, "a", false)
	b := fakeRunner(t, "b", false)
	hand := fakeRunner(t, "hand", true) // stopped by hand

	d, stop := startDaemon(t)
	defer stop()
	ap, ac := pids(t, a)
	bp, bc := pids(t, b)
	if _, err := os.Stat(filepath.Join(hand.Dir(), "pids.txt")); err == nil {
		t.Fatal("a hand-stopped runner started")
	}

	if err := control.Pause(); err != nil {
		t.Fatal(err)
	}
	if !store.IsPaused() || !d.Paused() {
		t.Fatal("not paused")
	}
	for _, pid := range []int{ap, ac, bp, bc} {
		if proc.Alive(pid) {
			t.Errorf("pid %d still running after pause", pid)
		}
	}
	if st := d.status(); !st.Paused || st.OnDemand != "paused" {
		t.Fatalf("status while paused: %+v", st)
	}
	// Nothing comes back by itself, however long the reconcile loop runs.
	d.reconcile()
	time.Sleep(300 * time.Millisecond)
	if s := state(d, "a"); s.PID != 0 {
		t.Fatalf("runner a restarted while paused: %+v", s)
	}
	if err := d.jit.launch(context.Background(), nil, store.Config{}, nil, github.Scope{}, nil); err == nil {
		t.Fatal("an on-demand runner launched while paused")
	}
	// Pausing twice is fine.
	if err := d.Pause(); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(a.Dir(), "pids.txt")); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(b.Dir(), "pids.txt"))
	if err := control.Resume(); err != nil {
		t.Fatal(err)
	}
	if store.IsPaused() || d.Paused() {
		t.Fatal("still paused")
	}
	pids(t, a)
	pids(t, b)
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(hand.Dir(), "pids.txt")); err == nil {
		t.Fatal("resume started the hand-stopped runner")
	}
	if r, _ := store.Load("hand"); !r.Disabled {
		t.Fatal("resume cleared Disabled")
	}
}

func TestPauseSurvivesRestart(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	bin, _ := os.Executable()
	t.Setenv("FAKE_BIN", bin)
	t.Setenv("FAKE_RUNNER", "parent")
	if err := store.SaveConfig(store.Config{DashboardPort: freePort(t)}); err != nil {
		t.Fatal(err)
	}
	a := fakeRunner(t, "a", false)
	if err := store.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	d, stop := startDaemon(t)
	defer stop()
	time.Sleep(500 * time.Millisecond)
	if !d.Paused() || !d.status().Paused {
		t.Fatal("a new daemon forgot the pause")
	}
	if _, err := os.Stat(filepath.Join(a.Dir(), "pids.txt")); err == nil {
		t.Fatal("a runner started in a paused home")
	}
	if err := d.Resume(); err != nil {
		t.Fatal(err)
	}
	pids(t, a)
}
