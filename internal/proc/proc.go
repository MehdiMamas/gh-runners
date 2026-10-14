// Package proc holds the OS-specific parts of starting, detaching and
// stopping child processes.
package proc

import (
	"os/exec"
	"time"
)

// Supervise prepares cmd to run as a supervised child in its own process
// group, so stopping it also stops the processes it starts.
func Supervise(cmd *exec.Cmd) { supervise(cmd) }

// Detach prepares cmd to keep running after the parent exits, with no
// console window on Windows.
func Detach(cmd *exec.Cmd) { detach(cmd) }

// Stop asks the process group to exit, and kills it after grace.
// done must be closed when cmd.Wait returns.
func Stop(cmd *exec.Cmd, done <-chan struct{}, grace time.Duration) {
	if cmd.Process == nil {
		return
	}
	interrupt(cmd)
	select {
	case <-done:
		return
	case <-time.After(grace):
	}
	kill(cmd)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
}
