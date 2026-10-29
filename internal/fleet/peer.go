package fleet

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/localci"
)

// Account machines: the PCs signed in to the same GitHub account find each
// other through the Directory (a private repository of that account) and
// connect to each other directly. Every machine listens on the fleet port
// with its own self-signed certificate and uses mutual TLS: each side only
// accepts a certificate whose SHA-256 fingerprint the directory lists, so
// there is no CA and no broker. One connection serves a pair: the machine
// with the lower id dials, the other one dials too after two quiet rounds.
// Both sides send dashboard API calls down it ("api"), and each checks the
// allowlist (PeerAllowed) and its allow_control setting on receiving.

// Peers is one machine's side of the account fleet.
type Peers struct {
	// Dir holds the machine id and the peer certificate.
	Dir string
	// Client returns the GitHub client of the signed-in user (never a
	// brokered one).
	Client func() (*github.Client, error)
	// Name and Version describe this machine in the directory; an empty
	// name means the hostname.
	Name, Version string
	// Port is the port to listen on; nil means PeerPort. Addr, when set,
	// is the listen address instead (tests use "127.0.0.1:0").
	Port func() int
	Addr string
	// Addrs lists the addresses put in the directory; nil means LocalAddrs.
	Addrs func() []string
	// Info describes this machine's runners for the others.
	Info func() Info
	// API is the dashboard other machines call into.
	API http.Handler
	// AllowControl says whether other machines may call the API here; nil
	// means yes.
	AllowControl func() bool
	Log          func(format string, args ...any)
	// CIDir, when set, lets the account's other PCs send this one CI jobs
	// and is where they work. CITools and CIEmulatorOK say what it can run.
	// CIFetch downloads a job's source; nil means this machine's own GitHub
	// client (it is the same account, so it has the same access).
	CIDir        string
	CITools      func() []string
	CIEmulatorOK func() bool
	CIFetch      func(ctx context.Context, repo, sha, dest string) error

	// Timings; zero means the defaults.
	PublishEvery, ListEvery, DialEvery time.Duration

	// RelayURL is the relay that links machines which cannot reach each
	// other directly; empty means no relay (the daemon passes
	// RelayURLFromEnv()). wants and relayT are for tests.
	RelayURL string
	wants    wantsStore
	relayT   relayTimes

	mu       sync.Mutex
	run      *peerRun
	startErr error
}

// peerRun is one started Peers: its listener, directory and connections.
type peerRun struct {
	p      *Peers
	cancel context.CancelFunc
	dir    *Directory
	ln     net.Listener
	port   int
	cert   tls.Certificate
	fp     string
	selfID string
	wg     sync.WaitGroup
	wake   chan struct{}
	rl     *relayState // nil without a relay
	ci     *ciRunner   // jobs other machines send here
	ciLogs ciLogSet    // logs of the jobs this machine sent out

	mu      sync.Mutex
	entries map[string]Entry
	byFP    map[string]string
	links   map[string]*link
	state   map[string]*peerState
	lastRef time.Time
}

type link struct {
	id     string
	c      *conn
	dialer string
	since  time.Time
	relay  bool // runs through the relay
}

type peerState struct {
	err      string
	lastSeen time.Time
	info     Info
	hasInfo  bool
	misses   int
	dialing  bool
}

// Peer is another machine of the account, as this one sees it.
type Peer struct {
	Entry
	Online   bool
	Since    time.Time
	LastSeen time.Time
	Info     Info
	HasInfo  bool
	// Error says in plain words why it is not connected.
	Error string
	// Via is the id of the machine that passes calls on to this one, and
	// ViaName its name, when there is no direct link but a linked machine
	// has one. Such a machine counts as online.
	Via, ViaName string
	// Relay says the link runs through the relay.
	Relay bool
}

// ErrPeerNotAllowed answers a call the allowlist does not cover.
var errNotAllowed = "not allowed from another PC"

func (p *Peers) logf(format string, args ...any) {
	if p.Log != nil {
		p.Log(format, args...)
	}
}

