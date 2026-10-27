package fleet

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/localci"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// StatusEvery is how often a member sends the host its runners.
const StatusEvery = 15 * time.Second

// Member is a machine's link to its main host.
type Member struct {
	// Dir holds member.json.
	Dir string
	// Info says what this machine tells the host.
	Info func() Info
	// API runs the host's dashboard calls; Member checks the allowlist
	// before it gets them.
	API http.Handler
	// Log, when set, gets one line per connection event.
	Log func(format string, args ...any)
	// OnChange is called when the link changes (joined, left, removed).
	OnChange func()
	// CIDir, when set, turns on CI jobs from the main host and is where
	// they work. CITools and CIEmulatorOK say what this machine can run.
	CIDir        string
	CITools      func() []string
	CIEmulatorOK func() bool

	jobs   map[string]context.CancelFunc // running CI jobs by id
	mu     sync.Mutex
	c      *conn
	since  time.Time
	addr   string
	err    string
	poke   chan struct{}
	pokeMu sync.Once
}

// memberState is member.json.
type memberState struct {
	Addrs       []string  `json:"addrs"`
	Port        int       `json:"port"`
	Fingerprint string    `json:"fingerprint_sha256"`
	MemberID    string    `json:"member_id"`
	Credential  string    `json:"credential"`
	HostName    string    `json:"host_name"`
	Joined      time.Time `json:"joined"`
}

// MemberStatus is shown on the member's own dashboard.
type MemberStatus struct {
	Joined    bool      `json:"joined"`
	HostName  string    `json:"host_name,omitempty"`
	Connected bool      `json:"connected"`
	Since     time.Time `json:"since,omitzero"`
	Addr      string    `json:"addr,omitempty"`
	Error     string    `json:"error,omitempty"`
}

func statePath(dir string) string { return filepath.Join(dir, "member.json") }

func loadMember(dir string) (memberState, bool, error) {
	var s memberState
	err := store.ReadJSON(statePath(dir), &s)
	if errors.Is(err, os.ErrNotExist) {
		return s, false, nil
	}
	return s, err == nil, err
}

func (m *Member) logf(format string, args ...any) {
	if m.Log != nil {
		m.Log(format, args...)
	}
}

// Joined reports whether this machine belongs to a main host.
func (m *Member) Joined() bool {
	_, ok, _ := loadMember(m.Dir)
	return ok
}

// Status says whether this machine has a main host and is connected to it.
func (m *Member) Status() MemberStatus {
	st, ok, err := loadMember(m.Dir)
	m.mu.Lock()
	defer m.mu.Unlock()
	s := MemberStatus{Joined: ok, HostName: st.HostName, Connected: ok && m.c != nil, Since: m.since, Addr: m.addr, Error: m.err}
	if err != nil {
		s.Error = err.Error()
	}
	if !s.Connected {
		s.Since, s.Addr = time.Time{}, ""
	}
	return s
}

func (m *Member) pokeCh() chan struct{} {
	m.pokeMu.Do(func() { m.poke = make(chan struct{}, 1) })
	return m.poke
}

// Reload makes Run look at member.json again (after join or leave) and
// sends the host fresh runner info.
func (m *Member) Reload() {
	select {
	case m.pokeCh() <- struct{}{}:
	default:
	}
}

// Run keeps this machine connected to its main host until ctx ends,
// reconnecting with backoff. It idles while the machine has not joined.
func (m *Member) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		st, ok, err := loadMember(m.Dir)
		if !ok {
			if err != nil {
				m.setErr(err)
			}
			m.wait(ctx, 0)
			continue
		}
		if m.session(ctx, st) {
			backoff = time.Second
		} else {
			backoff = min(backoff*2, time.Minute)
		}
		// Up to a fifth less, so machines that lost the host together do
		// not all come back in the same second.
		m.wait(ctx, backoff-time.Duration(rand.Int64N(int64(backoff/5))))
	}
}

