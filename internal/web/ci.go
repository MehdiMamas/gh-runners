package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/localci"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// Local CI runs a repository's ci.yml on this machine (and the fleet) for
// repositories where no runner can be added, say without admin rights. Runs
// have their own queue, so a long one never holds up adding runners.

// ciRun is one finished run, kept in ci/history.json.
type ciRun = localci.HistoryRun

func ciSeenPath() string { return filepath.Join(store.Home(), "ci", "seen.json") }

func (s *Server) ciRuns() []ciRun { return localci.History() }

func (s *Server) addCIRun(r ciRun) {
	if err := localci.AddHistory(r); err != nil {
		s.h.Log.Printf("local ci: save history: %v", err)
	}
}

type ciRepoView struct {
	Repo string `json:"repo"`
	store.CIRepo
}

type ciView struct {
	Repos    []ciRepoView      `json:"repos"`
	Runs     []ciRun           `json:"runs"`
	Tasks    []taskView        `json:"tasks"`
	Machines []localci.Machine `json:"machines"`
}

func (s *Server) getCI(w http.ResponseWriter, r *http.Request) {
	cfg, err := store.LoadConfig()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	v := ciView{Repos: []ciRepoView{}, Runs: s.ciRuns(), Tasks: s.ci.views(), Machines: s.ciMachines()}
	for name, c := range cfg.CI {
		v.Repos = append(v.Repos, ciRepoView{name, c})
	}
	if v.Runs == nil {
		v.Runs = []ciRun{}
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) ciMachines() []localci.Machine {
	if s.ciDispatch == nil {
		return nil
	}
	return s.ciDispatch().Machines()
}

type ciSettingsReq struct {
	Watch bool     `json:"watch"`
	Post  bool     `json:"post"`
	Merge bool     `json:"merge"`
	Allow []string `json:"allow"`
}

func (s *Server) putCI(w http.ResponseWriter, r *http.Request) {
	full := r.PathValue("owner") + "/" + r.PathValue("repo")
	var req ciSettingsReq
	if err := readBody(r, &req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	var allow []string
	for _, a := range req.Allow {
		if a = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(a), "@")); a != "" {
			allow = append(allow, a)
		}
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	set := store.CIRepo{Watch: req.Watch, Post: req.Post, Merge: req.Merge, Allow: allow}
	if cfg.CI == nil {
		cfg.CI = map[string]store.CIRepo{}
	}
	if set.Watch || set.Post || set.Merge || len(set.Allow) > 0 {
		cfg.CI[full] = set
	} else {
		delete(cfg.CI, full)
	}
	if err := store.SaveConfig(cfg); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	if set.Watch {
		s.wakeCI()
	}
	writeJSON(w, http.StatusOK, ciRepoView{full, set})
}

type ciRunReq struct {
	Repo    string `json:"repo"`
	PR      int    `json:"pr"`
	DryRun  bool   `json:"dry_run"`
	Approve bool   `json:"approve"`
}

func (s *Server) postCIRun(w http.ResponseWriter, r *http.Request) {
	var req ciRunReq
	if err := readBody(r, &req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	owner, repo, ok := strings.Cut(req.Repo, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") || req.PR <= 0 {
		fail(w, http.StatusBadRequest, errors.New("give a repository as owner/repo and a pull request number"))
		return
	}
	t, existing, err := s.queueCI(owner, repo, req.PR, req.DryRun, req.Approve, false)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, err)
		return
	}
	if existing {
		writeJSON(w, http.StatusAccepted, map[string]any{"task": t.ID, "existing": true})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"task": t.ID})
}