func (p *Peers) allowControl() bool { return p.AllowControl == nil || p.AllowControl() }

// Running reports whether the peers are started, and the port.
func (p *Peers) Running() (bool, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.run == nil {
		return false, 0
	}
	return true, p.run.port
}

// StartErr is why the last Start failed, if it did.
func (p *Peers) StartErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.startErr
}

// ID is this machine's id in the directory.
func (p *Peers) ID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.run == nil {
		return ""
	}
	return p.run.selfID
}

// Start listens, publishes this machine and connects to the others. It does
// nothing when already started.
func (p *Peers) Start(parent context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.run != nil {
		return nil
	}
	r, err := p.start(parent)
	p.startErr = err
	if err != nil {
		return err
	}
	p.run = r
	return nil
}

func (p *Peers) start(parent context.Context) (*peerRun, error) {
	certPath, keyPath := filepath.Join(p.Dir, "peer-cert.pem"), filepath.Join(p.Dir, "peer-key.pem")
	c, err := tls.LoadX509KeyPair(certPath, keyPath)
	if errors.Is(err, os.ErrNotExist) {
		eku := []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
		if err = makeCertFor(certPath, keyPath, "gh-runners account machine", eku); err == nil {
			c, err = tls.LoadX509KeyPair(certPath, keyPath)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("peer certificate: %w", err)
	}
	dir := &Directory{Dir: p.Dir, Client: p.Client, Log: p.Log, PublishEvery: p.PublishEvery, ListEvery: p.ListEvery}
	selfID, err := dir.ID()
	if err != nil {
		return nil, err
	}
	addr := p.Addr
	if addr == "" {
		port := PeerPort
		if p.Port != nil {
			port = p.Port()
		}
		addr = ":" + strconv.Itoa(port)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil && p.Addr == "" {
		// Taken (often by another program): any free port does, the
		// directory tells the others which one.
		ln, err = net.Listen("tcp", ":0")
	}
	if err != nil {
		return nil, fmt.Errorf("cannot listen on %s for the account's other PCs: %w", addr, err)
	}
	ctx, cancel := context.WithCancel(parent)
	r := &peerRun{p: p, cancel: cancel, dir: dir, ln: ln, port: ln.Addr().(*net.TCPAddr).Port, cert: c,
		fp: fingerprint(c.Certificate[0]), selfID: selfID, wake: make(chan struct{}, 1),
		entries: map[string]Entry{}, byFP: map[string]string{}, links: map[string]*link{}, state: map[string]*peerState{}}
	dir.Self = r.selfEntry
	dir.OnList = r.onList
	r.ci = &ciRunner{Dir: p.CIDir, Tools: p.CITools, EmulatorOK: p.CIEmulatorOK, Fetch: p.fetchSource, For: "another PC"}
	r.rl = newRelayState(p, dir)
	r.wg.Add(4)
	go func() { defer r.wg.Done(); dir.Run(ctx) }()
	go func() { defer r.wg.Done(); r.acceptLoop(ctx) }()
	go func() { defer r.wg.Done(); r.dialLoop(ctx) }()
	go func() { defer r.wg.Done(); r.statusLoop(ctx) }()
	if r.rl != nil {
		r.wg.Add(1)
		go func() { defer r.wg.Done(); r.wantsLoop(ctx) }()
	}
	p.logf("account machines: listening on port %d as %s", r.port, selfID)
	return r, nil
}

// Stop closes the listener and every connection. The directory entry stays
// (use Unpublish on sign-out).
func (p *Peers) Stop() {
	p.mu.Lock()
	r := p.run
	p.run = nil
	p.mu.Unlock()
	if r == nil {
		return
	}
	r.cancel()
	_ = r.ln.Close()
	r.mu.Lock()
	for _, l := range r.links {
		l.c.close(errors.New("stopped"))
	}
	r.mu.Unlock()
	r.wg.Wait()
}

// Unpublish removes this machine from the directory (sign-out).
func (p *Peers) Unpublish(ctx context.Context) error {
	p.mu.Lock()
	r := p.run
	p.mu.Unlock()
	if r == nil {
		return nil
	}
	return r.dir.Unpublish(ctx)
}

// Remove deletes another machine from the directory and drops its link.
func (p *Peers) Remove(ctx context.Context, id string) error {
	p.mu.Lock()
	r := p.run
	p.mu.Unlock()
	if r == nil {
		return errors.New("account machines are not running")
	}
	if id == r.selfID {
		return errors.New("this is the machine itself")
	}
	if err := r.dir.Remove(ctx, id); err != nil {
		return err
	}
	r.mu.Lock()
	delete(r.entries, id)
	r.rebuildLocked()
	r.mu.Unlock()
	return nil
}

// Peers lists the other machines of the account, each online or not.
func (p *Peers) Peers() []Peer {
	p.mu.Lock()
	r := p.run
	p.mu.Unlock()
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Peer, 0, len(r.entries))
	for id, e := range r.entries {
		pe := Peer{Entry: e}
		if st := r.state[id]; st != nil {
			pe.LastSeen, pe.Info, pe.HasInfo, pe.Error = st.lastSeen, st.info, st.hasInfo, st.err
		}
		if l := r.links[id]; l != nil {
			pe.Online, pe.Since, pe.Error, pe.Relay = true, l.since, "", l.relay
		} else if via := r.relayLocked(id); via != "" {
			pe.Online, pe.Error, pe.Via = true, "", via
			if v, ok := r.entries[via]; ok && v.Name != "" {
				pe.ViaName = v.Name
			} else {
				pe.ViaName = via
			}
		}
		out = append(out, pe)
	}
	slices.SortFunc(out, func(a, b Peer) int {
		return strings.Compare(strings.ToLower(a.Name)+a.ID, strings.ToLower(b.Name)+b.ID)
	})
	return out
}

// Call runs one dashboard API call on another machine. Only calls on the
// allowlist are sent, and the other machine checks them again.
func (p *Peers) Call(ctx context.Context, id, method, pathQuery string, body []byte) (int, []byte, error) {
	if !PeerAllowed(method, pathQuery) {
		return 0, nil, fmt.Errorf("%s %s is not allowed on another machine", method, pathQuery)
	}
	p.mu.Lock()
	r := p.run
	p.mu.Unlock()
	if r == nil {
		return 0, nil, ErrOffline
	}
	r.mu.Lock()
	l := r.links[id]
	var via *link
	if l == nil {
		via = r.links[r.relayLocked(id)]
	}
	r.mu.Unlock()
	if l == nil && via == nil {
		l = r.awaitRelay(ctx, id)
	}
	req := frame{Kind: kindAPI, Method: method, Path: pathQuery, Body: body}
	switch {
	case l != nil:
	case via != nil:
		// No direct link: a machine that has one passes the call on.
		inner := req
		l, req = via, frame{Kind: kindRelay, To: id, Inner: &inner}
	default:
		return 0, nil, ErrOffline
	}
	f, err := l.c.call(ctx, req)
	if err != nil {
		return 0, nil, err
	}
	if f.Error != "" {
		return 0, nil, errors.New(f.Error)
	}
	return f.Status, f.Body, nil
}

// relayLocked picks a machine with a live direct link that says it has a
// direct link to id, for when this one has none. It returns "" when there is
// none. The caller holds r.mu.
func (r *peerRun) relayLocked(id string) string {
	best := ""
	for cid := range r.links {
		if st := r.state[cid]; st != nil && st.hasInfo && slices.Contains(st.info.Links, id) && (best == "" || cid < best) {
			best = cid
		}
	}
	return best
}

// Linked reports whether this machine has a live direct link to id.
func (p *Peers) Linked(id string) bool {
	return p.link(id) != nil
}

// link is the live direct link to id, or nil.
func (p *Peers) link(id string) *link {
	p.mu.Lock()
	r := p.run
	p.mu.Unlock()
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if l := r.links[id]; l != nil {
		select {
		case <-l.c.done:
		default:
			return l
		}
	}
	return nil
}

// Login is the GitHub login of the account, once the directory knows it.
func (p *Peers) Login() string {
	p.mu.Lock()
	r := p.run
	p.mu.Unlock()
	if r == nil {
		return ""
	}
	return r.dir.Login()
}

// CIMachines lists the machines linked directly that take CI jobs. A machine
// reached through another one is left out: its logs would take two hops.
func (p *Peers) CIMachines() []localci.Machine {
	var out []localci.Machine
	for _, pe := range p.Peers() {
		if !pe.Online || pe.Via != "" || !pe.HasInfo || pe.Info.CI == nil {
			continue
		}
		name := pe.Info.Hostname
		if name == "" {
			name = pe.Name
		}
		out = append(out, localci.Machine{
			ID: pe.ID, Name: name, OS: pe.Info.OS, Arch: pe.Info.Arch,
			Tools: pe.Info.CI.Tools, EmulatorOK: pe.Info.CI.EmulatorOK,
		})
	}
	return out
}

// fetchSource downloads a job's source with this machine's own sign-in.
func (p *Peers) fetchSource(ctx context.Context, _ *conn, repo, sha, dest string) error {
	if p.CIFetch != nil {
		return p.CIFetch(ctx, repo, sha, dest)
	}
	c, err := p.Client()
	if err != nil {
		return err
	}
	owner, name, _ := strings.Cut(repo, "/")
	return c.DownloadTarball(ctx, owner, name, sha, dest)
}

// PeerExecutor runs jobs on another PC of the account, linked directly. It
// implements localci.Executor.
type PeerExecutor struct {
	Peers *Peers
	ID    string
}

// RunJob sends the job and waits for the result, like RemoteExecutor: the
// log is written to spec.Log as it arrives, and a link that drops mid-job
// fails the job with "machine went offline".
func (e PeerExecutor) RunJob(ctx context.Context, spec localci.JobSpec) localci.Result {
	e.Peers.mu.Lock()
	r := e.Peers.run
	e.Peers.mu.Unlock()
	if r == nil {
		return runRemote(ctx, spec, nil, &ciLogSet{})
	}
	r.mu.Lock()
	l := r.links[e.ID]
	r.mu.Unlock()
	if l == nil {
		l = r.awaitRelay(ctx, e.ID)
	}
	if l == nil {
		return runRemote(ctx, spec, nil, &r.ciLogs)
	}
	return runRemote(ctx, spec, l.c, &r.ciLogs)
}

// Has reports whether id is a machine of the account directory.
func (p *Peers) Has(id string) bool {
	p.mu.Lock()
	r := p.run
	p.mu.Unlock()
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.entries[id]
	return ok
}

func (r *peerRun) selfEntry() Entry {
	p := r.p
	name := p.Name
	if name == "" {
		name, _ = os.Hostname()
	}
	addrs := LocalAddrs
	if p.Addrs != nil {
		addrs = p.Addrs
	}
	return Entry{Name: name, OS: runtime.GOOS, Arch: runtime.GOARCH, Version: p.Version, Addrs: addrs(), Port: r.port, Fingerprint: r.fp}
}

func (r *peerRun) info() *Info {
	var i Info
	if r.p.Info != nil {
		i = r.p.Info()
	} else {
		i = Info{Hostname: r.p.Name, OS: runtime.GOOS, Arch: runtime.GOARCH, Version: r.p.Version}
	}
	if i.Runners == nil {
		i.Runners = []RunnerInfo{}
	}
	if i.CI == nil {
		i.CI = r.ci.info()
	}
	if i.CI != nil {
		i.CI.Active = localci.ActiveJobs()
	}
	if i.Login == "" {
		i.Login = r.dir.Login()
	}
	if i.MachineID == "" {
		i.MachineID = r.selfID
	}
	r.mu.Lock()
	for id := range r.links {
		i.Links = append(i.Links, id)
	}
	r.mu.Unlock()
	slices.Sort(i.Links)
	return &i
}

// onList takes a fresh directory: it is what the certificates are pinned
// against, so a machine that left it is dropped at once.
func (r *peerRun) onList(es []Entry) {
	r.mu.Lock()
	r.entries = map[string]Entry{}
	for _, e := range es {
		if e.ID != r.selfID {
			r.entries[e.ID] = e
		}
	}
	r.rebuildLocked()
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// rebuildLocked rebuilds the fingerprint index and drops links and state of
// machines that are gone or whose certificate changed.
func (r *peerRun) rebuildLocked() {
	r.byFP = map[string]string{}
	for id, e := range r.entries {
		r.byFP[e.Fingerprint] = id
	}
	for id, l := range r.links {
		if e, ok := r.entries[id]; !ok {
			l.c.close(errors.New("no longer in the account's directory"))
		} else if lfp, _ := l.c.peerFP(); lfp != e.Fingerprint {
			l.c.close(errors.New("its certificate changed in the directory"))
		}
	}
	for id := range r.state {
		if _, ok := r.entries[id]; !ok {
			delete(r.state, id)
		}
	}
}

func (r *peerRun) serverConfig() *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{r.cert},
		ClientAuth:   tls.RequireAnyClientCert,
		// Self-signed on both sides: the pin is the directory.
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("no certificate")
			}
			fp := fingerprint(raw[0])
			r.mu.Lock()
			_, ok := r.byFP[fp]
			stale := time.Since(r.lastRef) > 10*time.Second
			if !ok && stale {
				r.lastRef = time.Now()
			}
			r.mu.Unlock()
			if !ok {
				// Maybe a machine that signed in a moment ago.
				if stale {
					r.dir.Refresh()
				}
				return errors.New("certificate not in the account's directory")
			}
			return nil
		},
	}
}

