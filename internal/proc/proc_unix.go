//go:build !windows

package proc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func supervise(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func interrupt(cmd *exec.Cmd) {
	// The runner's run.sh forwards SIGINT to Runner.Listener, which then
	// closes its session with GitHub cleanly.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
}

func kill(cmd *exec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

func terminate(cmd *exec.Cmd, grace time.Duration) {
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	// The group outlives its leader while any member runs, so this also
	// reaches a child that ignored SIGTERM.
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if syscall.Kill(-pid, 0) == syscall.ESRCH {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

// treeGone reports whether no process is left in cmd's process group. The
// group outlives its leader for as long as one member runs, which is how an
// orphaned Runner.Listener shows up.
func treeGone(cmd *exec.Cmd, _ []procRef) bool {
	return syscall.Kill(-cmd.Process.Pid, 0) == syscall.ESRCH
}

func env() []string { return nil }

func missingTools() []string { return nil }

type procRef = int

// descendants is only needed where the tree has no process group to aim at.
func descendants(pid int) []procRef { return nil }

func killPIDs([]procRef) {}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// shells are the interpreters run.sh and run-helper.sh run under.
var shells = map[string]bool{"sh": true, "bash": true, "dash": true, "zsh": true}

// strays reads the process table with ps. The command column starts with the
// executable as it was started, and the runner is always started from its
// absolute folder, so the exact folder prefix identifies its processes. A
// script runs as "<shell> <dir>/run.sh", so those two names count under a
// shell too.
func strays(dir string) []Stray {
	out, err := exec.Command("ps", "-axo", "pid=,pgid=,etime=,command=").Output()
	if err != nil {
		return nil
	}
	self, selfGroup := os.Getpid(), syscall.Getpgrp()
	dir = filepath.Clean(dir)
	var found []Stray
	live := map[int]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		pid, pgid, elapsed, cmdline, ok := parsePS(line)
		if !ok {
			continue
		}
		live[pid] = true
		if pid == self || !ownedBy(dir, cmdline) {
			continue
		}
		s := Stray{PID: pid, Path: cmdline}
		if elapsed > 0 {
			s.Start = time.Now().Add(-elapsed)
		}
		// Take the process group along (the job's processes are in it) when
		// it is the runner's own: led by a process of this runner, or by none
		// any more (run.sh exited and left Listener behind).
		if pgid > 1 && pgid != selfGroup {
			s.group = pgid
		}
		found = append(found, s)
	}
	mine := map[int]bool{}
	for _, s := range found {
		mine[s.PID] = true
	}
	for i := range found {
		if g := found[i].group; g != 0 && live[g] && !mine[g] {
			found[i].group = 0
		}
	}
	return found
}

// parsePS splits a "pid pgid etime command" line of ps. elapsed is 0 when
// the etime column does not parse.
func parsePS(line string) (pid, pgid int, elapsed time.Duration, cmdline string, ok bool) {
	rest := strings.TrimSpace(line)
	var f [3]string
	for i := range f {
		j := strings.IndexAny(rest, " \t")
		if j < 0 {
			return 0, 0, 0, "", false
		}
		f[i] = rest[:j]
		rest = strings.TrimLeft(rest[j:], " \t")
	}
	pid, err1 := strconv.Atoi(f[0])
	pgid, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil {
		return 0, 0, 0, "", false
	}
	return pid, pgid, parseEtime(f[2]), rest, rest != ""
}

// parseEtime reads ps's [[dd-]hh:]mm:ss, or 0 when it is not that.
func parseEtime(s string) time.Duration {
	var days int
	if d, rest, found := strings.Cut(s, "-"); found {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0
		}
		days, s = n, rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0
	}
	secs := 0
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0
		}
		secs = secs*60 + n
	}
	return time.Duration(days*86400+secs) * time.Second
}

// KillTree kills pid and every process below it, children first. A process
// that is already gone is not an error.
func KillTree(pid int) error {
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=").Output()
	if err != nil {
		return err
	}
	kids := map[int][]int{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		c, e1 := strconv.Atoi(f[0])
		p, e2 := strconv.Atoi(f[1])
		if e1 == nil && e2 == nil {
			kids[p] = append(kids[p], c)
		}
	}
	var order []int
	var walk func(int)
	walk = func(p int) {
		for _, c := range kids[p] {
			walk(c)
		}
		order = append(order, p)
	}
	walk(pid)
	for _, p := range order {
		if err := syscall.Kill(p, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			return err
		}
	}
	return nil
}

// ownedBy reports whether a command line runs something inside dir.
func ownedBy(dir, cmdline string) bool {
	if _, ok := below(dir, cmdline, false); ok {
		return true
	}
	sh, rest, ok := strings.Cut(cmdline, " ")
	if !ok || !shells[filepath.Base(sh)] {
		return false
	}
	rel, ok := below(dir, strings.TrimLeft(rest, " "), false)
	if !ok {
		return false
	}
	script, _, _ := strings.Cut(rel, " ")
	return script == "run.sh" || script == "run-helper.sh"
}

func releaseStrays([]Stray) {}

// killStrays interrupts the strays so a listener can close its session with
// GitHub, then kills what is left after a few seconds.
func killStrays(found []Stray) {
	send := func(sig syscall.Signal) {
		for _, s := range found {
			if s.group != 0 {
				_ = syscall.Kill(-s.group, sig)
			}
			_ = syscall.Kill(s.PID, sig)
		}
	}
	gone := func() bool {
		for _, s := range found {
			if s.group != 0 && syscall.Kill(-s.group, 0) != syscall.ESRCH {
				return false
			}
			if alive(s.PID) && !zombie(s.PID) {
				return false
			}
		}
		return true
	}
	if len(found) == 0 {
		return
	}
	send(syscall.SIGINT)
	for deadline := time.Now().Add(3 * time.Second); !gone() && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
	}
	send(syscall.SIGKILL)
}

// zombie reports a process that has exited and waits to be reaped by init.
func zombie(pid int) bool {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return err != nil || strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}
