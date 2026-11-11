package daemon

import (
	"reflect"
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/fleet"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

var deferNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// freePeer is a linked, fresh, free machine with one idle runner for
// org/app carrying the heavy label.
func freePeer() fleet.Peer {
	return fleet.Peer{
		Entry:  fleet.Entry{ID: "id-b", Name: "build-pc"},
		Online: true, HasInfo: true, LastSeen: deferNow.Add(-10 * time.Second),
		Info: fleet.Info{Hostname: "build-pc", ReportsBusy: true, Runners: []fleet.RunnerInfo{
			{ID: "r1", Scope: "org/app", State: "running", Labels: []string{"self-hosted", "heavy"}},
		}},
	}
}

var (
	rules    = []store.DeferRule{{Label: "heavy", To: "build-pc"}}
	localRun = []deferRunner{{ID: "a", Scope: "org/app", Labels: []string{"self-hosted", "Heavy"}}}
)

func TestDeferHits(t *testing.T) {
	cases := []struct {
		name string
		mod  func(p *fleet.Peer)
		want int
	}{
		{"peer free", func(p *fleet.Peer) {}, 1},
		{"peer in use", func(p *fleet.Peer) { p.Info.InUse = true }, 0},
		{"peer paused", func(p *fleet.Peer) { p.Info.Paused = true }, 0},
		{"peer stale", func(p *fleet.Peer) { p.LastSeen = deferNow.Add(-3 * time.Minute) }, 0},
		{"peer never seen", func(p *fleet.Peer) { p.LastSeen = time.Time{} }, 0},
		{"peer just fresh", func(p *fleet.Peer) { p.LastSeen = deferNow.Add(-2 * time.Minute) }, 1},
		{"peer unlinked", func(p *fleet.Peer) { p.Online = false }, 0},
		{"peer only through a relay", func(p *fleet.Peer) { p.Via = "id-c" }, 0},
		{"peer without info", func(p *fleet.Peer) { p.HasInfo = false }, 0},
		{"peer runner running a job", func(p *fleet.Peer) { p.Info.Runners[0].Busy = true }, 0},
		{"peer runner held", func(p *fleet.Peer) { p.Info.Runners[0].State = "held" }, 0},
		{"peer runner other scope", func(p *fleet.Peer) { p.Info.Runners[0].Scope = "org/other" }, 0},
		{"peer runner without the label", func(p *fleet.Peer) { p.Info.Runners[0].Labels = []string{"self-hosted"} }, 0},
		{"peer on a version without busy flags", func(p *fleet.Peer) { p.Info.ReportsBusy = false }, 0},
		{"peer without runners", func(p *fleet.Peer) { p.Info.Runners = nil }, 0},
	}
	for _, c := range cases {
		p := freePeer()
		c.mod(&p)
		got := deferHits(rules, []fleet.Peer{p}, deferNow, "id-self", "me", localRun)
		if len(got) != c.want {
			t.Errorf("%s: %d hits, want %d (%v)", c.name, len(got), c.want, got)
		}
	}
}

func TestDeferHitsRules(t *testing.T) {
	peers := []fleet.Peer{freePeer()}
	if got := deferHits(rules, peers, deferNow, "id-self", "me", []deferRunner{{ID: "x", Scope: "org/app", Labels: []string{"light"}}}); len(got) != 0 {
		t.Errorf("runner without the label held: %v", got)
	}
	for _, to := range []string{"id-self", "ME", "nobody", "", "id-b"} {
		got := deferHits([]store.DeferRule{{Label: "heavy", To: to}}, peers, deferNow, "id-self", "me", localRun)
		want := 0
		if to == "id-b" {
			want = 1 // a peer is found by id too
		}
		if len(got) != want {
			t.Errorf("to %q: %d hits, want %d", to, len(got), want)
		}
	}
	// A peer that is this machine (same id) is ignored.
	if got := deferHits(rules, peers, deferNow, "id-b", "me", localRun); len(got) != 0 {
		t.Errorf("self by id deferred: %v", got)
	}
}

func TestDeferHoldNeverStopsBusy(t *testing.T) {
	hits := []deferHit{{"a", "heavy", "build-pc"}, {"b", "heavy", "build-pc"}, {"c", "heavy", "build-pc"}, {"a", "gpu", "build-pc"}}
	got := deferHold(hits, map[string]bool{"a": false, "b": true}) // c is not running
	if !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("deferHold = %v, want [a]", got)
	}
}