func (r *peerRun) clientConfig(fp string) *tls.Config {
	cfg := pinned(fp)
	cfg.Certificates = []tls.Certificate{r.cert}
	return cfg
}

func (r *peerRun) acceptLoop(ctx context.Context) {
	cfg := r.serverConfig()
	for {
		nc, err := r.ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.incoming(ctx, tls.Server(nc, cfg))
		}()
	}
}

// incoming handles a connection from another machine: the TLS handshake has
// already pinned its certificate to a directory entry once Handshake is done.
func (r *peerRun) incoming(ctx context.Context, tc *tls.Conn) {
	_ = tc.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := tc.Handshake(); err != nil {
		_ = tc.Close()
		return
	}
	c := newConn(tc)
	fp := fingerprint(tc.ConnectionState().PeerCertificates[0].Raw)
	f, err := c.recv(handshakeTimeout)
	if err != nil || f.Type != tHello {
		c.close(errors.New("no hello"))
		return
	}
	r.mu.Lock()
	id := r.byFP[fp]
	r.mu.Unlock()
	if id == "" || id != f.MemberID {
		_ = c.send(frame{Type: tDenied, Error: "this certificate does not belong to that machine id"})
		c.close(errors.New("id mismatch"))
		return
	}
	l := &link{id: id, c: c, dialer: id, relay: isRelayConn(c.nc)}
	if !r.register(l, f.Info) {
		_ = c.send(frame{Type: tDenied, Error: "already connected"})
		c.close(errors.New("duplicate"))
		return
	}
	if err := c.send(frame{Type: tWelcome, MemberID: r.selfID, Info: r.info()}); err != nil {
		return
	}
	_ = tc.SetDeadline(time.Time{})
	r.serveLink(ctx, l)
}

