//go:build !windows

package localci

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"
)

func selfPID() int { return os.Getpid() }

// listCwds lists every process's working folder: /proc on Linux, lsof on
// macOS.
func listCwds() []procCwd {
	var out []procCwd
	if runtime.GOOS == "linux" {
		dirs, _ := filepath.Glob("/proc/[0-9]*")
		for _, d := range dirs {
			pid, err := strconv.Atoi(filepath.Base(d))
			if err != nil {
				continue
			}
			if cwd, err := os.Readlink(filepath.Join(d, "cwd")); err == nil {
				out = append(out, procCwd{pid, cwd})
			}
		}
		return out
	}
	b, err := exec.Command("lsof", "-d", "cwd", "-Fpn").Output()
	if err != nil && len(b) == 0 {
		return nil
	}
	pid := 0
	for _, line := range bytes.Split(b, []byte("\n")) {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(string(line[1:]))
		case 'n':
			if pid > 0 {
				out = append(out, procCwd{pid, string(line[1:])})
			}
		}
	}
	return out
}

// killPIDs sends SIGTERM to each process and its group (the job started its
// processes in their own group), then SIGKILL to what is left.
func killPIDs(pids []int) {
	self := syscall.Getpgrp()
	send := func(sig syscall.Signal) {
		for _, pid := range pids {
			if g, err := syscall.Getpgid(pid); err == nil && g > 1 && g != self {
				_ = syscall.Kill(-g, sig)
			}
			_ = syscall.Kill(pid, sig)
		}
	}
	send(syscall.SIGTERM)
	for i := 0; i < 30; i++ {
		alive := false
		for _, pid := range pids {
			if syscall.Kill(pid, 0) == nil {
				alive = true
			}
		}
		if !alive {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	send(syscall.SIGKILL)
}
