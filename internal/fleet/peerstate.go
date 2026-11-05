package fleet

import "time"

type peerState struct {
	err      string
	lastSeen time.Time
	info     Info
	hasInfo  bool
	misses   int
	dialing  bool
	// logged is the last failure line written per kind, so a failure that
	// repeats every round is logged once, until it changes or the peer connects.
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

func (r *peerRun) stateLocked(id string) *peerState {
	st := r.state[id]
	if st == nil {
		st = &peerState{}
		r.state[id] = st
	}
	return st
}
