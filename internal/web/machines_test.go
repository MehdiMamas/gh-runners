package web

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/fleet"
	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/runner"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// fleetTest is a main host and one member in this process. The member's
// dashboard uses this test's home and is not signed in to GitHub; the
// host's token is good-token.
type fleetTest struct {
	host   http.Handler // the main host's dashboard
	member *Server
	id     string // the member's id on the host
	busy   atomic.Bool

	mu   sync.Mutex
	seen []string // GitHub calls: "METHOD /path Authorization"
}

func (f *fleetTest) github() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.seen)
}

func newFleetTest(t *testing.T) *fleetTest {
	t.Helper()
	setup(t)
	f := &fleetTest{}

	// GitHub: the calls below here, the rest from fakeGitHub.
	pkg, sum := runnerPackage(t)
	osName, arch, err := runner.Platform()
	if err != nil {
		t.Skip(err)
	}
	upstream, _ := url.Parse(github.APIBase)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	var gh *httptest.Server
	gh = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/pkg/") {
			f.mu.Lock()
			f.seen = append(f.seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
			f.mu.Unlock()
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/octo/private":
			_, _ = io.WriteString(w, `{"name":"private","private":true,"owner":{"login":"octo"},"permissions":{"admin":true}}`)
		case "GET /repos/octo/private/actions/runners/downloads":
			fmt.Fprintf(w, `[{"os":%q,"architecture":%q,"filename":"actions-runner-test-2.999.0.tar.gz","download_url":"%s/pkg/runner.tar.gz","sha256_checksum":%q}]`, osName, arch, gh.URL, sum)
		case "GET /pkg/runner.tar.gz":
			_, _ = w.Write(pkg)
		case "POST /repos/octo/private/actions/runners/registration-token", "POST /repos/octo/private/actions/runners/remove-token":
			_, _ = io.WriteString(w, `{"token":"RUNNER-TOKEN","expires_at":"2030-01-01T00:00:00Z"}`)
		case "GET /repos/octo/private/actions/runners":
			fmt.Fprintf(w, `{"total_count":1,"runners":[{"id":7,"name":"laptop","status":"online","busy":%v,"labels":[]}]}`, f.busy.Load())
		case "DELETE /repos/octo/private/actions/runners/7":
			w.WriteHeader(http.StatusNoContent)
		default:
			proxy.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(gh.Close)
	old := github.APIBase
	github.APIBase = gh.URL
	t.Cleanup(func() { github.APIBase = old })

	h := &fleet.Host{
		Dir:   t.TempDir(),
		Name:  "main",
		Addrs: func() []string { return []string{"127.0.0.1"} },
		Token: func(context.Context) (string, error) { return "good-token", nil },
	}
	if err := h.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)
	hs := New(Hooks{Version: "test", Host: h}, secret)
	f.host = hs.Handler(http.NotFoundHandler())

	m := &fleet.Member{Dir: t.TempDir()}
	f.member = New(Hooks{Version: "test", Member: m, Status: func() control.Status { return control.Status{PID: 42} }}, secret)
	f.member.spawn = func(string, ...string) error { return nil }
	m.API = f.member.FleetAPI()
	m.Info = func() fleet.Info {
		info := fleet.Info{Hostname: "pc2", OS: "linux", Arch: "amd64", Version: "test"}
		rs, _ := store.List()
		for _, r := range rs {
			info.Runners = append(info.Runners, fleet.RunnerInfo{ID: r.ID, Scope: r.Scope(), Name: r.Name, State: "running"})
		}
		return info
	}

	var inv struct {
		Code, Windows, Unix string
	}
	if code := api(t, f.host, "POST", "/api/machines/invite", "", &inv); code != 200 || inv.Code == "" {
		t.Fatalf("invite: %d %+v", code, inv)
	}
	if !strings.Contains(inv.Windows, inv.Code) || !strings.Contains(inv.Unix, "--join "+inv.Code) {
		t.Fatalf("commands: %+v", inv)
	}
	if _, err := fleet.Join(context.Background(), m.Dir, inv.Code, m.Info()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	var mv machinesView
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		api(t, f.host, "GET", "/api/machines", "", &mv)
		if len(mv.Machines) == 1 && mv.Machines[0].Online || time.Now().After(deadline) {
			break
		}
	}
	if len(mv.Machines) != 1 || !mv.Machines[0].Online || mv.Machines[0].Info.Hostname != "pc2" {
		t.Fatalf("machines: %+v", mv)
	}
	f.id = mv.Machines[0].ID
	return f
}

