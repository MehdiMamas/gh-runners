package localci

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

func shortWait(t *testing.T) {
	t.Helper()
	w, p := PortWait, PortPoll
	PortWait, PortPoll = 150*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { PortWait, PortPoll = w, p })
}

func hold(t *testing.T) (net.Listener, int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l, l.Addr().(*net.TCPAddr).Port
}

func TestParseEmulatorPorts(t *testing.T) {
	got := ParseEmulatorPorts([]byte(`{"emulators":{"hub":{"port":4400},"auth":{"port":9099},"firestore":{"port":8080,"host":"x"},"ui":{"enabled":true},"singleProjectMode":true}}`))
	if want := []int{4400, 8080, 9099}; !reflect.DeepEqual(got, want) {
		t.Errorf("ports = %v, want %v", got, want)
	}
	if got := ParseEmulatorPorts([]byte(`not json`)); got != nil {
		t.Errorf("bad json = %v", got)
	}
}

func TestEmulatorPortsFilesAndDefaults(t *testing.T) {
	root := t.TempDir()
	if got := EmulatorPorts(root); !reflect.DeepEqual(got, DefaultEmulatorPorts) {
		t.Errorf("empty tree = %v, want defaults", got)
	}
	write := func(p, s string) {
		p = filepath.Join(root, p)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o644)
	}
	write("firebase.json", `{"emulators":{"hub":{"port":4401}}}`)
	write("legacy/firebase.e2e.json", `{"emulators":{"auth":{"port":9100}}}`)
	write("node_modules/x/firebase.json", `{"emulators":{"auth":{"port":1234}}}`)
	if got, want := EmulatorPorts(root), []int{4401, 9100}; !reflect.DeepEqual(got, want) {
		t.Errorf("ports = %v, want %v", got, want)
	}
}

func TestParseLsof(t *testing.T) {
	if n, p := parseLsof("p61610\ncjava\n"); n != "java" || p != "61610" {
		t.Errorf("got %q %q", n, p)
	}
}

func TestCheckPorts(t *testing.T) {
	shortWait(t)
	_, port := hold(t)
	msg := CheckPorts(context.Background(), []int{port})
	if !strings.Contains(msg, "port "+strconv.Itoa(port)) || !strings.Contains(msg, "not started by gh-runners") {
		t.Errorf("message = %q", msg)
	}
	l, free := hold(t)
	l.Close()
	if msg := CheckPorts(context.Background(), []int{free}); msg != "" {
		t.Errorf("free port gave %q", msg)
	}
}

func TestCheckPortsWaitsForRelease(t *testing.T) {
	PortWait, PortPoll = 2*time.Second, 20*time.Millisecond
	t.Cleanup(func() { PortWait, PortPoll = 60*time.Second, 2*time.Second })
	l, port := hold(t)
	go func() { time.Sleep(100 * time.Millisecond); l.Close() }()
	if msg := CheckPorts(context.Background(), []int{port}); msg != "" {
		t.Errorf("port freed in time but got %q", msg)
	}
}

