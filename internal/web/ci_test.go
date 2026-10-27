package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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
	for i := 0; i < localci.MaxHistory+5; i++ {
		s.addCIRun(ciRun{ID: fmt.Sprint(i)})
	}
	runs := s.ciRuns()
	if len(runs) != localci.MaxHistory || runs[0].ID != fmt.Sprint(localci.MaxHistory+4) {
		t.Fatalf("got %d runs, newest %q", len(runs), runs[0].ID)
	}
}

func TestUpdateRefusedWhileCIRuns(t *testing.T) {
	s, h := setup(t)
	s.h.UpdateNow = func() { t.Error("update started during a Local CI run") }
	release := make(chan struct{})
	if _, err := s.ci.add("Local CI: o/r #1", []string{"o/r"}, func(io.Writer) error { <-release; return nil }); err != nil {
		t.Fatal(err)
	}
	var e struct {
		Error string `json:"error"`
	}
	if code := api(t, h, "POST", "/api/update", "", &e); code != http.StatusConflict || e.Error != "A Local CI run is in progress; update when it finishes." {
		t.Fatalf("update during a run: %d %+v", code, e)
	}
	close(release)
}

// ciHold queues a run that waits for release or for its context to end.
func ciHold(t *testing.T, s *Server, release chan struct{}, started chan int) *task {
	t.Helper()
	var tk *task
	tk, err := s.ci.add("Local CI: o/r #1", []string{"o/r"}, func(w io.Writer) error {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		w.(*task).setCancel(cancel)
		if started != nil {
			started <- w.(*task).ID
		}
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestCIRunNextAndCancelQueued(t *testing.T) {
	s, h := setup(t)
	release, started := make(chan struct{}), make(chan int, 3)
	first := ciHold(t, s, release, started)
	if got := <-started; got != first.ID {
		t.Fatalf("started %d", got)
	}
	a, b := ciHold(t, s, release, started), ciHold(t, s, release, started)
	if code := api(t, h, "POST", fmt.Sprintf("/api/ci/tasks/%d/next", b.ID), "", nil); code != http.StatusOK {
		t.Fatalf("next: %d", code)
	}
	if code := api(t, h, "POST", fmt.Sprintf("/api/ci/tasks/%d/next", first.ID), "", nil); code != http.StatusConflict {
		t.Fatalf("next of a running task: %d", code)
	}
	if code := api(t, h, "POST", "/api/ci/tasks/999/next", "", nil); code != http.StatusNotFound {
		t.Fatalf("next of an unknown task: %d", code)
	}
	if code := api(t, h, "DELETE", fmt.Sprintf("/api/ci/tasks/%d", a.ID), "", nil); code != http.StatusOK {
		t.Fatalf("cancel queued: %d", code)
	}
	if v := a.view(false); v.State != "cancelled" || v.Ended == "" {
		t.Fatalf("queued task after cancel: %+v", v)
	}
	if code := api(t, h, "DELETE", fmt.Sprintf("/api/ci/tasks/%d", a.ID), "", nil); code != http.StatusConflict {
		t.Fatalf("cancel twice: %d", code)
	}
	if code := api(t, h, "DELETE", "/api/ci/tasks/999", "", nil); code != http.StatusNotFound {
		t.Fatalf("cancel unknown: %d", code)
	}
	close(release)
	if got := <-started; got != b.ID {
		t.Fatalf("task %d ran after the first, want the moved one %d", got, b.ID)
	}
	select {
	case got := <-started:
		t.Fatalf("cancelled task %d ran", got)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestCICancelRunning(t *testing.T) {
	s, h := setup(t)
	started := make(chan int, 1)
	tk := ciHold(t, s, make(chan struct{}), started)
	<-started
	if code := api(t, h, "DELETE", fmt.Sprintf("/api/ci/tasks/%d", tk.ID), "", nil); code != http.StatusOK {
		t.Fatalf("cancel running: %d", code)
	}
	for end := time.Now().Add(5 * time.Second); tk.view(false).State == "running" && time.Now().Before(end); {
		time.Sleep(10 * time.Millisecond)
	}
	if v := tk.view(false); v.State != "cancelled" || v.Error != "" {
		t.Fatalf("running task after cancel: %+v", v)
	}
}

func TestCITaskRoutesGuarded(t *testing.T) {
	_, h := setup(t)
	for _, c := range []struct{ method, path string }{{"POST", "/api/ci/tasks/1/next"}, {"DELETE", "/api/ci/tasks/1"}} {
		if w := do(t, h, req{method: c.method, path: c.path, cookie: true, origin: "http://127.0.0.1:47321"}); w.Code != http.StatusForbidden {
			t.Errorf("%s %s without X-GHR: %d", c.method, c.path, w.Code)
		}
		if w := do(t, h, req{method: c.method, path: c.path, cookie: true, header: true, origin: "http://evil.example"}); w.Code != http.StatusForbidden {
			t.Errorf("%s %s cross-origin: %d", c.method, c.path, w.Code)
		}
	}
}

func TestCIBusyNamesQueuedOrRunningTask(t *testing.T) {
	s := New(Hooks{}, "x")
	if why := s.CIBusy(); why != "" {
		t.Fatalf("empty queue: %q", why)
	}
	release := make(chan struct{})
	tk, err := s.ci.add("run", nil, func(io.Writer) error { <-release; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if why := s.CIBusy(); !strings.Contains(why, strconv.Itoa(tk.ID)) {
		t.Fatalf("busy = %q, want task %d", why, tk.ID)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for s.CIBusy() != "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if why := s.CIBusy(); why != "" {
		t.Fatalf("after finish: %q", why)
	}
}
