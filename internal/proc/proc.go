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

// Stop stops cmd and everything it started: it asks the process group to
// exit (so GitHub sees the runner go offline), waits up to grace until the
// whole tree is gone, then kills what is left. The leader (run.sh) can exit
// while Runner.Listener and Runner.Worker, with the job, keep running, so
// the leader going is not enough. It is used for every stop: restart, remove,
// update, pause and shutdown.
// done must be closed when cmd.Wait returns.
func Stop(cmd *exec.Cmd, done <-chan struct{}, grace time.Duration) {
	if cmd.Process == nil {
		return
	}
	known := descendants(cmd.Process.Pid)
	interrupt(cmd)
	gone := func() bool {
		select {
		case <-done:
			return treeGone(cmd, known)
		default:
			return false
		}
	}
	for deadline := time.Now().Add(grace); !gone() && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
	}
	if !gone() {
		known = append(known, descendants(cmd.Process.Pid)...)
		kill(cmd)
	}
	killPIDs(known)
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

// MissingTools names the tools jobs expect that the runners' PATH lacks:
// bash, gh and node on Windows, where a plain install has none of them.
// Elsewhere it is nil.
func MissingTools() []string { return missingTools() }

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

// Alive reports whether a process with this PID is running.
func Alive(pid int) bool { return alive(pid) }

// Stray is a process that belongs to a runner (its executable is inside the
// runner's folder) but that nothing supervises.
type Stray struct {
	PID  int
	Path string // the command line on unix, the executable path on Windows
	ref  procRef
	// group is a process group that goes with it (unix), 0 for none.
	group int
}

// Strays lists the processes of the runner installed in dir: those whose
// executable is under dir (Runner.Listener, Runner.Worker, and run.sh or
// run-helper.sh started from it). A process outside dir never counts, and
// dir matches whole path elements: runners/a does not own runners/ab/bin.
func Strays(dir string) []Stray {
	found := strays(dir)
	releaseStrays(found)
	return found
}

// KillStrays kills the processes Strays finds, with what they started (their
// process group on unix, their children on Windows), and returns them.
func KillStrays(dir string) []Stray {
	found := strays(dir)
	killStrays(found)
	return found
}

// below returns the part of path under dir, comparing whole path elements.
func below(dir, path string, fold bool) (string, bool) {
	dir = strings.TrimRight(dir, `/\`)
	if len(path) <= len(dir)+1 || (path[len(dir)] != '/' && path[len(dir)] != '\\') {
		return "", false
	}
	if head := path[:len(dir)]; head != dir && !(fold && strings.EqualFold(head, dir)) {
		return "", false
	}
	return path[len(dir)+1:], true
}
