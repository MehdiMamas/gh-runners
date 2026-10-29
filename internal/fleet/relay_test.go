package fleet

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/MehdiMamas/gh-runners/internal/localci"
)

// fakeRelay speaks the relay's protocol in process: pairs one dial with one
// accept socket per (min, max) pair, sends "paired" to both, then passes
// binary messages on and closes both when one closes.
type fakeRelay struct {
	srv *httptest.Server
	mu  sync.Mutex
	// waiting holds the first socket of a pair until the partner comes.
	waiting map[string]*relaySock
	paired  int
	tokens  []string
}

type relaySock struct {
	c    *websocket.Conn
	role string
	done chan struct{}
	pair chan *relaySock
}

func newFakeRelay(t *testing.T) *fakeRelay {
	t.Helper()
	f := &fakeRelay{waiting: map[string]*relaySock{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.link))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRelay) pairs() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paired
}

func (f *fakeRelay) link(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	role, self, peer := q.Get("role"), q.Get("self"), q.Get("peer")
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if r.URL.Path != "/api/link" || (role != "dial" && role != "accept") || !idRe.MatchString(self) || !idRe.MatchString(peer) {
		http.Error(w, "bad request", 400)
		return
	}
	if tok == "" {
		http.Error(w, "unauthorized", 401)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(1 << 20)
	key := "octo|" + min(self, peer) + "|" + max(self, peer)
	me := &relaySock{c: c, role: role, done: make(chan struct{}), pair: make(chan *relaySock, 1)}
	f.mu.Lock()
	f.tokens = append(f.tokens, tok)
	other := f.waiting[key]
	if other != nil && other.role != role {
		delete(f.waiting, key)
		f.paired++
		f.mu.Unlock()
		other.pair <- me
		f.pump(me, other)
		return
	}
	f.waiting[key] = me // a second socket of the same role replaces the older one
	f.mu.Unlock()
	select {
	case p := <-me.pair:
		_ = p
		<-me.done
	case <-time.After(5 * time.Second):
		f.mu.Lock()
		if f.waiting[key] == me {
			delete(f.waiting, key)
		}
		f.mu.Unlock()
		_ = c.Close(4408, "no partner")
	}
}

func (f *fakeRelay) pump(a, b *relaySock) {
	ctx := context.Background()
	_ = a.c.Write(ctx, websocket.MessageText, []byte("paired"))
	_ = b.c.Write(ctx, websocket.MessageText, []byte("paired"))
	var wg sync.WaitGroup
	copyTo := func(from, to *websocket.Conn) {
		defer wg.Done()
		for {
			typ, msg, err := from.Read(ctx)
			if err != nil {
				_ = from.CloseNow()
				_ = to.CloseNow()
				return
			}
			if to.Write(ctx, typ, msg) != nil {
				_ = from.CloseNow()
				return
			}
		}
	}
	wg.Add(2)
	go copyTo(a.c, b.c)
	go copyTo(b.c, a.c)
	wg.Wait()
	close(b.done)
}

// memWants is an in-memory wake store shared by the test's machines.
type memWants struct {
	mu   sync.Mutex
	data map[string]map[string]time.Time // target → dialer → until
	puts int
}

func (m *memWants) Read(_ context.Context, self string) (map[string]time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]time.Time{}
	for id, t := range m.data[self] {
		if t.After(time.Now()) {
			out[id] = t
		}
	}
	return out, nil
}

func (m *memWants) Put(_ context.Context, target, dialer string, until time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		m.data = map[string]map[string]time.Time{}
	}
	if m.data[target] == nil {
		m.data[target] = map[string]time.Time{}
	}
	m.data[target][dialer] = until
	m.puts++
	return nil
}

