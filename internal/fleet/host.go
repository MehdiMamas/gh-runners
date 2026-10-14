package fleet

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// InviteTTL is how long a join code works.
const InviteTTL = time.Hour

// Host is the main host's side: it accepts members and talks to them.
type Host struct {
	// Dir holds the certificate, its key and host.json.
	Dir string
	// Name is shown to members; empty means the hostname.
	Name string
	// Token returns the host's GitHub token for members' GitHub calls.
	Token func(context.Context) (string, error)
	// Addrs lists the addresses put in join codes; nil means LocalAddrs.
	Addrs func() []string
	// Log, when set, gets one line per member event.
	Log func(format string, args ...any)

	smu      sync.Mutex // host.json
	mu       sync.Mutex
	ln       net.Listener
	port     int
	cert     *tls.Certificate
	fp       string
	sessions map[string]*session
	conns    map[net.Conn]bool // open connections, sessions or not yet
	stop     chan struct{}
	startErr error
	wg       sync.WaitGroup // accept and every connection
}

type session struct {
	c     *conn
	since time.Time
	info  Info
}

// hostState is host.json. Only hashes of secrets are kept.
type hostState struct {
	Members []memberRec `json:"members"`
	Invites []inviteRec `json:"invites"`
}

type memberRec struct {
	ID       string    `json:"id"`
	CredHash string    `json:"credential_sha256"`
	Added    time.Time `json:"added"`
	LastSeen time.Time `json:"last_seen"`
	Info     Info      `json:"info"`
}

type inviteRec struct {
	Hash    string    `json:"secret_sha256"`
	Expires time.Time `json:"expires"`
	Used    bool      `json:"used,omitempty"`
}

// Machine is a member as the host sees it.
type Machine struct {
	ID       string    `json:"id"`
	Online   bool      `json:"online"`
	Since    time.Time `json:"since,omitzero"`
	Added    time.Time `json:"added"`
	LastSeen time.Time `json:"last_seen,omitzero"`
	Info     Info      `json:"info"`
}

func (h *Host) logf(format string, args ...any) {
	if h.Log != nil {
		h.Log(format, args...)
	}
}

func (h *Host) statePath() string { return filepath.Join(h.Dir, "host.json") }

func (h *Host) load() (hostState, error) {
	var s hostState
	err := store.ReadJSON(h.statePath(), &s)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return s, err
}

func (h *Host) save(s hostState) error { return store.WriteJSON(h.statePath(), s, 0o600) }

// update changes host.json under the lock.
func (h *Host) update(fn func(*hostState) error) error {
	h.smu.Lock()
	defer h.smu.Unlock()
	s, err := h.load()
	if err != nil {
		return err
	}
	if err := fn(&s); err != nil {
		return err
	}
	return h.save(s)
}

// certificate loads the host's certificate, making one the first time. It
// is self-signed: members pin its fingerprint instead of trusting a CA.
func (h *Host) certificate() (*tls.Certificate, string, error) {
	if h.cert != nil {
		return h.cert, h.fp, nil
	}
	certPath, keyPath := filepath.Join(h.Dir, "host-cert.pem"), filepath.Join(h.Dir, "host-key.pem")
	c, err := tls.LoadX509KeyPair(certPath, keyPath)
	if errors.Is(err, os.ErrNotExist) {
		if err = makeCert(certPath, keyPath); err == nil {
			c, err = tls.LoadX509KeyPair(certPath, keyPath)
		}
	}
	if err != nil {
		return nil, "", fmt.Errorf("host certificate: %w", err)
	}
	h.cert, h.fp = &c, fingerprint(c.Certificate[0])
	return h.cert, h.fp, nil
}

