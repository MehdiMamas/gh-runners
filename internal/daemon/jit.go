package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/ops"
	"github.com/MehdiMamas/gh-runners/internal/proc"
	"github.com/MehdiMamas/gh-runners/internal/runner"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// On-demand mode: instead of one always-on runner per repository, the
// daemon polls your repositories for queued jobs whose runs-on labels this
// machine satisfies, and starts a just-in-time runner that takes one job and
// unregisters itself. Nothing has to be set up per repository.

const (
	jitIdleTimeout  = 3 * time.Minute // give up if no job was assigned
	repoListRefresh = 10 * time.Minute
)

type jitSlot struct {
	idx      int
	dir      string
	cmd      *exec.Cmd
	done     chan struct{}
	scope    github.Scope
	runnerID int64
	gotJob   bool
	state    control.ProcState
}

type jitManager struct {
	d *Daemon

	mu       sync.Mutex
	slots    []*jitSlot
	repos    []github.Repo
	me       string
	reposAt  time.Time
	lastPoll time.Time
	lastErr  string
	enabled  bool
	paused   bool // an update is about to restart the daemon
	watching int
	wakeUp   chan struct{}
}

func newJITManager(d *Daemon) *jitManager {
	return &jitManager{d: d, wakeUp: make(chan struct{}, 1)}
}

// wake makes the loop re-read the config and the repository list now,
// after the dashboard changed the on-demand settings.
func (m *jitManager) wake() {
	m.mu.Lock()
	m.reposAt = time.Time{}
	m.mu.Unlock()
	nudge(m.wakeUp)
}

// pause stops (or resumes) starting new on-demand runners.
func (m *jitManager) pause(on bool) {
	m.mu.Lock()
	m.paused = on
	m.mu.Unlock()
}

func (m *jitManager) states() []control.ProcState {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []control.ProcState
	for _, s := range m.slots {
		if s.cmd != nil {
			out = append(out, s.state)
		}
	}
	return out
}

func (m *jitManager) summary() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.enabled {
		return "off"
	}
	if m.d.paused.Load() {
		return "paused"
	}
	s := fmt.Sprintf("watching %d repo(s), last poll %s", m.watching, m.lastPoll.Format(time.Kitchen))
	if m.lastErr != "" {
		s += ", error: " + m.lastErr
	}
	return s
}

func (m *jitManager) loop(ctx context.Context) {
	for {
		cfg, err := store.LoadConfig()
		poll := time.Minute
		if err == nil {
			poll = cfg.OnDemand.Poll.Or(time.Minute)
			if poll < 15*time.Second {
				poll = 15 * time.Second
			}
			m.mu.Lock()
			m.enabled = cfg.OnDemand.Enabled
			paused := m.paused
			m.mu.Unlock()
			if cfg.OnDemand.Enabled && !paused && !m.d.paused.Load() {
				err = m.poll(ctx, cfg)
			}
		}
		m.mu.Lock()
		m.lastPoll = time.Now()
		m.lastErr = ""
		if err != nil {
			m.lastErr = err.Error()
			m.d.Log.Printf("on-demand: %v", err)
		}
		m.mu.Unlock()
		if m.d.paused.Load() {
			poll = pausedRecheck
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		case <-m.wakeUp:
		}
	}
}

// MachineLabels is the full label set a runner on this machine answers to:
// what GitHub adds by default plus the configured custom labels.
func MachineLabels(cfg store.Config) []string { return ops.MachineLabels(cfg) }

// JobMatches reports whether every runs-on label of a job is one we have.
func JobMatches(jobLabels, ours []string) bool {
	if len(jobLabels) == 0 {
		return false
	}
	have := map[string]bool{}
	for _, l := range ours {
		have[strings.ToLower(l)] = true
	}
	for _, l := range jobLabels {
		if !have[strings.ToLower(l)] {
			return false
		}
	}
	return true
}

