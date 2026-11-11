package fleet

import "time"

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
