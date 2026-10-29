package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/localci"
	"github.com/MehdiMamas/gh-runners/internal/service"
	"github.com/MehdiMamas/gh-runners/internal/store"
	"github.com/MehdiMamas/gh-runners/internal/update"
)

const (
	updateFirst = 2 * time.Minute
	// updateBusy is how often a pending update checks whether jobs finished.
	updateBusy = 2 * time.Minute
)

// updateLoop checks for a new gh-runners release every few hours and, when
// auto-update is on (or the dashboard asked), installs it and restarts the
// daemon, but only while no runner on this machine is running a job.
func (d *Daemon) updateLoop(ctx context.Context) {
	if exe, err := service.Executable(); err == nil {
		update.Cleanup(exe)
	}
	if !update.Releasable(d.Version) {
		d.setUpdate(func(u *control.UpdateState) { u.Note = devNote(d.Version) })
	}
	timer := time.NewTimer(updateFirst)
	defer timer.Stop()
	every := updateEvery()
	due := time.Now().Add(updateFirst)
	d.setUpdate(func(u *control.UpdateState) { u.Every, u.Next = store.ShortDuration(every), due })
	arm := func(wait time.Duration) {
		due = time.Now().Add(wait)
		d.setUpdate(func(u *control.UpdateState) { u.Every, u.Next = store.ShortDuration(updateEvery()), due })
		timer.Reset(wait)
	}
	pending := false // asked from the dashboard; retry until it is done
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-d.updateNow:
			pending = true
		case <-d.updateCfg:
			// Settings were saved. Only a changed interval moves the next
			// check: it is due the new interval after the last check.
			if now := updateEvery(); now != every {
				every = now
				wait := rescheduled(d.updateChecked(), time.Now(), every, due)
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				arm(wait)
			}
			continue
		}
		if d.paused.Load() {
			pending = false
			arm(d.pausedWait())
			continue
		}
		var next time.Duration
		next, pending = d.checkUpdate(ctx, pending)
		every = updateEvery()
		arm(next)
	}
}

// updateEvery is the interval from the settings, read each time so a change
// applies without a restart.
func updateEvery() time.Duration {
	cfg, _ := store.LoadConfig()
	return cfg.UpdateEvery()
}

// rescheduled says how long to wait for the next check after the interval
// changed to every: it counts from the last check (due as before when there
// was none), and a check that is already overdue runs in a few seconds.
func rescheduled(checked, now time.Time, every time.Duration, due time.Time) time.Duration {
	if checked.IsZero() {
		if w := due.Sub(now); w < every {
			return max(w, 0)
		}
		return every
	}
	return min(max(checked.Add(every).Sub(now), 5*time.Second), every)
}

func (d *Daemon) updateChecked() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.upd.Checked
}

