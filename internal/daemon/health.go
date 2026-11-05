package daemon

import (
	"context"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/proc"
	"github.com/MehdiMamas/gh-runners/internal/runner"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

const (
	healthEvery      = 2 * time.Minute
	healthMinRunning = 5 * time.Minute  // a fresh listener gets time to connect
	healthOfflineFor = 5 * time.Minute  // offline this long before a restart
	healthRestartGap = 15 * time.Minute // at most one health restart per runner in this time
)

// healthLoop notices runners that run here but that GitHub shows offline, and
// restarts them (#149). Machines without their own GitHub sign-in skip it.
func (d *Daemon) healthLoop(ctx context.Context) {
	t := time.NewTicker(healthEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, err := d.localClient()
		if err != nil {
			continue
		}
		runners, err := store.List()
		if err != nil {
			continue
		}
		list := func(s github.Scope) ([]github.Runner, error) {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			return c.ListRunners(cctx, s)
		}
		d.checkGitHub(time.Now(), runners, list, runner.Busy, proc.KillTree)
	}
}

type healthCand struct {
	r     store.Runner
	pid   int
	since time.Time
}

// checkGitHub compares the running runners with GitHub's list of them. A
// runner offline there for healthOfflineFor is killed; the Wait goroutine in
// start then restarts it. d.mu is never held across list.
func (d *Daemon) checkGitHub(now time.Time, runners []store.Runner,
	list func(github.Scope) ([]github.Runner, error),
	busy func(dir string) bool, kill func(pid int) error) {
	if d.paused.Load() {
		return
	}
	byID := map[string]store.Runner{}
	for _, r := range runners {
		byID[r.ID] = r
	}
	byScope := map[github.Scope][]healthCand{}
	d.mu.Lock()
	for id, c := range d.children {
		r, ok := byID[id]
		if !ok || !r.Configured() || c.cmd == nil || c.stopping || c.held || c.state.State != "running" ||
			now.Sub(c.state.Since) <= healthMinRunning {
			continue
		}
		sc, err := github.ParseScope(r.Scope(), r.Kind == store.KindOrg)
		if err != nil {
			continue
		}
		byScope[sc] = append(byScope[sc], healthCand{r, c.state.PID, c.state.Since})
	}
	// Forget bookkeeping of runners that are gone.
	for id := range d.healthKills {
		if d.children[id] == nil {
			delete(d.healthKills, id)
		}
	}
	for id := range d.healthSeen {
		if d.children[id] == nil {
			delete(d.healthSeen, id)
		}
	}
	d.mu.Unlock()

	for sc, cands := range byScope {
		gh, err := list(sc)
		d.mu.Lock()
		if err != nil {
			// A failed call is not proof of offline: change no flags.
			d.ghCheck = control.GitHubCheck{At: now, Err: err.Error()}
			d.mu.Unlock()
			continue
		}
		d.ghCheck = control.GitHubCheck{At: now}
		status := map[string]string{}
		for _, g := range gh {
			status[g.Name] = g.Status
		}
		for _, hc := range cands {
			d.healthOne(now, hc, status, busy, kill)
		}
		d.mu.Unlock()
	}
}

// healthOne applies GitHub's answer to one runner; caller holds d.mu.
func (d *Daemon) healthOne(now time.Time, hc healthCand, status map[string]string,
	busy func(dir string) bool, kill func(pid int) error) {
	id := hc.r.ID
	c := d.children[id]
	if c == nil || c.cmd == nil || c.stopping || c.held || c.state.PID != hc.pid || !c.state.Since.Equal(hc.since) {
		return // changed since it was collected
	}
	st, ok := status[hc.r.Name]
	if !ok {
		// Deleted or renamed on GitHub: sync deals with that.
		d.noteOnce(id, "missing", "GitHub does not list this runner; leaving it alone")
		return
	}
	if st != "offline" {
		c.state.GitHubOffline, c.state.GitHubOfflineSince = false, time.Time{}
		delete(d.healthSeen, id)
		return
	}
	if !c.state.GitHubOffline {
		c.state.GitHubOffline, c.state.GitHubOfflineSince = true, now
	}
	if now.Sub(c.state.GitHubOfflineSince) < healthOfflineFor {
		return
	}
	if last, ok := d.healthKills[id]; ok && now.Sub(last) < healthRestartGap {
		return
	}
	if busy(c.dir) {
		d.noteOnce(id, "busy", "GitHub shows it offline but a job is running; not restarting")
		return
	}
	d.Log.Printf("[%s] GitHub shows it offline since %s; restarting", id, c.state.GitHubOfflineSince.Format(time.RFC3339))
	if d.healthKills == nil {
		d.healthKills = map[string]time.Time{}
	}
	d.healthKills[id] = now
	delete(d.healthSeen, id)
	c.healthKill = true
	if err := kill(c.state.PID); err != nil {
		c.healthKill = false
		d.Log.Printf("[%s] could not restart it: %v", id, err)
	}
}

// noteOnce logs a note for a runner once until its situation changes;
// caller holds d.mu.
func (d *Daemon) noteOnce(id, key, msg string) {
	if d.healthSeen == nil {
		d.healthSeen = map[string]string{}
	}
	if d.healthSeen[id] == key {
		return
	}
	d.healthSeen[id] = key
	d.Log.Printf("[%s] %s", id, msg)
}
