package daemon

import "github.com/MehdiMamas/gh-runners/internal/store"

// stopAtStart is the rule for a fresh daemon start: every runner starts
// stopped, and the ones that were on are remembered as the last session so
// one click brings them back. It returns the runners to mark stopped and the
// ids to save. A paused machine is left alone (resume restores what was on),
// and so is a restart for an update (keepRunning).
func stopAtStart(runners []store.Runner, paused, keepRunning bool) (stop []store.Runner, session []string) {
	if paused || keepRunning {
		return nil, nil
	}
	for _, r := range runners {
		if r.Configured() && !r.Disabled {
			stop = append(stop, r)
			session = append(session, r.ID)
		}
	}
	return stop, session
}

// startStopped applies stopAtStart to the stored runners.
func (d *Daemon) startStopped() {
	keep := store.ConsumeKeepRunning()
	live := store.DaemonLiveBefore()
	if err := store.SetDaemonLive(); err != nil {
		d.Log.Printf("write daemon-live marker: %v", err)
	}
	runners, err := store.List()
	if err != nil {
		d.Log.Printf("list runners: %v", err)
		return
	}
	if live && !keep {
		for _, r := range runners {
			if r.Configured() && !r.Disabled {
				d.Log.Printf("came back after a reboot or crash; the runners that were on stay on")
				break
			}
		}
	}
	stop, session := stopAtStart(runners, d.paused.Load(), keep || live)
	if len(stop) == 0 {
		return
	}
	if err := store.SaveLastSession(session); err != nil {
		d.Log.Printf("save last session: %v", err)
		return // keep them on rather than lose what was on
	}
	n := 0
	for _, r := range stop {
		cur, err := store.Load(r.ID)
		if err != nil {
			continue
		}
		cur.Disabled = true
		if err := store.Save(cur); err != nil {
			d.Log.Printf("[%s] leave stopped: %v", r.ID, err)
			continue
		}
		n++
	}
	d.Log.Printf("started with %d runner(s) stopped; the dashboard, menu and `gh-runners start --last` bring them back", n)
}

// markExit clears the daemon-live marker when the daemon was asked to stop
// (POST /shutdown). A signal (reboot, logout, bootout, kill) leaves it, so
// the next start brings the runners back.
func (d *Daemon) markExit(signalled bool) {
	if !signalled {
		store.ClearDaemonLive()
	}
}
