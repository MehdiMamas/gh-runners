package localci

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/store"
)

// CIRunAge is how long a finished run's folder (logs) is kept.
const CIRunAge = 14 * 24 * time.Hour

// ciSpecialDirs are folders of ci/ that are not <owner>-<repo> folders.
var ciSpecialDirs = map[string]bool{"fleet": true, "locks": true, "running": true, "active": true}

// PruneRuns deletes run folders under ciDir (<owner>-<repo>/pr<N>-<sha7>-<unix>)
// older than keep and returns the bytes freed. It never enters running/,
// active/, locks/ or fleet/, and keeps a run listed in running/ or a run of a
// pull request that has a job in active/.
func PruneRuns(ciDir string, keep time.Duration) int64 {
	liveIDs, livePRs := liveRuns(ciDir)
	repos, _ := os.ReadDir(ciDir)
	cutoff := time.Now().Add(-keep)
	var freed int64
	for _, repo := range repos {
		if !repo.IsDir() || ciSpecialDirs[repo.Name()] {
			continue
		}
		repoPath := filepath.Join(ciDir, repo.Name())
		runs, _ := os.ReadDir(repoPath)
		for _, run := range runs {
			if !run.IsDir() || liveIDs[run.Name()] || livePRs[repo.Name()+"/"+runPR(run.Name())] {
				continue
			}
			path := filepath.Join(repoPath, run.Name())
			if !runTime(run.Name(), path).Before(cutoff) {
				continue
			}
			size := dirBytes(path)
			if removeAllRetry(path) == nil {
				freed += size
			}
		}
	}
	return freed
}

// liveRuns reads running/ and active/ under ciDir: the ids of the runs going
// on, and "<owner>-<repo>/<pr>" of the pull requests with a job going on.
func liveRuns(ciDir string) (ids, prs map[string]bool) {
	ids, prs = map[string]bool{}, map[string]bool{}
	ents, _ := os.ReadDir(filepath.Join(ciDir, "running"))
	for _, e := range ents {
		var r RunningRun
		if store.ReadJSON(filepath.Join(ciDir, "running", e.Name()), &r) != nil {
			continue
		}
		if r.ID != "" {
			ids[r.ID] = true
		}
		if r.Dir != "" {
			ids[filepath.Base(r.Dir)] = true
		}
	}
	ents, _ = os.ReadDir(filepath.Join(ciDir, "active"))
	for _, e := range ents {
		var a ActiveJob
		if store.ReadJSON(filepath.Join(ciDir, "active", e.Name()), &a) != nil {
			continue
		}
		prs[strings.ReplaceAll(a.Repo, "/", "-")+"/"+strconv.Itoa(a.PR)] = true
	}
	return ids, prs
}

// runPR is the pull request number in a run folder name "pr<N>-...".
func runPR(name string) string {
	s, _, _ := strings.Cut(strings.TrimPrefix(name, "pr"), "-")
	return s
}

// runTime is the start time in the folder name's last part, else the
// folder's modification time.
func runTime(name, path string) time.Time {
	if i := strings.LastIndex(name, "-"); i >= 0 {
		if n, err := strconv.ParseInt(name[i+1:], 10, 64); err == nil && n > 0 {
			return time.Unix(n, 0)
		}
	}
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime()
	}
	return time.Now()
}

func dirBytes(dir string) int64 {
	var n int64
	_ = filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			n += fi.Size()
		}
		return nil
	})
	return n
}

// removeAllRetry retries because Windows keeps files locked for a moment.
func removeAllRetry(path string) error {
	err := os.RemoveAll(path)
	for i := 0; i < 20 && err != nil; i++ {
		time.Sleep(100 * time.Millisecond)
		err = os.RemoveAll(path)
	}
	return err
}