// wait sleeps for d (forever when 0) or until Reload or ctx ends.
func (m *Member) wait(ctx context.Context, d time.Duration) {
	var t <-chan time.Time
	if d > 0 {
		timer := time.NewTimer(d)
		defer timer.Stop()
		t = timer.C
	}
	select {
	case <-ctx.Done():
	case <-m.pokeCh():
	case <-t:
	}
}

func (m *Member) setErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err == nil {
		m.err = ""
	} else {
		m.err = err.Error()
	}
}

func (m *Member) info() *Info {
	if m.Info == nil {
		return &Info{}
	}
	i := m.Info()
	if i.CI == nil {
		i.CI = m.ciInfo()
	}
	if i.CI != nil {
		i.CI.Active = localci.ActiveJobs()
	}
	return &i
}

// session runs one connection to the host; it reports whether it got in.
func (m *Member) session(ctx context.Context, st memberState) bool {
	c, addr, err := dial(ctx, st.Addrs, st.Port, st.Fingerprint)
	if err != nil {
		m.setErr(fmt.Errorf("cannot reach the main host: %w", err))
		return false
	}
	if err := c.send(frame{Type: tHello, MemberID: st.MemberID, Credential: st.Credential, Info: m.info()}); err != nil {
		m.setErr(err)
		return false
	}
	f, err := c.recv(handshakeTimeout)
	if err == nil && f.Type == tDenied {
		err = errors.New(f.Error)
		if f.Error == ErrRevoked.Error() {
			m.removed(st)
		}
	} else if err == nil && f.Type != tWelcome {
		err = fmt.Errorf("unexpected %q from the main host", f.Type)
	}
	if err != nil {
		c.close(err)
		m.setErr(err)
		return false
	}
	// The host says where it can be reached now; keep the address that
	// worked too.
	if len(f.Addrs) > 0 || f.HostName != st.HostName {
		nst := st
		nst.Addrs = slices.Compact(append(append([]string{}, f.Addrs...), hostOf(addr)))
		if f.HostName != "" {
			nst.HostName = f.HostName
		}
		if !slices.Equal(nst.Addrs, st.Addrs) || nst.HostName != st.HostName {
			_ = store.WriteJSON(statePath(m.Dir), nst, 0o600)
		}
	}
	m.mu.Lock()
	m.c, m.since, m.addr, m.err = c, time.Now(), addr, ""
	m.mu.Unlock()
	m.logf("connected to the main host %s at %s", f.HostName, addr)
	if m.OnChange != nil {
		m.OnChange()
	}

	stop := make(chan struct{})
	go m.sendStatus(ctx, c, st, stop)
	sctx, scancel := context.WithCancel(ctx)
	defer scancel()
	err = c.serve(m.ciHandler(sctx, c), func(f frame) error {
		if f.Type == tCICancel {
			m.cancelJob(f.Path)
		}
		if f.Type == tRemoved {
			m.removed(st)
			return ErrRevoked
		}
		return nil
	})
	close(stop)
	_, still, _ := loadMember(m.Dir)
	m.mu.Lock()
	if m.c == c {
		m.c, m.addr = nil, ""
		if still && !errors.Is(err, ErrRevoked) {
			m.err = "lost the main host: " + err.Error()
		}
	}
	m.mu.Unlock()
	m.logf("disconnected from the main host: %v", err)
	if m.OnChange != nil {
		m.OnChange()
	}
	return true
}

// sendStatus tells the host about this machine's runners every
// StatusEvery and on Reload, and drops the connection if member.json
// changed (left or joined elsewhere).
func (m *Member) sendStatus(ctx context.Context, c *conn, st memberState, stop chan struct{}) {
	t := time.NewTicker(StatusEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			c.close(ctx.Err())
			return
		case <-m.pokeCh():
			now, ok, _ := loadMember(m.Dir)
			if !ok || now.MemberID != st.MemberID {
				c.close(errors.New("left the main host"))
				return
			}
		case <-t.C:
		}
		_ = c.send(frame{Type: tStatus, Info: m.info()})
	}
}

