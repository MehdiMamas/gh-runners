//go:build !windows

package proc

import (
	"os/exec"
	"syscall"
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

func env() []string { return nil }

// descendants is only needed where the tree has no process group to aim at.
func descendants(pid int) []int { return nil }

func killPIDs([]int) {}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }
