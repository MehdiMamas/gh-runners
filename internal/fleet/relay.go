package fleet

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/MehdiMamas/gh-runners/internal/github"
)

// The relay links two machines of the account that cannot reach each other
// directly (see AGENTS.md, "Account machines through the relay"). A machine
// that wants a link writes a wake entry into wants/<target id>.json of the
// account's machines repository; the target polls its own file and connects
// to the relay too. The relay pairs the two WebSockets and passes bytes on;
// the mutual TLS of the direct link runs inside, so it never sees plaintext.

// DefaultRelayURL is the deployed relay. GH_RUNNERS_RELAY_URL overrides it,
// and an empty value turns the relay off.
const DefaultRelayURL = "https://gh-runners-relay.vercel.app"

// RelayURLFromEnv is the relay to use: GH_RUNNERS_RELAY_URL when set (an
// empty value means no relay), else DefaultRelayURL.
func RelayURLFromEnv() string {
	if v, ok := os.LookupEnv("GH_RUNNERS_RELAY_URL"); ok {
		return strings.TrimSpace(v)
	}
	return DefaultRelayURL
}

// Defaults of the relay timings.
const (
	relayPollEvery = 10 * time.Second // wants/<self>.json
	relayWantFor   = 2 * time.Minute  // how long a wish (and its wake entry) lives
	relayIdleAfter = 2 * time.Minute  // a relay link unused this long and not wanted is closed
	relayWaitFor   = 25 * time.Second // a Call or CI job waits this long for the link
	relayPairWait  = 45 * time.Second // dial plus the relay's own 30 s wait for the partner
)

// relayTimes are the timings of one Peers; zero means the defaults.
type relayTimes struct{ poll, want, idle, wait time.Duration }

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// errNoPartner is a normal failed attempt (close code 4408 "no partner" or
// 4409 "replaced"): the other end did not arrive on the same relay instance.
// The next round tries again, without a log line.
var errNoPartner = errors.New("the other PC did not reach the relay in time")

// wsConn marks a connection that runs through the relay.
type wsConn struct{ net.Conn }

// isRelayConn reports whether nc (possibly inside TLS) runs through the relay.
func isRelayConn(nc net.Conn) bool {
	if tc, ok := nc.(*tls.Conn); ok {
		nc = tc.NetConn()
	}
	_, ok := nc.(wsConn)
	return ok
}

// relayDial connects to the relay as role ("dial" or "accept") for the pair
// self/peer and returns once the relay says the other side is there.
func relayDial(ctx context.Context, base, token, role, self, peer string) (net.Conn, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("relay address: %w", err)
	}
	switch u.Scheme {
	case "https", "wss":
		u.Scheme = "wss"
	case "http", "ws":
		u.Scheme = "ws"
	default:
		return nil, fmt.Errorf("relay address %q is not a web address", base)
	}
	u.Path = "/api/link"
	u.RawQuery = url.Values{"role": {role}, "self": {self}, "peer": {peer}}.Encode()
	dctx, cancel := context.WithTimeout(ctx, relayPairWait)
	defer cancel()
	c, _, err := websocket.Dial(dctx, u.String(), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(1 << 20)
	typ, msg, err := c.Read(dctx)
	if err != nil {
		_ = c.CloseNow()
		if cs := websocket.CloseStatus(err); cs == 4408 || cs == 4409 {
			return nil, errNoPartner
		}
		return nil, err
	}
	if typ != websocket.MessageText || string(msg) != "paired" {
		_ = c.CloseNow()
		return nil, errors.New("the relay sent something unexpected")
	}
	return wsConn{websocket.NetConn(ctx, c, websocket.MessageBinary)}, nil
}

// wantsStore is the wake files of the machines repository.
type wantsStore interface {
	// Read returns the live entries of wants/<self>.json: dialer id → until.
	Read(ctx context.Context, self string) (map[string]time.Time, error)
	// Put sets the dialer's entry in wants/<target>.json and drops the
	// expired ones.
	Put(ctx context.Context, target, dialer string, until time.Time) error
}

func wantsPath(id string) string { return "wants/" + id + ".json" }

type wantsFile struct {
	From map[string]string `json:"from"`
}

func parseWants(raw []byte, now time.Time) map[string]time.Time {
	out := map[string]time.Time{}
	var f wantsFile
	if json.Unmarshal(raw, &f) != nil {
		return out
	}
	for id, s := range f.From {
		if t, err := time.Parse(time.RFC3339, s); err == nil && t.After(now) && idRe.MatchString(id) {
			out[id] = t
		}
	}
	return out
}

// ghWants keeps the wake files in the directory's repository. A read of an
// unchanged file is a 304 (the client sends If-None-Match), which GitHub does
// not count against the rate limit.
type ghWants struct{ d *Directory }

func (g ghWants) Read(ctx context.Context, self string) (map[string]time.Time, error) {
	c, owner, err := g.d.ready(ctx)
	if err != nil {
		return nil, err
	}
	cont, err := c.GetContents(ctx, owner, DirectoryRepo, wantsPath(self))
	if github.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	raw, err := cont.Data()
	if err != nil {
		return nil, err
	}
	return parseWants(raw, time.Now()), nil
}

func (g ghWants) Put(ctx context.Context, target, dialer string, until time.Time) error {
	c, owner, err := g.d.ready(ctx)
	if err != nil {
		return err
	}
	var last error
	for range 5 {
		sha, cur := "", map[string]time.Time{}
		cont, err := c.GetContents(ctx, owner, DirectoryRepo, wantsPath(target))
		switch {
		case err == nil:
			sha = cont.SHA
			if raw, err := cont.Data(); err == nil {
				cur = parseWants(raw, time.Now())
			}
		case !github.IsNotFound(err):
			return err
		}
		cur[dialer] = until
		f := wantsFile{From: map[string]string{}}
		for id, t := range cur {
			f.From[id] = t.UTC().Format(time.RFC3339)
		}
		b, _ := json.Marshal(f)
		_, last = c.PutContents(ctx, owner, DirectoryRepo, wantsPath(target), "gh-runners: wake "+target, b, sha)
		var ae *github.APIError
		if last == nil || !errors.As(last, &ae) || (ae.Status != http.StatusConflict && ae.Status != http.StatusUnprocessableEntity) {
			return last
		}
	}
	return last
}

// relayState is what one running Peers keeps for the relay.
type relayState struct {
	url   string
	store wantsStore
	t     relayTimes

	mu        sync.Mutex
	wantUntil map[string]time.Time // links this machine wants
	wrote     map[string]time.Time // when it last wrote a wake entry for them
	seen      map[string]time.Time // live wake entries other machines wrote for this one
	busy      map[string]bool      // a relay connection to this machine is being made
}

func newRelayState(p *Peers, dir *Directory) *relayState {
	if p.RelayURL == "" {
		return nil
	}
	s := &relayState{url: p.RelayURL, store: p.wants, t: p.relayT,
		wantUntil: map[string]time.Time{}, wrote: map[string]time.Time{}, seen: map[string]time.Time{}, busy: map[string]bool{}}
	if s.store == nil {
		s.store = ghWants{dir}
	}
	return s
}

func (s *relayState) wanted(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Now().Before(s.wantUntil[id])
}

func (s *relayState) want(id string) {
	s.mu.Lock()
	s.wantUntil[id] = time.Now().Add(orDefault(s.t.want, relayWantFor))
	s.mu.Unlock()
}

// claim marks a relay connection to id as being made; false when one is.
func (s *relayState) claim(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[id] {
		return false
	}
	s.busy[id] = true
	return true
}

func (s *relayState) release(id string) {
	s.mu.Lock()
	delete(s.busy, id)
	s.mu.Unlock()
}

func (r *peerRun) relayToken() (string, error) {
	c, err := r.p.Client()
	if err != nil {
		return "", err
	}
	return c.Token, nil
}

