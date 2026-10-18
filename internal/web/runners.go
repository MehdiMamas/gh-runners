package web

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/doctor"
	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/ops"
	"github.com/MehdiMamas/gh-runners/internal/service"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

type runnerView struct {
	ID         string   `json:"id"`
	Scope      string   `json:"scope"`
	Kind       string   `json:"kind"`
	Owner      string   `json:"owner"`
	Repo       string   `json:"repo,omitempty"`
	Name       string   `json:"name"`
	Labels     []string `json:"labels"`
	Source     string   `json:"source,omitempty"`
	Disabled   bool     `json:"disabled"`
	Configured bool     `json:"configured"`
	State      string   `json:"state"`
	LastExit   string   `json:"last_exit,omitempty"`
	Restarts   int      `json:"restarts"`
	Since      string   `json:"since,omitempty"`
	Version    string   `json:"version"`
	Dir        string   `json:"dir"`
	Created    string   `json:"created"`
}

type stateView struct {
	Paused        bool                `json:"paused"`
	PausedSince   string              `json:"paused_since,omitempty"`
	Version       string              `json:"version"`
	OS            string              `json:"os"`
	Arch          string              `json:"arch"`
	Hostname      string              `json:"hostname"`
	MachineLabels []string            `json:"machine_labels"`
	Home          string              `json:"home"`
	RunnersDir    string              `json:"runners_dir"`
	DefaultDir    string              `json:"default_runners_dir"`
	PID           int                 `json:"pid"`
	Started       string              `json:"started"`
	Service       serviceView         `json:"service"`
	Auth          authView            `json:"auth"`
	Runners       []runnerView        `json:"runners"`
	OnDemandProcs []control.ProcState `json:"on_demand_runners"`
	OnDemand      string              `json:"on_demand"`
	LastSync      string              `json:"last_sync,omitempty"`
	SyncErr       string              `json:"sync_error,omitempty"`
	Config        store.Config        `json:"config"`
	TasksBusy     int                 `json:"tasks_busy"`
	Update        control.UpdateState `json:"update"`
	// LastSession is how many runners of the last session are stopped now:
	// what "Run what was on before it closed" would start.
	LastSession int `json:"last_session"`
}

type serviceView struct {
	Installed bool   `json:"installed"`
	Where     string `json:"where"`
}

func (s *Server) runnerViews(st control.Status) []runnerView {
	runners, _ := store.List()
	procs := map[string]control.ProcState{}
	for _, p := range st.Runners {
		procs[p.ID] = p
	}
	out := []runnerView{}
	for _, r := range runners {
		state, last := ops.LocalState(r, st, true)
		p := procs[r.ID]
		out = append(out, runnerView{
			ID: r.ID, Scope: r.Scope(), Kind: r.Kind, Owner: r.Owner, Repo: r.Repo, Name: r.Name,
			Labels: r.Labels, Source: r.Source, Disabled: r.Disabled, Configured: r.Configured(),
			State: state, LastExit: last, Restarts: p.Restarts, Since: since(p.Since),
			Version: r.Version, Dir: r.Dir(), Created: since(r.CreatedAt),
		})
	}
	return out
}

func (s *Server) getState(w http.ResponseWriter, r *http.Request) {
	st := s.h.Status()
	cfg, _ := store.LoadConfig()
	host, _ := os.Hostname()
	svc := service.Status{}
	if inst := serviceStatus(); inst != nil {
		svc = *inst
	}
	v := stateView{
		Paused: st.Paused, PausedSince: since(st.PausedSince),
		Version: s.h.Version, OS: runtime.GOOS, Arch: runtime.GOARCH, Hostname: host,
		MachineLabels: ops.MachineLabels(cfg), Home: store.Home(), RunnersDir: store.RunnersDir(),
		DefaultDir: store.DefaultRunnersDir(), PID: st.PID, Started: since(st.Started),
		Service: serviceView{svc.Installed, svc.Where}, Auth: s.authView(r.Context()),
		Runners: s.runnerViews(st), OnDemand: st.OnDemand, LastSync: since(st.LastSync),
		SyncErr: st.SyncErr, Config: cfg, TasksBusy: s.tasks.busy(), OnDemandProcs: []control.ProcState{},
		Update: st.Update,
	}
	for _, r := range ops.LastSessionRunners() {
		if r.Disabled {
			v.LastSession++
		}
	}
	for _, p := range st.Runners {
		if strings.HasPrefix(p.State, "jit") {
			v.OnDemandProcs = append(v.OnDemandProcs, p)
		}
	}
	writeJSON(w, http.StatusOK, v)
}