// register adds a link, keeping the one the lower id dialed when two exist.
// It reports whether l was kept.
func (r *peerRun) register(l *link, info *Info) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.entries[l.id]; !ok {
		return false
	}
	preferred := func(x *link) bool { return x.dialer == min(r.selfID, x.id) }
	if old := r.links[l.id]; old != nil {
		select {
		case <-old.c.done:
		default:
			if preferred(old) && !preferred(l) {
				return false
			}
			old.c.close(errors.New("replaced by another connection"))
		}
	}
	l.since = time.Now()
	r.links[l.id] = l
	st := r.stateLocked(l.id)
	st.err, st.misses, st.lastSeen = "", 0, time.Now()
	if info != nil {
		st.info, st.hasInfo = *info, true
	}
	return true
}

func (r *peerRun) stateLocked(id string) *peerState {
	st := r.state[id]
	if st == nil {
		st = &peerState{}
		r.state[id] = st
	}
	return st
}

// serveLink runs a registered link until it fails.
func (r *peerRun) serveLink(ctx context.Context, l *link) {
	stop := context.AfterFunc(ctx, func() { l.c.close(ctx.Err()) })
	defer stop()
	r.p.logf("account machines: connected to %s", r.name(l.id))
	err := l.c.serve(func(f frame) frame { return r.handle(ctx, l, f) }, func(f frame) error {
		switch f.Type {
		case tCILog:
			r.ciLogs.write(f.Path, f.Body)
		case tCICancel:
			r.ci.cancel(f.Path)
		}
		if f.Type == tStatus && f.Info != nil {
			r.mu.Lock()
			if r.links[l.id] == l {
				st := r.stateLocked(l.id)
				st.info, st.hasInfo, st.lastSeen = *f.Info, true, time.Now()
			}
			r.mu.Unlock()
		}
		return nil
	})
	r.mu.Lock()
	if r.links[l.id] == l {
		delete(r.links, l.id)
		if _, ok := r.entries[l.id]; ok {
			st := r.stateLocked(l.id)
			st.lastSeen = time.Now()
			st.err = "the connection was lost: " + plainErr(err)
		}
	}
	r.mu.Unlock()
	r.p.logf("account machines: disconnected from %s: %v", r.name(l.id), err)
}