func (m *jitManager) poll(ctx context.Context, cfg store.Config) error {
	c, err := m.d.client()
	if err != nil {
		return err
	}
	od := cfg.OnDemand
	max := od.MaxConcurrent
	if max <= 0 {
		max = 2
	}
	m.mu.Lock()
	for len(m.slots) < max {
		m.slots = append(m.slots, &jitSlot{idx: len(m.slots), dir: filepath.Join(store.Home(), "jit", fmt.Sprintf("slot-%d", len(m.slots)))})
	}
	needRepos := time.Since(m.reposAt) > repoListRefresh
	m.mu.Unlock()

	if needRepos {
		me, _, err := c.Me(ctx)
		if err != nil {
			return err
		}
		repos, err := c.ListMyRepos(ctx)
		if err != nil {
			return err
		}
		m.mu.Lock()
		m.repos, m.me, m.reposAt = repos, me.Login, time.Now()
		m.mu.Unlock()
	}

	// Repositories that already have an always-on runner here are served by it.
	persistent := map[string]bool{}
	if local, err := store.List(); err == nil {
		for _, r := range local {
			if r.Kind == store.KindRepo && !r.Disabled {
				persistent[strings.ToLower(r.Scope())] = true
			}
		}
	}
	f := ops.RepoFilter{Owners: od.Owners, Include: od.Include, Exclude: od.Exclude, IncludePublic: od.IncludePublic}
	activeWithin := od.ActiveWithin.Or(14 * 24 * time.Hour)
	ours := MachineLabels(cfg)

	m.mu.Lock()
	repos, me := m.repos, m.me
	m.mu.Unlock()
	watching := 0
	for _, repo := range repos {
		if ok, _ := f.Match(repo, me); !ok || persistent[strings.ToLower(repo.FullName)] {
			continue
		}
		if pushed, err := time.Parse(time.RFC3339, repo.PushedAt); err == nil && time.Since(pushed) > activeWithin {
			continue
		}
		watching++
		scope := github.Scope{Owner: repo.Owner.Login, Repo: repo.Name}
		queued, err := m.matchingQueuedJobs(ctx, c, scope, ours)
		if err != nil {
			m.d.Log.Printf("on-demand %s: %v", scope, err)
			continue
		}
		for need := queued - m.idleFor(scope); need > 0; need-- {
			slot := m.freeSlot()
			if slot == nil {
				break
			}
			if err := m.launch(ctx, c, cfg, slot, scope, ours); err != nil {
				m.d.Log.Printf("on-demand %s: launch: %v", scope, err)
				m.release(slot)
				break
			}
		}
	}
	m.mu.Lock()
	m.watching = watching
	m.mu.Unlock()
	return nil
}

func (m *jitManager) matchingQueuedJobs(ctx context.Context, c *github.Client, scope github.Scope, ours []string) (int, error) {
	n := 0
	// A run stays in_progress while its later jobs wait in the queue.
	for _, status := range []string{"queued", "in_progress"} {
		runs, err := c.Runs(ctx, scope.Owner, scope.Repo, status)
		if err != nil {
			return 0, err
		}
		for _, run := range runs {
			jobs, err := c.RunJobs(ctx, scope.Owner, scope.Repo, run.ID)
			if err != nil {
				return 0, err
			}
			for _, j := range jobs {
				if j.Status == "queued" && JobMatches(j.Labels, ours) {
					n++
				}
			}
		}
	}
	return n, nil
}

// idleFor counts our JIT runners for scope that have not picked a job yet.
func (m *jitManager) idleFor(scope github.Scope) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.slots {
		if s.cmd != nil && !s.gotJob && strings.EqualFold(s.scope.String(), scope.String()) {
			n++
		}
	}
	return n
}

func (m *jitManager) freeSlot() *jitSlot {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.slots {
		if s.cmd == nil && s.state.State != "starting" {
			s.state = control.ProcState{State: "starting"}
			return s
		}
	}
	return nil
}

func (m *jitManager) release(s *jitSlot) {
	m.mu.Lock()
	s.cmd, s.state = nil, control.ProcState{}
	m.mu.Unlock()
}

// jobWatcher marks the slot busy when the runner logs that it took a job.
type jobWatcher struct {
	f   *os.File
	hit func()
}

func (w jobWatcher) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("Running job:")) {
		w.hit()
	}
	return w.f.Write(p)
}