// serviceStatus is service.Get without asking the daemon (we are the daemon).
var serviceStatus = func() *service.Status {
	st := service.Installed()
	return &st
}

// repoCache keeps the repository list for a minute; listing hundreds of
// repos takes several API pages.
type repoCache struct {
	mu    sync.Mutex
	at    time.Time
	me    string
	repos []github.Repo
}

func (c *repoCache) reset() {
	c.mu.Lock()
	c.at = time.Time{}
	c.mu.Unlock()
}

type repoView struct {
	FullName    string   `json:"full_name"`
	Owner       string   `json:"owner"`
	OwnerType   string   `json:"owner_type"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	URL         string   `json:"url"`
	Private     bool     `json:"private"`
	Archived    bool     `json:"archived"`
	Fork        bool     `json:"fork"`
	Admin       bool     `json:"admin"`
	PushedAt    string   `json:"pushed_at"`
	RunnerID    string   `json:"runner_id,omitempty"`  // first local runner, for older pages
	RunnerIDs   []string `json:"runner_ids,omitempty"` // every local runner of the repo
	SyncMatch   bool     `json:"sync_match"`
	SyncReason  string   `json:"sync_reason,omitempty"`
}

func (s *Server) getRepos(w http.ResponseWriter, r *http.Request) {
	c, me, err := s.client(r.Context())
	if err != nil {
		fail(w, http.StatusUnauthorized, err)
		return
	}
	s.repos.mu.Lock()
	if r.URL.Query().Get("refresh") == "1" || s.repos.at.IsZero() || time.Since(s.repos.at) > time.Minute || s.repos.me != me {
		repos, err := c.ListMyRepos(r.Context())
		if err != nil {
			s.repos.mu.Unlock()
			fail(w, http.StatusBadGateway, err)
			return
		}
		s.repos.repos, s.repos.at, s.repos.me = repos, time.Now(), me
	}
	repos, at := s.repos.repos, s.repos.at
	s.repos.mu.Unlock()

	cfg, _ := store.LoadConfig()
	f := ops.RepoFilter{Owners: cfg.Sync.Owners, Include: cfg.Sync.Include, Exclude: cfg.Sync.Exclude, IncludePublic: cfg.Sync.IncludePublic, IncludeForks: cfg.Sync.IncludeForks}
	local, _ := store.List()
	have := map[string][]string{}
	for _, l := range local {
		if l.Kind == store.KindRepo {
			key := strings.ToLower(l.Scope())
			have[key] = append(have[key], l.ID)
		}
	}
	out := make([]repoView, 0, len(repos))
	for _, repo := range repos {
		ok, why := f.Match(repo, me)
		ids := have[strings.ToLower(repo.FullName)]
		first := ""
		if len(ids) > 0 {
			first = ids[0]
		}
		out = append(out, repoView{
			FullName: repo.FullName, Owner: repo.Owner.Login, OwnerType: repo.Owner.Type, Name: repo.Name,
			Description: repo.Description, URL: repo.HTMLURL, Private: repo.Private, Archived: repo.Archived,
			Fork: repo.Fork, Admin: repo.Permissions.Admin, PushedAt: repo.PushedAt,
			RunnerID: first, RunnerIDs: ids, SyncMatch: ok, SyncReason: why,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"me": me, "repos": out, "fetched": since(at)})
}

// remoteCache is what GitHub says about our runners (online, busy, ...).
type remoteCache struct {
	mu   sync.Mutex
	at   time.Time
	byID map[string]string
}

func (c *remoteCache) reset() {
	c.mu.Lock()
	c.at = time.Time{}
	c.mu.Unlock()
}

func (s *Server) getRemote(w http.ResponseWriter, r *http.Request) {
	// fresh=1 asks GitHub now: the main host checks for running jobs.
	out, err := s.remoteStates(r.Context(), r.URL.Query().Get("fresh") == "1")
	if err != nil {
		fail(w, http.StatusUnauthorized, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// remoteStates is what GitHub says about each local runner; fresh skips the
// answer cached for a minute.
func (s *Server) remoteStates(ctx context.Context, fresh bool) (map[string]string, error) {
	s.remote.mu.Lock()
	defer s.remote.mu.Unlock()
	if !s.remote.at.IsZero() && time.Since(s.remote.at) < time.Minute && !fresh {
		return s.remote.byID, nil
	}
	c, _, err := s.client(ctx)
	if err != nil {
		return nil, err
	}
	local, _ := store.List()
	byScope := map[string][]github.Runner{}
	failed := map[string]bool{}
	out := map[string]string{}
	for _, l := range local {
		key := l.Scope()
		if _, seen := byScope[key]; !seen && !failed[key] {
			list, err := c.ListRunners(ctx, github.Scope{Owner: l.Owner, Repo: l.Repo})
			if err != nil {
				failed[key] = true
			} else {
				byScope[key] = list
			}
		}
		state := "missing"
		if failed[key] {
			state = "error"
		}
		for _, gr := range byScope[key] {
			if gr.Name == l.Name {
				state = gr.Status
				if gr.Busy {
					state = "busy"
				}
			}
		}
		out[l.ID] = state
	}
	s.remote.byID, s.remote.at = out, time.Now()
	return out, nil
}

func (s *Server) postAdd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repos       []string `json:"repos"`
		Org         string   `json:"org"`
		Labels      []string `json:"labels"`
		AllowPublic bool     `json:"allow_public"`
		// Another adds one more runner to repos that already have one here;
		// Name names it (one repo only), else the machine name gets -2, -3...
		Another bool   `json:"another"`
		Name    string `json:"name"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	var scopes []github.Scope
	for _, full := range body.Repos {
		sc, err := github.ParseScope(full, false)
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		scopes = append(scopes, sc)
	}
	if body.Org != "" {
		sc, err := github.ParseScope(body.Org, true)
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		scopes = append(scopes, sc)
	}
	if len(scopes) == 0 {
		fail(w, http.StatusBadRequest, errors.New("pick at least one repository"))
		return
	}
	labels, err := ops.CleanLabels(body.Labels)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name != "" && len(scopes) > 1 {
		fail(w, http.StatusBadRequest, errors.New("a runner name works for one repository at a time"))
		return
	}
	title := "Add a runner to " + scopes[0].String()
	if body.Another {
		title = "Add another runner to " + scopes[0].String()
	}
	if body.Name != "" {
		title += " named " + body.Name
	}
	if len(scopes) > 1 {
		title = fmt.Sprintf("Add runners to %d repositories", len(scopes))
	}
	if len(labels) > 0 {
		title += " (" + strings.Join(labels, ", ") + ")"
	}
	names := make([]string, len(scopes))
	for i, sc := range scopes {
		names[i] = sc.String()
	}
	s.startScopedTask(w, title, names, func(out io.Writer) error {
		ctx := context.Background()
		c, _, err := s.client(ctx)
		if err != nil {
			return err
		}
		failed := 0
		for _, sc := range scopes {
			fmt.Fprintf(out, "== %s\n", sc)
			_, err := ops.Add(ctx, c, sc, ops.AddOptions{Source: "add", Labels: labels, AllowPublic: body.AllowPublic, Another: body.Another, Name: body.Name}, out)
			switch {
			case errors.Is(err, ops.ErrExists):
				fmt.Fprintln(out, err)
			case err != nil:
				failed++
				fmt.Fprintf(out, "failed: %v\n", err)
			default:
				fmt.Fprintf(out, "%s is ready.\n", sc)
			}
			fmt.Fprintln(out)
		}
		s.repos.reset()
		s.remote.reset()
		s.ghRunners.reset()
		s.h.Reload()
		s.ensureService(out)
		if failed > 0 {
			return fmt.Errorf("%d of %d failed; see the log", failed, len(scopes))
		}
		return nil
	})
}

