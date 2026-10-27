package web

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/MehdiMamas/gh-runners/internal/localci"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

func TestCISettings(t *testing.T) {
	s, h := setup(t)
	s.ciDispatch = nil
	var got ciRepoView
	if code := api(t, h, "PUT", "/api/ci/o/r", `{"watch":true,"allow":[" @alice ",""]}`, &got); code != http.StatusOK {
		t.Fatalf("put: %d", code)
	}
	if got.Repo != "o/r" || !got.Watch || got.Post || got.Merge || len(got.Allow) != 1 || got.Allow[0] != "alice" {
		t.Fatalf("saved %+v", got)
	}
	cfg, _ := store.LoadConfig()
	if c := cfg.CI["o/r"]; !c.Watch || c.Post {
		t.Fatalf("config %+v", cfg.CI)
	}
	var v ciView
	if code := api(t, h, "GET", "/api/ci", "", &v); code != http.StatusOK || len(v.Repos) != 1 || v.Runs == nil {
		t.Fatalf("get: %d %+v", code, v)
	}
	// Everything off removes the entry.
	if code := api(t, h, "PUT", "/api/ci/o/r", `{}`, &got); code != http.StatusOK {
		t.Fatalf("put empty: %d", code)
	}
	cfg, _ = store.LoadConfig()
	if _, ok := cfg.CI["o/r"]; ok {
		t.Fatalf("entry kept: %+v", cfg.CI)
	}
}

func TestCIRunValidation(t *testing.T) {
	s, h := setup(t)
	s.ciDispatch = nil
	for _, body := range []string{`{"repo":"o","pr":1}`, `{"repo":"o/r/x","pr":1}`, `{"repo":"o/r","pr":0}`, `nope`} {
		if code := api(t, h, "POST", "/api/ci/run", body, nil); code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", body, code)
		}
	}
	if code := api(t, h, "GET", "/api/ci/tasks/999", "", nil); code != http.StatusNotFound {
		t.Errorf("missing task: got %d", code)
	}
}

func TestCILogStaysInCIFolder(t *testing.T) {
	s, h := setup(t)
	s.ciDispatch = nil
	inside := filepath.Join(store.Home(), "ci", "o-r", "run", "logs", "a.log")
	_ = os.MkdirAll(filepath.Dir(inside), 0o755)
	_ = os.WriteFile(inside, []byte("hello\n"), 0o644)
	outside := filepath.Join(store.Home(), "config.json")
	s.addCIRun(ciRun{ID: "x", Repo: "o/r", PR: 1, Results: []localci.Result{
		{Job: "a", Log: inside}, {Job: "b", Log: outside}, {Job: "c", Log: filepath.Join(store.Home(), "ci", "..", "config.json")},
	}})
	var out map[string]string
	if code := api(t, h, "GET", "/api/ci/runs/x/logs/a", "", &out); code != http.StatusOK || out["text"] != "hello\n" && out["text"] != "hello" {
		t.Errorf("inside: %d %q", code, out["text"])
	}
	for _, job := range []string{"b", "c"} {
		if code := api(t, h, "GET", "/api/ci/runs/x/logs/"+job, "", nil); code != http.StatusForbidden {
			t.Errorf("%s: got %d, want 403", job, code)
		}
	}
	if code := api(t, h, "GET", "/api/ci/runs/x/logs/zz", "", nil); code != http.StatusNotFound {
		t.Errorf("missing job: got %d", code)
	}
}

func TestCIHistoryCapped(t *testing.T) {
	s, _ := setup(t)
	for i := 0; i < maxCIRuns+5; i++ {
		s.addCIRun(ciRun{ID: fmt.Sprint(i)})
	}
	runs := s.ciRuns()
	if len(runs) != maxCIRuns || runs[0].ID != fmt.Sprint(maxCIRuns+4) {
		t.Fatalf("got %d runs, newest %q", len(runs), runs[0].ID)
	}
}
