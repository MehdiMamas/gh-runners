package daemon

import (
	"fmt"
	"strings"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/proc"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// sweepEvery is how often the daemon looks for processes of runners that
// should not be running.
var sweepEvery = time.Minute

// stopChild stops a runner's whole process tree, then kills whatever of the
// runner's folder is still running outside it, and only then marks the runner
// stopped. c.stopping must be set. Used for every stop of a supervised runner.
func (d *Daemon) stopChild(id string, c *child, grace time.Duration) {
	proc.Stop(c.cmd, c.done, grace)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.logKilled(id, proc.KillStrays(c.dir))
	if n := len(proc.Strays(c.dir)); n > 0 {
		d.strays[id] = n
	} else {
		delete(d.strays, id)
	}
	c.cmd, c.state.PID = nil, 0
	c.state.State, c.state.Since = "stopped", time.Now()
	delete(d.children, id)
	nudge(d.reload)
}

// sweep kills the processes of runners that should not run: stopped by hand,
// or the machine is paused. That is what an older version left behind when it
// stopped only the shell script of a runner and not its listener. A process
// has to be seen on two sweeps in a row, so the short-lived `config remove`
// of a runner being removed is left alone. Runners that should run are not
// swept: starting one kills its leftovers first.
func (d *Daemon) sweep() {
	runners, err := store.List()
	if err != nil {
		return
	}
	next := map[int]bool{}
	for _, r := range runners {
		if !r.Configured() {
			continue
		}
		found := proc.Strays(r.Dir())
		d.mu.Lock()
		if len(found) == 0 {
			delete(d.strays, r.ID)
			d.mu.Unlock()
			continue
		}
		cur, err := store.Load(r.ID)
		if c := d.children[r.ID]; (c != nil && c.cmd != nil) || err != nil || (!cur.Disabled && !d.paused.Load()) {
			d.mu.Unlock()
			continue
		}
		d.strays[r.ID] = len(found)
		again := false
		for _, s := range found {
			next[s.PID] = true
			again = again || d.seen[s.PID]
		}
		if again {
			d.logKilled(r.ID, proc.KillStrays(r.Dir()))
			if n := len(proc.Strays(r.Dir())); n > 0 {
				d.strays[r.ID] = n
			} else {
				delete(d.strays, r.ID)
			}
		}
		d.mu.Unlock()
	}
	d.seen = next
}

func (d *Daemon) logKilled(id string, killed []proc.Stray) {
	if len(killed) == 0 {
		return
	}
	var parts []string
	for i, s := range killed {
		if i == 5 {
			parts = append(parts, fmt.Sprintf("and %d more", len(killed)-i))
			break
		}
		parts = append(parts, fmt.Sprintf("%d %s", s.PID, s.Path))
	}
	d.Log.Printf("[%s] killed %d leftover process(es): %s", id, len(killed), strings.Join(parts, "; "))
}