// relayPair is two peers whose direct addresses are unreachable (they publish
// none), joined through the fake relay and a fake wake store.
func relayPair(t *testing.T, idle, want time.Duration, extra ...func(*Peers)) (*fakeRelay, *memWants, *peerT, *peerT) {
	t.Helper()
	rl, ws := newFakeRelay(t), &memWants{}
	g := newFakeGH(t)
	set := func(p *Peers) {
		p.Addrs = func() []string { return nil }
		p.RelayURL, p.wants = rl.srv.URL, ws
		p.relayT = relayTimes{poll: 50 * time.Millisecond, want: want, idle: idle, wait: 10 * time.Second}
		for _, e := range extra {
			e(p)
		}
	}
	a, b := newPeerWith(t, g, "alpha", set), newPeerWith(t, g, "beta", func(p *Peers) {
		set(p)
		if len(extra) > 0 {
			withCI(t)(p)
		}
	})
	waitFor(t, func() bool { return len(a.Peers.Peers()) == 1 && len(b.Peers.Peers()) == 1 })
	return rl, ws, a, b
}

func relayLinked(p *Peers, id string) bool {
	for _, x := range p.Peers() {
		if x.ID == id && x.Online && x.Relay && x.HasInfo {
			return true
		}
	}
	return false
}

func TestRelayLinksWhenDirectIsUnreachable(t *testing.T) {
	rl, ws, a, b := relayPair(t, time.Hour, time.Minute)
	// Nothing wants a link yet: no relay traffic.
	time.Sleep(300 * time.Millisecond)
	if rl.pairs() != 0 || ws.puts != 0 || a.Linked(b.ID()) {
		t.Fatalf("linked without wanting: pairs %d puts %d", rl.pairs(), ws.puts)
	}
	if ps := a.Peers.Peers(); len(ps) != 1 || ps[0].Online || ps[0].Error == "" {
		t.Fatalf("unreachable text lost: %+v", ps)
	}
	// The Machines tab asks for all links.
	a.WantAll()
	waitFor(t, func() bool { return relayLinked(a.Peers, b.ID()) && relayLinked(b.Peers, a.ID()) })
	if rl.pairs() != 1 || ws.puts == 0 {
		t.Fatalf("pairs %d puts %d", rl.pairs(), ws.puts)
	}
	if rl.tokens[0] != "tok" {
		t.Fatalf("token sent to the relay: %q", rl.tokens[0])
	}
}

func TestRelayPauseCallWaitsForTheLink(t *testing.T) {
	_, _, a, b := relayPair(t, time.Hour, time.Minute)
	// No link yet: the call itself wakes the other side and waits.
	status, body, err := a.Call(context.Background(), b.ID(), "POST", "/api/pause", nil)
	if err != nil || status != 200 || !strings.Contains(string(body), `"machine":"beta"`) {
		t.Fatalf("pause over the relay: %d %s %v", status, body, err)
	}
	status, body, err = b.Call(context.Background(), a.ID(), "POST", "/api/resume", nil)
	if err != nil || status != 200 || !strings.Contains(string(body), `"machine":"alpha"`) {
		t.Fatalf("resume back: %d %s %v", status, body, err)
	}
}

func TestRelayRunsARemoteCIJob(t *testing.T) {
	_, _, a, b := relayPair(t, time.Hour, time.Minute, func(*Peers) {})
	waitFor(t, func() bool { return b.run != nil })
	s := spec(t, "cat data.txt && echo repo=$GITHUB_REPOSITORY")
	// A has not seen B's CI info yet (no link): the job waits for the relay link.
	r := PeerExecutor{Peers: a.Peers, ID: b.ID()}.RunJob(context.Background(), s)
	if r.Status != localci.Pass {
		t.Fatalf("status %v (%s)", r.Status, r.Reason)
	}
	if log, err := os.ReadFile(s.Log); err != nil || !strings.Contains(string(log), "hello from the repo") {
		t.Fatalf("log = %q, %v", log, err)
	}
}

func TestRelayLinkClosesWhenIdle(t *testing.T) {
	_, _, a, b := relayPair(t, 300*time.Millisecond, 300*time.Millisecond)
	a.WantAll()
	waitFor(t, func() bool { return relayLinked(a.Peers, b.ID()) && relayLinked(b.Peers, a.ID()) })
	// Nothing is wanted any more and nothing is sent: both ends drop it.
	waitFor(t, func() bool { return a.Peers.link(b.ID()) == nil && b.Peers.link(a.ID()) == nil })
}