// runnerPackage is a runner archive with no config script in it.
func runnerPackage(t *testing.T) ([]byte, string) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	body := []byte("not a real runner\n")
	_ = tw.WriteHeader(&tar.Header{Name: "README.txt", Mode: 0o644, Size: int64(len(body))})
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = zw.Close()
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

func (f *fleetTest) remote(t *testing.T, method, path, body string, out any) int {
	t.Helper()
	return api(t, f.host, method, "/api/machines/"+f.id+path, body, out)
}

func (f *fleetTest) waitTask(t *testing.T, id int) taskView {
	t.Helper()
	var tv taskView
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		f.remote(t, "GET", fmt.Sprintf("/api/tasks/%d", id), "", &tv)
		if tv.State == "done" || tv.State == "failed" || time.Now().After(deadline) {
			return tv
		}
	}
}

func TestMachineSignedInThroughHost(t *testing.T) {
	f := newFleetTest(t)
	var st stateView
	if code := f.remote(t, "GET", "/api/state", "", &st); code != 200 {
		t.Fatalf("state: %d", code)
	}
	if !st.Auth.SignedIn || st.Auth.Login != "octo" || st.Auth.Source != "main host main" {
		t.Fatalf("auth on the member: %+v", st.Auth)
	}
	if g := f.github(); len(g) == 0 || g[0] != "GET /user Bearer good-token" {
		t.Fatalf("GitHub saw %v", g)
	}
	// The host's token is nowhere on the member.
	_ = filepath.WalkDir(store.Home(), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte("good-token")) {
				t.Errorf("%s holds the host's token", p)
			}
		}
		return nil
	})
}

func TestMachineAllowlist(t *testing.T) {
	f := newFleetTest(t)
	for _, c := range [][2]string{
		{"POST", "/api/login/token"},
		{"POST", "/api/logout"},
		{"GET", "/api/login/device"},
		{"POST", "/api/service/uninstall"},
		{"PUT", "/api/config"},
		{"GET", "/api/fs?path=/"},
		{"POST", "/api/fs/mkdir"},
		{"POST", "/api/runners-dir"},
		{"POST", "/api/adopt"},
		{"GET", "/api/doctor"},
		{"POST", "/api/sync/apply"},
		{"POST", "/api/machines/leave"},
		{"POST", "/api/machines/x/pause"},
	} {
		if code := f.remote(t, c[0], c[1], `{"token":"x"}`, nil); code != http.StatusForbidden {
			t.Errorf("host passed on %s %s: %d", c[0], c[1], code)
		}
		// The member refuses them on its own too.
		w := httptest.NewRecorder()
		f.member.FleetAPI().ServeHTTP(w, httptest.NewRequest(c[0], c[1], strings.NewReader(`{"token":"x"}`)))
		if w.Code != http.StatusForbidden {
			t.Errorf("member ran %s %s: %d", c[0], c[1], w.Code)
		}
	}
	if _, err := os.Stat(store.TokenPath()); err == nil {
		t.Fatal("a remote call saved a token on the member")
	}
}

func TestRemoteAddUsesHostGitHub(t *testing.T) {
	f := newFleetTest(t)
	var started struct {
		Task int `json:"task"`
	}
	if code := f.remote(t, "POST", "/api/add", `{"repos":["octo/private"],"labels":["heavy"]}`, &started); code != 202 && code != 200 {
		t.Fatalf("add: %d", code)
	}
	tv := f.waitTask(t, started.Task)
	// The test package has no config script, so registering stops there,
	// after the host handed over a registration token.
	if tv.State != "failed" || !strings.Contains(tv.Log+tv.Error, "config script") {
		t.Fatalf("task: %+v", tv)
	}
	g := f.github()
	for _, want := range []string{
		"GET /repos/octo/private Bearer good-token",
		"GET /repos/octo/private/actions/runners/downloads Bearer good-token",
		"POST /repos/octo/private/actions/runners/registration-token Bearer good-token",
	} {
		if !slices.Contains(g, want) {
			t.Errorf("GitHub did not see %q: %v", want, g)
		}
	}
	if rn, err := store.Load("octo__private"); err != nil || !slices.Contains(rn.Labels, "heavy") {
		t.Fatalf("runner on the member: %+v %v", rn, err)
	}
}

