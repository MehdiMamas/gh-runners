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
	"github.com/MehdiMamas/gh-runners/internal/service"
	"github.com/MehdiMamas/gh-runners/internal/store"
	"github.com/MehdiMamas/gh-runners/internal/update"
)

const (
	updateFirst = 2 * time.Minute
	updateEvery = 6 * time.Hour
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
	pending := false // asked from the dashboard; retry until it is done
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-d.updateNow:
			pending = true
		}
		if d.paused.Load() {
			pending = false
			timer.Reset(d.pausedWait())
			continue
		}
		var next time.Duration
		next, pending = d.checkUpdate(ctx, pending)
		timer.Reset(next)
	}
}

// checkUpdate does one round and says when to do the next one and whether
// a requested update is still pending.
func (d *Daemon) checkUpdate(ctx context.Context, asked bool) (time.Duration, bool) {
	if !update.Releasable(d.Version) {
		d.setUpdate(func(u *control.UpdateState) {
			u.Note = devNote(d.Version)
		})
		return updateEvery, false
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
		return updateEvery, false
	}
	cfg, _ := store.LoadConfig()
	if !asked && !cfg.AutoUpdateOn() {
		d.setUpdate(func(u *control.UpdateState) { u.Note = latest + " is available; auto-update is off" })
		return updateEvery, false
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
	return updateEvery, false
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
	d.Log.Printf("installed gh-runners %s at %s; restarting", tag, exe)
	d.setUpdate(func(u *control.UpdateState) { u.Note = "restarting into " + tag })
	if err := service.Spawn(filepath.Join(store.Home(), "update-restart.log"), "service", "restart"); err != nil {
		d.jit.pause(false)
		return false, fmt.Errorf("restart: %w", err)
	}
	return false, nil
}

// busy says what is running a job on this machine, or "" when nothing is.
// When GitHub can't be asked, it assumes a job is running.
func (d *Daemon) busy(ctx context.Context) string {
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
	byScope := map[string][]store.Runner{}
	for _, r := range runners {
		if running[r.ID] {
			byScope[r.Scope()] = append(byScope[r.Scope()], r)
		}
	}
	c, err := d.client()
	if err != nil {
		return "GitHub sign-in to check running jobs"
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
