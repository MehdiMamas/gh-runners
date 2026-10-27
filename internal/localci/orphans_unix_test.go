//go:build !windows

package localci

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/store"
)

func waitGone(t *testing.T, pid int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("process %d still runs", pid)
}

func TestCancelKillsChildren(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// The child ignores SIGTERM, so only the SIGKILL after the grace ends it.
		done <- runCmd(ctx, dir, os.Environ(), os.Stderr, "sh", "-c", "(trap '' TERM; sleep 300) & echo $! > "+pidFile+"; wait")
	}()
	var pid int
	for i := 0; i < 100 && pid == 0; i++ {
		time.Sleep(50 * time.Millisecond)
		b, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	if pid == 0 {
		t.Fatal("child did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("cancelled step did not return")
	}
	waitGone(t, pid)
}

func TestSweepOrphans(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GH_RUNNERS_HOME", home)
	run := filepath.Join(store.Home(), "ci", "octo-repo", "pr7-abc1234-1700000000", "build", "src")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	mk := func(dir string) *exec.Cmd {
		c := exec.Command("sleep", "300")
		c.Dir = dir
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		go c.Wait()
		return c
	}
	orphan, bystander := mk(run), mk(other)
	defer bystander.Process.Kill()
	time.Sleep(300 * time.Millisecond)

	if n := SweepOrphans(t.Logf); n < 1 {
		t.Fatalf("killed %d processes", n)
	}
	waitGone(t, orphan.Process.Pid)
	if syscall.Kill(bystander.Process.Pid, 0) != nil {
		t.Fatal("a process outside ci/ was killed")
	}
	h := History()
	if len(h) != 1 || h[0].Repo != "octo/repo" || h[0].PR != 7 || !strings.Contains(h[0].Error, "daemon restarted") {
		t.Fatalf("history: %+v", h)
	}
	if SweepOrphans(t.Logf); len(History()) != 1 {
		t.Fatalf("second sweep added history: %+v", History())
	}
}

func TestSweepSkipsRunOwnedByLiveProcess(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	ci := filepath.Join(store.Home(), "ci", "octo-repo")
	liveRun := filepath.Join(ci, "pr1-aaaaaaa-1700000001", "build", "src")
	deadRun := filepath.Join(ci, "pr2-bbbbbbb-1700000002", "build", "src")
	for _, d := range []string{liveRun, deadRun} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(dir string) *exec.Cmd {
		c := exec.Command("sleep", "300")
		c.Dir = dir
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		go c.Wait()
		return c
	}
	// A live owner: another process that is not us.
	owner := mk(t.TempDir())
	defer owner.Process.Kill()
	// A dead owner: started and reaped.
	d := exec.Command("true")
	if err := d.Run(); err != nil {
		t.Fatal(err)
	}
	write := func(run string, pid int) {
		f := filepath.Join(filepath.Dir(filepath.Dir(run)), ownerFile)
		if err := os.WriteFile(f, []byte(strconv.Itoa(pid)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(liveRun, owner.Process.Pid)
	write(deadRun, d.Process.Pid)

	live, dead := mk(liveRun), mk(deadRun)
	defer live.Process.Kill()
	time.Sleep(300 * time.Millisecond)

	SweepOrphans(t.Logf)
	waitGone(t, dead.Process.Pid)
	if syscall.Kill(live.Process.Pid, 0) != nil {
		t.Fatal("a run owned by a live process was killed")
	}
	h := History()
	if len(h) != 1 || h[0].PR != 2 {
		t.Fatalf("history: %+v", h)
	}
}
