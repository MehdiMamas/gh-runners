package daemon

import (
	"sort"

	"github.com/MehdiMamas/gh-runners/internal/runner"
)

// holdTicks is how many reconcile ticks in a row the busy count must stay
// below the limit before held runners start again.
const holdTicks = 2

// holdIdle returns the IDs of the idle always-on runners to hold when the
// jobs running here (busy runners plus jitBusy) have reached maxJobs. busy
// maps each running always-on runner to whether it runs a job. A busy runner
// is never held, and maxJobs <= 0 means no limit.
func holdIdle(busy map[string]bool, jitBusy, maxJobs int) (hold []string) {
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
func jitAllowed(max, maxJobs, busyAlways int) int {
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
