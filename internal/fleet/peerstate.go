package fleet

import (
	"fmt"
	"time"
)

type peerState struct {
	err      string
	lastSeen time.Time
	info     Info
	hasInfo  bool
	misses   int
	dialing  bool
	// logged is the last result line written per kind, so a result that
	// repeats every round (or every reconnect) is logged once, until it changes.
	logged map[string]string
	// Link stability (#186): see linkDown.
	up        bool
	upSince   time.Time
	unstable  bool
	reconnect int       // quick drops since the last summary
	summaryAt time.Time // when the last summary (or the first quick drop) was logged
}

const (
	quickDrop    = time.Minute
	stableAfter  = 5 * time.Minute
	summaryEvery = 10 * time.Minute
)

func (r *peerRun) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// linkUp logs a connect. While the peer's link is unstable the line is
// left out; the summary tells.
func (r *peerRun) linkUp(id string) {
	now := r.clock()
	r.mu.Lock()
	st := r.stateLocked(id)
	st.up, st.upSince = true, now
	quiet := st.unstable
	r.mu.Unlock()
	if !quiet {
		r.logLink(id, "connected")
	}
}

// linkDown logs a drop. A link that drops within a minute of its connect
// is a quick drop: the first one logs as usual, the following ones are
// counted and summed up at most every 10 minutes.
func (r *peerRun) linkDown(id string, err error) {
	now := r.clock()
	var lines []string
	r.mu.Lock()
	st := r.stateLocked(id)
	wasUp := st.up
	quick := wasUp && now.Sub(st.upSince) < quickDrop
	if st.up && st.unstable && now.Sub(st.upSince) >= stableAfter {
		lines = append(lines, r.stableLocked(st))
	}
	st.up = false
	log := true
	switch {
	case st.unstable:
		st.reconnect++
		log = false
		if now.Sub(st.summaryAt) >= summaryEvery {
			lines = append(lines, r.summaryLocked(st, now))
		}
	case quick:
		st.unstable, st.reconnect, st.summaryAt = true, 0, now
	}
	name := r.nameLocked(id)
	r.mu.Unlock()
	for _, l := range lines {
		r.p.logf("account machines: %s: %s", name, l)
	}
	if log {
		r.logLink(id, fmt.Sprintf("disconnected: %v", err))
	}
}

// stableLocked ends the unstable state and returns the line to log.
func (r *peerRun) stableLocked(st *peerState) string {
	st.unstable, st.reconnect = false, 0
	delete(st.logged, "link") // the next drop logs normally
	return "link stable again"
}

func (r *peerRun) summaryLocked(st *peerState, now time.Time) string {
	n := st.reconnect
	st.reconnect, st.summaryAt = 0, now
	return fmt.Sprintf("link unstable: %d reconnects in the last 10 min", n)
}

// checkLinks runs from the status loop: an unstable peer whose link has been
// up 5 minutes is stable again, and a due summary is logged.
func (r *peerRun) checkLinks() {
	now := r.clock()
	type line struct{ name, msg string }
	var out []line
	r.mu.Lock()
	for id, st := range r.state {
		if !st.unstable {
			continue
		}
		if st.up && now.Sub(st.upSince) >= stableAfter {
			out = append(out, line{r.nameLocked(id), r.stableLocked(st)})
		} else if st.reconnect > 0 && now.Sub(st.summaryAt) >= summaryEvery {
			out = append(out, line{r.nameLocked(id), r.summaryLocked(st, now)})
		}
	}
	r.mu.Unlock()
	for _, l := range out {
		r.p.logf("account machines: %s: %s", l.name, l.msg)
	}
}

// logFailure logs "account machines: <name>: <msg>" unless the last line of
// this kind for the peer was the same.
func (r *peerRun) logFailure(id, kind, name, msg string) {
	r.mu.Lock()
	st := r.stateLocked(id)
	same := st.logged[kind] == msg
	if st.logged == nil {
		st.logged = map[string]string{}
	}
	st.logged[kind] = msg
	r.mu.Unlock()
	if !same {
		r.p.logf("account machines: %s: %s", name, msg)
	}
}

// logLink logs a connect or disconnect of a peer, only when it differs from
// the last link line logged for that peer (#174).
func (r *peerRun) logLink(id, msg string) {
	r.logFailure(id, "link", r.name(id), msg)
}

func (r *peerRun) stateLocked(id string) *peerState {
	st := r.state[id]
	if st == nil {
		st = &peerState{}
		r.state[id] = st
	}
	return st
}

// clearFailures runs on a connect: a failure after it logs again; only the
// link line is kept, so a flapping link logs each change once.
func (st *peerState) clearFailures() {
	for k := range st.logged {
		if k != "link" {
			delete(st.logged, k)
		}
	}
}
