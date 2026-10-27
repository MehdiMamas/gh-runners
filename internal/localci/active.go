package localci

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/proc"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// ActiveJob is a CI job running right now. Each one is a file under
// ci/active, so the dashboard also sees jobs that a `gh-runners ci run` in
// a terminal or a fleet member started.
type ActiveJob struct {
	Repo    string    `json:"repo"`
	PR      int       `json:"pr,omitempty"`
	Job     string    `json:"job"`
	Machine string    `json:"machine"`       // where it runs
	For     string    `json:"for,omitempty"` // set when this machine runs it for the main host
	PID     int       `json:"pid"`           // the process that runs it
	Started time.Time `json:"started"`
}

var activeSeq atomic.Int64

func activeDir() string { return filepath.Join(store.Home(), "ci", "active") }

// PRFromRef is the pull request number of refs/pull/N/merge, or 0.
func PRFromRef(ref string) int {
	s, ok := strings.CutPrefix(ref, "refs/pull/")
	if !ok {
		return 0
	}
	s, _, _ = strings.Cut(s, "/")
	n, _ := strconv.Atoi(s)
	return n
}

// StartActive records a job as running until the returned func is called.
func StartActive(a ActiveJob) (done func()) {
	a.PID = os.Getpid()
	if a.Started.IsZero() {
		a.Started = time.Now()
	}
	path := filepath.Join(activeDir(), fmt.Sprintf("%d-%d.json", a.PID, activeSeq.Add(1)))
	if os.MkdirAll(activeDir(), 0o755) != nil || store.WriteJSON(path, a, 0o644) != nil {
		return func() {}
	}
	return func() { _ = os.Remove(path) }
}

// ActiveJobs lists the running jobs, oldest first. Files left by a process
// that has ended are removed.
func ActiveJobs() []ActiveJob {
	ents, _ := os.ReadDir(activeDir())
	out := []ActiveJob{}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(activeDir(), e.Name())
		b, err := os.ReadFile(path)
		var a ActiveJob
		if err != nil || json.Unmarshal(b, &a) != nil {
			continue
		}
		if a.PID != os.Getpid() && !proc.Alive(a.PID) {
			_ = os.Remove(path)
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}
