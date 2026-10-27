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
	t, err := s.queueCI(owner, repo, req.PR, req.DryRun, req.Approve, false)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"task": t.ID})
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

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// queueCI queues one run of a pull request. auto marks runs the watch
// started.
func (s *Server) queueCI(owner, repo string, pr int, dry, approve, auto bool) (*task, error) {
	full := owner + "/" + repo
	title := fmt.Sprintf("Local CI: %s #%d", full, pr)
	if dry {
		title += " (dry run)"
	}
	return s.ci.add(title, []string{full}, func(w io.Writer) error {
		ctx := s.ciContext()
		start := time.Now()
		o := localci.Options{Owner: owner, Repo: repo, PR: pr, DryRun: dry, Approve: approve, Out: w}
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
		if t, ok := w.(*task); ok {
			run.Task = t.ID
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
			if _, err := s.queueCI(owner, repo, pr.Number, false, false, true); err != nil {
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
