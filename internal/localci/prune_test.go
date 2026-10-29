package localci

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/store"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func mkRun(t *testing.T, ci, repo, name string) string {
	t.Helper()
	p := filepath.Join(ci, repo, name, "logs")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "a.log"), []byte("log"), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(p)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestPruneRuns(t *testing.T) {
	ci := t.TempDir()
	old := time.Now().Add(-20 * 24 * time.Hour).Unix()
	fresh := time.Now().Add(-time.Hour).Unix()
	name := func(pr int, ts int64) string {
		return "pr" + itoa(pr) + "-abcdef1-" + itoa64(ts)
	}
	oldRun := mkRun(t, ci, "o-r", name(1, old))
	freshRun := mkRun(t, ci, "o-r", name(2, fresh))
	runningRun := mkRun(t, ci, "o-r", name(3, old))
	activeRun := mkRun(t, ci, "o-r", name(4, old))
	for _, d := range []string{"running", "active", "locks", "fleet"} {
		if err := os.MkdirAll(filepath.Join(ci, d, name(9, old)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(ci, "running", "1-1.json"), `{"id":"`+filepath.Base(runningRun)+`","repo":"o/r","pr":3}`)
	write(filepath.Join(ci, "active", "1-1.json"), `{"repo":"o/r","pr":4,"job":"x"}`)
	write(filepath.Join(ci, "history.json"), "{}")

	freed := PruneRuns(ci, CIRunAge)
	if exists(oldRun) {
		t.Error("old run kept")
	}
	if freed == 0 {
		t.Error("freed 0 bytes")
	}
	for _, p := range []string{freshRun, runningRun, activeRun, filepath.Join(ci, "history.json"),
		filepath.Join(ci, "running", name(9, old)), filepath.Join(ci, "active", name(9, old)),
		filepath.Join(ci, "locks", name(9, old)), filepath.Join(ci, "fleet", name(9, old))} {
		if !exists(p) {
			t.Errorf("%s was removed", p)
		}
	}
}

func itoa(n int) string     { return itoa64(int64(n)) }
func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

// srcExec leaves a checkout behind like a real job and then fails.
type srcExec struct{}

func (srcExec) RunJob(ctx context.Context, s JobSpec) Result {
	_ = os.MkdirAll(filepath.Join(s.RunDir, "src"), 0o755)
	_ = os.WriteFile(filepath.Join(s.RunDir, "src", "f.txt"), []byte("x"), 0o644)
	return Result{Job: s.Name, Status: Fail, Reason: "boom"}
}

func TestRunRemovesSrcAndTarballOnFail(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GH_RUNNERS_HOME", home)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/7"):
			w.Write([]byte(`{"number":7,"title":"t","state":"open","user":{"login":"me"},"head":{"sha":"abcdef1234","repo":{"full_name":"o/r"}},"base":{"repo":{"full_name":"o/r"}}}`))
		case strings.Contains(r.URL.Path, "/contents/"):
			json.NewEncoder(w).Encode(map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(emuWorkflow))})
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
		Client: c, Owner: "o", Repo: "r", PR: 7, Login: "me", Out: &out, Exec: srcExec{}, Conc: 1,
		Settings: store.CIRepo{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Dir == "" {
		t.Fatal("no run dir")
	}
	srcs, _ := filepath.Glob(filepath.Join(rep.Dir, "*", "src"))
	if len(srcs) != 0 {
		t.Errorf("src kept after a failed job: %v", srcs)
	}
	if exists(filepath.Join(rep.Dir, "source.tgz")) {
		t.Error("tarball kept after the run")
	}
}