// postCINext moves a queued run to the front of the queue.
func (s *Server) postCINext(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	t := s.ci.get(id)
	if t == nil {
		fail(w, http.StatusNotFound, fmt.Errorf("no task %d", id))
		return
	}
	if !s.ci.moveFront(t) {
		fail(w, http.StatusConflict, fmt.Errorf("task %d is not waiting in the queue", id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"task": id})
}

// deleteCITask cancels a queued or running run.
func (s *Server) deleteCITask(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	t := s.ci.get(id)
	if t == nil {
		fail(w, http.StatusNotFound, fmt.Errorf("no task %d", id))
		return
	}
	if !s.ci.stop(t) {
		fail(w, http.StatusConflict, fmt.Errorf("task %d has already ended", id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"task": id})
}

func (s *Server) getCITask(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	t := s.ci.get(id)
	if t == nil {
		fail(w, http.StatusNotFound, fmt.Errorf("no task %d", id))
		return
	}
	writeJSON(w, http.StatusOK, t.view(true))
}

// getCILog is the end of one job's log of a recorded run.
func (s *Server) getCILog(w http.ResponseWriter, r *http.Request) {
	id, job := r.PathValue("id"), r.PathValue("job")
	for _, rr := range localci.RunningRuns() {
		if rr.ID != id || !inCIFolder(rr.Dir) {
			continue
		}
		text, err := tailFile(filepath.Join(rr.Dir, "logs", filepath.Base(job)+".log"), lineCount(r))
		if err != nil {
			text = "No log yet for this job (" + err.Error() + ")."
		}
		writeJSON(w, http.StatusOK, map[string]string{"text": text})
		return
	}
	for _, run := range s.ciRuns() {
		if run.ID != id {
			continue
		}
		for _, res := range run.Results {
			if res.Job != job {
				continue
			}
			if res.Log == "" {
				writeJSON(w, http.StatusOK, map[string]string{"text": "This job has no log: " + orDash(res.Reason)})
				return
			}
			// Only logs under ci/ are served, whatever history.json says.
			root := filepath.Join(store.Home(), "ci") + string(filepath.Separator)
			if !strings.HasPrefix(filepath.Clean(res.Log), root) {
				fail(w, http.StatusForbidden, errors.New("log is outside the CI folder"))
				return
			}
			text, err := tailFile(res.Log, lineCount(r))
			if err != nil {
				text = "The log is not on this machine (" + err.Error() + ")."
			}
			writeJSON(w, http.StatusOK, map[string]string{"text": text})
			return
		}
	}
	fail(w, http.StatusNotFound, fmt.Errorf("no job %s in run %s", job, id))
}

// runDir finds the folder of a run by its ID: a recorded run or one going on.
func (s *Server) runDir(id string) (string, bool) {
	for _, run := range s.ciRuns() {
		if run.ID == id && run.Dir != "" {
			return run.Dir, true
		}
	}
	for _, r := range localci.RunningRuns() {
		if r.ID == id {
			return r.Dir, true
		}
	}
	return "", false
}

// inCIFolder reports whether path is under the CI folder.
func inCIFolder(path string) bool {
	root := filepath.Join(store.Home(), "ci") + string(filepath.Separator)
	return strings.HasPrefix(filepath.Clean(path), root)
}

// getCIEvents is the feed of one run, recorded or going on.
func (s *Server) getCIEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dir, ok := s.runDir(id)
	if !ok || !inCIFolder(dir) {
		fail(w, http.StatusNotFound, fmt.Errorf("no event feed for run %s", id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": localci.ReadEvents(dir)})
}

// liveRun is a run going on now, as the dashboard shows it.
type liveRun struct {
	localci.RunningRun
	JobStates []liveJob `json:"job_states"`
}

type liveJob struct {
	Job     string    `json:"job"`
	State   string    `json:"state"` // queued, running, or the result: pass, fail, skipped, unsupported, blocked
	Machine string    `json:"machine,omitempty"`
	Started time.Time `json:"started,omitempty"`
	Ended   time.Time `json:"ended,omitempty"`
	Reason  string    `json:"reason,omitempty"`
}

// liveRuns lists the runs going on, with each job's state from the feed and
// the machine of the jobs that run now from the active-job files.
func liveRuns() []liveRun {
	active := localci.ActiveJobs()
	out := []liveRun{}
	for _, r := range localci.RunningRuns() {
		lr := liveRun{RunningRun: r, JobStates: []liveJob{}}
		idx := map[string]int{}
		for _, j := range r.Jobs {
			idx[j] = len(lr.JobStates)
			lr.JobStates = append(lr.JobStates, liveJob{Job: j, State: "queued"})
		}
		get := func(name string) *liveJob {
			if i, ok := idx[name]; ok {
				return &lr.JobStates[i]
			}
			idx[name] = len(lr.JobStates)
			lr.JobStates = append(lr.JobStates, liveJob{Job: name, State: "queued"})
			return &lr.JobStates[len(lr.JobStates)-1]
		}
		for _, e := range localci.ReadEvents(r.Dir) {
			if e.Job == "" {
				continue
			}
			switch e.Kind {
			case "job_started":
				j := get(e.Job)
				j.State, j.Started = "running", e.Time
			case "job_finished":
				j := get(e.Job)
				j.State, j.Ended, j.Machine = e.Status, e.Time, e.Machine
				if j.Started.IsZero() {
					j.Started = e.Time
				}
				if e.Status != string(localci.Pass) {
					j.Reason = e.Text
				}
			}
		}
		for i := range lr.JobStates {
			j := &lr.JobStates[i]
			if j.State != "running" {
				continue
			}
			for _, a := range active {
				if a.Repo == r.Repo && a.PR == r.PR && a.Job == j.Job {
					j.Machine = a.Machine
				}
			}
		}
		out = append(out, lr)
	}
	return out
}

func (s *Server) getCILive(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"runs": liveRuns()})
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ciKey is what makes two run requests the same: repository, pull request and
// the dry-run and approve options.
func ciKey(full string, pr int, dry, approve bool) string {
	return fmt.Sprintf("%s#%d/dry=%t/approve=%t", full, pr, dry, approve)
}

// queueCI queues one run of a pull request. auto marks runs the watch
// started. At most one run per pull request (and dry/approve kind) waits in
// the queue: a repeat request returns that task with existing true.
func (s *Server) queueCI(owner, repo string, pr int, dry, approve, auto bool) (*task, bool, error) {
	full := owner + "/" + repo
	title := fmt.Sprintf("Local CI: %s #%d", full, pr)
	if dry {
		title += " (dry run)"
	}
	var before []localci.Event
	queuedAt := time.Now()
	if auto {
		before = append(before, localci.Event{Time: queuedAt, Kind: "head", Text: fmt.Sprintf("The watcher saw a new commit on PR #%d.", pr)})
	}
	before = append(before, localci.Event{Time: queuedAt, Kind: "queued", Text: fmt.Sprintf("Queued Local CI for PR #%d.", pr)})
	return s.ci.addKeyed(ciKey(full, pr, dry, approve), title, []string{full}, func(w io.Writer) error {
		ctx, cancel := context.WithCancel(s.ciContext())
		defer cancel()
		t, _ := w.(*task)
		if t != nil && t.setCancel(cancel) {
			return nil
		}
		start := time.Now()
		o := localci.Options{Owner: owner, Repo: repo, PR: pr, DryRun: dry, Approve: approve, Out: w, Before: before}
		var rep localci.Report
		err := func() error {
			c, login, err := s.client(ctx)
			if err != nil {
				return err
			}
			cfg, _ := store.LoadConfig()
			if s.ciDispatch != nil {
				o.Exec = s.ciDispatch()
			}
			o.Client, o.Settings, o.Login = c, cfg.CI[full], login
			rep, err = localci.Run(ctx, o)
			return err
		}()
		run := localci.NewHistoryRun(o, auto, start, rep, err)
		if t != nil {
			run.Task = t.ID
			t.mu.Lock()
			stopped := t.cancelled
			t.mu.Unlock()
			if stopped {
				run.Passed, run.Error = false, "cancelled"
			}
		}
		s.addCIRun(run)
		if err == nil && !run.Passed {
			return errors.New("a job failed")
		}
		return err
	})
}

func (s *Server) ciContext() context.Context {
	s.ciMu.Lock()
	defer s.ciMu.Unlock()
	if s.ciCtx == nil {
		return context.Background()
	}
	return s.ciCtx
}

func (s *Server) wakeCI() {
	select {
	case s.ciWake <- struct{}{}:
	default:
	}
}

// CIWatch runs until ctx ends: every minute it queues a run for each new
// head of an open pull request in the repositories with Local CI watching,
// when the pull request passes the trust gate (the signed-in user or an
// allowed author, never a fork).
func (s *Server) CIWatch(ctx context.Context) {
	// Anything left under ci/ belongs to a run the last daemon dropped.
	if n := localci.SweepOrphans(s.h.Log.Printf); n > 0 {
		s.h.Log.Printf("local ci: cleaned up %d leftover process(es)", n)
	}
	s.ciMu.Lock()
	s.ciCtx = ctx
	s.ciMu.Unlock()
	for {
		s.ciWatchOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.ciWake:
		case <-time.After(time.Minute):
		}
	}
}

func (s *Server) ciWatchOnce(ctx context.Context) {
	cfg, err := store.LoadConfig()
	if err != nil {
		return
	}
	var watched []string
	for full, set := range cfg.CI {
		if set.Watch {
			watched = append(watched, full)
		}
	}
	if len(watched) == 0 {
		return
	}
	c, login, err := s.client(ctx)
	if err != nil {
		return
	}
	seen := map[string]bool{}
	_ = store.ReadJSON(ciSeenPath(), &seen)
	changed := false
	for _, full := range watched {
		owner, repo, _ := strings.Cut(full, "/")
		prs, err := c.OpenPullRequests(ctx, owner, repo)
		if err != nil {
			s.h.Log.Printf("local ci: %s: %v", full, err)
			continue
		}
		for _, pr := range prs {
			key := fmt.Sprintf("%s#%d@%s", full, pr.Number, pr.Head.SHA)
			if seen[key] || localci.CheckTrust(pr, localci.Allowlist(cfg.CI[full], login), false) != nil {
				continue
			}
			if _, _, err := s.queueCI(owner, repo, pr.Number, false, false, true); err != nil {
				s.h.Log.Printf("local ci: %s #%d: %v", full, pr.Number, err)
				continue
			}
			seen[key], changed = true, true
		}
	}
	if changed {
		_ = os.MkdirAll(filepath.Dir(ciSeenPath()), 0o755)
		_ = store.WriteJSON(ciSeenPath(), seen, 0o644)
	}
}

// WaitCI waits up to d for the Local CI runs to end. The daemon calls it
// after cancelling their context, so each run kills its job processes and
// records itself in the history before the process exits.
func (s *Server) WaitCI(d time.Duration) {
	for end := time.Now().Add(d); s.ci.busy() > 0 && time.Now().Before(end); {
		time.Sleep(100 * time.Millisecond)
	}
}

// ciBusy reports whether a Local CI run is queued or running.
func (s *Server) ciBusy() bool { return s.ci.busy() > 0 }

// CIBusy says what Local CI task the daemon is waiting on before it may
// restart, or "" when none is queued or running.
func (s *Server) CIBusy() string {
	if id, ok := s.ci.first(); ok {
		return fmt.Sprintf("Local CI task %d to finish", id)
	}
	return ""
}
