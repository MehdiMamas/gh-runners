package fleet

import (
	"context"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/localci"
)

// fakeGH is a GitHub with one user, "octo", and the contents API of one
// repository.
type fakeGH struct {
	srv *httptest.Server

	mu       sync.Mutex
	repo     bool
	creates  int
	files    map[string]string // path → content
	fileGets int
	puts     int
}

func newFakeGH(t *testing.T) *fakeGH {
	t.Helper()
	g := &fakeGH{files: map[string]string{}}
	mux := http.NewServeMux()
	sha := func(s string) string { h := sha1.Sum([]byte(s)); return hex.EncodeToString(h[:]) }
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"login":"octo"}`)
	})
	mux.HandleFunc("GET /repos/octo/"+DirectoryRepo, func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if !g.repo {
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"message":"Not Found"}`)
			return
		}
		_, _ = io.WriteString(w, `{"full_name":"octo/`+DirectoryRepo+`","private":true}`)
	})
	mux.HandleFunc("POST /user/repos", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &in)
		if in["private"] != true || in["name"] != DirectoryRepo {
			t.Errorf("create repo body: %s", b)
		}
		g.mu.Lock()
		g.repo, g.creates = true, g.creates+1
		g.mu.Unlock()
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"full_name":"octo/`+DirectoryRepo+`","private":true}`)
	})
	const pre = "/repos/octo/" + DirectoryRepo + "/contents/"
	mux.HandleFunc(pre+"{path...}", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		path := r.PathValue("path")
		switch r.Method {
		case "GET":
			if c, ok := g.files[path]; ok {
				g.fileGets++
				b, _ := json.Marshal(map[string]string{"name": filepath.Base(path), "path": path, "sha": sha(c), "type": "file", "content": base64.StdEncoding.EncodeToString([]byte(c))})
				_, _ = w.Write(b)
				return
			}
			var list []map[string]string
			var names []string
			for p := range g.files {
				if strings.HasPrefix(p, path+"/") {
					names = append(names, p)
				}
			}
			sort.Strings(names)
			for _, p := range names {
				list = append(list, map[string]string{"name": filepath.Base(p), "path": p, "sha": sha(g.files[p]), "type": "file"})
			}
			if len(list) == 0 {
				w.WriteHeader(404)
				_, _ = io.WriteString(w, `{"message":"Not Found"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(list)
		case "PUT":
			var in struct{ Content, SHA string }
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &in)
			raw, _ := base64.StdEncoding.DecodeString(in.Content)
			if old, ok := g.files[path]; ok && in.SHA != sha(old) {
				w.WriteHeader(409)
				_, _ = io.WriteString(w, `{"message":"sha mismatch"}`)
				return
			}
			g.files[path] = string(raw)
			g.puts++
			_, _ = io.WriteString(w, `{"content":{"sha":"`+sha(string(raw))+`"}}`)
		case "DELETE":
			if _, ok := g.files[path]; !ok {
				w.WriteHeader(404)
				return
			}
			delete(g.files, path)
			_, _ = io.WriteString(w, `{}`)
		}
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGH) client() (*github.Client, error) {
	return &github.Client{Token: "tok", BaseURL: g.srv.URL, HTTP: g.srv.Client()}, nil
}

func (g *fakeGH) has(path string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.files[path]
	return ok
}

func (g *fakeGH) gets() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.fileGets
}

