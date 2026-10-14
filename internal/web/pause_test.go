package web

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// pausable gives the member's dashboard a daemon that pauses and resumes.
func pausable(f *fleetTest) *atomic.Bool {
	var paused atomic.Bool
	h := &f.member.h
	h.Status = func() control.Status {
		return control.Status{PID: 42, Paused: paused.Load(),
			Runners: []control.ProcState{{ID: "octo__private", State: "running", PID: 99}}}
	}
	h.Pause = func() error { paused.Store(true); return nil }
	h.Resume = func() error { paused.Store(false); return nil }
	return &paused
}

func TestRemotePauseBusyThenConfirm(t *testing.T) {
	f := newFleetTest(t)
	rn := store.Runner{ID: "octo__private", Kind: store.KindRepo, Owner: "octo", Repo: "private", Name: "laptop"}
	if err := store.Save(rn); err != nil {
		t.Fatal(err)
	}
	paused := pausable(f)
	f.busy.Store(true)

	var e struct {
		Error   string   `json:"error"`
		Busy    []string `json:"busy"`
		Confirm bool     `json:"confirm"`
	}
	if code := f.remote(t, "POST", "/api/pause", "", &e); code != http.StatusConflict || !e.Confirm || len(e.Busy) != 1 || !strings.Contains(e.Busy[0], "laptop") && !strings.Contains(e.Busy[0], "octo__private") {
		t.Fatalf("pause while busy: %d %+v", code, e)
	}
	if paused.Load() {
		t.Fatal("paused before the question was answered")
	}
	if code := f.remote(t, "POST", "/api/pause?confirm_busy=1", "", nil); code != 200 || !paused.Load() {
		t.Fatalf("confirmed pause: %d paused=%v", code, paused.Load())
	}
	var st stateView
	if f.remote(t, "GET", "/api/state", "", &st); !st.Paused {
		t.Fatalf("member state not paused: %+v", st)
	}
	// Updating a paused machine is refused.
	if code := f.remote(t, "POST", "/api/update?confirm_busy=1", "", nil); code != http.StatusConflict {
		t.Fatalf("update while paused: %d", code)
	}
	if code := f.remote(t, "POST", "/api/resume", "", nil); code != 200 || paused.Load() {
		t.Fatalf("resume: %d paused=%v", code, paused.Load())
	}

	// Idle: no question.
	f.busy.Store(false)
	if code := f.remote(t, "POST", "/api/pause", "", nil); code != 200 || !paused.Load() {
		t.Fatalf("pause when idle: %d", code)
	}
}
