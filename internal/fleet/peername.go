package fleet

import (
	"errors"
	"os"
)

var (
	errOff        = errors.New("account machines are off on this PC")
	errNotRunning = errors.New("account machines are not running")
)

// OffNote is shown in the Machines tab when GH_RUNNERS_ACCOUNT_MACHINES=off.
const OffNote = "Account machines are off on this PC (GH_RUNNERS_ACCOUNT_MACHINES=off)."

// disabled reports (and remembers) that GH_RUNNERS_ACCOUNT_MACHINES=off
// keeps this daemon out of the account machines. Callers hold p.mu.
func (p *Peers) disabled() bool {
	if os.Getenv("GH_RUNNERS_ACCOUNT_MACHINES") == "off" {
		p.off = true
	}
	return p.off
}

// Off reports whether the account machines are switched off by the env.
func (p *Peers) Off() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.off
}

func (p *Peers) notRunning() error {
	if p.Off() {
		return errOff
	}
	return errNotRunning
}

// selfName is this machine's display name in the directory.
func (p *Peers) selfName() string {
	if p.Name != "" {
		return p.Name
	}
	n, _ := os.Hostname()
	return n
}

// name is the display name of a peer for logs and the Machines tab.
func (r *peerRun) name(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.nameLocked(id)
}

// nameLocked returns the peer's name, with " (<first 6 chars of id>)" added
// when this machine or another directory entry has the same name.
func (r *peerRun) nameLocked(id string) string {
	e, ok := r.entries[id]
	if !ok || e.Name == "" {
		return id
	}
	clash := e.Name == r.p.selfName()
	for oid, o := range r.entries {
		if oid != id && o.Name == e.Name {
			clash = true
		}
	}
	if !clash {
		return e.Name
	}
	return e.Name + " (" + id[:min(6, len(id))] + ")"
}