func TestDirectory(t *testing.T) {
	g := newFakeGH(t)
	d := &Directory{Dir: t.TempDir(), Client: g.client}
	d.Self = func() Entry {
		return Entry{Name: "mac", OS: "darwin", Arch: "arm64", Version: "1", Addrs: []string{"10.0.0.2"}, Port: 47322, Fingerprint: strings.Repeat("ab", 32)}
	}
	ctx := context.Background()

	// Create: the private repository is made on first use, once.
	if err := d.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	if g.creates != 1 || !g.repo {
		t.Fatalf("repo creates = %d", g.creates)
	}
	id, _ := d.ID()
	if id2, _ := d.ID(); id2 != id || !idRe.MatchString(id) {
		t.Fatalf("machine id not stable: %q %q", id, id2)
	}
	if !g.has("machines/" + id + ".json") {
		t.Fatal("entry not written")
	}
	// Publish again: replaces the file (sha handled), no second repo.
	if err := d.Publish(ctx); err != nil || g.creates != 1 {
		t.Fatalf("republish: %v creates=%d", err, g.creates)
	}

	// A second machine writes its own file.
	other := `{"id":"0123456789abcdef","name":"pc","os":"windows","arch":"amd64","version":"1","addrs":["10.0.0.3"],"port":47322,"fingerprint":"` + strings.Repeat("cd", 32) + `"}`
	g.mu.Lock()
	g.files["machines/0123456789abcdef.json"] = other
	g.files["machines/junk.json"] = "{not json"
	g.mu.Unlock()

	es, err := d.List(ctx)
	if err != nil || len(es) != 2 {
		t.Fatalf("List: %v %+v", err, es)
	}
	if es[1].ID != id && es[0].ID != id {
		t.Fatalf("self missing: %+v", es)
	}

	// Cache by sha: nothing changed, no file is fetched again.
	before := g.gets()
	if _, err := d.List(ctx); err != nil || g.gets() != before {
		t.Fatalf("unchanged List fetched files: %d -> %d (%v)", before, g.gets(), err)
	}
	// One file changed: exactly that one is fetched.
	g.mu.Lock()
	g.files["machines/0123456789abcdef.json"] = strings.Replace(other, `"pc"`, `"pc2"`, 1)
	g.mu.Unlock()
	before = g.gets()
	es, _ = d.List(ctx)
	if g.gets() != before+1 {
		t.Fatalf("changed file: fetched %d", g.gets()-before)
	}
	found := false
	for _, e := range es {
		found = found || e.Name == "pc2"
	}
	if !found {
		t.Fatalf("new content not read: %+v", es)
	}

	// Remove, and Unpublish for itself.
	if err := d.Remove(ctx, "0123456789abcdef"); err != nil || g.has("machines/0123456789abcdef.json") {
		t.Fatalf("Remove: %v", err)
	}
	if err := d.Unpublish(ctx); err != nil || g.has("machines/"+id+".json") {
		t.Fatalf("Unpublish: %v", err)
	}
	if es, _ := d.List(ctx); len(es) != 0 {
		t.Fatalf("directory not empty: %+v", es)
	}
}

type peerT struct {
	*Peers
	allow *bool
}

func newPeer(t *testing.T, g *fakeGH, name string) *peerT {
	t.Helper()
	return newPeerWith(t, g, name, nil)
}

// newPeerWith is newPeer with a chance to change the settings before it
// starts.
func newPeerWith(t *testing.T, g *fakeGH, name string, set func(*Peers)) *peerT {
	t.Helper()
	pt := &peerT{}
	on := true
	pt.allow = &on
	var mu sync.Mutex
	pt.Peers = &Peers{
		Dir: t.TempDir(), Client: g.client, Name: name, Version: "1", Addr: "127.0.0.1:0",
		Addrs: func() []string { return []string{"127.0.0.1"} },
		Info: func() Info {
			return Info{Hostname: name, OS: "linux", Arch: "amd64", Version: "1", Runners: []RunnerInfo{{ID: "r1", Scope: "o/r", Name: name + "-r", State: "idle"}}}
		},
		API: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"machine": name, "path": r.URL.Path})
		}),
		AllowControl: func() bool { mu.Lock(); defer mu.Unlock(); return *pt.allow },
		PublishEvery: 150 * time.Millisecond, ListEvery: 100 * time.Millisecond, DialEvery: 50 * time.Millisecond,
	}
	if set != nil {
		set(pt.Peers)
	}
	t.Cleanup(pt.Stop)
	if err := pt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return pt
}

func onlineWith(p *Peers, id string) bool {
	for _, x := range p.Peers() {
		if x.ID == id && x.Online && x.HasInfo {
			return true
		}
	}
	return false
}

func twoPeers(t *testing.T) (*fakeGH, *peerT, *peerT) {
	g := newFakeGH(t)
	a, b := newPeer(t, g, "alpha"), newPeer(t, g, "beta")
	waitFor(t, func() bool { return onlineWith(a.Peers, b.ID()) && onlineWith(b.Peers, a.ID()) })
	return g, a, b
}