// removed forgets the host after it removed this machine.
func (m *Member) removed(st memberState) {
	m.mu.Lock()
	m.c, m.addr, m.err = nil, "", ErrRevoked.Error()
	m.mu.Unlock()
	cur, ok, _ := loadMember(m.Dir)
	if ok && cur.MemberID == st.MemberID {
		_ = os.Remove(statePath(m.Dir))
	}
	m.logf("the main host %s removed this machine", st.HostName)
	if m.OnChange != nil {
		m.OnChange()
	}
}

// handleAPI runs a dashboard call from the host through API, after
// checking the allowlist here on the member: a host can do nothing else.
func (m *Member) handleAPI(f frame) frame {
	if f.Kind != kindAPI || !MemberAllowed(f.Method, f.Path) || m.API == nil {
		b, _ := json.Marshal(map[string]string{"error": "not allowed from the main host"})
		return frame{Status: http.StatusForbidden, Body: b}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, f.Method, "http://fleet"+f.Path, bytes.NewReader(f.Body))
	if err != nil {
		return frame{Error: err.Error()}
	}
	req.RemoteAddr = "main-host"
	if len(f.Body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := &recorder{header: http.Header{}}
	m.API.ServeHTTP(rec, req)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return frame{Status: rec.status, Body: rec.body.Bytes()}
}

type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(s int) {
	if r.status == 0 {
		r.status = s
	}
}
func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.body.Len()+len(b) > maxFrame/2 {
		return 0, errors.New("response too large")
	}
	return r.body.Write(b)
}

// ErrNotConnected is a GitHub call made while the host is unreachable.
var ErrNotConnected = errors.New("not connected to the main host")

// Client is a GitHub client whose calls the main host makes with its own
// token. This machine never sees that token.
func (m *Member) Client() *github.Client {
	return &github.Client{BaseURL: "https://main-host.invalid", HTTP: &http.Client{Transport: brokered{m}, Timeout: 90 * time.Second}}
}

type brokered struct{ m *Member }

func (b brokered) RoundTrip(req *http.Request) (*http.Response, error) {
	b.m.mu.Lock()
	c := b.m.c
	b.m.mu.Unlock()
	if c == nil {
		return nil, ErrNotConnected
	}
	var body []byte
	if req.Body != nil {
		var err error
		if body, err = io.ReadAll(req.Body); err != nil {
			return nil, err
		}
		_ = req.Body.Close()
	}
	f := frame{Kind: kindGitHub, Method: req.Method, Path: req.URL.RequestURI(), Body: body}
	if v := req.Header.Get("If-None-Match"); v != "" {
		f.Header = map[string]string{"If-None-Match": v}
	}
	r, err := c.call(req.Context(), f)
	if err != nil {
		return nil, err
	}
	if r.Error != "" {
		return nil, errors.New(r.Error)
	}
	resp := &http.Response{
		Status:     strconv.Itoa(r.Status) + " " + http.StatusText(r.Status),
		StatusCode: r.Status,
		Proto:      "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header:        http.Header{},
		Body:          io.NopCloser(bytes.NewReader(r.Body)),
		ContentLength: int64(len(r.Body)),
		Request:       req,
	}
	for k, v := range r.Header {
		resp.Header.Set(k, v)
	}
	return resp, nil
}