// WantAll says that links to every other machine are wanted for a while (the
// Machines tab does this on each request).
func (p *Peers) WantAll() {
	p.mu.Lock()
	r := p.run
	p.mu.Unlock()
	if r == nil || r.rl == nil {
		return
	}
	r.mu.Lock()
	ids := make([]string, 0, len(r.entries))
	for id := range r.entries {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, id := range ids {
		r.rl.want(id)
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// awaitRelay is for a Call or CI job to a machine with no link: it says the
// link is wanted and waits for it. It returns nil when the relay is off, the
// machine is unknown, or no link came in time.
func (r *peerRun) awaitRelay(ctx context.Context, id string) *link {
	if r.rl == nil {
		return nil
	}
	r.mu.Lock()
	_, known := r.entries[id]
	r.mu.Unlock()
	if !known {
		return nil
	}
	r.rl.want(id)
	select {
	case r.wake <- struct{}{}:
	default:
	}
	deadline := time.NewTimer(orDefault(r.rl.t.wait, relayWaitFor))
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if l := r.p.link(id); l != nil {
			return l
		}
		select {
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			return nil
		case <-tick.C:
		}
	}
}

// connect opens a connection to e: directly, and through the relay when that
// fails and a link is wanted. The direct error is the one reported when the
// relay does not help either.
func (r *peerRun) connect(ctx context.Context, e Entry) (*conn, error) {
	var err error
	if len(e.Addrs) == 0 {
		err = errors.New("it published no address")
	} else {
		var c *conn
		if c, _, err = dialWith(ctx, e.Addrs, e.Port, r.clientConfig(e.Fingerprint)); err == nil {
			return c, nil
		}
	}
	if r.rl == nil || !r.rl.wanted(e.ID) {
		return nil, err
	}
	c, rerr := r.relayConnect(ctx, e)
	if rerr != nil {
		if !errors.Is(rerr, errNoPartner) {
			r.p.logf("account machines: %s: relay: %s", e.Name, plainErr(rerr))
		}
		return nil, err
	}
	return c, nil
}

// relayConnect wakes e through its wants file and links to it as the dialing
// side, with the same TLS as a direct link.
func (r *peerRun) relayConnect(ctx context.Context, e Entry) (*conn, error) {
	s := r.rl
	s.mu.Lock()
	// Both want each other: the lower id dials, the higher one accepts.
	theyDial := r.selfID > e.ID && time.Now().Before(s.seen[e.ID])
	needWrite := time.Since(s.wrote[e.ID]) > s.t.wantOr()/2
	s.mu.Unlock()
	if theyDial {
		return nil, errors.New("waiting for it to connect through the relay")
	}
	if !s.claim(e.ID) {
		return nil, errors.New("a relay connection is being made")
	}
	defer s.release(e.ID)
	tok, err := r.relayToken()
	if err != nil {
		return nil, err
	}
	if needWrite {
		if err := s.store.Put(ctx, e.ID, r.selfID, time.Now().Add(s.t.wantOr())); err != nil {
			return nil, fmt.Errorf("wake entry: %w", err)
		}
		s.mu.Lock()
		s.wrote[e.ID] = time.Now()
		s.mu.Unlock()
	}
	nc, err := relayDial(ctx, s.url, tok, "dial", r.selfID, e.ID)
	if err != nil {
		return nil, err
	}
	tc := tls.Client(nc, r.clientConfig(e.Fingerprint))
	_ = tc.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := tc.Handshake(); err != nil {
		_ = tc.Close()
		return nil, err
	}
	_ = tc.SetDeadline(time.Time{})
	return newConn(tc), nil
}

func (t relayTimes) wantOr() time.Duration { return orDefault(t.want, relayWantFor) }

// wantsLoop polls this machine's wake file and connects as the accepting
// side to every machine that wants a link, and closes idle relay links.
func (r *peerRun) wantsLoop(ctx context.Context) {
	s := r.rl
	t := time.NewTicker(orDefault(s.t.poll, relayPollEvery))
	defer t.Stop()
	for {
		r.pollWants(ctx)
		r.refreshWakes(ctx)
		r.closeIdle()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (r *peerRun) pollWants(ctx context.Context) {
	s := r.rl
	from, err := s.store.Read(ctx, r.selfID)
	if err != nil {
		if ctx.Err() == nil {
			r.p.logf("account machines: wake file: %s", plainErr(err))
		}
		return
	}
	s.mu.Lock()
	s.seen = from
	s.mu.Unlock()
	for id := range from {
		r.mu.Lock()
		e, known := r.entries[id]
		linked := r.links[id] != nil
		// Both want each other: the lower id dials, so it does not accept.
		dialing := r.selfID < id && r.stateLocked(id).dialing
		r.mu.Unlock()
		if !known || linked || dialing || !s.claim(id) {
			continue
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			defer s.release(id)
			r.relayAccept(ctx, e)
		}()
	}
}

func (r *peerRun) relayAccept(ctx context.Context, e Entry) {
	tok, err := r.relayToken()
	if err == nil {
		var nc net.Conn
		if nc, err = relayDial(ctx, r.rl.url, tok, "accept", r.selfID, e.ID); err == nil {
			r.incoming(ctx, tls.Server(nc, r.serverConfig()))
			return
		}
	}
	if ctx.Err() == nil && !errors.Is(err, errNoPartner) {
		r.p.logf("account machines: %s: relay: %s", e.Name, plainErr(err))
	}
}

// closeIdle closes relay links that carried nothing for the idle time while
// nothing wants them.
func (r *peerRun) closeIdle() {
	idle := orDefault(r.rl.t.idle, relayIdleAfter)
	r.mu.Lock()
	var stale []*link
	for id, l := range r.links {
		if l.relay && !r.rl.wanted(id) && l.c.idleFor() > idle {
			stale = append(stale, l)
		}
	}
	r.mu.Unlock()
	for _, l := range stale {
		l.c.close(errors.New("closed: nothing used the relay link"))
	}
}

// refreshWakes renews the wake entry of every relay link this machine dialed
// and still wants, so the other side reconnects if the link drops.
func (r *peerRun) refreshWakes(ctx context.Context) {
	s := r.rl
	r.mu.Lock()
	var ids []string
	for id, l := range r.links {
		if l.relay && l.dialer == r.selfID {
			ids = append(ids, id)
		}
	}
	r.mu.Unlock()
	for _, id := range ids {
		s.mu.Lock()
		due := time.Now().Before(s.wantUntil[id]) && time.Since(s.wrote[id]) > s.t.wantOr()/2
		s.mu.Unlock()
		if !due {
			continue
		}
		if err := s.store.Put(ctx, id, r.selfID, time.Now().Add(s.t.wantOr())); err == nil {
			s.mu.Lock()
			s.wrote[id] = time.Now()
			s.mu.Unlock()
		}
	}
}
