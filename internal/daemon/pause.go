package daemon

import (
	"sync"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/store"
)

const (
	// pauseGrace is how long a runner gets to close its session with GitHub
	// when pausing; then it and its job's processes are killed.
	pauseGrace = 5 * time.Second
	// pausedRecheck is how often the sync and update loops look whether the
	// pause is over; they do nothing else while paused.
	pausedRecheck = 2 * time.Minute
)

// Paused reports whether the machine is paused.
func (d *Daemon) Paused() bool { return d.paused.Load() }

// Pause stops everything gh-runners runs on this machine and keeps it
// stopped until Resume: every runner and its process tree (a running job
// is cancelled), on-demand runners, auto-sync and the update check. The
// daemon itself and the fleet connection stay up. The pause is written to
// disk first, so it survives a restart or reboot. It returns when the
// processes are gone.
func (d *Daemon) Pause() error {
	if err := store.SetPaused(true); err != nil {
		return err
	}
	d.paused.Store(true)
	d.Log.Printf("pausing: stopping all runners")
	d.mu.Lock()
	var wg sync.WaitGroup
	for id, c := range d.children {
		if c.cmd != nil {
			c.stopping = true
			c.state.State = "stopping"
			wg.Add(1)
			go func(id string, c *child) {
				defer wg.Done()
				d.stopChild(id, c, pauseGrace)
			}(id, c)
		}
	}
	d.mu.Unlock()
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.jit.stopAllNow()
	}()
	wg.Wait()
	nudge(d.reload)
	d.Log.Printf("paused")
	d.tellHost()
	return nil
}

// Resume ends the pause; the next reconcile starts the runners that are not
// stopped by hand, and on-demand, sync and updates run again.
func (d *Daemon) Resume() error {
	if err := store.SetPaused(false); err != nil {
		return err
	}
	d.paused.Store(false)
	d.Log.Printf("resumed")
	nudge(d.reload)
	d.jit.wake()
	d.tellHost()
	return nil
}

// tellHost sends the main host and the account PCs the new state now instead
// of at their next status, so their Machines tabs show the pause at once.
func (d *Daemon) tellHost() {
	if d.member != nil {
		d.member.Reload()
	}
	if d.peers != nil {
		d.peers.SendStatus()
	}
}

// pausedWait is what the sync and update loops wait while paused.
func (d *Daemon) pausedWait() time.Duration { return pausedRecheck }
