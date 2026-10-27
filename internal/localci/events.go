package localci

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/proc"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// EventsFile is the feed of one run, one JSON object per line, in the run
// folder. The dashboard shows it live and for past runs.
const EventsFile = "events.jsonl"

// Event is one thing that happened in a run, in plain words.
type Event struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"` // head, queued, started, job_started, job_finished, status, comment, merge, outcome, error
	Text    string    `json:"text"`
	Job     string    `json:"job,omitempty"`
	Machine string    `json:"machine,omitempty"`
	Status  string    `json:"status,omitempty"` // job_finished: the job's result
}

// EventLog appends events to a run folder's feed. A nil log ignores them.
type EventLog struct {
	mu   sync.Mutex
	path string
}

// OpenEvents returns the feed of the run folder dir.
func OpenEvents(dir string) *EventLog { return &EventLog{path: filepath.Join(dir, EventsFile)} }

// Add records an event now.
func (l *EventLog) Add(kind, format string, a ...any) {
	l.AddEvent(Event{Kind: kind, Text: fmt.Sprintf(format, a...)})
}

// AddEvent records e; a zero Time means now.
func (l *EventLog) AddEvent(e Event) {
	if l == nil {
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// ReadEvents returns the feed of the run folder dir, oldest first.
func ReadEvents(dir string) []Event {
	out := []Event{}
	f, err := os.Open(filepath.Join(dir, EventsFile))
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// RunningRun is a run that is going on now. Each one is a file under
// ci/running, so the dashboard also sees runs a terminal started.
type RunningRun struct {
	ID      string    `json:"id"` // the run folder's name
	Repo    string    `json:"repo"`
	PR      int       `json:"pr"`
	SHA     string    `json:"sha"`
	Title   string    `json:"title,omitempty"`
	Dir     string    `json:"dir"`
	Jobs    []string  `json:"jobs,omitempty"` // in workflow order
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
}

var runningSeq atomic.Int64

func runningDir() string { return filepath.Join(store.Home(), "ci", "running") }

// StartRunning records a run as going on until the returned func is called.
// setJobs updates the job list once it is known.
func StartRunning(r RunningRun) (setJobs func([]string), done func()) {
	r.PID = os.Getpid()
	r.ID = filepath.Base(r.Dir)
	if r.Started.IsZero() {
		r.Started = time.Now()
	}
	path := filepath.Join(runningDir(), fmt.Sprintf("%d-%d.json", r.PID, runningSeq.Add(1)))
	if os.MkdirAll(runningDir(), 0o755) != nil || store.WriteJSON(path, r, 0o644) != nil {
		return func([]string) {}, func() {}
	}
	return func(jobs []string) {
			r.Jobs = jobs
			_ = store.WriteJSON(path, r, 0o644)
		},
		func() { _ = os.Remove(path) }
}

// RunningRuns lists the runs going on, oldest first. Files left by a
// process that has ended are removed.
func RunningRuns() []RunningRun {
	ents, _ := os.ReadDir(runningDir())
	out := []RunningRun{}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(runningDir(), e.Name())
		var r RunningRun
		if store.ReadJSON(path, &r) != nil {
			continue
		}
		if r.PID != os.Getpid() && !proc.Alive(r.PID) {
			_ = os.Remove(path)
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}
