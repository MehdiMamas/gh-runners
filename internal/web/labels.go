package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/ops"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// timedCache keeps per-key answers from GitHub for a while.
type timedCache[T any] struct {
	mu   sync.Mutex
	ttl  time.Duration
	byID map[string]timed[T]
}

type timed[T any] struct {
	at  time.Time
	val T
}

func (c *timedCache[T]) get(key string) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.byID[key]
	if !ok || time.Since(v.at) > c.ttl {
		var zero T
		return zero, false
	}
	return v.val, true
}

func (c *timedCache[T]) put(key string, val T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byID == nil {
		c.byID = map[string]timed[T]{}
	}
	c.byID[key] = timed[T]{time.Now(), val}
}

func (c *timedCache[T]) drop(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byID, key)
}

func (c *timedCache[T]) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byID = nil
}

// getWorkflowLabels tells the add dialog which labels the repository's
// workflows ask for, so it can suggest them.
func (s *Server) getWorkflowLabels(w http.ResponseWriter, r *http.Request) {
	sc, err := github.ParseScope(r.URL.Query().Get("repo"), false)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	key := strings.ToLower(sc.String())
	if sets, ok := s.wfLabels.get(key); ok {
		writeJSON(w, http.StatusOK, map[string]any{"sets": sets})
		return
	}
	c, _, err := s.client(r.Context())
	if err != nil {
		fail(w, http.StatusUnauthorized, err)
		return
	}
	files, err := c.Workflows(r.Context(), sc.Owner, sc.Repo)
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	sets := ops.WorkflowLabelSets(files)
	s.wfLabels.put(key, sets)
	writeJSON(w, http.StatusOK, map[string]any{"sets": sets})
}

// ghRunnerView is a runner GitHub lists for a repository, made here or
// anywhere else.
type ghRunnerView struct {
	ID     int64    `json:"id"`
	Name   string   `json:"name"`
	OS     string   `json:"os"`
	Status string   `json:"status"`
	Busy   bool     `json:"busy"`
	Labels []string `json:"labels"`
	Custom []string `json:"custom"`
	Local  string   `json:"local,omitempty"` // id of the gh-runners runner on this machine
	Found  string   `json:"found,omitempty"` // folder of a hand-made runner on this machine
}

// postRepoRunners lists the runners GitHub has for each repository asked
// for, including ones set up by hand or on other machines.
func (s *Server) postRepoRunners(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repos   []string `json:"repos"`
		Refresh bool     `json:"refresh"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if len(body.Repos) > 200 {
		body.Repos = body.Repos[:200]
	}
	c, _, err := s.client(r.Context())
	if err != nil {
		fail(w, http.StatusUnauthorized, err)
		return
	}
	type result struct {
		list []github.Runner
		err  string
	}
	results := make([]result, len(body.Repos))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, full := range body.Repos {
		sc, err := github.ParseScope(full, false)
		if err != nil {
			results[i].err = err.Error()
			continue
		}
		key := strings.ToLower(sc.String())
		if list, ok := s.ghRunners.get(key); ok && !body.Refresh {
			results[i].list = list
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			list, err := c.ListRunners(r.Context(), sc)
			if err != nil {
				results[i].err = err.Error()
				return
			}
			s.ghRunners.put(key, list)
			results[i].list = list
		}()
	}
	wg.Wait()

	local, _ := store.List()
	found, _ := s.found.get("")
	out := map[string]any{}
	errs := map[string]string{}
	for i, full := range body.Repos {
		if results[i].err != "" {
			errs[full] = results[i].err
			continue
		}
		views := []ghRunnerView{}
		for _, gr := range results[i].list {
			v := ghRunnerView{ID: gr.ID, Name: gr.Name, OS: gr.OS, Status: gr.Status, Busy: gr.Busy, Labels: []string{}, Custom: []string{}}
			for _, l := range gr.Labels {
				v.Labels = append(v.Labels, l.Name)
				if l.Type != "read-only" && !ops.IsBuiltinLabel(l.Name) {
					v.Custom = append(v.Custom, l.Name)
				}
			}
			for _, l := range local {
				if strings.EqualFold(l.Scope(), full) && l.Name == gr.Name {
					v.Local = l.ID
				}
			}
			for _, f := range found {
				if strings.EqualFold(f.Scope, full) && f.Name == gr.Name {
					v.Found = f.Dir
				}
			}
			views = append(views, v)
		}
		out[full] = views
	}
	writeJSON(w, http.StatusOK, map[string]any{"runners": out, "errors": errs})
}

// putRunnerLabels replaces the custom labels of a runner on GitHub.
func (s *Server) putRunnerLabels(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repo     string   `json:"repo"`
		RunnerID int64    `json:"runner_id"`
		Labels   []string `json:"labels"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	isOrg := !strings.Contains(body.Repo, "/")
	sc, err := github.ParseScope(body.Repo, isOrg)
	if err != nil || body.RunnerID <= 0 {
		fail(w, http.StatusBadRequest, errors.New("which runner? need repo and runner_id"))
		return
	}
	if _, err := ops.CleanLabels(body.Labels); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	c, _, err := s.client(r.Context())
	if err != nil {
		fail(w, http.StatusUnauthorized, err)
		return
	}
	got, err := ops.SetLabels(r.Context(), c, sc, body.RunnerID, body.Labels)
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	s.ghRunners.drop(strings.ToLower(sc.String()))
	names := []string{}
	for _, l := range got {
		names = append(names, l.Name)
	}
	writeJSON(w, http.StatusOK, map[string]any{"labels": names})
}

// getFound scans this machine for runners set up without gh-runners.
func (s *Server) getFound(w http.ResponseWriter, r *http.Request) {
	if list, ok := s.found.get(""); ok && r.URL.Query().Get("refresh") != "1" {
		writeJSON(w, http.StatusOK, list)
		return
	}
	list := ops.Scan(ops.ScanRoots())
	s.found.put("", list)
	writeJSON(w, http.StatusOK, list)
}

// postAdopt moves a hand-made runner into gh-runners.
func (s *Server) postAdopt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Dir string `json:"dir"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	f := ops.Inspect(body.Dir)
	if f.Name == "" {
		fail(w, http.StatusBadRequest, fmt.Errorf("%s: %s", body.Dir, f.Problem))
		return
	}
	s.startScopedTask(w, fmt.Sprintf("Move %q (%s) into gh-runners", f.Name, f.Scope), []string{f.Scope}, func(out io.Writer) error {
		ctx := context.Background()
		c, _, err := s.client(ctx)
		if err != nil {
			return err
		}
		_, err = ops.Adopt(ctx, c, body.Dir, out)
		s.found.reset()
		s.ghRunners.reset()
		s.repos.reset()
		s.remote.reset()
		s.h.Reload()
		if err == nil {
			s.ensureService(out)
		}
		return err
	})
}
