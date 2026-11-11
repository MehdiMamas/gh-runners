package fleet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type flapLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *flapLog) add(f string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(f, a...))
}

func (l *flapLog) count(sub string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, s := range l.lines {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

// flapRun is a peerRun with a fake clock and one peer "a" named PC.
func flapRun() (*peerRun, *flapLog, *time.Time) {
	lg := &flapLog{}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	r := &peerRun{p: &Peers{Name: "Me", Log: lg.add}, state: map[string]*peerState{}, entries: map[string]Entry{"a": {Name: "PC"}}}
	r.now = func() time.Time { return now }
	return r, lg, &now
}

var errEOF = errors.New("EOF")

func TestUnstableLinkIsSummarized(t *testing.T) {
	r, lg, now := flapRun()
	start := *now
	for ; now.Sub(start) <= 30*time.Minute; *now = now.Add(5 * time.Second) {
		r.linkUp("a")
		*now = now.Add(2 * time.Second)
		r.linkDown("a", errEOF)
	}
	if n := lg.count(": connected") + lg.count(": disconnected"); n != 2 {
		t.Fatalf("%d link lines: %q", n, lg.lines)
	}
	if n := lg.count("link unstable"); n < 2 || n > 3 {
		t.Fatalf("%d unstable lines: %q", n, lg.lines)
	}
}

func TestUnstableLinkStableAgain(t *testing.T) {
	r, lg, now := flapRun()
	for i := 0; i < 3; i++ {
		r.linkUp("a")
		*now = now.Add(2 * time.Second)
		r.linkDown("a", errEOF)
	}
	r.linkUp("a")
	*now = now.Add(4 * time.Minute)
	r.checkLinks()
	if lg.count("stable again") != 0 {
		t.Fatalf("stable too early: %q", lg.lines)
	}
	*now = now.Add(2 * time.Minute)
	r.checkLinks()
	r.checkLinks()
	if n := lg.count("link stable again"); n != 1 {
		t.Fatalf("%d stable lines: %q", n, lg.lines)
	}
	before := lg.count("disconnected")
	r.linkDown("a", errEOF) // same error as the first drop: still logged
	if lg.count("disconnected") != before+1 {
		t.Fatalf("later drop not logged: %q", lg.lines)
	}
}

func TestStableAgainOnNextDrop(t *testing.T) {
	r, lg, now := flapRun()
	for i := 0; i < 2; i++ {
		r.linkUp("a")
		*now = now.Add(2 * time.Second)
		r.linkDown("a", errEOF)
	}
	r.linkUp("a")
	*now = now.Add(6 * time.Minute)
	r.linkDown("a", errEOF) // no status round ran
	if lg.count("link stable again") != 1 || lg.count("disconnected") != 2 {
		t.Fatalf("lines: %q", lg.lines)
	}
}

func TestDropAfterLongUptimeIsNormal(t *testing.T) {
	r, lg, now := flapRun()
	r.linkUp("a")
	*now = now.Add(time.Hour)
	r.linkDown("a", errEOF)
	if lg.count("disconnected") != 1 || lg.count("unstable") != 0 || lg.count("stable") != 0 {
		t.Fatalf("lines: %q", lg.lines)
	}
}

func TestNilClockFallsBackToTimeNow(t *testing.T) {
	r := &peerRun{p: &Peers{}, state: map[string]*peerState{}, entries: map[string]Entry{}}
	r.linkUp("a")
	r.linkDown("a", errEOF)
	r.checkLinks()
}

func TestPeerNamesAreUnique(t *testing.T) {
	r := &peerRun{p: &Peers{Name: "Mac"}, state: map[string]*peerState{}, entries: map[string]Entry{
		"aaaaaaaa1": {ID: "aaaaaaaa1", Name: "Mac"}, "bbbbbbbb2": {ID: "bbbbbbbb2", Name: "PC"}, "cccccccc3": {ID: "cccccccc3", Name: "Lab"}, "dddddddd4": {ID: "dddddddd4", Name: "Lab"},
		"eeeeeeee5": {ID: "eeeeeeee5", Name: "Solo"},
	}, links: map[string]*link{}}
	for id, want := range map[string]string{
		"aaaaaaaa1": "Mac (aaaaaa)", "bbbbbbbb2": "PC", "cccccccc3": "Lab (cccccc)", "dddddddd4": "Lab (dddddd)", "eeeeeeee5": "Solo",
	} {
		if got := r.name(id); got != want {
			t.Errorf("name(%s) = %q, want %q", id, got, want)
		}
	}
	// ViaName, Peers() and Machine() carry the same names.
	r.links["bbbbbbbb2"] = &link{id: "bbbbbbbb2"}
	r.p.run = r
	var lg flapLog
	r.p.Log = lg.add
	r.logLink("aaaaaaaa1", "connected")
	if lg.count("Mac (aaaaaa): connected") != 1 {
		t.Fatalf("link log: %q", lg.lines)
	}
	byID := map[string]Peer{}
	for _, pe := range r.p.Peers() {
		byID[pe.ID] = pe
	}
	if byID["aaaaaaaa1"].Name != "Mac (aaaaaa)" || byID["bbbbbbbb2"].Name != "PC" {
		t.Fatalf("Peers() names: %+v", byID)
	}
	if h := byID["cccccccc3"].Machine().Info.Hostname; h != "Lab (cccccc)" {
		t.Fatalf("Machine hostname = %q", h)
	}
	if h := byID["bbbbbbbb2"].Machine().Info.Hostname; h != "PC" {
		t.Fatalf("Machine hostname = %q", h)
	}
	if got := r.nameLocked("dddddddd4"); got != "Lab (dddddd)" { // what ViaName uses
		t.Fatalf("via name = %q", got)
	}
}

func TestAccountMachinesOff(t *testing.T) {
	t.Setenv("GH_RUNNERS_ACCOUNT_MACHINES", "off")
	g := newFakeGH(t)
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()
	p := &Peers{Dir: t.TempDir(), Client: g.client, Name: "x", Addr: addr}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	if ents, _ := os.ReadDir(p.Dir); len(ents) != 0 {
		t.Fatalf("files written: %v", ents)
	}
	if on, _ := p.Running(); on || p.StartErr() != nil || !p.Off() {
		t.Fatal("Running/StartErr/Off wrong")
	}
	if err := p.Remove(context.Background(), "x"); err == nil {
		t.Fatal("Remove must fail safely")
	}
	if len(p.Peers()) != 0 {
		t.Fatal("peers listed")
	}
	if ln, err := net.Listen("tcp", addr); err != nil {
		t.Fatalf("something listens on %s: %v", addr, err)
	} else {
		_ = ln.Close()
	}
	time.Sleep(200 * time.Millisecond)
	if g.puts != 0 || g.gets() != 0 {
		t.Fatalf("GitHub used: %d puts, %d gets", g.puts, g.gets())
	}
}

func TestAccountMachinesOtherValueRuns(t *testing.T) {
	t.Setenv("GH_RUNNERS_ACCOUNT_MACHINES", "on")
	g := newFakeGH(t)
	pt := newPeer(t, g, "one")
	if on, port := pt.Running(); !on || port == 0 || pt.Off() {
		t.Fatal("account machines did not start")
	}
}
