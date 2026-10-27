package localci

import (
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/store"
)

// MaxHistory is how many finished runs ci/history.json keeps.
const MaxHistory = 50

// HistoryRun is one finished run, kept in ci/history.json. The dashboard
// and the ci run and ci watch commands all add to it, so the dashboard
// shows runs started from the command line too.
type HistoryRun struct {
	ID      string    `json:"id"`
	Task    int       `json:"task,omitempty"`
	Repo    string    `json:"repo"`
	PR      int       `json:"pr"`
	Title   string    `json:"title,omitempty"`
	SHA     string    `json:"sha,omitempty"`
	Auto    bool      `json:"auto,omitempty"` // started by the watch
	DryRun  bool      `json:"dry_run,omitempty"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended"`
	Results []Result  `json:"results,omitempty"`
	Passed  bool      `json:"passed"`
	Merged  bool      `json:"merged,omitempty"`
	Error   string    `json:"error,omitempty"`
	Dir     string    `json:"dir,omitempty"` // the run folder: logs and the event feed
	Outcome Outcome   `json:"outcome"`
}

var historyMu sync.Mutex

func HistoryPath() string { return filepath.Join(store.Home(), "ci", "history.json") }

// NewHistoryRun describes a run of o that started at start and ended now.
func NewHistoryRun(o Options, auto bool, start time.Time, rep Report, err error) HistoryRun {
	r := HistoryRun{
		ID: strconv.FormatInt(start.UnixNano(), 36), Repo: o.Owner + "/" + o.Repo, PR: o.PR,
		Auto: auto, DryRun: o.DryRun, Started: start, Ended: time.Now(),
		Title: rep.PR.Title, SHA: rep.PR.Head.SHA, Results: rep.Results, Merged: rep.Merged,
		Passed: err == nil && len(rep.Results) > 0 && rep.AllPassed(),
	}
	r.Dir, r.Outcome = rep.Dir, rep.Outcome
	if err != nil {
		r.Error = err.Error()
		if r.Outcome.State == "" {
			r.Outcome = Outcome{State: NotPosted, Reason: "No comment: the run stopped before it finished: " + err.Error(), Next: "Fix the cause above and run it again."}
		}
	}
	if r.Outcome.State == "" && len(rep.Results) > 0 {
		r.Outcome = Outcome{State: NotPosted, Reason: "No comment: the run did not finish.", Next: "Run it again."}
	}
	return r
}

// History returns the kept runs, newest first.
func History() []HistoryRun {
	historyMu.Lock()
	defer historyMu.Unlock()
	var runs []HistoryRun
	_ = store.ReadJSON(HistoryPath(), &runs)
	return runs
}

// AddHistory puts r first and drops the oldest runs past MaxHistory.
func AddHistory(r HistoryRun) error {
	historyMu.Lock()
	defer historyMu.Unlock()
	var runs []HistoryRun
	_ = store.ReadJSON(HistoryPath(), &runs)
	runs = append([]HistoryRun{r}, runs...)
	if len(runs) > MaxHistory {
		runs = runs[:MaxHistory]
	}
	return store.WriteJSON(HistoryPath(), runs, 0o644)
}