// Two fleets in one process, two homes, one fake GitHub: they find each
// other through the directory and run allowlisted calls, both ways.
func TestTwoPeersSeeEachOtherAndCall(t *testing.T) {
	_, a, b := twoPeers(t)
	if ps := a.Peers.Peers(); len(ps) != 1 || ps[0].Name != "beta" || ps[0].Info.Runners[0].Name != "beta-r" || ps[0].Error != "" {
		t.Fatalf("alpha sees %+v", ps)
	}
	for _, c := range []struct {
		from, to *peerT
		want     string
	}{{a, b, "beta"}, {b, a, "alpha"}} {
		status, body, err := c.from.Call(context.Background(), c.to.ID(), "GET", "/api/state", nil)
		if err != nil || status != 200 || !strings.Contains(string(body), `"machine":"`+c.want+`"`) {
			t.Fatalf("call to %s: %d %s %v", c.want, status, body, err)
		}
	}
}

// The allowlist is checked on the receiving side, in both directions: a
// sender that skips its own check is still refused.
func TestPeerAllowlistBothWays(t *testing.T) {
	_, a, b := twoPeers(t)
	for _, c := range []struct{ from, to *peerT }{{a, b}, {b, a}} {
		if _, _, err := c.from.Call(context.Background(), c.to.ID(), "POST", "/api/login", nil); err == nil {
			t.Fatal("sender let a login call through")
		}
		// Send straight down the link, as a modified sender would.
		r := c.from.run
		r.mu.Lock()
		l := r.links[c.to.ID()]
		r.mu.Unlock()
		for _, path := range []string{"/api/login", "/api/fs", "/api/settings", "/api/state/../login"} {
			f, err := l.c.call(context.Background(), frame{Kind: kindAPI, Method: "POST", Path: path})
			if err != nil || f.Status != 403 || !strings.Contains(string(f.Body), "not allowed") {
				t.Fatalf("receiver let %s through: %+v %v", path, f, err)
			}
		}
		f, err := l.c.call(context.Background(), frame{Kind: kindGitHub, Method: "GET", Path: "/user"})
		if err != nil || f.Status != 403 {
			t.Fatalf("receiver let a github frame through: %+v %v", f, err)
		}
	}
	for _, p := range []string{"GET /api/state", "POST /api/runners/abc/restart"} {
		m, path, _ := strings.Cut(p, " ")
		if !PeerAllowed(m, path) {
			t.Errorf("%s should be allowed", p)
		}
	}
	for _, p := range []string{"POST /api/login", "POST /api/logout", "GET /api/fs", "PUT /api/config", "POST /api/service/stop"} {
		m, path, _ := strings.Cut(p, " ")
		if PeerAllowed(m, path) {
			t.Errorf("%s should be refused", p)
		}
	}
}

func TestPeerControlOff(t *testing.T) {
	_, a, b := twoPeers(t)
	*b.allow = false
	status, body, err := a.Call(context.Background(), b.ID(), "POST", "/api/runners/abc/stop", nil)
	if err != nil || status != 403 || !strings.Contains(string(body), "does not let your other PCs control it") {
		t.Fatalf("control off: %d %s %v", status, body, err)
	}
	// The link stays up and shows the machine; turning it back on works.
	if !onlineWith(a.Peers, b.ID()) {
		t.Fatal("peer should stay online")
	}
	*b.allow = true
	if status, _, err := a.Call(context.Background(), b.ID(), "GET", "/api/state", nil); err != nil || status != 200 {
		t.Fatalf("control on again: %d %v", status, err)
	}
}

