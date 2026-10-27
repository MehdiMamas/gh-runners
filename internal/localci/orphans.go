package localci

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/proc"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// A Local CI job runs in ci/<owner>-<repo>/pr<N>-<sha7>-<unix>/<job>/src.
// When the daemon stops in the middle of a run, the job's processes (an
// emulator, npm, vite) can outlive it and keep their ports. SweepOrphans
// finds them by their working folder and kills them.

const ownerFile = "owner.pid"

// ownedByOther reports whether the run folder belongs to a live gh-runners
// process other than self (a CLI run, a second daemon). A folder with no
// owner file, or whose owner is gone, is not.
func ownedByOther(folder string, self int) bool {
	b, err := os.ReadFile(filepath.Join(folder, ownerFile))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return err == nil && pid > 0 && pid != self && proc.Alive(pid)
}

// procCwd is one process and the folder it runs in.
type procCwd struct {
	PID int
	Cwd string
}

// WriteOwner marks folder as owned by this process, so a sweep or the port
// guard leaves it alone while it lives.
func WriteOwner(folder string) {
	_ = os.WriteFile(filepath.Join(folder, ownerFile), []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// folderLive reports whether folder still exists and its owner process runs.
// A fleet job's folder is deleted when the job ends, so a process still
// working in a missing folder is a leftover.
func folderLive(folder string) bool {
	if _, err := os.Stat(folder); err != nil {
		return false
	}
	b, err := os.ReadFile(filepath.Join(folder, ownerFile))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return err == nil && pid > 0 && proc.Alive(pid)
}

// isFleetFolder reports whether folder is a fleet job's (ci/fleet/jobs/<id>).
func isFleetFolder(root, folder string) bool {
	rel, err := filepath.Rel(root, folder)
	return err == nil && strings.HasPrefix(filepath.ToSlash(rel), "fleet/jobs/")
}

// runFolder returns the run folder that cwd is inside (ci/<repo>/<run> for
// a Local CI run, ci/fleet/jobs/<id> for a fleet job), or "" when cwd is not
// under root, or not inside one.
func runFolder(root, cwd string) string {
	// Linux appends this to the path of a deleted folder.
	cwd = strings.TrimSuffix(cwd, " (deleted)")
	rel, ok := strings.CutPrefix(filepath.Clean(cwd), filepath.Clean(root)+string(filepath.Separator))
	if !ok {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) >= 3 && parts[0] == "fleet" && parts[1] == "jobs" {
		return filepath.Join(root, "fleet", "jobs", parts[2])
	}
	// Only a run's own folder counts: not ci/locks, not a repo folder.
	if len(parts) < 2 || !strings.HasPrefix(parts[1], "pr") {
		return ""
	}
	return filepath.Join(root, parts[0], parts[1])
}

// orphanRuns groups the processes under root by run folder.
func orphanRuns(root string, procs []procCwd, self int) map[string][]int {
	out := map[string][]int{}
	for _, p := range procs {
		if p.PID == self || p.PID <= 1 {
			continue
		}
		if f := runFolder(root, p.Cwd); f != "" {
			out[f] = append(out[f], p.PID)
		}
	}
	return out
}

// orphanHistory is the history entry for a run folder whose run was lost.
func orphanHistory(folder string, now time.Time) HistoryRun {
	run := filepath.Base(folder)
	repo := filepath.Base(filepath.Dir(folder))
	r := HistoryRun{
		ID: "orphan-" + repo + "-" + run, Repo: repo, Ended: now, Started: now,
		Error: "cancelled: the daemon restarted",
	}
	// pr<N>-<sha7>-<unix>
	if f := strings.Split(run, "-"); len(f) == 3 {
		r.PR, _ = strconv.Atoi(strings.TrimPrefix(f[0], "pr"))
		r.SHA = f[1]
		if u, err := strconv.ParseInt(f[2], 10, 64); err == nil {
			r.Started = time.Unix(u, 0)
		}
	}
	// The folder is owner-repo; GitHub owners may hold dashes, so the
	// first one is a best guess at the split.
	if o, rest, ok := strings.Cut(repo, "-"); ok {
		r.Repo = o + "/" + rest
	}
	return r
}

// SweepOrphans kills the processes left under <home>/ci/ by a run the
// daemon no longer owns and records each lost run in the history as
// cancelled. Call it when the daemon starts, before any run is queued.
// It returns the number of processes killed.
func SweepOrphans(logf func(string, ...any)) int {
	root := filepath.Join(store.Home(), "ci")
	// lsof and /proc report real paths; the home may sit behind a symlink.
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	n := 0
	for folder, pids := range orphanRuns(root, listCwds(), selfPID()) {
		if ownedByOther(folder, selfPID()) {
			continue
		}
		killPIDs(pids)
		n += len(pids)
		if logf != nil {
			logf("local ci: killed %d leftover process(es) of %s", len(pids), folder)
		}
		if isFleetFolder(root, folder) {
			continue // not a PR run: nothing to record
		}
		hr := orphanHistory(folder, time.Now())
		known := false
		for _, h := range History() {
			known = known || h.ID == hr.ID
		}
		if !known {
			if err := AddHistory(hr); err != nil && logf != nil {
				logf("local ci: save history: %v", err)
			}
		}
	}
	return n
}

// KillUnder kills every process whose working folder is inside dir, with
// its process group. A job calls it when it ends, so nothing it started
// (an emulator that detached itself) outlives it. It returns the count.
func KillUnder(dir string) int {
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	dir = filepath.Clean(dir)
	var pids []int
	for _, p := range listCwds() {
		cwd := strings.TrimSuffix(p.Cwd, " (deleted)")
		if p.PID != selfPID() && p.PID > 1 && (cwd == dir || strings.HasPrefix(cwd, dir+string(filepath.Separator))) {
			pids = append(pids, p.PID)
		}
	}
	if len(pids) > 0 {
		killPIDs(pids)
	}
	return len(pids)
}
