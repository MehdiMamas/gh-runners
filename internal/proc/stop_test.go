package proc

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain doubles as the fake runner: with FAKE_RUNNER set the test binary
// starts a sleeping child, prints both PIDs and sleeps itself.
func TestMain(m *testing.M) {
	switch os.Getenv("FAKE_RUNNER") {
	case "parent":
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "FAKE_RUNNER=child")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		fmt.Printf("%d %d\n", os.Getpid(), child.Process.Pid)
		time.Sleep(time.Minute)
		os.Exit(0)
	case "child":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestStopTreeKillsChildren(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "FAKE_RUNNER=parent")
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
	parent, _ := strconv.Atoi(f[0])
	child, _ := strconv.Atoi(f[1])
	if !Alive(parent) || !Alive(child) {
		t.Fatalf("not running before the stop: %d %d", parent, child)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	StopTree(cmd, done, time.Second)
	deadline := time.Now().Add(10 * time.Second)
	for (Alive(parent) || Alive(child)) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if Alive(parent) || Alive(child) {
		t.Fatalf("still running after StopTree: parent %v child %v", Alive(parent), Alive(child))
	}
}
