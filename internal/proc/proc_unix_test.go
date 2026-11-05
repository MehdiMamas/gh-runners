//go:build !windows

package proc

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParsePSEtime(t *testing.T) {
	cases := []struct {
		etime string
		want  time.Duration
	}{
		{"00:05", 5 * time.Second},
		{"12:34", 12*time.Minute + 34*time.Second},
		{"01:02:03", time.Hour + 2*time.Minute + 3*time.Second},
		{"2-03:04:05", 51*time.Hour + 4*time.Minute + 5*time.Second},
		{"10-00:00:00", 240 * time.Hour},
	}
	for _, c := range cases {
		pid, pgid, el, cmd, ok := parsePS("  42   7 " + c.etime + " /x/bin/Runner.Worker spawnclient 3 4")
		if !ok || pid != 42 || pgid != 7 || el != c.want || cmd != "/x/bin/Runner.Worker spawnclient 3 4" {
			t.Errorf("%s: got %d %d %v %q %v, want elapsed %v", c.etime, pid, pgid, el, cmd, ok, c.want)
		}
	}
	if _, _, el, _, ok := parsePS("42 7 junk cmd"); !ok || el != 0 {
		t.Errorf("bad etime: got elapsed %v ok %v, want 0 true", el, ok)
	}
	if _, _, _, _, ok := parsePS("42 7"); ok {
		t.Error("line without command parsed")
	}
}

func TestKillTreeKillsChildren(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 60 & sleep 60 & wait")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	var kids []int
	for deadline := time.Now().Add(5 * time.Second); len(kids) < 2 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		kids = kids[:0]
		out, _ := exec.Command("ps", "-A", "-o", "pid=,ppid=").Output()
		for _, l := range strings.Split(string(out), "\n") {
			if f := strings.Fields(l); len(f) == 2 && f[1] == strconv.Itoa(cmd.Process.Pid) {
				n, _ := strconv.Atoi(f[0])
				kids = append(kids, n)
			}
		}
	}
	if len(kids) < 2 {
		t.Fatalf("found %d children, want 2", len(kids))
	}
	if err := KillTree(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("leader still running")
	}
	for _, k := range kids {
		deadline := time.Now().Add(5 * time.Second)
		for alive(k) && !zombie(k) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if alive(k) && !zombie(k) {
			t.Errorf("child %d still running", k)
		}
	}
}
