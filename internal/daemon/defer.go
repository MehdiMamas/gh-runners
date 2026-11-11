package daemon

import (
	"os"
	"sort"
	"strings"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/fleet"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// Prefer a machine for some jobs (#188): the config "defer" list names a
// label and a linked machine. While that machine is free and has an idle
// runner for the same scope and label, this machine's idle runners with the
// label are held, so the job goes to the preferred machine; otherwise they
// run as usual. Only the config file sets it.

// peerFresh is how long a peer's last report counts. A peer silent for
// longer is unavailable, so a stale state never holds a runner forever.
const peerFresh = 2 * time.Minute

// deferRunner is a runner of this machine, as the decision sees it.
type deferRunner struct {
	ID, Scope string
	Labels    []string
}

// deferHit says runner ID is deferred for Label to the machine To.
type deferHit struct{ ID, Label, To string }

// deferState is the daemon's memory for the defer rules, guarded by Daemon.mu
// except for the seams.
type deferState struct {
	// peers and self are test seams; nil means the real account peers and
	// this machine's id and host name.
	peers func() []fleet.Peer
	self  func() (id, host string)

	on     map[string]string // label -> machine it is held for, for the log
	warned map[string]bool   // rules already reported as unusable
	held   []control.Deferred
}

func (s *deferState) status() []control.Deferred {
	return append([]control.Deferred(nil), s.held...)
}

// findPeer returns the peer a rule's To names: its id, its name or its host
// name, ignoring case.
func findPeer(to string, peers []fleet.Peer) (fleet.Peer, bool) {
	for _, p := range peers {
		if strings.EqualFold(p.ID, to) || strings.EqualFold(p.Name, to) || strings.EqualFold(p.Info.Hostname, to) {
			return p, true
		}
	}
	return fleet.Peer{}, false
}

// hasLabel reports whether labels holds l, ignoring case.
func hasLabel(labels []string, l string) bool {
	for _, x := range labels {
		if strings.EqualFold(x, l) {
			return true
		}
	}
	return false
}

// peerTakes says peer p is linked directly, reported within peerFresh, is not
// in use or paused, reports which runners run jobs (an older version
// does not), and has a runner for scope and label that is up and not
// running a job.
func peerTakes(p fleet.Peer, scope, label string, now time.Time) bool {
	if !p.Online || p.Via != "" || !p.HasInfo || p.LastSeen.IsZero() || now.Sub(p.LastSeen) > peerFresh {
		return false
	}
	if p.Info.InUse || p.Info.Paused || !p.Info.ReportsBusy {
		return false
	}
	for _, r := range p.Info.Runners {
		if r.State == "running" && !r.Busy && strings.EqualFold(r.Scope, scope) && hasLabel(r.Labels, label) {
			return true
		}
	}
	return false
}

// deferHits lists the runners that carry a rule's label for which the rule's
// machine takes the job now. A rule naming this machine (selfID or selfHost)
// or a machine that is not a peer never matches.
func deferHits(rules []store.DeferRule, peers []fleet.Peer, now time.Time, selfID, selfHost string, local []deferRunner) []deferHit {
	var out []deferHit
	for _, rule := range rules {
		if rule.Label == "" || rule.To == "" || strings.EqualFold(rule.To, selfID) || strings.EqualFold(rule.To, selfHost) {
			continue
		}
		p, ok := findPeer(rule.To, peers)
		if !ok || strings.EqualFold(p.ID, selfID) {
			continue
		}
		for _, r := range local {
			if hasLabel(r.Labels, rule.Label) && peerTakes(p, r.Scope, rule.Label, now) {
				out = append(out, deferHit{r.ID, rule.Label, p.Name})
			}
		}
	}
	return out
}

// deferHold is the IDs among hits to stop now: only runners that are up and
// not running a job (busy maps each running runner to whether it has a job).
func deferHold(hits []deferHit, busy map[string]bool) []string {
	var ids []string
	seen := map[string]bool{}
	for _, h := range hits {
		if b, up := busy[h.ID]; up && !b && !seen[h.ID] {
			seen[h.ID] = true
			ids = append(ids, h.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func (d *Daemon) selfNames() (id, host string) {
	if d.pref.self != nil {
		return d.pref.self()
	}
	host, _ = os.Hostname()
	if d.peers != nil {
		id = d.peers.ID()
	}
	return id, host
}

// deferPeers reads the linked machines, or nothing when no rule is set. Call
// it without d.mu.
func (d *Daemon) deferPeers(rules []store.DeferRule) []fleet.Peer {
	if len(rules) == 0 {
		return nil
	}
	if d.pref.peers != nil {
		return d.pref.peers()
	}
	if d.peers == nil {
		return nil
	}
	return d.peers.Peers()
}

// deferJob says a queued job for scope with these labels is left to a
// preferred machine, so an on-demand runner here must not start for it.
func (d *Daemon) deferJob(rules []store.DeferRule, scope string, labels []string) bool {
	if len(rules) == 0 {
		return false
	}
	var use []store.DeferRule
	for _, r := range rules {
		if hasLabel(labels, r.Label) {
			use = append(use, r)
		}
	}
	if len(use) == 0 {
		return false
	}
	id, host := d.selfNames()
	return len(deferHits(use, d.deferPeers(use), time.Now(), id, host, []deferRunner{{ID: "job", Scope: scope, Labels: labels}})) > 0
}

// applyDefer holds the idle runners the rules defer and lifts the hold of the
// ones no longer deferred, so reconcile starts them again in the same pass. A
// runner with a job is never stopped. Caller holds d.mu.
func (d *Daemon) applyDefer(runners []store.Runner, rules []store.DeferRule, peers []fleet.Peer) {
	s := &d.pref
	if len(rules) == 0 && len(s.on) == 0 && len(s.held) == 0 {
		for _, c := range d.children {
			c.deferred = false
		}
		return
	}
	id, host := d.selfNames()
	d.warnRules(rules, peers, id, host)
	var local []deferRunner
	for _, r := range runners {
		local = append(local, deferRunner{r.ID, r.Scope(), r.Labels})
	}
	hits := deferHits(rules, peers, time.Now(), id, host, local)
	want := map[string]bool{}
	for _, h := range hits {
		want[h.ID] = true
	}
	for cid, c := range d.children {
		if c.deferred && !want[cid] {
			c.deferred = false
		}
	}
	for _, cid := range deferHold(hits, d.busyAlways()) {
		c := d.children[cid]
		if c == nil || c.cmd == nil || c.stopping || c.deferred || d.isBusy(c.dir) {
			continue
		}
		c.deferred, c.stopping = true, true
		c.state.State = "stopping"
		d.Log.Printf("[%s] holding: deferred to %s", cid, hitTo(hits, cid))
		go d.stopChild(cid, c, stopGrace)
	}
	d.noteDefer(hits)
}

func hitTo(hits []deferHit, id string) string {
	for _, h := range hits {
		if h.ID == id {
			return h.To
		}
	}
	return ""
}

// noteDefer logs a rule only when it turns on or off, and keeps the list for
// Status.
func (d *Daemon) noteDefer(hits []deferHit) {
	s := &d.pref
	now := map[string]string{}
	count := map[string]int{}
	for _, h := range hits {
		now[h.Label] = h.To
		if c := d.children[h.ID]; c != nil && c.deferred {
			count[h.Label]++
		}
	}
	for label, to := range now {
		if s.on[label] != to {
			d.Log.Printf("defer: holding %s runners for %s", label, to)
		}
	}
	for label, to := range s.on {
		if now[label] == "" {
			d.Log.Printf("defer: %s busy, taking %s jobs", to, label)
		}
	}
	s.on = now
	s.held = s.held[:0]
	for label, to := range now {
		s.held = append(s.held, control.Deferred{Label: label, To: to, Held: count[label]})
	}
	sort.Slice(s.held, func(i, j int) bool { return s.held[i].Label < s.held[j].Label })
}

// warnRules logs once a rule that names this machine or an unknown machine.
func (d *Daemon) warnRules(rules []store.DeferRule, peers []fleet.Peer, selfID, selfHost string) {
	s := &d.pref
	for _, r := range rules {
		key := r.Label + "\x00" + r.To
		if s.warned[key] {
			continue
		}
		if strings.EqualFold(r.To, selfID) || strings.EqualFold(r.To, selfHost) {
			d.Log.Printf("defer: %q names this machine, ignored", r.To)
		} else {
			if _, ok := findPeer(r.To, peers); ok || len(peers) == 0 {
				continue // len 0: the account link may not be up yet; look again
			}
			d.Log.Printf("defer: no linked machine %q, ignored", r.To)
		}
		if s.warned == nil {
			s.warned = map[string]bool{}
		}
		s.warned[key] = true
	}
}