func TestRemoteRestartBusy(t *testing.T) {
	f := newFleetTest(t)
	rn := store.Runner{ID: "octo__private", Kind: store.KindRepo, Owner: "octo", Repo: "private", Name: "laptop"}
	if err := store.Save(rn); err != nil {
		t.Fatal(err)
	}
	f.member.h.Reload()
	f.busy.Store(true)

	var e struct {
		Error   string   `json:"error"`
		Busy    []string `json:"busy"`
		Confirm bool     `json:"confirm"`
	}
	for _, p := range []string{"/api/runners/octo__private/restart", "/api/runners/octo__private/remove", "/api/update"} {
		e.Busy = nil
		if code := f.remote(t, "POST", p, "", &e); code != http.StatusConflict || !e.Confirm || len(e.Busy) != 1 || !strings.Contains(e.Busy[0], "octo__private") && !strings.Contains(e.Busy[0], "laptop") {
			t.Fatalf("%s while busy: %d %+v", p, code, e)
		}
	}
	if r, _ := store.Load(rn.ID); r.Disabled {
		t.Fatal("the runner was stopped before confirming")
	}

	var started struct {
		Task int `json:"task"`
	}
	if code := f.remote(t, "POST", "/api/runners/octo__private/restart?confirm_busy=1", "", &started); code != 202 {
		t.Fatalf("confirmed restart: %d", code)
	}
	if tv := f.waitTask(t, started.Task); tv.State != "done" || !strings.Contains(tv.Log, "Restarted octo/private (laptop)") {
		t.Fatalf("task: %+v", tv)
	}

	// Not busy: no question asked.
	f.busy.Store(false)
	if code := f.remote(t, "POST", "/api/runners/octo__private/restart", "", &started); code != 202 {
		t.Fatalf("restart when idle: %d", code)
	}
	f.waitTask(t, started.Task)
}

func TestRemoteRemove(t *testing.T) {
	f := newFleetTest(t)
	rn := store.Runner{ID: "octo__private", Kind: store.KindRepo, Owner: "octo", Repo: "private", Name: "laptop"}
	if err := store.Save(rn); err != nil {
		t.Fatal(err)
	}
	// Registered: removing unregisters it on GitHub.
	if err := os.MkdirAll(rn.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rn.Dir(), ".runner"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	var started struct {
		Task int `json:"task"`
	}
	if code := f.remote(t, "POST", "/api/runners/octo__private/remove", "", &started); code != 202 {
		t.Fatalf("remove: %d", code)
	}
	tv := f.waitTask(t, started.Task)
	if tv.State != "done" {
		t.Fatalf("task: %+v", tv)
	}
	if _, err := store.Load(rn.ID); err == nil {
		t.Fatal("the runner is still there")
	}
	if g := f.github(); !slices.Contains(g, "DELETE /repos/octo/private/actions/runners/7 Bearer good-token") {
		t.Fatalf("GitHub saw %v", g)
	}
}

func TestRemoveMachine(t *testing.T) {
	f := newFleetTest(t)
	if code := api(t, f.host, "POST", "/api/machines/"+f.id+"/remove", "", nil); code != 200 {
		t.Fatalf("remove machine: %d", code)
	}
	var mv machinesView
	api(t, f.host, "GET", "/api/machines", "", &mv)
	if len(mv.Machines) != 0 {
		t.Fatalf("machines: %+v", mv.Machines)
	}
	if code := f.remote(t, "GET", "/api/state", "", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("a removed machine still answers: %d", code)
	}
	for deadline := time.Now().Add(5 * time.Second); f.member.h.Member.Joined() && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
	}
	if f.member.h.Member.Joined() {
		t.Fatal("the member still has the host's link")
	}
}

func TestMainHostSwitch(t *testing.T) {
	setup(t)
	h := &fleet.Host{Dir: t.TempDir(), Addrs: func() []string { return []string{"127.0.0.1"} }}
	s := New(Hooks{Version: "test", Host: h}, secret)
	hh := s.Handler(http.NotFoundHandler())
	t.Cleanup(h.Stop)
	if code := api(t, hh, "POST", "/api/machines/invite", "", nil); code != http.StatusConflict {
		t.Fatalf("invite with the host off: %d", code)
	}
	cfg, _ := store.LoadConfig()
	cfg.FleetPort = freePort(t)
	_ = store.SaveConfig(cfg)
	var mv machinesView
	if code := api(t, hh, "POST", "/api/machines/host", `{"on":true}`, &mv); code != 200 || !mv.MainHost || !mv.Listening || mv.Port != cfg.FleetPort {
		t.Fatalf("turn on: %d %+v", code, mv)
	}
	if code := api(t, hh, "POST", "/api/machines/host", `{"on":false}`, &mv); code != 200 || mv.MainHost || mv.Listening {
		t.Fatalf("turn off: %d %+v", code, mv)
	}
	if cfg, _ := store.LoadConfig(); cfg.MainHost {
		t.Fatal("main_host still on in config.json")
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	h := &fleet.Host{Dir: t.TempDir()}
	if err := h.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	_, port := h.Running()
	h.Stop()
	if runtime.GOOS == "windows" {
		time.Sleep(50 * time.Millisecond)
	}
	return port
}