func makeCert(certPath, keyPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "gh-runners main host"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(30, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	kb, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// Start listens for members on addr (":47322" style; "" means every
// interface on DefaultPort).
func (h *Host) Start(addr string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ln != nil {
		return nil
	}
	cert, _, err := h.certificate()
	if err != nil {
		h.startErr = err
		return err
	}
	if addr == "" {
		addr = ":" + strconv.Itoa(DefaultPort)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		h.startErr = fmt.Errorf("cannot listen for machines on %s: %w", addr, err)
		return h.startErr
	}
	h.startErr = nil
	h.ln, h.port = ln, ln.Addr().(*net.TCPAddr).Port
	h.stop = make(chan struct{})
	if h.sessions == nil {
		h.sessions, h.conns = map[string]*session{}, map[net.Conn]bool{}
	}
	tl := tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{*cert}, MinVersion: tls.VersionTLS13})
	h.wg.Add(1)
	go h.accept(tl, h.stop)
	h.logf("main host: listening for machines on port %d", h.port)
	return nil
}

// Stop closes the listener and every member's connection, and waits until
// they are done with host.json.
func (h *Host) Stop() {
	h.mu.Lock()
	if h.ln == nil {
		h.mu.Unlock()
		return
	}
	close(h.stop)
	_ = h.ln.Close()
	h.ln, h.startErr = nil, nil
	for id, s := range h.sessions {
		s.c.close(errors.New("the main host stopped"))
		delete(h.sessions, id)
	}
	for nc := range h.conns {
		_ = nc.Close()
	}
	h.mu.Unlock()
	h.wg.Wait()
}

// Running reports whether the host is listening, and on which port.
func (h *Host) Running() (bool, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ln != nil, h.port
}

// StartErr is why the last Start failed, if it did.
func (h *Host) StartErr() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.startErr
}

// Fingerprint is the SHA-256 of the host certificate, made if needed.
func (h *Host) Fingerprint() (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, fp, err := h.certificate()
	return fp, err
}