func (r *peerRun) name(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[id]; ok && e.Name != "" {
		return e.Name
	}
	return id
}

// handle answers a request from another machine. Both checks happen here,
// on the receiving side: the sender's own checks prove nothing.
func (r *peerRun) handle(ctx context.Context, l *link, f frame) frame {
	refuse := func(msg string) frame {
		b, _ := json.Marshal(map[string]string{"error": msg})
		return frame{Status: http.StatusForbidden, Body: b}
	}
	const noControl = "this PC does not let your other PCs control it (turn it on in Machines)"
	switch f.Kind {
	case kindCI:
		if !r.p.allowControl() {
			return ciError(http.StatusForbidden, noControl)
		}
		return r.ci.run(ctx, l.c, f)
	case kindRelay:
		if f.To != r.selfID {
			return r.relay(ctx, l, f)
		}
		// Passed on to this machine: the same checks as a direct call.
		in := f.Inner
		if in == nil || in.Kind != kindAPI || r.p.API == nil || !PeerAllowed(in.Method, in.Path) {
			return refuse(errNotAllowed)
		}
		if !r.p.allowControl() {
			return refuse(noControl)
		}
		return serveAPI(r.p.API, *in, "peer")
	}
	if f.Kind != kindAPI || r.p.API == nil || !PeerAllowed(f.Method, f.Path) {
		return refuse(errNotAllowed)
	}
	if !r.p.allowControl() {
		return refuse(noControl)
	}
	return serveAPI(r.p.API, f, "peer")
}

