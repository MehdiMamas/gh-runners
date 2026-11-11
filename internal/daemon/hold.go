package daemon

import (
	"fmt"
	"sort"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/runner"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// holdTicks is how many reconcile ticks in a row the busy count must stay
// below the limit before held runners start again.
const holdTicks = 2

// holdIdle returns the IDs of the idle always-on runners to hold when the
// jobs running here (busy runners plus jitBusy) have reached maxJobs. busy
// maps each running always-on runner to whether it runs a job. A busy runner
// is never held, and maxJobs <= 0 means no limit. With inUse (the machine is
// in use, #164) the limit counts as reached whatever the count.
func holdIdle(busy map[string]bool, jitBusy, maxJobs int, inUse bool) (hold []string) {
	if !inUse {
		if maxJobs <= 0 {
			return nil
		}
		n := jitBusy
		for _, b := range busy {
			if b {
				n++
			}
		}
		if n < maxJobs {
			return nil
		}
	}
	for id, b := range busy {
		if !b {
			hold = append(hold, id)
		}
	}
	sort.Strings(hold)
	return hold
}

// jitAllowed is how many on-demand runners may run at once: max, cut down so
// that the always-on runners busy with a job plus these stay within maxJobs.
// It is 0 while the machine is in use.
func jitAllowed(max, maxJobs, busyAlways int, inUse bool) int {
	if inUse {
		return 0
	}
	if maxJobs <= 0 {
		return max
	}
	n := maxJobs - busyAlways
	if n > max {
		n = max
	}
	if n < 0 {
		n = 0
	}
	return n
}

// holdAll says every idle runner is held and no on-demand runner starts:
// the machine is in use (#164) or an update is draining (#174).
func (d *Daemon) holdAll() bool { return d.deskBusy.Load() || d.draining.Load() }

// busyAlways reports, for each always-on runner that is running, whether it
// runs a job. Caller holds d.mu.
func (d *Daemon) busyAlways() map[string]bool {
	out := map[string]bool{}
	for id, c := range d.children {
		if c.cmd != nil && !c.stopping {
			out[id] = d.isBusy(c.dir)
		}
	}
	return out
}

// busyAlwaysCount is the number of always-on runners running a job. It is 0
// without calling the process list when no limit is set.
func (d *Daemon) busyAlwaysCount() int {
	if cfg, _ := store.LoadConfig(); cfg.MaxJobs <= 0 && !d.holdAll() {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, b := range d.busyAlways() {
		if b {
			n++
		}
	}
	return n
}

// applyLimit holds the idle runners while the max_jobs limit is reached and
// reports whether held runners must stay stopped. A hold lifts only after the
// busy count stayed under the limit for holdTicks ticks. With inUse every idle
// runner is held and the hold lifts holdTicks ticks after the machine is free.
// Caller holds d.mu.
func (d *Daemon) applyLimit(maxJobs int, inUse bool) (holding bool) {
	anyHeld := false
	for _, c := range d.children {
		anyHeld = anyHeld || c.held
	}
	if maxJobs <= 0 && !inUse && !anyHeld {
		d.below = 0
		for _, c := range d.children {
			c.held, c.holdAfterJob = false, false
		}
		return false
	}
	busy := d.busyAlways()
	n := d.jit.busy()
	for _, b := range busy {
		if b {
			n++
		}
	}
	reached := inUse || (maxJobs > 0 && n >= maxJobs)
	if reached {
		d.below = 0
	} else if d.below < holdTicks {
		d.below++
	}
	for _, id := range holdIdle(busy, d.jit.busy(), maxJobs, inUse) {
		c := d.children[id]
		// A job may have arrived since the busy set was taken.
		if c == nil || c.cmd == nil || c.stopping || d.isBusy(c.dir) {
			continue
		}
		c.held, c.stopping = true, true
		c.state.State = "stopping"
		d.Log.Printf("[%s] holding: %s", id, d.holdReason(inUse, n, maxJobs))
		go d.stopChild(id, c, stopGrace)
	}
	if reached {
		// A busy runner is held the moment its job ends, not at the next tick.
		for id, c := range d.children {
			if busy[id] && !c.stopping && !c.holdAfterJob {
				c.holdAfterJob = true
				go d.holdWhenIdle(id, c)
			}
		}
	}
	if !reached && d.below >= holdTicks {
		for _, c := range d.children {
			c.held, c.holdAfterJob = false, false
		}
		return false
	}
	return true
}

// holdPoll is how often holdWhenIdle looks at whether a job has ended.
var holdPoll = 500 * time.Millisecond

// isBusy reports whether the runner in dir runs a job. busyFn is a test seam.
func (d *Daemon) isBusy(dir string) bool {
	if d.busyFn != nil {
		return d.busyFn(dir)
	}
	return runner.Busy(dir)
}

// holdReason is the text of the "holding" log line.
func (d *Daemon) holdReason(inUse bool, n, maxJobs int) string {
	switch {
	case inUse && !d.deskBusy.Load():
		return "update pending"
	case inUse:
		return "machine in use"
	}
	return fmt.Sprintf("%d jobs running, limit %d", n, maxJobs)
}

// holdWhenIdle watches a busy runner for which a hold is wanted and stops it
// the moment its job ends, so a listener that picks the next job within a
// second is not missed by the 5 s reconcile tick. holdAfterJob only marks
// that a watcher runs for c; whether to stop is decided here from live state,
// under d.mu. It exits, clearing the flag, when it stopped the runner, when
// the hold is no longer wanted, or when c was replaced, stopped or lifted.
func (d *Daemon) holdWhenIdle(id string, c *child) {
	for {
		d.mu.Lock()
		if !d.watching(id, c) {
			c.holdAfterJob = false
			d.mu.Unlock()
			return
		}
		d.mu.Unlock()
		if d.isBusy(c.dir) {
			time.Sleep(holdPoll)
			continue
		}
		d.mu.Lock()
		if !d.watching(id, c) || d.isBusy(c.dir) {
			// Gone, or a new job arrived since the read: keep watching.
			if !d.watching(id, c) {
				c.holdAfterJob = false
				d.mu.Unlock()
				return
			}
			d.mu.Unlock()
			time.Sleep(holdPoll)
			continue
		}
		c.holdAfterJob = false
		maxJobs := 0
		if cfg, _ := store.LoadConfig(); cfg.MaxJobs > 0 {
			maxJobs = cfg.MaxJobs
		}
		inUse := d.holdAll()
		n := d.jit.busy()
		for _, b := range d.busyAlways() {
			if b {
				n++
			}
		}
		if inUse || (maxJobs > 0 && n >= maxJobs) {
			c.held, c.stopping = true, true
			c.state.State = "stopping"
			d.Log.Printf("[%s] holding: %s", id, d.holdReason(inUse, n, maxJobs))
			go d.stopChild(id, c, stopGrace)
		}
		d.mu.Unlock()
		return
	}
}

// watching says c is still the live child of id and a watcher has a reason to
// run. Caller holds d.mu.
func (d *Daemon) watching(id string, c *child) bool {
	return d.children[id] == c && c.cmd != nil && !c.stopping && c.holdAfterJob
}