func TestDeferJob(t *testing.T) {
	d := &Daemon{}
	p := freePeer()
	p.LastSeen = time.Now()
	d.pref.peers = func() []fleet.Peer { return []fleet.Peer{p} }
	d.pref.self = func() (string, string) { return "id-self", "me" }
	if !d.deferJob(rules, "org/app", []string{"self-hosted", "heavy"}) {
		t.Error("job for a free preferred machine not skipped")
	}
	if d.deferJob(rules, "org/app", []string{"self-hosted"}) {
		t.Error("job without the label skipped")
	}
	if d.deferJob(nil, "org/app", []string{"heavy"}) {
		t.Error("skipped without rules")
	}
	if d.deferJob(rules, "org/other", []string{"heavy"}) {
		t.Error("skipped though the peer has no runner for the scope")
	}
	p.Info.InUse = true
	if d.deferJob(rules, "org/app", []string{"heavy"}) {
		t.Error("skipped while the peer is in use")
	}
	p.Info.InUse = false
	p.LastSeen = time.Now().Add(-time.Hour)
	if d.deferJob(rules, "org/app", []string{"heavy"}) {
		t.Error("skipped on a stale peer")
	}
}

// The daemon holds idle runners while the peer is free, never a runner with a
// job, and lifts the hold the moment the peer is in use.
func TestApplyDeferThroughSeam(t *testing.T) {
	d, f := watchDaemon(t, 0, "a", "b", "c")
	p := freePeer()
	p.LastSeen = time.Now()
	d.pref.peers = func() []fleet.Peer { return []fleet.Peer{p} }
	d.pref.self = func() (string, string) { return "id-self", "me" }
	mk := func(id string, labels ...string) store.Runner {
		return store.Runner{ID: id, Kind: store.KindRepo, Owner: "org", Repo: "app", Labels: labels}
	}
	runners := []store.Runner{mk("a", "heavy"), mk("b", "heavy"), mk("c", "light")}
	f.set(d.children["b"].dir, true) // b runs a job
	f.set(d.children["a"].dir, false)
	f.set(d.children["c"].dir, false)

	apply := func() {
		peers := d.deferPeers(rules)
		d.mu.Lock()
		d.applyDefer(runners, rules, peers)
		d.mu.Unlock()
	}
	apply()
	d.mu.Lock()
	a, b, c := d.children["a"].deferred, d.children["b"].deferred, d.children["c"].deferred
	st := d.pref.status()
	d.mu.Unlock()
	if !a || b || c {
		t.Fatalf("deferred a=%v b=%v c=%v, want only a", a, b, c)
	}
	if len(st) != 1 || st[0].Label != "heavy" || st[0].To != "build-pc" || st[0].Held != 1 {
		t.Fatalf("status = %+v", st)
	}
	waitFor(t, "a stopped", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.children["a"].cmd == nil
	})

	p.Info.InUse = true
	apply()
	d.mu.Lock()
	a = d.children["a"].deferred
	st = d.pref.status()
	d.mu.Unlock()
	if a || len(st) != 0 {
		t.Fatalf("hold not lifted: deferred=%v status=%+v", a, st)
	}
}

func TestFindPeerPrefersIDAndName(t *testing.T) {
	peers := []fleet.Peer{
		{ID: "aaa", Name: "box", Info: fleet.Info{Hostname: "build"}},
		{ID: "build", Name: "other", Info: fleet.Info{Hostname: "build"}},
	}
	if p, _ := findPeer("build", peers); p.ID != "build" {
		t.Fatalf("id match should win over host name, got %q", p.ID)
	}
	if p, _ := findPeer("BOX", peers); p.ID != "aaa" {
		t.Fatalf("name match, got %q", p.ID)
	}
	if _, ok := findPeer("nope", peers); ok {
		t.Fatal("unknown name matched")
	}
}
