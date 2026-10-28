package fleet

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/localci"
)

func tinyTarball(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("hello from the repo\n")
	_ = tw.WriteHeader(&tar.Header{Name: "o-r-abc1234/", Typeflag: tar.TypeDir, Mode: 0o755})
	_ = tw.WriteHeader(&tar.Header{Name: "o-r-abc1234/data.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// stubGitHub serves the tarball and counts hits.
func stubGitHub(t *testing.T, hits *int) {
	t.Helper()
	data := tinyTarball(t)
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		if !strings.Contains(r.URL.Path, "/tarball/") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	old := github.APIBase
	github.APIBase = gh.URL
	t.Cleanup(func() { github.APIBase = old; gh.Close() })
}

// connectCI joins a CI-enabled member; cancel drops it.
func connectCI(t *testing.T, h *Host) (*Member, context.CancelFunc) {
	t.Helper()
	dir := t.TempDir()
	if _, err := Join(context.Background(), dir, invite(t, h), Info{Hostname: "pc2"}); err != nil {
		t.Fatal(err)
	}
	m := &Member{
		Dir: dir, CIDir: t.TempDir(),
		CITools:      func() []string { return []string{"bash", "node"} },
		CIEmulatorOK: func() bool { return false },
		Info:         func() Info { return Info{Hostname: "pc2", OS: "linux", Arch: "amd64"} },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, func() bool { return m.Status().Connected })
	return m, cancel
}

func ciMemberID(t *testing.T, h *Host) string {
	t.Helper()
	var id string
	waitFor(t, func() bool {
		ms := h.CIMachines()
		if len(ms) == 1 {
			id = ms[0].ID
			return true
		}
		return false
	})
	return id
}

func spec(t *testing.T, script string) localci.JobSpec {
	return localci.JobSpec{
		Name:   "build",
		Job:    &localci.Job{Name: "build", RunsOn: "ubuntu-latest", Steps: []localci.Step{{Run: script}}},
		Log:    filepath.Join(t.TempDir(), "runs", "build.log"),
		RunEnv: localci.RunEnv{Repo: "o/r", SHA: "abc1234", Ref: "refs/heads/x"},
	}
}

func TestBrokerRefusesTarballOfUnconfiguredRepo(t *testing.T) {
	hits := 0
	stubGitHub(t, &hits)
	h := newHost(t)
	h.CIRepos = func() []string { return []string{"o/r"} }

	other := h.broker(frame{Kind: kindGitHub, Method: "GET", Path: "/repos/evil/secret/tarball/abc1234"})
	if other.Status != http.StatusForbidden {
		t.Fatalf("unconfigured repo: status %d, want 403", other.Status)
	}
	if hits != 0 {
		t.Fatal("the host asked GitHub for a repo that is not in the CI config")
	}
	ok := h.broker(frame{Kind: kindGitHub, Method: "GET", Path: "/repos/O/R/tarball/abc1234"})
	if ok.Status != http.StatusOK || len(ok.Body) == 0 {
		t.Fatalf("configured repo: status %d", ok.Status)
	}
	// With no CI repos at all, nothing is handed out.
	h.CIRepos = nil
	if r := h.broker(frame{Kind: kindGitHub, Method: "GET", Path: "/repos/o/r/tarball/abc1234"}); r.Status != http.StatusForbidden {
		t.Fatalf("no config: status %d", r.Status)
	}
}

func TestRemoteExecutorEndToEnd(t *testing.T) {
	hits := 0
	stubGitHub(t, &hits)
	h := newHost(t)
	h.CIRepos = func() []string { return []string{"o/r"} }
	connectCI(t, h)
	id := ciMemberID(t, h)

	s := spec(t, "cat data.txt && echo repo=$GITHUB_REPOSITORY")
	r := RemoteExecutor{Host: h, MemberID: id}.RunJob(context.Background(), s)
	if r.Status != localci.Pass {
		t.Fatalf("status %v (%s)", r.Status, r.Reason)
	}
	log, err := os.ReadFile(s.Log)
	if err != nil || !strings.Contains(string(log), "hello from the repo") {
		t.Fatalf("log under the run dir = %q, %v", log, err)
	}

	bad := spec(t, "echo nope; exit 3")
	bad.Log = filepath.Join(filepath.Dir(s.Log), "bad.log")
	if r := (RemoteExecutor{Host: h, MemberID: id}).RunJob(context.Background(), bad); r.Status != localci.Fail {
		t.Fatalf("failing job: status %v", r.Status)
	}
	if b, _ := os.ReadFile(bad.Log); !strings.Contains(string(b), "nope") {
		t.Fatalf("failing log = %q", b)
	}
}

func TestRemoteExecutorMemberDropsMidJob(t *testing.T) {
	hits := 0
	stubGitHub(t, &hits)
	h := newHost(t)
	h.CIRepos = func() []string { return []string{"o/r"} }
	m, drop := connectCI(t, h)
	id := ciMemberID(t, h)
	// The member's job winds down after the drop; Windows cannot delete the
	// temp dir while it still holds the log open.
	t.Cleanup(func() {
		waitFor(t, func() bool {
			return m.ciRun().busy() == 0
		})
	})

	s := spec(t, "echo started; sleep 30")
	go func() {
		time.Sleep(1500 * time.Millisecond)
		drop()
	}()
	start := time.Now()
	r := RemoteExecutor{Host: h, MemberID: id}.RunJob(context.Background(), s)
	if r.Status != localci.Fail || !strings.Contains(r.Reason, "machine went offline") {
		t.Fatalf("got %v %q, want FAIL machine went offline", r.Status, r.Reason)
	}
	if time.Since(start) > 20*time.Second {
		t.Fatal("the host waited for the dead machine")
	}
}

func TestRemoteExecutorUnknownMember(t *testing.T) {
	h := newHost(t)
	r := RemoteExecutor{Host: h, MemberID: "nobody"}.RunJob(context.Background(), spec(t, "true"))
	if r.Status != localci.Fail || !strings.Contains(r.Reason, "offline") {
		t.Fatalf("got %v %q", r.Status, r.Reason)
	}
}
