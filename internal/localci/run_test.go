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