// ensureService makes the runners come back after a restart: added from
// the dashboard means "just make it work".
func (s *Server) ensureService(out io.Writer) {
	if st := serviceStatus(); st == nil || st.Installed {
		return
	}
	fmt.Fprintln(out, "Turning on \"Start at login\" so the runners come back after a restart ...")
	if err := s.spawn("service-install.log", "service", "install"); err != nil {
		fmt.Fprintf(out, "could not: %v (turn it on in Settings)\n", err)
	}
}

func (s *Server) findRunner(w http.ResponseWriter, r *http.Request) (store.Runner, bool) {
	rn, err := store.Load(r.PathValue("id"))
	if err != nil || strings.ContainsAny(r.PathValue("id"), `/\`) {
		fail(w, http.StatusNotFound, fmt.Errorf("no runner %q on this machine", r.PathValue("id")))
		return store.Runner{}, false
	}
	return rn, true
}

// postStartStop starts or stops a runner. Stopping kills the runner's job
// processes, so like a restart it first answers 409 when GitHub says the
// runner is running a job; confirm_busy=1 goes on.
func (s *Server) postStartStop(disable bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rn, ok := s.findRunner(w, r)
		if !ok {
			return
		}
		if disable && !rn.Disabled && r.URL.Query().Get("confirm_busy") != "1" && s.runnerRunning(rn.ID) {
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			states, err := s.remoteStates(ctx, true)
			cancel()
			switch {
			case err != nil:
				writeJSON(w, http.StatusConflict, map[string]any{
					"error":   "could not check with GitHub whether a job is running here (" + err.Error() + ")",
					"confirm": true,
				})
				return
			case states[rn.ID] == "busy":
				busy := []string{rn.Name + " (" + rn.Scope() + ")"}
				writeJSON(w, http.StatusConflict, map[string]any{
					"error": "running a job now: " + busy[0], "busy": busy, "confirm": true,
				})
				return
			}
		}
		rn.Disabled = disable
		if err := store.Save(rn); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		s.h.Reload()
		writeJSON(w, http.StatusOK, map[string]bool{"disabled": disable})
	}
}

// runnerRunning reports whether the daemon has a process of the runner.
func (s *Server) runnerRunning(id string) bool {
	for _, p := range s.h.Status().Runners {
		if p.ID == id && (p.PID != 0 || p.Strays > 0) {
			return true
		}
	}
	return false
}

// postRestart stops a runner, waits until the daemon has stopped it, and
// starts it again with a fresh environment (a new PATH, say). A stopped
// runner just starts.
func (s *Server) postRestart(w http.ResponseWriter, r *http.Request) {
	rn, ok := s.findRunner(w, r)
	if !ok {
		return
	}
	s.startScopedTask(w, "Restart the runner "+rn.Display(), []string{rn.Scope()}, func(out io.Writer) error {
		if !rn.Disabled {
			fmt.Fprintf(out, "Stopping %s ...\n", rn.Display())
			if err := s.setDisabled(rn.ID, true); err != nil {
				return err
			}
			if err := s.waitStopped(rn.ID, restartTimeout); err != nil {
				return err
			}
		}
		fmt.Fprintf(out, "Starting %s ...\n", rn.Display())
		if err := s.setDisabled(rn.ID, false); err != nil {
			return err
		}
		fmt.Fprintf(out, "Restarted %s.\n", rn.Display())
		return nil
	})
}

// restartTimeout is how long a restart waits for the runner to stop: the
// runner gets 30 seconds to close its session before it is killed.
var restartTimeout = 90 * time.Second

// setDisabled reloads the runner first, so a label edit or another change
// made since the request came in is kept.
func (s *Server) setDisabled(id string, disabled bool) error {
	rn, err := store.Load(id)
	if err != nil {
		return err
	}
	rn.Disabled = disabled
	if err := store.Save(rn); err != nil {
		return err
	}
	s.h.Reload()
	return nil
}

func (s *Server) waitStopped(id string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		running := false
		for _, p := range s.h.Status().Runners {
			if p.ID == id && p.PID != 0 {
				running = true
			}
		}
		if !running {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the runner is still running after %s", timeout)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (s *Server) postRemove(w http.ResponseWriter, r *http.Request) {
	rn, ok := s.findRunner(w, r)
	if !ok {
		return
	}
	force := r.URL.Query().Get("force") == "1"
	s.startTask(w, "Remove the runner "+rn.Display(), func(out io.Writer) error {
		ctx := context.Background()
		c, _, err := s.client(ctx)
		if err != nil {
			if !force {
				return fmt.Errorf("%v (or remove it from this machine only)", err)
			}
			c = github.New("")
		}
		if err := ops.Remove(ctx, c, rn, force, out); err != nil {
			return err
		}
		fmt.Fprintf(out, "Removed %s.\n", rn.Display())
		s.repos.reset()
		s.remote.reset()
		return nil
	})
}

// tailFile returns the last n lines of a file.
func tailFile(path string, n int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > 1<<20 {
		_, _ = f.Seek(st.Size()-1<<20, io.SeekStart)
	}
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	return strings.Join(lines, "\n"), nil
}

func lineCount(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if n <= 0 || n > 5000 {
		n = 300
	}
	return n
}

func (s *Server) getRunnerLog(w http.ResponseWriter, r *http.Request) {
	rn, ok := s.findRunner(w, r)
	if !ok {
		return
	}
	text, err := tailFile(rn.LogPath(), lineCount(r))
	if err != nil {
		text = "(no output yet)"
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text, "path": rn.LogPath()})
}

func (s *Server) getDaemonLog(w http.ResponseWriter, r *http.Request) {
	text, err := tailFile(store.DaemonLog(), lineCount(r))
	if err != nil {
		text = "(no log yet)"
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text, "path": store.DaemonLog()})
}

func (s *Server) getSyncPlan(w http.ResponseWriter, r *http.Request) {
	c, _, err := s.client(r.Context())
	if err != nil {
		fail(w, http.StatusUnauthorized, err)
		return
	}
	cfg, _ := store.LoadConfig()
	plan, err := ops.PlanSync(r.Context(), c, cfg.Sync)
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	add, prune := []string{}, []string{}
	for _, r := range plan.Add {
		add = append(add, r.FullName)
	}
	for _, r := range plan.Prune {
		prune = append(prune, r.Scope())
	}
	writeJSON(w, http.StatusOK, map[string]any{"have": plan.Have, "add": add, "prune": prune})
}

func (s *Server) postSyncApply(w http.ResponseWriter, r *http.Request) {
	s.startTask(w, "Sync runners with your repositories", func(out io.Writer) error {
		ctx := context.Background()
		c, _, err := s.client(ctx)
		if err != nil {
			return err
		}
		cfg, _ := store.LoadConfig()
		plan, err := ops.PlanSync(ctx, c, cfg.Sync)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%d repos already have a runner here; adding %d, removing %d.\n", plan.Have, len(plan.Add), len(plan.Prune))
		ops.ApplySync(ctx, c, &plan, out)
		s.repos.reset()
		s.remote.reset()
		s.h.Reload()
		if len(plan.Add) > 0 {
			s.ensureService(out)
		}
		if len(plan.Errors) > 0 {
			return fmt.Errorf("%d failed:\n%s", len(plan.Errors), strings.Join(plan.Errors, "\n"))
		}
		fmt.Fprintln(out, "Done.")
		return nil
	})
}

// putConfig saves the settings the page edits. The runners folder and the
// dashboard port have their own paths.
func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	var in store.Config
	if err := readBody(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	if in.OnDemand.MaxConcurrent < 0 || in.OnDemand.MaxConcurrent > 64 {
		fail(w, http.StatusBadRequest, errors.New("on-demand runners at once must be between 1 and 64"))
		return
	}
	if in.Sync.Interval.Duration != 0 && in.Sync.Interval.Duration < 5*time.Minute {
		fail(w, http.StatusBadRequest, errors.New("sync at most every 5 minutes"))
		return
	}
	turnedOnSync := in.Sync.Auto && !cfg.Sync.Auto
	cfg.Labels, cfg.RunnerName, cfg.Sync, cfg.OnDemand = clean(in.Labels), strings.TrimSpace(in.RunnerName), in.Sync, in.OnDemand
	cfg.AutoUpdate = in.AutoUpdate
	cfg.Sync.Owners, cfg.Sync.Include, cfg.Sync.Exclude = clean(cfg.Sync.Owners), clean(cfg.Sync.Include), clean(cfg.Sync.Exclude)
	cfg.OnDemand.Owners, cfg.OnDemand.Include, cfg.OnDemand.Exclude = clean(cfg.OnDemand.Owners), clean(cfg.OnDemand.Include), clean(cfg.OnDemand.Exclude)
	if err := store.SaveConfig(cfg); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	s.repos.reset()
	s.h.Reload()
	s.h.WakeJIT()
	if turnedOnSync {
		s.h.SyncNow()
	}
	writeJSON(w, http.StatusOK, cfg)
}

func clean(list []string) []string {
	var out []string
	for _, v := range list {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (s *Server) postRunnersDir(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	p := strings.TrimSpace(body.Path)
	if p != "" && !filepath.IsAbs(p) {
		fail(w, http.StatusBadRequest, errors.New("use a full path, like "+store.DefaultRunnersDir()))
		return
	}
	title := "Use " + p + " for runners"
	if p == "" {
		title = "Use the default runners folder"
	}
	s.startTask(w, title, func(out io.Writer) error {
		return ops.MoveRunners(p, out)
	})
}

func (s *Server) postService(w http.ResponseWriter, r *http.Request) {
	var args []string
	switch r.PathValue("action") {
	case "install":
		args = []string{"service", "install"}
	case "uninstall":
		args = []string{"service", "uninstall", "--keep-running"}
	case "stop":
		args = []string{"service", "stop"}
	case "restart":
		args = []string{"service", "restart"}
	default:
		fail(w, http.StatusNotFound, errors.New("unknown service action"))
		return
	}
	// A helper process does it: installing or stopping the service restarts
	// or stops this daemon, and the page reconnects afterwards.
	if err := s.spawn("service-"+r.PathValue("action")+".log", args...); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"ok": "started"})
}

// postUpdate asks the daemon to install the latest release now (once no
// job is running); the page follows it in the state's update field.
func (s *Server) postUpdate(w http.ResponseWriter, r *http.Request) {
	if s.h.Status().Paused {
		fail(w, http.StatusConflict, errors.New("this machine is paused: resume it before updating"))
		return
	}
	s.h.UpdateNow()
	writeJSON(w, http.StatusAccepted, map[string]string{"ok": "started"})
}

func (s *Server) getDoctor(w http.ResponseWriter, r *http.Request) {
	st := s.h.Status()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	checks := doctor.Run(ctx, &st)
	sort.SliceStable(checks, func(i, j int) bool { return rank(checks[i].Level) < rank(checks[j].Level) })
	writeJSON(w, http.StatusOK, checks)
}

func rank(level string) int {
	return map[string]int{doctor.Fix: 0, doctor.Note: 1, doctor.OK: 2}[level]
}

// postStartSet runs the runners of the last session (all=false) or every
// runner (all=true). It answers 409 on a paused machine.
func (s *Server) postStartSet(all bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rs := ops.LastSessionRunners()
		if all {
			rs = ops.ConfiguredRunners()
		}
		started, err := ops.Enable(rs, func() error { s.h.Reload(); return nil })
		if errors.Is(err, ops.ErrPaused) {
			fail(w, http.StatusConflict, err)
			return
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"started": len(started)})
	}
}
