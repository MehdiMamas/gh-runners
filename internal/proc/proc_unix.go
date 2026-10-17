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
	out, err := exec.Command("ps", "-axo", "pid=,pgid=,command=").Output()
	if err != nil {
		return nil
	}
	self, selfGroup := os.Getpid(), syscall.Getpgrp()
	dir = filepath.Clean(dir)
	var found []Stray
	live := map[int]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		pid, pgid, cmdline, ok := parsePS(line)
		if !ok {
			continue
		}
		live[pid] = true
		if pid == self || !ownedBy(dir, cmdline) {
			continue
		}
		s := Stray{PID: pid, Path: cmdline}
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

func parsePS(line string) (pid, pgid int, cmdline string, ok bool) {
	rest := strings.TrimSpace(line)
	var nums [2]int
	for i := range nums {
		j := strings.IndexAny(rest, " \t")
		if j < 0 {
			return 0, 0, "", false
		}
		n, err := strconv.Atoi(rest[:j])
		if err != nil {
			return 0, 0, "", false
		}
		nums[i] = n
		rest = strings.TrimLeft(rest[j:], " \t")
	}
	return nums[0], nums[1], rest, rest != ""
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
