package daemon

import (
	"sort"

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
			out[id] = runner.Busy(c.dir)
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
			c.held = false
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
		if c == nil || c.cmd == nil || c.stopping || runner.Busy(c.dir) {
			continue
		}
		c.held, c.stopping = true, true
		c.state.State = "stopping"
		if inUse && !d.deskBusy.Load() {
			d.Log.Printf("[%s] holding: update pending", id)
		} else if inUse {
			d.Log.Printf("[%s] holding: machine in use", id)
		} else {
			d.Log.Printf("[%s] holding: %d jobs running, limit %d", id, n, maxJobs)
		}
		go d.stopChild(id, c, stopGrace)
	}
	if !reached && d.below >= holdTicks {
		for _, c := range d.children {
			c.held = false
		}
		return false
	}
	return true
}