// relay passes an API call from the machine on l to the machine in f.To,
// which this one must have a direct link to. The id of the caller is the one
// proved on l. A relay inside a relay is refused: no chains.
func (r *peerRun) relay(ctx context.Context, l *link, f frame) frame {
	refuse := func(msg string) frame {
		b, _ := json.Marshal(map[string]string{"error": msg})
		return frame{Status: http.StatusForbidden, Body: b}
	}
	if f.Inner == nil || f.Inner.Kind != kindAPI {
		return refuse(errNotAllowed)
	}
	r.mu.Lock()
	out := r.links[f.To]
	r.mu.Unlock()
	if out == nil || f.To == l.id {
		return refuse("this PC has no direct link to that PC")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	resp, err := out.c.call(ctx, frame{Kind: kindRelay, To: f.To, From: l.id, Inner: f.Inner})
	if err != nil {
		return frame{Error: plainErr(err)}
	}
	return frame{Status: resp.Status, Body: resp.Body, Error: resp.Error}
}

func (r *peerRun) dialLoop(ctx context.Context) {
	every := r.p.DialEvery
	if every <= 0 {
		every = 10 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		r.dialRound(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.wake:
		}
	}
}

// dialRound dials every machine without a live link: the lower id at once,
// the higher one after two rounds in which the other side did not come.
func (r *peerRun) dialRound(ctx context.Context) {
	r.mu.Lock()
	var todo []Entry
	for id, e := range r.entries {
		if l := r.links[id]; l != nil {
			continue
		}
		st := r.stateLocked(id)
		if st.dialing {
			continue
		}
		if r.selfID > id && (r.rl == nil || !r.rl.wanted(id)) {
			st.misses++
			if st.misses <= 2 {
				continue
			}
		}
		st.dialing = true
		todo = append(todo, e)
	}
	r.mu.Unlock()
	for _, e := range todo {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			err := r.dialPeer(ctx, e)
			r.mu.Lock()
			st := r.stateLocked(e.ID)
			st.dialing = false
			if err != nil && ctx.Err() == nil {
				st.err = plainErr(err)
			}
			r.mu.Unlock()
			if err != nil && ctx.Err() == nil {
				r.p.logf("account machines: %s: %s", e.Name, plainErr(err))
			}
		}()
	}
}

