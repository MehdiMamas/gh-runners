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

const filterWorkflow = `name: CI
on: [pull_request]
jobs:
  changes:
    runs-on: ubuntu-latest
    outputs:
      a: ${{ steps.f.outputs.a }}
      b: ${{ steps.f.outputs.b }}
    steps:
      - uses: dorny/paths-filter@v3
        id: f
        with:
          filters: |
            a:
              - 'a/**'
            b:
              - 'b/**'
  ja:
    needs: [changes]
    if: needs.changes.outputs.a == 'true'
    runs-on: ubuntu-latest
    steps:
      - run: echo a
  jb:
    needs: [changes]
    if: needs.changes.outputs.b == 'true'
    runs-on: ubuntu-latest
    steps:
      - run: echo b
`

type passExec struct{}

func (passExec) RunJob(ctx context.Context, s JobSpec) Result {
	return Result{Job: s.Name, Status: Pass}
}

// runStatuses runs the workflow against a fake GitHub and returns the status
// posts in order, as context -> last body, plus the list of all bodies.
func runStatuses(t *testing.T, wf string, files string, ex Executor, logURL func(runID, job string) string) []map[string]string {
	t.Helper()
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	var mu sync.Mutex
	var posts []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != http.MethodGet {
			b, _ := io.ReadAll(r.Body)
			if strings.Contains(r.URL.Path, "/statuses/") {
				m := map[string]string{}
				_ = json.Unmarshal(b, &m)
				posts = append(posts, m)
			}
			w.Write([]byte(`{}`))
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/7"):
			w.Write([]byte(`{"number":7,"title":"t","state":"open","user":{"login":"me"},"head":{"sha":"abcdef1234","repo":{"full_name":"o/r"}},"base":{"repo":{"full_name":"o/r"}}}`))
		case strings.Contains(r.URL.Path, "/contents/"):
			json.NewEncoder(w).Encode(map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(wf))})
		case strings.HasSuffix(r.URL.Path, "/files"):
			w.Write([]byte(files))
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
	if _, err := Run(context.Background(), Options{
		Client: c, Owner: "o", Repo: "r", PR: 7, Login: "me", Out: io.Discard, Exec: ex, Conc: 1,
		Settings: store.CIRepo{Post: true}, LogURL: logURL,
	}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	return append([]map[string]string(nil), posts...)
}

// last gives the final state and description posted for each context.
func last(posts []map[string]string) map[string]map[string]string {
	m := map[string]map[string]string{}
	for _, p := range posts {
		m[p["context"]] = p
	}
	return m
}

func TestBlockedJobPostsError(t *testing.T) {
	end := last(runStatuses(t, emuWorkflow, `[]`, blockedExec{}, nil))
	if p := end["local-ci/rules"]; p["state"] != "error" || !strings.HasPrefix(p["description"], "Blocked: port 9000") {
		t.Errorf("rules ended %v", p)
	}
	if p := end["ci-passed"]; p["state"] != "error" || !strings.HasPrefix(p["description"], "Blocked: ") {
		t.Errorf("ci-passed ended %v", p)
	}
	for name, p := range end {
		if p["state"] == "pending" {
			t.Errorf("%s was left pending", name)
		}
	}
}

func TestSkippedJobPostsSuccess(t *testing.T) {
	end := last(runStatuses(t, filterWorkflow, `[{"filename":"a/x.go"}]`, passExec{}, nil))
	if p := end["local-ci/jb"]; p["state"] != "success" || p["description"] != "Skipped: no changed files match its filter" {
		t.Errorf("jb ended %v", p)
	}
	if p := end["local-ci/ja"]; p["state"] != "success" || !strings.HasPrefix(p["description"], "Passed") {
		t.Errorf("ja ended %v", p)
	}
}

func TestStatusHasLogURL(t *testing.T) {
	link := func(runID, job string) string { return "http://x/" + runID + "/" + job }
	posts := runStatuses(t, emuWorkflow, `[]`, blockedExec{}, link)
	for _, p := range posts {
		if p["context"] == "local-ci/rules" && !strings.HasSuffix(p["target_url"], "/rules") {
			t.Errorf("status without a log link: %v", p)
		}
	}
	if p := last(posts)["ci-passed"]; !strings.HasSuffix(p["target_url"], "/rules") {
		t.Errorf("ci-passed has no link to the blocked job: %v", p)
	}
	for _, p := range runStatuses(t, emuWorkflow, `[]`, blockedExec{}, nil) {
		if _, ok := p["target_url"]; ok {
			t.Errorf("link sent without LogURL: %v", p)
		}
	}
}