func TestRelayAcceptorKeepsAWantedLink(t *testing.T) {
	_, _, a, b := relayPair(t, 300*time.Millisecond, time.Hour)
	a.WantAll()
	waitFor(t, func() bool { return relayLinked(a.Peers, b.ID()) && relayLinked(b.Peers, a.ID()) })
	// Only a wants the link and nothing is sent: b must not drop it while
	// a's wake entry is live (it used to, every idle period, and a then
	// linked again).
	l := b.Peers.link(a.ID())
	time.Sleep(time.Second)
	if got := b.Peers.link(a.ID()); got != l {
		t.Fatal("idle link dropped while the other side still wants it")
	}
}

func TestRelayBusyLinkStaysOpen(t *testing.T) {
	_, _, a, b := relayPair(t, 300*time.Millisecond, 300*time.Millisecond)
	a.WantAll()
	waitFor(t, func() bool { return relayLinked(a.Peers, b.ID()) })
	// Traffic keeps it alive past the idle time.
	end := time.Now().Add(900 * time.Millisecond)
	for time.Now().Before(end) {
		if _, _, err := a.Call(context.Background(), b.ID(), "GET", "/api/state", nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRelayOffByDefault(t *testing.T) {
	g := newFakeGH(t)
	a := newPeer(t, g, "alpha")
	a.WantAll() // no relay: nothing happens, no panic
	if a.run.rl != nil {
		t.Fatal("relay on without a URL")
	}
	t.Setenv("GH_RUNNERS_RELAY_URL", "")
	if RelayURLFromEnv() != "" {
		t.Fatal("empty env must turn the relay off")
	}
	os.Unsetenv("GH_RUNNERS_RELAY_URL")
	if RelayURLFromEnv() != DefaultRelayURL {
		t.Fatal("default relay URL")
	}
}

func TestRelayDialErrors(t *testing.T) {
	rl := newFakeRelay(t)
	ctx := context.Background()
	if _, err := relayDial(ctx, rl.srv.URL, "", "dial", strings.Repeat("a", 16), strings.Repeat("b", 16)); err == nil {
		t.Fatal("an empty token must be refused")
	}
	if _, err := relayDial(ctx, "ftp://x", "t", "dial", "a", "b"); err == nil {
		t.Fatal("bad scheme")
	}
}

func TestWakeFileRoundTrip(t *testing.T) {
	g := newFakeGH(t)
	a := newPeer(t, g, "alpha")
	w := ghWants{a.run.dir}
	ctx := context.Background()
	dialer, other := strings.Repeat("c", 16), strings.Repeat("d", 16)
	if m, err := w.Read(ctx, a.ID()); err != nil || len(m) != 0 {
		t.Fatalf("empty read: %v %v", m, err)
	}
	if err := w.Put(ctx, a.ID(), dialer, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := w.Put(ctx, a.ID(), other, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := w.Put(ctx, a.ID(), dialer, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	m, err := w.Read(ctx, a.ID())
	if err != nil || len(m) != 1 || m[dialer].IsZero() {
		t.Fatalf("read back: %v %v", m, err)
	}
	if !g.has("wants/" + a.ID() + ".json") {
		t.Fatal("wake file not in the machines repository")
	}
}

// To a PC on another network the direct dial hangs until its timeout, so the
// lower id is dialing for most of each round. Woken by the higher id, it must
// still accept through the relay (#94: it never did, so no link came).
func TestRelayAcceptsWhileDirectDialHangs(t *testing.T) {
	hang := func(p *Peers) {
		p.directDial = func(ctx context.Context, _ Entry) error {
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
			}
			return errors.New("no answer")
		}
	}
	_, _, a, b := relayPair(t, time.Hour, time.Minute, hang)
	hi, lo := a, b
	if a.ID() < b.ID() {
		hi, lo = b, a
	}
	hi.WantAll()
	waitFor(t, func() bool { return relayLinked(hi.Peers, lo.ID()) && relayLinked(lo.Peers, hi.ID()) })
}