func (r *peerRun) dialPeer(ctx context.Context, e Entry) error {
	c, err := r.connect(ctx, e)
	if err != nil {
		return err
	}
	if err := c.send(frame{Type: tHello, MemberID: r.selfID, Info: r.info()}); err != nil {
		return err
	}
	f, err := c.recv(handshakeTimeout)
	if err == nil && f.Type == tDenied {
		err = errors.New(f.Error)
	} else if err == nil && f.Type != tWelcome {
		err = fmt.Errorf("unexpected %q", f.Type)
	}
	if err != nil {
		c.close(err)
		return err
	}
	l := &link{id: e.ID, c: c, dialer: r.selfID, relay: isRelayConn(c.nc)}
	if !r.register(l, f.Info) {
		c.close(errors.New("duplicate"))
		return nil
	}
	r.serveLink(ctx, l)
	return nil
}

func (r *peerRun) statusLoop(ctx context.Context) {
	every := r.p.DialEvery
	if every <= 0 {
		every = 15 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		r.mu.Lock()
		ls := make([]*link, 0, len(r.links))
		for _, l := range r.links {
			ls = append(ls, l)
		}
		r.mu.Unlock()
		if len(ls) == 0 {
			continue
		}
		info := r.info()
		for _, l := range ls {
			_ = l.c.send(frame{Type: tStatus, Info: info})
		}
	}
}

// Machine describes a peer for the dashboard's Machines list.
func (pe Peer) Machine() Machine {
	m := Machine{ID: pe.ID, Via: pe.Via, ViaName: pe.ViaName, Online: pe.Online, Since: pe.Since, LastSeen: pe.LastSeen, Info: pe.Info,
		Peer: true, Relay: pe.Relay, Error: pe.Error, Addrs: pe.Addrs, Port: pe.Port}
	if !pe.HasInfo {
		m.Info = Info{Hostname: pe.Name, OS: pe.OS, Arch: pe.Arch, Version: pe.Version, Runners: []RunnerInfo{}}
	}
	if m.Info.Hostname == "" {
		m.Info.Hostname = pe.Name
	}
	if m.Info.Runners == nil {
		m.Info.Runners = []RunnerInfo{}
	}
	if m.LastSeen.IsZero() {
		m.LastSeen = pe.Updated
	}
	return m
}
