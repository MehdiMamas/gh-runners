// Package proc holds the OS-specific parts of starting, detaching and
// stopping child processes.
package proc

import (
	"os"
	"os/exec"
	"path/filepath"
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

// EnvFor is Env for the runner installed in dir. The runner itself never
// reads its .path file (only env.sh writes it, once, at config time, and
// only .env is loaded at startup), so its entries are put first on PATH here
// and reach job steps. The daemon's own PATH entries follow, so tools
// installed since then stay found. The runner's .env still applies on top.
//
// On Windows, Git Bash (and the Rust, node and pnpm dirs that exist) go ahead
// of everything else: otherwise `shell: bash` finds the WSL launcher in
// WindowsApps, which cannot run the runner's temp script.
func EnvFor(dir string) []string {
	env := withPathFile(Env(), dir)
	if runtime.GOOS == "windows" {
		env = withFirstDirs(env, windowsToolDirs(os.Getenv, dirExists))
	}
	return env
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// windowsToolDirs lists the tool dirs jobs need first on PATH, only those
// that exist: Git Bash, rustup/cargo, node and pnpm.
func windowsToolDirs(getenv func(string) string, exists func(string) bool) []string {
	var cand []string
	for _, pf := range []string{getenv("ProgramFiles"), getenv("ProgramW6432")} {
		if pf != "" {
			cand = append(cand, pf+`\Git\bin`, pf+`\Git\usr\bin`)
		}
	}
	if la := getenv("LOCALAPPDATA"); la != "" {
		cand = append(cand, la+`\Programs\Git\bin`, la+`\Programs\Git\usr\bin`)
	}
	if h := getenv("USERPROFILE"); h != "" {
		cand = append(cand, h+`\.cargo\bin`)
	}
	for _, pf := range []string{getenv("ProgramFiles"), getenv("ProgramW6432")} {
		if pf != "" {
			cand = append(cand, pf+`\nodejs`)
		}
	}
	if a := getenv("APPDATA"); a != "" {
		cand = append(cand, a+`\npm`)
	}
	if la := getenv("LOCALAPPDATA"); la != "" {
		cand = append(cand, la+`\pnpm`)
	}
	var out []string
	seen := map[string]bool{}
	for _, d := range cand {
		if k := strings.ToLower(d); !seen[k] && exists(d) {
			seen[k] = true
			out = append(out, d)
		}
	}
	return out
}

// withFirstDirs puts dirs at the front of PATH in env.
func withFirstDirs(base []string, dirs []string) []string {
	if len(dirs) == 0 {
		return base
	}
	if base == nil {
		base = os.Environ()
	}
	var cur string
	for _, kv := range base {
		if envKey(kv) == "PATH" {
			cur = kv[len("PATH="):]
		}
	}
	seen := map[string]bool{}
	var parts []string
	for _, list := range [][]string{dirs, filepath.SplitList(cur)} {
		for _, d := range list {
			if k := strings.ToLower(d); d != "" && !seen[k] {
				seen[k] = true
				parts = append(parts, d)
			}
		}
	}
	return mergeEnv(base, []string{"PATH=" + strings.Join(parts, string(os.PathListSeparator))})
}

func withPathFile(base []string, dir string) []string {
	b, err := os.ReadFile(filepath.Join(dir, ".path"))
	file := strings.TrimSpace(string(b))
	if err != nil || file == "" {
		return base
	}
	if base == nil {
		base = os.Environ()
	}
	var daemon string
	for _, kv := range base {
		if envKey(kv) == "PATH" {
			daemon = kv[len("PATH="):]
		}
	}
	seen := map[string]bool{}
	var parts []string
	for _, list := range []string{file, daemon} {
		for _, d := range filepath.SplitList(list) {
			if d != "" && !seen[d] {
				seen[d] = true
				parts = append(parts, d)
			}
		}
	}
	return mergeEnv(base, []string{"PATH=" + strings.Join(parts, string(os.PathListSeparator))})
}

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