func (h *Host) accept(ln net.Listener, stop chan struct{}) {
	defer h.wg.Done()
	for {
		nc, err := ln.Accept()
		if err != nil {
			select {
			case <-stop:
				return
			default:
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		h.mu.Lock()
		if h.ln == nil || h.stop != stop {
			// Stopped (and maybe started again) since Accept returned.
			h.mu.Unlock()
			_ = nc.Close()
			return
		}
		h.conns[nc] = true
		h.wg.Add(1)
		h.mu.Unlock()
		go h.handle(nc)
	}
}

func (h *Host) handle(nc net.Conn) {
	defer func() {
		h.mu.Lock()
		delete(h.conns, nc)
		h.mu.Unlock()
		h.wg.Done()
	}()
	_ = nc.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := nc.(*tls.Conn).Handshake(); err != nil {
		_ = nc.Close()
		return
	}
	c := newConn(nc)
	f, err := c.recv(handshakeTimeout)
	if err != nil {
		_ = nc.Close()
		return
	}
	_ = nc.SetDeadline(time.Time{})
	switch f.Type {
	case tPair:
		h.pair(c, f)
	case tHello:
		h.hello(c, f, nc.RemoteAddr().String())
	default:
		_ = nc.Close()
	}
}

// Join code errors, sent to the member in a denied frame.
var (
	ErrUnknownCode = errors.New("this join code is not valid on the main host; make a new one with Add a machine")
	ErrUsedCode    = errors.New("this join code was already used; make a new one with Add a machine")
	ErrExpiredCode = errors.New("this join code expired; make a new one with Add a machine")
	// ErrRevoked means the host does not know the member (any more).
	ErrRevoked = errors.New("this machine was removed from the main host")
)

func (h *Host) pair(c *conn, f frame) {
	defer c.close(nil)
	id, cred := randomHex(4), randomHex(32)
	err := h.update(func(s *hostState) error {
		now := time.Now()
		s.Invites = slices.DeleteFunc(s.Invites, func(i inviteRec) bool { return now.Sub(i.Expires) > 24*time.Hour })
		i := slices.IndexFunc(s.Invites, func(i inviteRec) bool {
			return subtle.ConstantTimeCompare([]byte(i.Hash), []byte(hashHex(f.Secret))) == 1
		})
		switch {
		case f.Secret == "" || i < 0:
			return ErrUnknownCode
		case s.Invites[i].Used:
			return ErrUsedCode
		case now.After(s.Invites[i].Expires):
			return ErrExpiredCode
		}
		s.Invites[i].Used = true
		rec := memberRec{ID: id, CredHash: hashHex(cred), Added: now}
		if f.Info != nil {
			rec.Info = *f.Info
		}
		s.Members = append(s.Members, rec)
		return nil
	})
	if err != nil {
		_ = c.send(frame{Type: tDenied, Error: err.Error()})
		h.logf("main host: refused a join: %v", err)
		return
	}
	_ = c.send(frame{Type: tPaired, MemberID: id, Credential: cred, HostName: h.name()})
	name := id
	if f.Info != nil {
		name = f.Info.Hostname
	}
	h.logf("main host: %s joined", name)
}

func (h *Host) hello(c *conn, f frame, remote string) {
	now := time.Now()
	err := h.update(func(s *hostState) error {
		i := slices.IndexFunc(s.Members, func(m memberRec) bool { return m.ID == f.MemberID })
		if i < 0 || subtle.ConstantTimeCompare([]byte(s.Members[i].CredHash), []byte(hashHex(f.Credential))) != 1 {
			return ErrRevoked
		}
		s.Members[i].LastSeen = now
		if f.Info != nil {
			s.Members[i].Info = *f.Info
		}
		return nil
	})
	if err != nil {
		_ = c.send(frame{Type: tDenied, Error: err.Error()})
		c.close(nil)
		return
	}
	h.mu.Lock()
	if h.ln == nil {
		h.mu.Unlock()
		c.close(nil)
		return
	}
	if old := h.sessions[f.MemberID]; old != nil {
		old.c.close(errors.New("replaced by a new connection"))
	}
	s := &session{c: c, since: now}
	if f.Info != nil {
		s.info = *f.Info
	}
	h.sessions[f.MemberID] = s
	h.mu.Unlock()
	h.logf("main host: machine %s connected from %s", f.MemberID, remote)

	_ = c.send(frame{Type: tWelcome, HostName: h.name(), Addrs: h.addrs()})
	_ = c.serve(h.broker, func(f2 frame) error {
		switch f2.Type {
		case tStatus:
			// Kept in memory; host.json gets the last one when it goes.
			if f2.Info != nil {
				h.mu.Lock()
				s.info = *f2.Info
				h.mu.Unlock()
			}
		case tLeave:
			_ = h.forget(f.MemberID)
			h.logf("main host: machine %s left", f.MemberID)
			return errors.New("left")
		}
		return nil
	})
	h.mu.Lock()
	if h.sessions[f.MemberID] == s {
		delete(h.sessions, f.MemberID)
	}
	info := s.info
	h.mu.Unlock()
	_ = h.update(func(st *hostState) error {
		if i := slices.IndexFunc(st.Members, func(m memberRec) bool { return m.ID == f.MemberID }); i >= 0 {
			st.Members[i].LastSeen, st.Members[i].Info = time.Now(), info
		}
		return nil
	})
}

func (h *Host) name() string {
	if h.Name != "" {
		return h.Name
	}
	n, _ := os.Hostname()
	return n
}

func (h *Host) addrs() []string {
	if h.Addrs != nil {
		return h.Addrs()
	}
	return LocalAddrs()
}

// Invite makes a one-time join code that works for InviteTTL.
func (h *Host) Invite() (string, time.Time, error) {
	h.mu.Lock()
	_, fp, err := h.certificate()
	port := h.port
	running := h.ln != nil
	h.mu.Unlock()
	if err != nil {
		return "", time.Time{}, err
	}
	if !running {
		return "", time.Time{}, errors.New("turn on Main host first")
	}
	secret := randomHex(16)
	expires := time.Now().Add(InviteTTL)
	err = h.update(func(s *hostState) error {
		s.Invites = append(s.Invites, inviteRec{Hash: hashHex(secret), Expires: expires})
		return nil
	})
	if err != nil {
		return "", time.Time{}, err
	}
	code, err := Code{Addrs: h.addrs(), Port: port, Fingerprint: fp, Secret: secret}.Encode()
	return code, expires, err
}

// Machines lists the members, with whether each is connected now.
func (h *Host) Machines() ([]Machine, error) {
	h.smu.Lock()
	s, err := h.load()
	h.smu.Unlock()
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Machine, 0, len(s.Members))
	for _, m := range s.Members {
		mm := Machine{ID: m.ID, Added: m.Added, LastSeen: m.LastSeen, Info: m.Info}
		if ss := h.sessions[m.ID]; ss != nil {
			mm.Online, mm.Since, mm.Info, mm.LastSeen = true, ss.since, ss.info, time.Now()
		}
		out = append(out, mm)
	}
	return out, nil
}

// Remove revokes a member: its credential stops working, and if it is
// connected it is told to forget the host.
func (h *Host) Remove(id string) error {
	if err := h.forget(id); err != nil {
		return err
	}
	h.mu.Lock()
	s := h.sessions[id]
	delete(h.sessions, id)
	h.mu.Unlock()
	if s != nil {
		_ = s.c.send(frame{Type: tRemoved})
		s.c.close(ErrRevoked)
	}
	h.logf("main host: removed machine %s", id)
	return nil
}

func (h *Host) forget(id string) error {
	found := false
	err := h.update(func(s *hostState) error {
		n := len(s.Members)
		s.Members = slices.DeleteFunc(s.Members, func(m memberRec) bool { return m.ID == id })
		found = len(s.Members) < n
		return nil
	})
	if err == nil && !found {
		return fmt.Errorf("no machine %q", id)
	}
	return err
}

// ErrOffline means the member is not connected.
var ErrOffline = errors.New("this machine is offline")

// Call runs one dashboard API call on a member and returns its status and
// body. Only calls in the member allowlist are sent.
func (h *Host) Call(ctx context.Context, id, method, pathQuery string, body []byte) (int, []byte, error) {
	if !MemberAllowed(method, pathQuery) {
		return 0, nil, fmt.Errorf("%s %s is not allowed on another machine", method, pathQuery)
	}
	h.mu.Lock()
	s := h.sessions[id]
	h.mu.Unlock()
	if s == nil {
		return 0, nil, ErrOffline
	}
	r, err := s.c.call(ctx, frame{Kind: kindAPI, Method: method, Path: pathQuery, Body: body})
	if err != nil {
		return 0, nil, err
	}
	if r.Error != "" {
		return 0, nil, errors.New(r.Error)
	}
	return r.Status, r.Body, nil
}

// broker makes a member's GitHub API call with the host's token. The
// token goes only to GitHub; the member gets the status and body back.
func (h *Host) broker(f frame) frame {
	if f.Kind != kindGitHub || !GitHubAllowed(f.Method, f.Path) {
		return githubError(http.StatusForbidden, "the main host does not make this GitHub call for other machines")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	token := ""
	if h.Token != nil {
		token, _ = h.Token(ctx)
	}
	if token == "" {
		return githubError(http.StatusBadGateway, "the main host is not signed in to GitHub")
	}
	var body io.Reader
	if len(f.Body) > 0 {
		body = bytes.NewReader(f.Body)
	}
	req, err := http.NewRequestWithContext(ctx, f.Method, github.APIBase+f.Path, body)
	if err != nil {
		return githubError(http.StatusBadRequest, err.Error())
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "gh-runners")
	if len(f.Body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if v := f.Header["If-None-Match"]; v != "" {
		req.Header.Set("If-None-Match", v)
	}
	resp, err := brokerHTTP.Do(req)
	if err != nil {
		return githubError(http.StatusBadGateway, "the main host could not reach GitHub: "+err.Error())
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFrame/2))
	if err != nil {
		return githubError(http.StatusBadGateway, err.Error())
	}
	hdr := map[string]string{}
	for _, k := range []string{"Content-Type", "ETag", "X-OAuth-Scopes"} {
		if v := resp.Header.Get(k); v != "" {
			hdr[k] = v
		}
	}
	return frame{Status: resp.StatusCode, Header: hdr, Body: data}
}

var brokerHTTP = &http.Client{Timeout: 60 * time.Second}

func githubError(status int, msg string) frame {
	b, _ := json.Marshal(map[string]string{"message": msg})
	return frame{Status: status, Header: map[string]string{"Content-Type": "application/json"}, Body: b}
}