func (m *jitManager) launch(ctx context.Context, c *github.Client, cfg store.Config, s *jitSlot, scope github.Scope, labels []string) error {
	if m.d.paused.Load() {
		return errors.New("the machine is paused")
	}
	if _, err := os.Stat(filepath.Join(s.dir, "run.sh")); err != nil {
		if _, err := os.Stat(filepath.Join(s.dir, "run.cmd")); err != nil {
			pkg, err := runner.EnsurePackage(ctx, c, scope, logWriter{m.d.Log})
			if err != nil {
				return err
			}
			if err := runner.Extract(pkg.Path, s.dir); err != nil {
				return err
			}
		}
	}
	// Leftovers of a previous registration would make the runner ignore
	// the new JIT config.
	for _, f := range []string{".runner", ".credentials", ".credentials_rsaparams"} {
		_ = os.Remove(filepath.Join(s.dir, f))
	}
	name := fmt.Sprintf("%s-jit-%d-%d", ops.MachineLabel(cfg), s.idx, time.Now().Unix())
	jit, err := c.GenerateJITConfig(ctx, scope, name, labels, 0)
	if err != nil {
		return err
	}
	out, err := openLog(filepath.Join(s.dir, "gh-runners.log"))
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\n=== %s on-demand runner %s for %s ===\n", time.Now().Format(time.RFC3339), name, scope)
	cmd := runner.RunCommand(s.dir, jit.EncodedJITConfig)
	w := jobWatcher{f: out, hit: func() {
		m.mu.Lock()
		if !s.gotJob {
			s.gotJob, s.state.State = true, "jit-busy"
		}
		m.mu.Unlock()
	}}
	cmd.Stdout, cmd.Stderr = w, w
	proc.Supervise(cmd)
	if err := cmd.Start(); err != nil {
		out.Close()
		_ = c.DeleteRunner(context.Background(), scope, jit.Runner.ID)
		return err
	}
	m.mu.Lock()
	s.cmd, s.done, s.scope, s.runnerID, s.gotJob = cmd, make(chan struct{}), scope, jit.Runner.ID, false
	s.state = control.ProcState{ID: "on-demand:" + scope.String() + "#" + fmt.Sprint(s.idx), State: "jit-idle", PID: cmd.Process.Pid, Since: time.Now()}
	done := s.done
	m.mu.Unlock()
	m.d.Log.Printf("on-demand %s: started %s (pid %d)", scope, name, cmd.Process.Pid)

	go func() {
		err := cmd.Wait()
		out.Close()
		close(done)
		m.d.Log.Printf("on-demand %s: %s exited (%v)", scope, name, err)
		// GitHub deletes a JIT runner after its job; this cleans up one
		// that never got a job or crashed.
		_ = c.DeleteRunner(context.Background(), scope, jit.Runner.ID)
		m.release(s)
	}()
	go func() {
		select {
		case <-done:
		case <-time.After(jitIdleTimeout):
			m.mu.Lock()
			idle := !s.gotJob && s.cmd == cmd
			m.mu.Unlock()
			if idle {
				m.d.Log.Printf("on-demand %s: %s got no job in %s, stopping", scope, name, jitIdleTimeout)
				proc.Stop(cmd, done, 15*time.Second)
			}
		}
	}()
	return nil
}

func (m *jitManager) stopAll() {
	m.mu.Lock()
	var running []*jitSlot
	for _, s := range m.slots {
		if s.cmd != nil {
			running = append(running, s)
		}
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range running {
		wg.Add(1)
		go func(s *jitSlot) {
			defer wg.Done()
			proc.Stop(s.cmd, s.done, stopGrace)
		}(s)
	}
	wg.Wait()
}

// stopAllNow stops every on-demand runner and its process tree right away
// (a pause): a short grace to close the session, then a kill.
func (m *jitManager) stopAllNow() {
	m.mu.Lock()
	var running []*jitSlot
	for _, s := range m.slots {
		if s.cmd != nil {
			running = append(running, s)
		}
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range running {
		wg.Add(1)
		go func(s *jitSlot) {
			defer wg.Done()
			proc.Stop(s.cmd, s.done, pauseGrace)
		}(s)
	}
	wg.Wait()
}