// dial tries every address at once and keeps the first that answers with
// the pinned certificate.
func dial(ctx context.Context, addrs []string, port int, fp string) (*conn, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	type result struct {
		nc   net.Conn
		addr string
		err  error
	}
	ch := make(chan result, len(addrs))
	for _, a := range addrs {
		go func() {
			addr := net.JoinHostPort(a, strconv.Itoa(port))
			d := tls.Dialer{
				NetDialer: &net.Dialer{Timeout: 8 * time.Second},
				Config:    pinned(fp),
			}
			nc, err := d.DialContext(ctx, "tcp", addr)
			ch <- result{nc, addr, err}
		}()
	}
	var errs []error
	var got *result
	for range addrs {
		r := <-ch
		switch {
		case r.err != nil:
			errs = append(errs, r.err)
		case got == nil:
			got = &r
			cancel()
		default:
			_ = r.nc.Close()
		}
	}
	if got == nil {
		for _, e := range errs {
			if errors.Is(e, ErrFingerprint) {
				return nil, "", ErrFingerprint
			}
		}
		return nil, "", errors.Join(errs...)
	}
	return newConn(got.nc), got.addr, nil
}

// ErrFingerprint means something answered on the host's address with a
// different certificate than the join code names.
var ErrFingerprint = errors.New("the main host's certificate does not match the join code")

// pinned trusts exactly one certificate, by its SHA-256 fingerprint.
func pinned(fp string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		// The certificate is self-signed; VerifyConnection checks it.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || fingerprint(cs.PeerCertificates[0].Raw) != fp {
				return ErrFingerprint
			}
			return nil
		},
	}
}

func hostOf(addr string) string {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return h
}

// Join pairs this machine with the main host in the join code and saves
// the link in dir. It returns the host's name.
func Join(ctx context.Context, dir, code string, info Info) (string, error) {
	c, err := ParseCode(code)
	if err != nil {
		return "", err
	}
	if st, ok, _ := loadMember(dir); ok {
		return "", fmt.Errorf("this machine already belongs to the main host %s; run `gh-runners leave` first", st.HostName)
	}
	conn, _, err := dial(ctx, c.Addrs, c.Port, c.Fingerprint)
	if err != nil {
		return "", fmt.Errorf("cannot reach the main host (tried %v on port %d): %w", c.Addrs, c.Port, err)
	}
	defer conn.close(nil)
	if err := conn.send(frame{Type: tPair, Secret: c.Secret, Info: &info}); err != nil {
		return "", err
	}
	f, err := conn.recv(handshakeTimeout)
	switch {
	case err != nil:
		return "", err
	case f.Type == tDenied:
		return "", errors.New(f.Error)
	case f.Type != tPaired || f.MemberID == "" || f.Credential == "":
		return "", fmt.Errorf("unexpected %q from the main host", f.Type)
	}
	st := memberState{Addrs: c.Addrs, Port: c.Port, Fingerprint: c.Fingerprint, MemberID: f.MemberID, Credential: f.Credential, HostName: f.HostName, Joined: time.Now()}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return f.HostName, store.WriteJSON(statePath(dir), st, 0o600)
}

// Leave tells the main host to forget this machine (when it can reach it)
// and deletes the link. It reports whether the host was told.
func (m *Member) Leave(ctx context.Context) (bool, error) {
	st, ok, err := loadMember(m.Dir)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, errors.New("this machine does not belong to a main host")
	}
	// Gone first, so Run does not reconnect once the host hangs up.
	if err := os.Remove(statePath(m.Dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	told := false
	m.mu.Lock()
	c := m.c
	m.err = ""
	m.mu.Unlock()
	if c != nil {
		told = c.send(frame{Type: tLeave}) == nil
	} else if c, _, err := dial(ctx, st.Addrs, st.Port, st.Fingerprint); err == nil {
		if c.send(frame{Type: tHello, MemberID: st.MemberID, Credential: st.Credential, Info: m.info()}) == nil {
			if f, err := c.recv(handshakeTimeout); err == nil && f.Type == tWelcome {
				told = c.send(frame{Type: tLeave}) == nil
				// Give the host a moment to read it before closing.
				_, _ = c.recv(time.Second)
			}
		}
		c.close(nil)
	}
	m.Reload()
	if m.OnChange != nil {
		m.OnChange()
	}
	return told, nil
}
