// Package proc holds the OS-specific parts of starting, detaching and
// stopping child processes.
package proc

import (
	"os/exec"
	"strings"
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

// Env is the environment for a runner started now. On Windows it re-reads
// the system and user variables from the registry, so a tool installed
// after the daemon started (a new PATH entry, say) reaches the runners it
// starts or restarts from then on. nil means the daemon's own environment.
func Env() []string { return env() }

// mergeEnv returns base with every variable of fresh added or replacing
// the one of the same name. Names compare without case, as on Windows.
func mergeEnv(base, fresh []string) []string {
	at := map[string]int{}
	out := make([]string, 0, len(base)+len(fresh))
	for _, kv := range base {
		at[envKey(kv)] = len(out)
		out = append(out, kv)
	}
	for _, kv := range fresh {
		if i, ok := at[envKey(kv)]; ok {
			out[i] = kv
		} else {
			at[envKey(kv)] = len(out)
			out = append(out, kv)
		}
	}
	return out
}

func envKey(kv string) string {
	// Look from the second byte: Windows keeps hidden "=C:=C:\dir" entries.
	if kv != "" {
		if i := strings.IndexByte(kv[1:], '='); i >= 0 {
			return strings.ToUpper(kv[:i+1])
		}
	}
	return strings.ToUpper(kv)
}
