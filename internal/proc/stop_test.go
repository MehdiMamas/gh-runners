package proc

import (
	"bufio"
	"fmt"
	"io"
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
)

// TestMain doubles as the fake runner. FAKE_RUNNER=parent starts a sleeping
// child, prints both PIDs and sleeps itself. "leader" is run.sh as it was on
// the Mac: it exits on SIGINT / CTRL_BREAK while its child, "stubborn"
// (Runner.Listener), ignores the signal and keeps running.
func TestMain(m *testing.M) {
	switch os.Getenv("FAKE_RUNNER") {
	case "parent", "leader":
		kind := "child"
		if os.Getenv("FAKE_RUNNER") == "leader" {
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
		fmt.Printf("%d %d\n", os.Getpid(), child.Process.Pid)
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

// startFake starts the fake runner in the given mode, supervised, and
// returns the leader and its child.
func startFake(t *testing.T, mode string) (cmd *exec.Cmd, done chan struct{}, leader, child int) {
	t.Helper()
	cmd = exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "FAKE_RUNNER="+mode)
	Supervise(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(line)
	leader, _ = strconv.Atoi(f[0])
	child, _ = strconv.Atoi(f[1])
	if !Alive(leader) || !Alive(child) {
		t.Fatalf("not running before the stop: %d %d", leader, child)
	}
	done = make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, out); _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); killPIDs(descendants(cmd.Process.Pid)) })
	return
}

func goneWithin(d time.Duration, pids ...int) bool {
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		running := false
		for _, p := range pids {
			running = running || Alive(p)
		}
		if !running {
			return true
		}
	}
	return false
}

func TestStopKillsChildren(t *testing.T) {
	cmd, done, parent, child := startFake(t, "parent")
	Stop(cmd, done, time.Second)
	if !goneWithin(10*time.Second, parent, child) {
		t.Fatalf("still running after Stop: parent %v child %v", Alive(parent), Alive(child))
	}
}

// The Mac bug: the leader exits on the interrupt but its child keeps running.
// Stop must not return before the child is gone too.
func TestStopKillsChildThatOutlivesLeader(t *testing.T) {
	cmd, done, leader, child := startFake(t, "leader")
	start := time.Now()
	Stop(cmd, done, time.Second)
	if !goneWithin(10*time.Second, leader, child) {
		t.Fatalf("still running after Stop: leader %v child %v", Alive(leader), Alive(child))
	}
	if time.Since(start) > 8*time.Second {
		t.Fatalf("Stop took %s", time.Since(start))
	}
}

// strayAt copies the test binary to <folder>/bin/Runner.Listener and starts
// it as a leftover listener nothing supervises.
func strayAt(t *testing.T, folder string) (pid int) {
	t.Helper()
	exe := "Runner.Listener"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	bin := filepath.Join(folder, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bin, exe)
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

func hasPID(list []Stray, pid int) bool {
	for _, s := range list {
		if s.PID == pid {
			return true
		}
	}
	return false
}

func TestStraysMatchTheFolderOnly(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "runners", "a")
	pa := strayAt(t, a)
	pab := strayAt(t, filepath.Join(root, "runners", "ab"))         // the name is a prefix
	pother := strayAt(t, filepath.Join(root, "runners", "a-other")) // a similar name
	pin := strayAt(t, filepath.Join(root, "runners", "x", "a"))     // the same name elsewhere
	time.Sleep(200 * time.Millisecond)

	found := Strays(a)
	if len(found) != 1 || !hasPID(found, pa) {
		t.Fatalf("Strays(a) = %+v, want only pid %d", found, pa)
	}
	if got := Strays(filepath.Join(root, "runners")); len(got) != 4 {
		t.Fatalf("the folder above the runners owns all four, got %+v", got)
	}
	if got := Strays(filepath.Join(root, "runner")); len(got) != 0 {
		t.Fatalf("a name prefix of the folder matched %+v", got)
	}
	killed := KillStrays(a)
	if len(killed) != 1 || !hasPID(killed, pa) {
		t.Fatalf("KillStrays(a) = %+v", killed)
	}
	if !goneWithin(10*time.Second, pa) {
		t.Fatal("the stray is still running")
	}
	for name, pid := range map[string]int{"runners/ab": pab, "runners/a-other": pother, "runners/x/a": pin} {
		if !Alive(pid) {
			t.Errorf("%s was killed with a's strays", name)
		}
	}
	// The process list can trail a kill by a moment (Windows CI).
	for deadline := time.Now().Add(10 * time.Second); len(Strays(a)) != 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("strays listed after the kill")
		}
	}
}
