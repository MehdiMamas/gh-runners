package runner

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/proc"
)

const (
	workCap     = 2 << 30
	workIdleAge = 72 * time.Hour
)

// keepInWork are the folders of _work that hold caches a job reuses.
var keepInWork = map[string]bool{"_actions": true, "_tool": true, "_PipelineMapping": true, "_update.sh": true}

// Busy reports whether a runner in dir is running a job: a Runner.Worker
// process whose path is under the folder.
func Busy(dir string) bool {
	for _, s := range proc.Strays(dir) {
		if strings.Contains(s.Path, "Runner.Worker") {
			return true
		}
	}
	return false
}

// CleanRunners frees disk in every runner folder under root and returns the
// bytes freed: runner versions the bin and externals links do not point to,
// the update staging folder, and, for a runner that is not busy, its _work
// folder when it is over the cap or untouched for 3 days.
func CleanRunners(root string, busy func(dir string) bool) int64 {
	ents, _ := os.ReadDir(root)
	var freed int64
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		freed += dropOldVersions(dir, "bin")
		freed += dropOldVersions(dir, "externals")
		freed += removeCounted(filepath.Join(dir, "_work", "_update"))
		freed += removeTrash(filepath.Join(dir, "_work"))
		if !busy(dir) {
			freed += cleanWork(filepath.Join(dir, "_work"))
		}
	}
	return freed
}

// dropOldVersions removes <name>.<ver> folders next to a <name> link that
// points at another one. A <name> that is not a link is left alone.
func dropOldVersions(dir, name string) int64 {
	target, err := os.Readlink(filepath.Join(dir, name))
	if err != nil {
		return 0
	}
	keep := filepath.Base(target)
	ents, _ := os.ReadDir(dir)
	var freed int64
	for _, e := range ents {
		if e.IsDir() && strings.HasPrefix(e.Name(), name+".") && e.Name() != keep {
			freed += removeCounted(filepath.Join(dir, e.Name()))
		}
	}
	return freed
}

// removeTrash deletes the .trash-* folders an earlier pass left behind.
func removeTrash(work string) int64 {
	var freed int64
	old, _ := filepath.Glob(filepath.Join(work, ".trash-*"))
	for _, p := range old {
		freed += removeCounted(p)
	}
	return freed
}

// cleanWork empties _work except the caches, when the rest is over the cap
// or untouched for 3 days. Each entry moves into _work/.trash-<unix> with one
// rename, so a job that starts right after checks out fresh. Trash left by
// an earlier pass is deleted by removeTrash.
func cleanWork(work string) int64 {
	ents, err := os.ReadDir(work)
	if err != nil {
		return 0
	}
	var freed int64
	var move []string
	var size int64
	var newest time.Time
	for _, e := range ents {
		path := filepath.Join(work, e.Name())
		if keepInWork[e.Name()] || e.Name() == "_update" || strings.HasPrefix(e.Name(), ".trash-") {
			continue
		}
		move = append(move, e.Name())
		size += sizeOf(path)
		if fi, err := e.Info(); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	if len(move) == 0 || (size <= workCap && time.Since(newest) < workIdleAge) {
		return freed
	}
	trash := filepath.Join(work, ".trash-"+strconv.FormatInt(time.Now().Unix(), 10))
	if os.MkdirAll(trash, 0o755) != nil {
		return freed
	}
	for _, name := range move {
		_ = os.Rename(filepath.Join(work, name), filepath.Join(trash, name))
	}
	return freed + removeCounted(trash)
}

// removeCounted deletes path and returns the bytes it held, 0 when it could
// not be removed (Windows keeps files locked for a moment; the next pass
// tries again).
func removeCounted(path string) int64 {
	if _, err := os.Lstat(path); err != nil {
		return 0
	}
	n := sizeOf(path)
	for i := 0; i < 20; i++ {
		if os.RemoveAll(path) == nil {
			return n
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0
}

func sizeOf(path string) int64 {
	var n int64
	_ = filepath.Walk(path, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			n += fi.Size()
		}
		return nil
	})
	return n
}
