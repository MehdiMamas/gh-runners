package fleet

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

func newHost(t *testing.T) *Host {
	t.Helper()
	h := &Host{
		Dir:   t.TempDir(),
		Name:  "main",
		Addrs: func() []string { return []string{"127.0.0.1"} },
		Token: func(context.Context) (string, error) { return "host-token", nil },
	}
	if err := h.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)
	return h
}

func invite(t *testing.T, h *Host) string {
	t.Helper()
	code, _, err := h.Invite()
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// connect runs a member against h until the test ends and waits for it to
// be online.
func connect(t *testing.T, h *Host, api http.Handler) *Member {
	t.Helper()
	dir := t.TempDir()
	if _, err := Join(context.Background(), dir, invite(t, h), Info{Hostname: "pc2"}); err != nil {
		t.Fatal(err)
	}
	m := &Member{Dir: dir, API: api, Info: func() Info { return Info{Hostname: "pc2", OS: "linux"} }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, func() bool { return m.Status().Connected })
	return m
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func TestCodeRoundTrip(t *testing.T) {
	c := Code{Addrs: []string{"192.168.1.5", "100.101.102.103", "pc.tail1234.ts.net"}, Port: 47322, Fingerprint: strings.Repeat("ab", 32), Secret: "s3cret"}
	s, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseCode("  " + s + "\n")
	if err != nil || got.Port != c.Port || got.Secret != c.Secret || len(got.Addrs) != 3 {
		t.Fatalf("ParseCode = %+v, %v", got, err)
	}
	for _, bad := range []string{"", "ghr1-", "ghr1-!!", "nope", s[:len(s)-4]} {
		if _, err := ParseCode(bad); err == nil {
			t.Errorf("ParseCode(%q) accepted", bad)
		}
	}
	win, unix := Commands(s)
	if !strings.Contains(win, "$env:GH_RUNNERS_JOIN='"+s+"'") || !strings.Contains(win, "install.ps1 | iex") {
		t.Errorf("windows command %q", win)
	}
	if !strings.HasSuffix(unix, "| sh -s -- --join "+s) {
		t.Errorf("unix command %q", unix)
	}
}

func TestPairing(t *testing.T) {
	h := newHost(t)
	ctx := context.Background()

	code := invite(t, h)
	dir := t.TempDir()
	name, err := Join(ctx, dir, code, Info{Hostname: "pc2"})
	if err != nil || name != "main" {
		t.Fatalf("Join = %q, %v", name, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "member.json"))
	if strings.Contains(string(b), "host-token") || !strings.Contains(string(b), `"credential"`) {
		t.Fatalf("member.json: %s", b)
	}
	hb, _ := os.ReadFile(filepath.Join(h.Dir, "host.json"))
	var st memberState
	_ = json.Unmarshal(b, &st)
	if strings.Contains(string(hb), st.Credential) {
		t.Fatal("host.json keeps the credential itself, not its hash")
	}
	if ms, _ := h.Machines(); len(ms) != 1 || ms[0].Info.Hostname != "pc2" {
		t.Fatalf("Machines = %+v", ms)
	}

	t.Run("reused code", func(t *testing.T) {
		if _, err := Join(ctx, t.TempDir(), code, Info{}); err == nil || err.Error() != ErrUsedCode.Error() {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("already joined", func(t *testing.T) {
		if _, err := Join(ctx, dir, invite(t, h), Info{}); err == nil || !strings.Contains(err.Error(), "leave") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("expired code", func(t *testing.T) {
		code := invite(t, h)
		_ = h.update(func(s *hostState) error {
			for i := range s.Invites {
				if !s.Invites[i].Used {
					s.Invites[i].Expires = time.Now().Add(-time.Minute)
				}
			}
			return nil
		})
		if _, err := Join(ctx, t.TempDir(), code, Info{}); err == nil || err.Error() != ErrExpiredCode.Error() {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unknown secret", func(t *testing.T) {
		c, _ := ParseCode(invite(t, h))
		c.Secret = "guess"
		s, _ := c.Encode()
		if _, err := Join(ctx, t.TempDir(), s, Info{}); err == nil || err.Error() != ErrUnknownCode.Error() {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("wrong fingerprint", func(t *testing.T) {
		c, _ := ParseCode(invite(t, h))
		c.Fingerprint = strings.Repeat("00", 32)
		s, _ := c.Encode()
		d := t.TempDir()
		if _, err := Join(ctx, d, s, Info{}); err == nil || !strings.Contains(err.Error(), ErrFingerprint.Error()) {
			t.Fatalf("err = %v", err)
		}
		if _, err := os.Stat(filepath.Join(d, "member.json")); err == nil {
			t.Fatal("saved a link to a host with the wrong certificate")
		}
	})
	if ms, _ := h.Machines(); len(ms) != 1 {
		t.Fatalf("failed joins added machines: %+v", ms)
	}
}

func TestInviteNeedsRunningHost(t *testing.T) {
	h := &Host{Dir: t.TempDir()}
	if _, _, err := h.Invite(); err == nil {
		t.Fatal("Invite worked with the host off")
	}
}

func TestCallAndAllowlist(t *testing.T) {
	h := newHost(t)
	var mu sync.Mutex
	var got []string
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write(b)
	})
	m := connect(t, h, api)
	id := memberID(t, m)
	ctx := context.Background()

	status, body, err := h.Call(ctx, id, "POST", "/api/runners/octo-repo/restart", []byte(`{"x":1}`))
	if err != nil || status != http.StatusAccepted || string(body) != `{"x":1}` {
		t.Fatalf("Call = %d %s %v", status, body, err)
	}
	for _, c := range [][2]string{
		{"POST", "/api/login/token"},
		{"POST", "/api/logout"},
		{"POST", "/api/service/uninstall"},
		{"GET", "/api/fs?path=/"},
		{"POST", "/api/adopt"},
		{"PUT", "/api/config"},
		{"POST", "/api/runners-dir"},
		{"POST", "/api/sync/apply"},
		{"GET", "/api/runners/../../etc/log"},
		{"GET", "/api/runners/%2e%2e/log"},
		{"DELETE", "/api/runners/x/remove"},
	} {
		if _, _, err := h.Call(ctx, id, c[0], c[1], nil); err == nil {
			t.Errorf("host sent %s %s", c[0], c[1])
		}
		// A host that skips its own check still gets nothing from the member.
		if r := m.handleAPI(frame{Kind: kindAPI, Method: c[0], Path: c[1]}); r.Status != http.StatusForbidden {
			t.Errorf("member ran %s %s: %d", c[0], c[1], r.Status)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("member API got %v", got)
	}
	if _, _, err := h.Call(ctx, "nope", "GET", "/api/state", nil); err != ErrOffline {
		t.Fatalf("offline err = %v", err)
	}
}

func memberID(t *testing.T, m *Member) string {
	t.Helper()
	st, ok, err := loadMember(m.Dir)
	if !ok {
		t.Fatal(err)
	}
	return st.MemberID
}

func TestBrokeredGitHub(t *testing.T) {
	var mu sync.Mutex
	var auth []string
	seen := func() []string { mu.Lock(); defer mu.Unlock(); return append([]string{}, auth...) }
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		switch r.URL.Path {
		case "/repos/octo/app/actions/runners/registration-token":
			_, _ = w.Write([]byte(`{"token":"REG","expires_at":"2030-01-01T00:00:00Z"}`))
		case "/user":
			w.Header().Set("X-OAuth-Scopes", "repo")
			_, _ = w.Write([]byte(`{"login":"octo"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer gh.Close()
	old := github.APIBase
	github.APIBase = gh.URL
	defer func() { github.APIBase = old }()

	h := newHost(t)
	m := connect(t, h, nil)
	ctx := context.Background()
	c := m.Client()

	tok, err := c.RegistrationToken(ctx, github.Scope{Owner: "octo", Repo: "app"})
	if err != nil || tok != "REG" {
		t.Fatalf("RegistrationToken = %q, %v", tok, err)
	}
	u, scopes, err := c.Me(ctx)
	if err != nil || u.Login != "octo" || scopes != "repo" {
		t.Fatalf("Me = %+v %q %v", u, scopes, err)
	}
	if a := seen(); len(a) != 2 || !strings.HasSuffix(a[0], "Bearer host-token") {
		t.Fatalf("GitHub saw %v", a)
	}

	// Calls gh-runners never makes are refused before GitHub sees them.
	for _, p := range [][2]string{
		{"GET", "/repos/octo/app/actions/secrets"},
		{"DELETE", "/repos/octo/app"},
		{"GET", "/user/../repos/octo/app/actions/secrets"},
		{"POST", "/user/repos"},
		{"GET", "/repos/octo/app/contents/src/main.go"},
	} {
		r := h.broker(frame{Kind: kindGitHub, Method: p[0], Path: p[1]})
		if r.Status != http.StatusForbidden {
			t.Errorf("%s %s: %d", p[0], p[1], r.Status)
		}
	}
	if a := seen(); len(a) != 2 {
		t.Fatalf("GitHub saw %v", a)
	}

	// A host that is not signed in says so.
	h.Token = func(context.Context) (string, error) { return "", nil }
	if _, err := c.RegistrationToken(ctx, github.Scope{Owner: "octo", Repo: "app"}); err == nil || !strings.Contains(err.Error(), "not signed in") {
		t.Fatalf("err = %v", err)
	}
}

func TestRemoveAndLeave(t *testing.T) {
	h := newHost(t)
	m := connect(t, h, nil)
	id := memberID(t, m)
	if err := h.Remove(id); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return !m.Joined() })
	if s := m.Status(); s.Connected || s.Error != ErrRevoked.Error() {
		t.Fatalf("status after remove = %+v", s)
	}
	if ms, _ := h.Machines(); len(ms) != 0 {
		t.Fatalf("Machines = %+v", ms)
	}

	m2 := connect(t, h, nil)
	told, err := m2.Leave(context.Background())
	if err != nil || !told {
		t.Fatalf("Leave = %v, %v", told, err)
	}
	waitFor(t, func() bool { ms, _ := h.Machines(); return len(ms) == 0 })
	if _, _, err := m2.Client().Me(context.Background()); err == nil {
		t.Fatal("GitHub calls still work after leaving")
	}
}

func TestReconnect(t *testing.T) {
	h := newHost(t)
	m := connect(t, h, nil)
	_, port := h.Running()
	h.Stop()
	waitFor(t, func() bool { return !m.Status().Connected })
	if !m.Joined() {
		t.Fatal("a stopped host made the member forget it")
	}
	if err := h.Start("127.0.0.1:" + strconv.Itoa(port)); err != nil {
		t.Skipf("port %d taken again: %v", port, err)
	}
	m.Reload()
	waitFor(t, func() bool { return m.Status().Connected })
	if ms, _ := h.Machines(); len(ms) != 1 || !ms[0].Online {
		t.Fatalf("Machines = %+v", ms)
	}
}

func TestHostStateIsPrivate(t *testing.T) {
	h := newHost(t)
	_ = invite(t, h)
	var s hostState
	if err := store.ReadJSON(filepath.Join(h.Dir, "host.json"), &s); err != nil || len(s.Invites) != 1 || len(s.Invites[0].Hash) != 64 {
		t.Fatalf("host.json = %+v, %v", s, err)
	}
}