func makeTarball(t *testing.T, port int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "src.tgz")
	f, _ := os.Create(p)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	body := `{"emulators":{"auth":{"port":` + strconv.Itoa(port) + `}}}`
	tw.WriteHeader(&tar.Header{Name: "repo-abc/firebase.json", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
	tw.Write([]byte(body))
	tw.Close()
	gz.Close()
	f.Close()
	return p
}

const emuWorkflow = `name: CI
on: [pull_request]
jobs:
  rules:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-java@v4
      - run: echo ok
  ci-passed:
    runs-on: ubuntu-latest
    needs: [rules]
    if: always()
    steps:
      - run: echo "${{ join(needs.*.result, ',') }}"
`

func TestExecutorBlockedWhenPortTaken(t *testing.T) {
	shortWait(t)
	wf, err := ParseWorkflow([]byte(emuWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	_, port := hold(t)
	dir := t.TempDir()
	res := LocalExecutor{}.RunJob(context.Background(), JobSpec{
		Name: "rules", Job: wf.Jobs["rules"], Tarball: makeTarball(t, port),
		RunDir: filepath.Join(dir, "rules"), Log: filepath.Join(dir, "rules.log"),
	})
	if res.Status != Blocked || !strings.Contains(res.Reason, "port "+strconv.Itoa(port)) {
		t.Fatalf("got %v %q", res.Status, res.Reason)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "rules.log")); !strings.Contains(string(b), "port") {
		t.Errorf("log lacks the port: %q", b)
	}
}

type blockedExec struct{}

func (blockedExec) RunJob(ctx context.Context, s JobSpec) Result {
	return Result{Job: s.Name, Status: Blocked, Reason: "port 9000 is used by java (pid 61610), not started by gh-runners"}
}

func TestBlockedPostsAndMergesNothing(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	var mu sync.Mutex
	var writes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != http.MethodGet {
			writes = append(writes, r.Method+" "+r.URL.Path)
			w.Write([]byte(`{}`))
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/7"):
			w.Write([]byte(`{"number":7,"title":"t","state":"open","user":{"login":"me"},"head":{"sha":"abcdef1234","repo":{"full_name":"o/r"}},"base":{"repo":{"full_name":"o/r"}}}`))
		case strings.Contains(r.URL.Path, "/contents/"):
			json.NewEncoder(w).Encode(map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(emuWorkflow))})
		case strings.HasSuffix(r.URL.Path, "/files"):
			w.Write([]byte(`[]`))
		case strings.Contains(r.URL.Path, "/tarball/"):
			b, _ := os.ReadFile(makeTarball(t, 1))
			w.Write(b)
		default:
			w.Write([]byte(`[]`))
		}
	}))
	defer srv.Close()
	c := github.New("tok")
	c.BaseURL = srv.URL
	var out strings.Builder
	rep, err := Run(context.Background(), Options{
		Client: c, Owner: "o", Repo: "r", PR: 7, Login: "me", Out: &out, Exec: blockedExec{}, Conc: 1,
		Settings: store.CIRepo{Post: true, Merge: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.HasBlocked() || rep.AllPassed() || rep.Merged {
		t.Fatalf("report = %+v", rep)
	}
	got := map[string]Status{}
	for _, r := range rep.Results {
		got[r.Job] = r.Status
	}
	if got["rules"] != Blocked || got["ci-passed"] != Blocked {
		t.Errorf("statuses = %v", got)
	}
	for _, w := range writes {
		if strings.Contains(w, "/merge") || strings.Contains(w, "/comments") {
			t.Errorf("unexpected write %s", w)
		}
	}
	if !strings.Contains(out.String(), "BLOCKED") {
		t.Errorf("output lacks BLOCKED:\n%s", out.String())
	}
}

func TestPortFreeSeesWildcardHolder(t *testing.T) {
	l, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Skip(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	if portFree(port) {
		t.Fatalf("port %d held on 0.0.0.0 reported free", port)
	}
	l.Close()
	if !portFree(port) {
		t.Fatalf("port %d released but reported taken", port)
	}
}

func TestNeedsEmulatorWithoutJava(t *testing.T) {
	wf, err := ParseWorkflow([]byte(`
name: ci
on: pull_request
jobs:
  rules:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: npm run test:rules
  viaexec:
    runs-on: ubuntu-latest
    steps:
      - run: cd firebase && firebase emulators:exec --only firestore "node t.js"
  plain:
    runs-on: ubuntu-latest
    steps:
      - run: npm test
`))
	if err != nil {
		t.Fatal(err)
	}
	if wf.Jobs["viaexec"].NeedsEmulator() == false || wf.Jobs["plain"].NeedsEmulator() || wf.Jobs["rules"].NeedsEmulator() {
		t.Fatal("emulator detection wrong")
	}
	if _, emu := wf.Jobs["viaexec"].Requires(); !emu {
		t.Fatal("Requires misses emulators:exec job")
	}
}

// Two emulator jobs of one run never overlap, and an outside holder of 8080
// style ports blocks the job instead of failing it.
func TestEmulatorJobsSerializedAndBlocked(t *testing.T) {
	shortWait(t)
	wf, err := ParseWorkflow([]byte(emuWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Skip(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	tb := makeTarball(t, port)
	dir := t.TempDir()
	var cur, max int32
	tasks := []Task{{Name: "a", Lock: "emulator"}, {Name: "b", Lock: "emulator"}}
	res := Schedule(context.Background(), tasks, 2, &Locks{Dir: t.TempDir()}, func(ctx context.Context, tk Task, _ map[string]Result) Result {
		n := atomic.AddInt32(&cur, 1)
		for {
			m := atomic.LoadInt32(&max)
			if n <= m || atomic.CompareAndSwapInt32(&max, m, n) {
				break
			}
		}
		defer atomic.AddInt32(&cur, -1)
		return LocalExecutor{}.RunJob(ctx, JobSpec{
			Name: tk.Name, Job: wf.Jobs["rules"], Tarball: tb,
			RunDir: filepath.Join(dir, tk.Name), Log: filepath.Join(dir, tk.Name+".log"),
		})
	})
	if max != 1 {
		t.Errorf("%d emulator jobs ran at once", max)
	}
	for n, r := range res {
		if r.Status != Blocked {
			t.Errorf("%s: got %v %q, want BLOCKED", n, r.Status, r.Reason)
		}
	}
}

func TestHolderOf(t *testing.T) {
	root := "/h/ci"
	cwds := map[int]string{
		10: "/h/ci/fleet/jobs/abcd/src/functions",
		11: "/h/ci/octo-repo/pr7-abc1234-1/build/src",
		12: "/home/me/other",
		13: "/h/ci/fleet/jobs/dead (deleted)",
	}
	cwdOf := func(pid int) string { return cwds[pid] }
	live := func(f string) bool { return f == filepath.FromSlash("/h/ci/fleet/jobs/abcd") }
	for _, c := range []struct {
		pid  string
		want holder
	}{
		{"10", holder{Live: filepath.FromSlash("/h/ci/fleet/jobs/abcd")}},
		{"11", holder{Stale: filepath.FromSlash("/h/ci/octo-repo/pr7-abc1234-1")}},
		{"12", holder{}},
		{"13", holder{Stale: filepath.FromSlash("/h/ci/fleet/jobs/dead")}},
		{"", holder{}},
		{"99", holder{}},
	} {
		if got := holderOf(root, c.pid, cwdOf, live); got != c.want {
			t.Errorf("pid %q: %+v, want %+v", c.pid, got, c.want)
		}
	}
}
