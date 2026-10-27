package localci

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

type fixedExec struct{ res Result }

func (f fixedExec) RunJob(ctx context.Context, s JobSpec) Result {
	r := f.res
	r.Job = s.Name
	return r
}

// runOnce runs PR 7 against a fake GitHub. commentFails makes the comment post
// answer 403.
func runOnce(t *testing.T, ex Executor, st store.CIRepo, dry, commentFails bool) (Report, []Event) {
	t.Helper()
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			if commentFails && strings.Contains(r.URL.Path, "/comments") {
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
				return
			}
			w.Write([]byte(`{}`))
			return
		}
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
	t.Cleanup(srv.Close)
	c := github.New("tok")
	c.BaseURL = srv.URL
	var out strings.Builder
	rep, err := Run(context.Background(), Options{
		Client: c, Owner: "o", Repo: "r", PR: 7, Login: "me", Out: &out, Exec: ex, Conc: 1, DryRun: dry, Settings: st,
		Before: []Event{{Kind: "queued", Text: "Queued Local CI for PR #7."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Dir == "" {
		t.Fatal("report has no run folder")
	}
	return rep, ReadEvents(rep.Dir)
}

func lastKind(evs []Event, kind string) *Event {
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind == kind {
			return &evs[i]
		}
	}
	return nil
}

func wantNotPosted(t *testing.T, rep Report, evs []Event, reasonHas, nextHas string) {
	t.Helper()
	o := rep.Outcome
	if o.State != NotPosted || !strings.Contains(o.Reason, reasonHas) || !strings.Contains(o.Next, nextHas) {
		t.Errorf("outcome = %+v, want not-posted with %q and next %q", o, reasonHas, nextHas)
	}
	if e := lastKind(evs, "outcome"); e == nil || !strings.Contains(e.Text, reasonHas) {
		t.Errorf("no outcome event naming the reason: %+v", evs)
	}
}

func TestOutcomeUnsupportedEmulator(t *testing.T) {
	ex := fixedExec{Result{Status: Unsupported, Reason: "this machine is not set up for emulator jobs"}}
	rep, evs := runOnce(t, ex, store.CIRepo{Post: true}, false, false)
	wantNotPosted(t, rep, evs, "cannot run on any machine", "gh-runners ci emulator on")
}

func TestOutcomeBlocked(t *testing.T) {
	ex := fixedExec{Result{Status: Blocked, Reason: "disk full"}}
	rep, evs := runOnce(t, ex, store.CIRepo{Post: true}, false, false)
	wantNotPosted(t, rep, evs, "infrastructure error", "run again")
	if !strings.Contains(rep.Outcome.Reason, "disk full") {
		t.Errorf("reason lacks the cause: %s", rep.Outcome.Reason)
	}
}

func TestOutcomeDryRun(t *testing.T) {
	rep, evs := runOnce(t, fixedExec{Result{Status: Pass}}, store.CIRepo{Post: true}, true, false)
	wantNotPosted(t, rep, evs, "dry run", "without Dry run")
}

func TestOutcomePostingOff(t *testing.T) {
	rep, evs := runOnce(t, fixedExec{Result{Status: Pass}}, store.CIRepo{}, false, false)
	wantNotPosted(t, rep, evs, "posting is off", "Post results on the pull request")
}

func TestOutcomeCommentRefused(t *testing.T) {
	rep, evs := runOnce(t, fixedExec{Result{Status: Pass}}, store.CIRepo{Post: true}, false, true)
	wantNotPosted(t, rep, evs, "GitHub refused", "GitHub sign-in")
	if !strings.Contains(rep.Outcome.PostError, "Resource not accessible") {
		t.Errorf("post error = %q", rep.Outcome.PostError)
	}
}

func TestOutcomePostedAndFeed(t *testing.T) {
	rep, evs := runOnce(t, fixedExec{Result{Status: Pass, Machine: "mac"}}, store.CIRepo{Post: true}, false, false)
	if rep.Outcome.State != Posted || rep.Outcome.PostError != "" {
		t.Fatalf("outcome = %+v", rep.Outcome)
	}
	kinds := map[string]bool{}
	for _, e := range evs {
		kinds[e.Kind] = true
	}
	for _, k := range []string{"queued", "started", "job_started", "job_finished", "status", "comment", "outcome"} {
		if !kinds[k] {
			t.Errorf("feed lacks %q: %+v", k, evs)
		}
	}
	if evs[0].Kind != "queued" {
		t.Errorf("first event = %q, want the queued event first", evs[0].Kind)
	}
	named := false
	for _, e := range evs {
		named = named || (e.Kind == "job_finished" && e.Machine == "mac" && strings.Contains(e.Text, "on mac"))
	}
	if !named {
		t.Errorf("no job_finished event names the machine: %+v", evs)
	}
	// The run is no longer listed as running once it ended.
	if got := RunningRuns(); len(got) != 0 {
		t.Errorf("still running: %+v", got)
	}
}

func TestHistoryKeepsOutcomeAndDir(t *testing.T) {
	rep, _ := runOnce(t, fixedExec{Result{Status: Pass}}, store.CIRepo{}, false, false)
	h := NewHistoryRun(Options{Owner: "o", Repo: "r", PR: 7}, false, time.Now(), rep, nil)
	if h.Dir != rep.Dir || h.Outcome.State != NotPosted {
		t.Errorf("history run = %+v", h)
	}
	e := NewHistoryRun(Options{Owner: "o", Repo: "r", PR: 7}, false, time.Now(), Report{}, errors.New("boom"))
	if e.Outcome.State != NotPosted || !strings.Contains(e.Outcome.Reason, "boom") {
		t.Errorf("failed run outcome = %+v", e.Outcome)
	}
}
