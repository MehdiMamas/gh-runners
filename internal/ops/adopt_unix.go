//go:build darwin || linux

package ops

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func hideWindow(*exec.Cmd) {}

// procsIn lists processes whose command line runs something inside dir
// (run.sh, runsvc.sh, bin/Runner.Listener, bin/Runner.Worker).
func procsIn(dir string) []int {
	out, err := exec.Command("ps", "-axww", "-o", "pid=,command=").Output()
	if err != nil {
		return nil
	}
	return matchProcs(string(out), filepath.Clean(dir), os.Getpid())
}

// matchProcs picks the pids of `ps -o pid=,command=` lines that run a file
// inside dir.
func matchProcs(ps, dir string, self int) []int {
	var pids []int
	for _, line := range strings.Split(ps, "\n") {
		line = strings.TrimSpace(line)
		pidText, command, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(pidText)
		if err != nil || pid == self {
			continue
		}
		if strings.Contains(command, dir+"/") {
			pids = append(pids, pid)
		}
	}
	return pids
}

func terminate(pid int, force bool) {
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	_ = syscall.Kill(pid, sig)
}
