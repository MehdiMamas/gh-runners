package localci

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

type unsupportedExec struct{}

func (unsupportedExec) RunJob(ctx context.Context, s JobSpec) Result {
	return Result{Job: s.Name, Status: Unsupported, Reason: "needs macOS, not available here"}
}

func TestUnsupportedPostsAndMergesNothing(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	var mu sync.Mutex
	var writes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != http.MethodGet {
			b, _ := io.ReadAll(r.Body)
			writes = append(writes, r.Method+" "+r.URL.Path+" "+string(b))
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
		Client: c, Owner: "o", Repo: "r", PR: 7, Login: "me", Out: &out, Exec: unsupportedExec{}, Conc: 1,
		Settings: store.CIRepo{Post: true, Merge: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.HasUnsupported() || rep.AllPassed() || rep.Merged {
		t.Fatalf("report = %+v", rep)
	}
	got := map[string]Status{}
	for _, r := range rep.Results {
		got[r.Job] = r.Status
	}
	if got["rules"] != Unsupported {
		t.Errorf("statuses = %v", got)
	}
	for _, w := range writes {
		if strings.Contains(w, "/statuses/") && !strings.Contains(w, `"state":"pending"`) && !strings.Contains(w, `"state":"error"`) {
			t.Errorf("verdict status posted: %s", w)
		}
		if strings.Contains(w, "/merge") || strings.Contains(w, "/comments") {
			t.Errorf("unexpected write %s", w)
		}
	}
	if !strings.Contains(out.String(), "NOT SUPPORTED") {
		t.Errorf("output lacks NOT SUPPORTED:\n%s", out.String())
	}
}

type failExec struct{}

func (failExec) RunJob(ctx context.Context, s JobSpec) Result {
	return Result{Job: s.Name, Status: Fail, Reason: "step failed: x"}
}

func TestFailedJobNeverGivesCIPassedSuccess(t *testing.T) {
	end := last(runStatuses(t, emuWorkflow, `[]`, failExec{}, nil))
	if p := end["ci-passed"]; p["state"] == "success" || p["state"] == "pending" {
		t.Errorf("ci-passed ended %v", p)
	}
	if p := end["local-ci/rules"]; p["state"] == "success" {
		t.Errorf("rules ended %v", p)
	}
}

const secretRunWF = `jobs:
  up:
    runs-on: ubuntu-latest
    env:
      TOK: ${{ secrets.UP_TOKEN }}
    steps:
      - run: echo upload ${{ secrets.UP_TOKEN }}
`

type recordExec struct {
	mu    sync.Mutex
	specs []JobSpec
}

func (r *recordExec) RunJob(ctx context.Context, s JobSpec) Result {
	r.mu.Lock()
	r.specs = append(r.specs, s)
	r.mu.Unlock()
	return Result{Job: s.Name, Status: Pass}
}

// runSecretJob runs secretRunWF on PR 7 (head abcdef1234, author me) against a
// fake GitHub whose reviews are the given JSON. It returns the up job's
// result and the specs the executor got.
func runSecretJob(t *testing.T, reviews string, approve bool, set map[string]string) (Result, []JobSpec) {
	t.Helper()
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	for n, v := range set {
		if err := store.SetSecret("o/r", n, store.Secret{Value: v}); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Write([]byte(`{}`))
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/7"):
			w.Write([]byte(`{"number":7,"title":"t","state":"open","user":{"login":"me"},"head":{"sha":"abcdef1234","repo":{"full_name":"o/r"}},"base":{"repo":{"full_name":"o/r"}}}`))
		case strings.HasSuffix(r.URL.Path, "/pulls/7/reviews"):
			w.Write([]byte(reviews))
		case strings.Contains(r.URL.Path, "/contents/"):
			json.NewEncoder(w).Encode(map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(secretRunWF))})
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
	ex := &recordExec{}
	rep, err := Run(context.Background(), Options{
		Client: c, Owner: "o", Repo: "r", PR: 7, Login: "me", Out: io.Discard, Exec: ex, Conc: 1, Approve: approve,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Results {
		if r.Job == "up" {
			return r, ex.specs
		}
	}
	t.Fatal("no result for up")
	return Result{}, nil
}

func TestSecretJobNeedsApprovalOfHead(t *testing.T) {
	set := map[string]string{"UP_TOKEN": "tok-abc-123"}
	skipped := []struct{ name, reviews string }{
		{"no approval", `[]`},
		{"approval of an older commit", `[{"state":"APPROVED","commit_id":"0000000","user":{"login":"me"}}]`},
		{"approval by a login not on the allowlist", `[{"state":"APPROVED","commit_id":"abcdef1234","user":{"login":"bob"}}]`},
		{"approval taken back", `[{"state":"APPROVED","commit_id":"abcdef1234","user":{"login":"me"}},{"state":"CHANGES_REQUESTED","commit_id":"abcdef1234","user":{"login":"me"}}]`},
	}
	for _, c := range skipped {
		res, specs := runSecretJob(t, c.reviews, false, set)
		if res.Status != Skipped || !strings.Contains(res.Reason, "runs after me approves commit abcdef1") || len(specs) != 0 {
			t.Errorf("%s: %v %q, ran %d", c.name, res.Status, res.Reason, len(specs))
		}
	}
	res, specs := runSecretJob(t, `[{"state":"APPROVED","commit_id":"abcdef1234","user":{"login":"me"}}]`, false, set)
	if res.Status != Pass || len(specs) != 1 {
		t.Fatalf("approved head: %v %q, ran %d", res.Status, res.Reason, len(specs))
	}
	if !strings.Contains(specs[0].Job.Steps[0].Run, "upload tok-abc-123") || specs[0].SecretEnv["TOK"] != "tok-abc-123" || len(specs[0].SecretValues) != 1 {
		t.Errorf("spec lacks the secret: %+v", specs[0])
	}
}

func TestSecretJobApproveFlagAndMissingName(t *testing.T) {
	res, specs := runSecretJob(t, `[]`, true, map[string]string{"UP_TOKEN": "tok-abc-123"})
	if res.Status != Pass || len(specs) != 1 {
		t.Fatalf("--approve: %v %q", res.Status, res.Reason)
	}
	res, specs = runSecretJob(t, `[]`, true, nil)
	if res.Status != Skipped || res.Reason != "needs secrets: missing UP_TOKEN" || len(specs) != 0 {
		t.Fatalf("missing: %v %q ran %d", res.Status, res.Reason, len(specs))
	}
}

// runJobsErr runs wf with the given --jobs names against a fake GitHub and
// returns Run's error, the executor and every write the fake received.
func runJobsErr(t *testing.T, wf string, jobs []string) (error, *recordExec, []string) {
	t.Helper()
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
			json.NewEncoder(w).Encode(map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(wf))})
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
	ex := &recordExec{}
	_, err := Run(context.Background(), Options{
		Client: c, Owner: "o", Repo: "r", PR: 7, Login: "me", Out: io.Discard, Exec: ex, Conc: 1,
		Settings: store.CIRepo{Post: true}, Jobs: jobs,
	})
	mu.Lock()
	defer mu.Unlock()
	return err, ex, append([]string(nil), writes...)
}

func TestRunUnknownJobNameIsAnError(t *testing.T) {
	err, ex, writes := runJobsErr(t, secretRunWF, []string{"dashboard"})
	if err == nil || !strings.Contains(err.Error(), `unknown job "dashboard"`) || !strings.Contains(err.Error(), "jobs: up") {
		t.Fatalf("err = %v", err)
	}
	if len(ex.specs) != 0 || len(writes) != 0 {
		t.Errorf("ran %d jobs, wrote %v", len(ex.specs), writes)
	}
	// A known name next to an unknown one is still an error.
	if err, _, _ := runJobsErr(t, secretRunWF, []string{"up", "nope"}); err == nil {
		t.Error("want an error for a mixed list")
	}
}

func TestRunWorkflowWithoutJobsIsAnError(t *testing.T) {
	err, ex, writes := runJobsErr(t, "name: ci\njobs: {}\n", nil)
	if err == nil || !strings.Contains(err.Error(), "no jobs to run") {
		t.Fatalf("err = %v", err)
	}
	if len(ex.specs) != 0 || len(writes) != 0 {
		t.Errorf("ran %d jobs, wrote %v", len(ex.specs), writes)
	}
}

func TestRunKnownJobNameStillRuns(t *testing.T) {
	err, ex, _ := runJobsErr(t, emuWorkflow, []string{"rules"})
	if err != nil || len(ex.specs) != 1 {
		t.Fatalf("err = %v, ran %d", err, len(ex.specs))
	}
}