// An unknown certificate is refused, and a machine removed from the
// directory is dropped.
func TestPeerPinning(t *testing.T) {
	g, a, b := twoPeers(t)

	// A stranger with its own certificate, not in the directory.
	dir := t.TempDir()
	cp, kp := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	if err := makeCertFor(cp, kp, "stranger", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}); err != nil {
		t.Fatal(err)
	}
	cert, err := tls.LoadX509KeyPair(cp, kp)
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(a.run.ln.Addr().String())
	nc, err := tls.Dial("tcp", "127.0.0.1:"+port, &tls.Config{InsecureSkipVerify: true, Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
	if err == nil {
		c := newConn(nc)
		_ = c.send(frame{Type: tHello, MemberID: b.ID()})
		if f, rerr := c.recv(3 * time.Second); rerr == nil {
			t.Fatalf("stranger got a %q frame", f.Type)
		}
		c.close(nil)
	}
	if n, _ := strconv.Atoi(port); n == 0 {
		t.Fatal("no port")
	}

	// A peer that pins the wrong fingerprint is not trusted either.
	if _, _, err := dialWith(context.Background(), []string{"127.0.0.1"}, atoi(port), pinned(strings.Repeat("0", 64))); err == nil {
		t.Fatal("dial with a wrong pin worked")
	}

	// Removing beta from the directory drops its link and its entry.
	bid := b.ID()
	g.mu.Lock()
	delete(g.files, "machines/"+bid+".json")
	g.mu.Unlock()
	b.Stop() // else it would publish itself again
	waitFor(t, func() bool { return !a.Peers.Has(bid) })
	if len(a.Peers.Peers()) != 0 {
		t.Fatalf("removed machine still listed: %+v", a.Peers.Peers())
	}
	if _, _, err := a.Call(context.Background(), bid, "GET", "/api/state", nil); err == nil {
		t.Fatal("call to a removed machine worked")
	}
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// An unreachable peer shows as such, with the reason in plain words.
func TestPeerUnreachable(t *testing.T) {
	g := newFakeGH(t)
	a := newPeer(t, g, "alpha")
	ln, _ := net.Listen("tcp", "127.0.0.1:0") // a port nothing listens on
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	g.mu.Lock()
	g.files["machines/ffffffffffffffff.json"] = `{"id":"ffffffffffffffff","name":"ghost","os":"linux","arch":"amd64","version":"1","addrs":["127.0.0.1"],"port":` + strconv.Itoa(port) + `,"fingerprint":"` + strings.Repeat("ee", 32) + `"}`
	g.mu.Unlock()
	waitFor(t, func() bool {
		for _, p := range a.Peers.Peers() {
			if p.ID == "ffffffffffffffff" && !p.Online && p.Error != "" {
				return true
			}
		}
		return false
	})
	for _, p := range a.Peers.Peers() {
		if !strings.Contains(p.Error, "Connection refused") {
			t.Fatalf("error in plain words: %q", p.Error)
		}
	}
	if _, _, err := a.Call(context.Background(), "ffffffffffffffff", "GET", "/api/state", nil); err != ErrOffline {
		t.Fatalf("call to unreachable: %v", err)
	}
}

// A machine that has not updated its entry for StaleAfter is removed from the
// directory; a fresh one and this machine itself stay.
func TestDirectoryDropsStale(t *testing.T) {
	g := newFakeGH(t)
	d := &Directory{Dir: t.TempDir(), Client: g.client}
	d.Self = func() Entry {
		return Entry{Name: "mac", Addrs: []string{"10.0.0.2"}, Port: 47323, Fingerprint: strings.Repeat("ab", 32)}
	}
	ctx := context.Background()
	if err := d.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	entry := func(id string, at time.Time) string {
		return `{"id":"` + id + `","name":"pc","addrs":["10.0.0.3"],"port":47323,"fingerprint":"` + strings.Repeat("cd", 32) + `","updated":"` + at.UTC().Format(time.RFC3339) + `"}`
	}
	g.mu.Lock()
	g.files["machines/0000000000000001.json"] = entry("0000000000000001", time.Now().Add(-StaleAfter-time.Hour))
	g.files["machines/0000000000000002.json"] = entry("0000000000000002", time.Now().Add(-time.Hour))
	g.mu.Unlock()
	es, err := d.List(ctx)
	if err != nil || len(es) != 2 {
		t.Fatalf("List: %v %+v", err, es)
	}
	if g.has("machines/0000000000000001.json") || !g.has("machines/0000000000000002.json") {
		t.Fatal("stale entry kept or fresh one removed")
	}
}

// withCI turns on CI jobs on a peer, with a fake source download; the job
// directory is not removed while a job still runs.
func withCI(t *testing.T) func(*Peers) {
	return func(p *Peers) {
		p.CIDir = t.TempDir()
		p.CITools = func() []string { return []string{"bash"} }
		p.CIEmulatorOK = func() bool { return false }
		p.CIFetch = func(_ context.Context, _, _, dest string) error { return os.WriteFile(dest, tinyTarball(t), 0o644) }
		t.Cleanup(func() {
			waitFor(t, func() bool { return p.run == nil || p.run.ci.busy() == 0 })
		})
	}
}

// ciPair is two peers where the second takes CI jobs and the first knows it.
func ciPair(t *testing.T) (a, b *peerT) {
	g := newFakeGH(t)
	a, b = newPeer(t, g, "alpha"), newPeerWith(t, g, "beta", withCI(t))
	waitFor(t, func() bool { return len(a.CIMachines()) == 1 })
	return a, b
}

func TestPeerCIRunsAndStreamsLog(t *testing.T) {
	a, b := ciPair(t)
	if ms := a.CIMachines(); ms[0].ID != b.ID() || ms[0].Name != "beta" || len(ms[0].Tools) != 1 {
		t.Fatalf("CIMachines = %+v", ms)
	}
	s := spec(t, "cat data.txt && echo repo=$GITHUB_REPOSITORY")
	r := PeerExecutor{Peers: a.Peers, ID: b.ID()}.RunJob(context.Background(), s)
	if r.Status != localci.Pass {
		t.Fatalf("status %v (%s)", r.Status, r.Reason)
	}
	if log, err := os.ReadFile(s.Log); err != nil || !strings.Contains(string(log), "hello from the repo") {
		t.Fatalf("log = %q, %v", log, err)
	}
	bad := spec(t, "echo nope; exit 3")
	bad.Log = filepath.Join(filepath.Dir(s.Log), "bad.log")
	if r := (PeerExecutor{Peers: a.Peers, ID: b.ID()}).RunJob(context.Background(), bad); r.Status != localci.Fail {
		t.Fatalf("failing job: status %v", r.Status)
	}
	if b, _ := os.ReadFile(bad.Log); !strings.Contains(string(b), "nope") {
		t.Fatalf("failing log = %q", b)
	}
}

func TestPeerCICancelStopsTheJob(t *testing.T) {
	a, b := ciPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan localci.Result, 1)
	go func() { res <- PeerExecutor{Peers: a.Peers, ID: b.ID()}.RunJob(ctx, spec(t, "echo started; sleep 30")) }()
	waitFor(t, func() bool { return b.run.ci.busy() == 1 })
	cancel()
	select {
	case r := <-res:
		if r.Status != localci.Skipped || r.Reason != "cancelled" {
			t.Fatalf("got %v %q", r.Status, r.Reason)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not return")
	}
	waitFor(t, func() bool { return b.run.ci.busy() == 0 })
}

func TestPeerCILinkDropsMidJob(t *testing.T) {
	a, b := ciPair(t)
	res := make(chan localci.Result, 1)
	go func() {
		res <- PeerExecutor{Peers: a.Peers, ID: b.ID()}.RunJob(context.Background(), spec(t, "echo started; sleep 30"))
	}()
	waitFor(t, func() bool { return b.run.ci.busy() == 1 })
	a.run.mu.Lock()
	l := a.run.links[b.ID()]
	a.run.mu.Unlock()
	l.c.close(errors.New("cable pulled"))
	select {
	case r := <-res:
		if r.Status != localci.Fail || !strings.Contains(r.Reason, "machine went offline") {
			t.Fatalf("got %v %q", r.Status, r.Reason)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the sender waited for the dead link")
	}
	waitFor(t, func() bool { return b.run.ci.busy() == 0 })
	if r := (PeerExecutor{Peers: a.Peers, ID: "nobody"}).RunJob(context.Background(), spec(t, "true")); r.Status != localci.Fail || !strings.Contains(r.Reason, "offline") {
		t.Fatalf("unknown peer: %v %q", r.Status, r.Reason)
	}
}

func TestPeerCIRefused(t *testing.T) {
	a, b := ciPair(t)
	*b.allow = false
	r := PeerExecutor{Peers: a.Peers, ID: b.ID()}.RunJob(context.Background(), spec(t, "true"))
	if r.Status != localci.Fail || !strings.Contains(r.Reason, "does not let your other PCs control it") {
		t.Fatalf("control off: %v %q", r.Status, r.Reason)
	}
	*b.allow = true
	// A PC that has CI off says so.
	g := newFakeGH(t)
	x, y := newPeer(t, g, "xray"), newPeer(t, g, "yankee")
	waitFor(t, func() bool { return onlineWith(x.Peers, y.ID()) })
	r = PeerExecutor{Peers: x.Peers, ID: y.ID()}.RunJob(context.Background(), spec(t, "true"))
	if r.Status != localci.Fail || !strings.Contains(r.Reason, "this PC does not run CI jobs") {
		t.Fatalf("CI off: %v %q", r.Status, r.Reason)
	}
}

// threePeers has A and B each unable to dial the other (and C): the only
// links are A–C and B–C.
func threePeers(t *testing.T) (a, b, c *peerT) {
	g := newFakeGH(t)
	dead := func(p *Peers) { p.Addrs = func() []string { return []string{"192.0.2.1"} } }
	a, b, c = newPeerWith(t, g, "alpha", dead), newPeerWith(t, g, "beta", func(p *Peers) { dead(p); withCI(t)(p) }), newPeer(t, g, "gamma")
	waitFor(t, func() bool {
		for _, pe := range a.Peers.Peers() {
			if pe.ID == b.ID() && pe.Via == c.ID() {
				return true
			}
		}
		return false
	})
	return a, b, c
}

func TestPeerRelayThroughThirdMachine(t *testing.T) {
	a, b, c := threePeers(t)
	for _, pe := range a.Peers.Peers() {
		if pe.ID == b.ID() && (!pe.Online || pe.ViaName != "gamma" || pe.Machine().ViaName != "gamma") {
			t.Fatalf("B on A's list: %+v", pe)
		}
	}
	if a.Linked(b.ID()) || !a.Linked(c.ID()) {
		t.Fatal("A should be linked to C only")
	}
	// A pauses B through C.
	status, body, err := a.Call(context.Background(), b.ID(), "POST", "/api/pause", nil)
	if err != nil || status != 200 || !strings.Contains(string(body), `"machine":"beta"`) {
		t.Fatalf("relayed call: %d %s %v", status, body, err)
	}
	// A relayed machine takes no CI jobs in this phase.
	if ms := a.CIMachines(); len(ms) != 0 {
		t.Fatalf("CIMachines through a relay: %+v", ms)
	}

	// B's allowlist and control switch decide, whatever C or A say.
	a.run.mu.Lock()
	l := a.run.links[c.ID()]
	a.run.mu.Unlock()
	relay := func(to string, in *frame) frame {
		f, err := l.c.call(context.Background(), frame{Kind: kindRelay, To: to, From: "someone-else", Inner: in})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	for _, path := range []string{"/api/login", "/api/fs", "/api/settings"} {
		f := relay(b.ID(), &frame{Kind: kindAPI, Method: "POST", Path: path})
		if f.Status != 403 || !strings.Contains(string(f.Body), "not allowed") {
			t.Fatalf("B let %s through the relay: %+v", path, f)
		}
	}
	if f := relay(b.ID(), &frame{Kind: kindGitHub, Method: "GET", Path: "/user"}); f.Status != 403 {
		t.Fatalf("a github frame through the relay: %+v", f)
	}
	*b.allow = false
	if f := relay(b.ID(), &frame{Kind: kindAPI, Method: "GET", Path: "/api/state"}); f.Status != 403 || !strings.Contains(string(f.Body), "does not let your other PCs control it") {
		t.Fatalf("control off through the relay: %+v", f)
	}
	*b.allow = true
	// C has no direct link to this id.
	if f := relay("0123456789abcdef", &frame{Kind: kindAPI, Method: "GET", Path: "/api/state"}); f.Status != 403 || !strings.Contains(string(f.Body), "no direct link") {
		t.Fatalf("relay to a machine C does not know: %+v", f)
	}
}

func TestPeerRelayNoChains(t *testing.T) {
	a, b, c := threePeers(t)
	a.run.mu.Lock()
	l := a.run.links[c.ID()]
	a.run.mu.Unlock()
	inner := &frame{Kind: kindRelay, To: b.ID(), Inner: &frame{Kind: kindAPI, Method: "GET", Path: "/api/state"}}
	f, err := l.c.call(context.Background(), frame{Kind: kindRelay, To: b.ID(), Inner: inner})
	if err != nil || f.Status != 403 || strings.Contains(string(f.Body), "beta") {
		t.Fatalf("a chained relay was passed on: %+v %v", f, err)
	}
	// Sent straight to the machine it is for, it still is not a call.
	a.run.mu.Lock()
	l = a.run.links[c.ID()]
	a.run.mu.Unlock()
	f, err = l.c.call(context.Background(), frame{Kind: kindRelay, To: c.ID(), Inner: inner})
	if err != nil || f.Status != 403 {
		t.Fatalf("a relay inside a relay for C itself: %+v %v", f, err)
	}
}

// hostWithInfo is a main host that says who it is, for the migration tests.
func hostWithInfo(t *testing.T, p *Peers) *Host {
	h := newHost(t)
	h.Info = func() Info {
		return Info{Hostname: "main", Login: p.Login(), MachineID: p.ID(), Runners: []RunnerInfo{}}
	}
	return h
}

func joinedMember(t *testing.T, h *Host, p *Peers, login string, logf func(string, ...any)) *Member {
	dir := t.TempDir()
	if _, err := Join(context.Background(), dir, invite(t, h), Info{Hostname: "pc2"}); err != nil {
		t.Fatal(err)
	}
	m := &Member{
		Dir: dir, IsPeer: p.Linked, Log: logf,
		Info: func() Info { return Info{Hostname: "pc2", Login: login, MachineID: p.ID(), Runners: []RunnerInfo{}} },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return m
}

// A member whose main host is a PC of its own GitHub account leaves the
// host and stays as a peer.
func TestMigrationSameLogin(t *testing.T) {
	g := newFakeGH(t)
	hp, mp := newPeer(t, g, "main-pc"), newPeer(t, g, "member-pc")
	waitFor(t, func() bool {
		return onlineWith(hp.Peers, mp.ID()) && onlineWith(mp.Peers, hp.ID()) && hp.Login() == "octo"
	})
	h := hostWithInfo(t, hp.Peers)
	var mu sync.Mutex
	var logs []string
	m := joinedMember(t, h, mp.Peers, "octo", func(f string, a ...any) { mu.Lock(); logs = append(logs, fmt.Sprintf(f, a...)); mu.Unlock() })
	waitFor(t, func() bool { return !m.Joined() })
	waitFor(t, func() bool { ms, _ := h.Machines(); return len(ms) == 0 })
	if !onlineWith(mp.Peers, hp.ID()) || !onlineWith(hp.Peers, mp.ID()) {
		t.Fatal("both should be listed as peers")
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(strings.Join(logs, "\n"), "switched to account machines: main is on the same GitHub account") {
		t.Fatalf("log = %q", logs)
	}
}

// A member on another account, or not signed in, keeps its join.
func TestMigrationOtherLoginStaysMember(t *testing.T) {
	g := newFakeGH(t)
	hp, mp := newPeer(t, g, "main-pc"), newPeer(t, g, "member-pc")
	waitFor(t, func() bool {
		return onlineWith(hp.Peers, mp.ID()) && onlineWith(mp.Peers, hp.ID()) && hp.Login() == "octo"
	})
	h := hostWithInfo(t, hp.Peers)
	for _, login := range []string{"someone-else", ""} {
		m := joinedMember(t, h, mp.Peers, login, nil)
		waitFor(t, func() bool { return m.Status().Connected })
		time.Sleep(300 * time.Millisecond)
		if !m.Joined() || !m.Status().Connected {
			t.Fatalf("login %q: the member left its host", login)
		}
	}
}