// checkUpdate does one round and says when to do the next one and whether
// a requested update is still pending.
func (d *Daemon) checkUpdate(ctx context.Context, asked bool) (time.Duration, bool) {
	if !update.Releasable(d.Version) {
		d.setUpdate(func(u *control.UpdateState) {
			u.Note = devNote(d.Version)
		})
		return updateEvery(), false
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	latest, err := update.Latest(cctx)
	cancel()
	if err != nil {
		d.setUpdate(func(u *control.UpdateState) { u.Error = err.Error() })
		d.Log.Printf("update check: %v", err)
		if asked {
			return updateBusy, true
		}
		return time.Hour, false
	}
	d.setUpdate(func(u *control.UpdateState) { u.Latest, u.Checked, u.Error = latest, time.Now(), "" })
	if !update.Newer(latest, d.Version) {
		d.setUpdate(func(u *control.UpdateState) { u.Note = "up to date" })
		return updateEvery(), false
	}
	cfg, _ := store.LoadConfig()
	if !asked && !cfg.AutoUpdateOn() {
		d.setUpdate(func(u *control.UpdateState) { u.Note = latest + " is available; auto-update is off" })
		return updateEvery(), false
	}
	waiting, err := d.applyUpdate(ctx, latest)
	if err != nil {
		d.setUpdate(func(u *control.UpdateState) { u.Error = err.Error() })
		d.Log.Printf("update to %s: %v", latest, err)
		return time.Hour, false
	}
	if waiting {
		return updateBusy, asked
	}
	return updateEvery(), false
}

// applyUpdate downloads the release, waits for idle runners, swaps the
// binary and has a helper restart the service into it. waiting means a
// job is running and it should try again later.
func (d *Daemon) applyUpdate(ctx context.Context, tag string) (waiting bool, err error) {
	wait := func(why string) (bool, error) {
		d.setUpdate(func(u *control.UpdateState) { u.Note = "waiting for " + why + " before updating to " + tag })
		return true, nil
	}
	if why := d.busy(ctx); why != "" {
		return wait(why)
	}
	exe, err := service.Executable()
	if err != nil {
		return false, err
	}
	staged, err := update.Download(ctx, tag, filepath.Dir(exe), logWriter{d.Log})
	if err != nil {
		return false, err
	}
	defer os.Remove(staged)

	// Hold new on-demand runners and look once more: the download took a
	// while and a job may have started.
	d.jit.pause(true)
	if why := d.busy(ctx); why != "" {
		d.jit.pause(false)
		return wait(why)
	}
	if err := update.Replace(staged, exe); err != nil {
		d.jit.pause(false)
		return false, fmt.Errorf("replace %s: %w", exe, err)
	}
	d.updated.Store(true)
	d.Log.Printf("installed gh-runners %s at %s; restarting", tag, exe)
	d.setUpdate(func(u *control.UpdateState) { u.Note = "restarting into " + tag })
	if err := store.SetKeepRunning(); err != nil {
		d.Log.Printf("keep-running marker: %v", err)
	}
	if err := service.Spawn(filepath.Join(store.Home(), "update-restart.log"), "service", "restart"); err != nil {
		_ = os.Remove(store.KeepRunningPath())
		d.jit.pause(false)
		return false, fmt.Errorf("restart: %w", err)
	}
	return false, nil
}

// busy says what is running a job on this machine, or "" when nothing is.
// When GitHub can't be asked, it assumes a job is running.
func (d *Daemon) busy(ctx context.Context) string {
	if f := d.ciBusy; f != nil {
		if why := f(); why != "" {
			return why
		}
	}
	if why := d.member.CIBusy(); why != "" {
		return why
	}
	if why := d.peers.CIBusy(); why != "" {
		return why
	}
	if len(localci.RunningRuns()) > 0 {
		return "a local CI run is in progress"
	}
	if len(localci.ActiveJobs()) > 0 {
		return "a CI job is running"
	}
	if n := len(d.jit.states()); n > 0 {
		return fmt.Sprintf("%d on-demand runner(s) to finish their job", n)
	}
	d.mu.Lock()
	running := map[string]bool{}
	for id, c := range d.children {
		if c.cmd != nil {
			running[id] = true
		}
	}
	d.mu.Unlock()
	if len(running) == 0 {
		return ""
	}
	runners, err := store.List()
	if err != nil {
		return "the runner list (" + err.Error() + ")"
	}
	var rs []store.Runner
	for _, r := range runners {
		if running[r.ID] {
			rs = append(rs, r)
		}
	}
	c, err := d.client()
	if err != nil {
		return "GitHub sign-in to check running jobs"
	}
	return JobRunning(ctx, c, rs)
}

// JobRunning says which of runners GitHub reports busy with a job, or ""
// when none is. When GitHub can't be asked, it says that instead.
func JobRunning(ctx context.Context, c *github.Client, runners []store.Runner) string {
	byScope := map[string][]store.Runner{}
	for _, r := range runners {
		byScope[r.Scope()] = append(byScope[r.Scope()], r)
	}
	for scope, rs := range byScope {
		s, err := github.ParseScope(scope, rs[0].Kind == store.KindOrg)
		if err != nil {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		remote, err := c.ListRunners(cctx, s)
		cancel()
		if err != nil {
			return "GitHub to answer (" + err.Error() + ")"
		}
		for _, r := range rs {
			for _, g := range remote {
				if g.Busy && strings.EqualFold(g.Name, r.Name) {
					return "the job on " + r.Name + " (" + scope + ") to finish"
				}
			}
		}
	}
	return ""
}

func devNote(version string) string {
	return "this is a development build (" + version + "); it doesn't update itself"
}

func (d *Daemon) setUpdate(f func(*control.UpdateState)) {
	d.mu.Lock()
	f(&d.upd)
	d.mu.Unlock()
}
