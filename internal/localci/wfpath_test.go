package localci

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/MehdiMamas/gh-runners/internal/github"
)

func TestValidateWorkflowPath(t *testing.T) {
	for _, ok := range []string{".github/workflows/ci.yml", ".github/workflows/local-ci.yaml"} {
		if err := ValidateWorkflowPath(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"/etc/x.yml", "ci.yml", ".github/workflows/../../x.yml", ".github/workflows/ci.txt", ".github/other/ci.yml", `.github\workflows\ci.yml`, ".github/workflows//ci.yml"} {
		if ValidateWorkflowPath(bad) == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestRunReadsNamedWorkflow(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/7"):
			w.Write([]byte(`{"number":7,"title":"t","state":"open","user":{"login":"me"},"head":{"sha":"abcdef1234","repo":{"full_name":"o/r"}},"base":{"repo":{"full_name":"o/r"}}}`))
		case strings.Contains(r.URL.Path, "/contents/"):
			asked = append(asked, r.URL.Path)
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
	o := Options{Client: c, Owner: "o", Repo: "r", PR: 7, Login: "me", DryRun: true, Exec: unsupportedExec{}, Conc: 1, Workflow: ".github/workflows/local-ci.yml"}
	if _, err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || !strings.HasSuffix(asked[0], "/contents/.github/workflows/local-ci.yml") {
		t.Fatalf("read %v", asked)
	}
	o.Workflow = "../x.yml"
	if _, err := Run(context.Background(), o); err == nil {
		t.Fatal("bad path accepted")
	}
	if len(asked) != 1 {
		t.Fatalf("bad path still fetched: %v", asked)
	}
}
