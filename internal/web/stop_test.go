package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// Stopping a runner that GitHub says runs a job asks first, like restart and
// remove do; confirm_busy=1 goes on.
func TestStopRunnerBusyThenConfirm(t *testing.T) {
	s, h := setup(t)
	t.Setenv("GH_RUNNERS_TOKEN", "good-token")
	var busy atomic.Bool
	busy.Store(true)
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			w.Header().Set("X-OAuth-Scopes", "repo")
			_, _ = io.WriteString(w, `{"login":"octo"}`)
		case "/repos/octo/private/actions/runners":
			if busy.Load() {
				_, _ = io.WriteString(w, `{"total_count":1,"runners":[{"id":7,"name":"laptop","status":"online","busy":true,"labels":[]}]}`)
			} else {
				_, _ = io.WriteString(w, `{"total_count":1,"runners":[{"id":7,"name":"laptop","status":"online","busy":false,"labels":[]}]}`)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer gh.Close()
	old := github.APIBase
	github.APIBase = gh.URL
	defer func() { github.APIBase = old }()

	rn := store.Runner{ID: "octo__private", Kind: store.KindRepo, Owner: "octo", Repo: "private", Name: "laptop"}
	if err := store.Save(rn); err != nil {
		t.Fatal(err)
	}
	var running atomic.Bool
	running.Store(true)
	reloads := 0
	s.h.Reload = func() { reloads++ }
	s.h.Status = func() control.Status {
		st := control.Status{PID: 42}
		if running.Load() {
			st.Runners = []control.ProcState{{ID: rn.ID, State: "running", PID: 7}}
		}
		return st
	}

	var e struct {
		Error   string   `json:"error"`
		Busy    []string `json:"busy"`
		Confirm bool     `json:"confirm"`
	}
	if code := api(t, h, "POST", "/api/runners/octo__private/stop", "", &e); code != http.StatusConflict || !e.Confirm || len(e.Busy) != 1 || !strings.Contains(e.Busy[0], "laptop") {
		t.Fatalf("stop while busy: %d %+v", code, e)
	}
	if r, _ := store.Load(rn.ID); r.Disabled || reloads != 0 {
		t.Fatal("the runner was stopped before confirming")
	}
	if code := api(t, h, "POST", "/api/runners/octo__private/stop?confirm_busy=1", "", nil); code != 200 {
		t.Fatalf("confirmed stop: %d", code)
	}
	if r, _ := store.Load(rn.ID); !r.Disabled || reloads != 1 {
		t.Fatalf("after the confirmed stop: %+v, %d reloads", r, reloads)
	}

	// Idle, or with no process left, no question.
	rn.Disabled = false
	_ = store.Save(rn)
	busy.Store(false)
	if code := api(t, h, "POST", "/api/runners/octo__private/stop", "", nil); code != 200 {
		t.Fatalf("stop when idle: %d", code)
	}
	rn.Disabled = false
	_ = store.Save(rn)
	busy.Store(true)
	running.Store(false)
	if code := api(t, h, "POST", "/api/runners/octo__private/stop", "", nil); code != 200 {
		t.Fatalf("stop with nothing running: %d", code)
	}
}

func TestRemoteStopBusy(t *testing.T) {
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
	if code := f.remote(t, "POST", "/api/runners/octo__private/stop", "", &e); code != http.StatusConflict || !e.Confirm || len(e.Busy) != 1 {
		t.Fatalf("stop while busy: %d %+v", code, e)
	}
	if r, _ := store.Load(rn.ID); r.Disabled {
		t.Fatal("the runner was stopped before confirming")
	}
	if code := f.remote(t, "POST", "/api/runners/octo__private/stop?confirm_busy=1", "", nil); code != 200 {
		t.Fatalf("confirmed stop: %d", code)
	}
	if r, _ := store.Load(rn.ID); !r.Disabled {
		t.Fatal("the confirmed stop did not stop the runner")
	}
}
