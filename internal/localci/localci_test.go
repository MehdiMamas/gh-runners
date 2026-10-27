package localci

import (
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/github"
)

func fixture(t *testing.T) *Workflow {
	t.Helper()
	b, err := os.ReadFile("testdata/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	wf, err := ParseWorkflow(b)
	if err != nil {
		t.Fatal(err)
	}
	return wf
}

func TestParseWorkflow(t *testing.T) {
	wf := fixture(t)
	if wf.Order[0] != "changes" || len(wf.Order) != 11 {
		t.Fatalf("order = %v", wf.Order)
	}
	if got := wf.Jobs["rules"].Needs; len(got) != 1 || got[0] != "changes" {
		t.Errorf("rules needs = %v", got)
	}
	for _, n := range []string{"rules", "legacy-golden", "legacy-two-screen", "dashboard-e2e", "legacy-e2e"} {
		if !wf.Jobs[n].NeedsJava() {
			t.Errorf("%s should need java", n)
		}
	}
	if wf.Jobs["dashboard"].NeedsJava() {
		t.Error("dashboard does not need java")
	}
	if !wf.Jobs["ci-passed"].IsAggregate() || wf.Jobs["backend"].IsAggregate() {
		t.Error("aggregate detection wrong")
	}
	if wf.Jobs["backend"].WorkDir != "aces_backend" {
		t.Errorf("default working-directory = %q", wf.Jobs["backend"].WorkDir)
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"firebase/**", "firebase/rules/a.rules", true},
		{"firebase/**", "firebase.json", false},
		{"design/tokens/**", "design/tokens/x/y.json", true},
		{"vite.config.*", "vite.config.ts", true},
		{"vite.config.*", "sub/vite.config.ts", false},
		{"scripts/deploy-*.sh", "scripts/deploy-prod.sh", true},
		{"scripts/deploy-*.sh", "scripts/x/deploy-prod.sh", false},
		{".nvmrc", ".nvmrc", true},
		{"**/*.md", "a/b/c.md", true},
		{"*.md", "a/c.md", false},
	}
	for _, c := range cases {
		if got := MatchGlob(c.glob, c.path); got != c.want {
			t.Errorf("MatchGlob(%q, %q) = %v", c.glob, c.path, got)
		}
	}
	if !FilterMatches([]string{"a/**", "!a/skip/**"}, []string{"a/x"}) || FilterMatches([]string{"a/**", "!a/skip/**"}, []string{"a/skip/x"}) {
		t.Error("negation wrong")
	}
}

func TestChangesOutputs(t *testing.T) {
	wf := fixture(t)
	out, ok := ChangesOutputs(wf.Jobs["changes"], []string{"firebase/database.rules.json"})
	if !ok || out["rules"] != "true" || out["mobile"] != "false" {
		t.Errorf("outputs = %v ok=%v", out, ok)
	}
	if _, ok := ChangesOutputs(wf.Jobs["backend"], nil); ok {
		t.Error("backend is not a changes job")
	}
}

func TestEvalIf(t *testing.T) {
	out := map[string]map[string]string{"changes": {"a": "true", "b": "false"}}
	cases := map[string]Cond{
		"":                                  CondTrue,
		"success()":                         CondTrue,
		"always()":                          CondAlways,
		"failure()":                         CondFailure,
		"needs.changes.outputs.a == 'true'": CondTrue,
		"needs.changes.outputs.b == 'true'": CondFalse,
		"needs.changes.outputs.b != 'true'": CondTrue,
		"${{ needs.changes.outputs.a == 'true' }}": CondTrue,
		"github.event_name == 'push'":              CondUnsupported,
	}
	for e, want := range cases {
		if got := EvalIf(e, out); got != want {
			t.Errorf("EvalIf(%q) = %v, want %v", e, got, want)
		}
	}
}

func TestScheduleOrderAndLock(t *testing.T) {
	var cur, max int32
	var mu sync.Mutex
	order := []string{}
	tasks := []Task{{Name: "first"}}
	for _, n := range []string{"j1", "j2", "j3", "j4"} {
		tasks = append(tasks, Task{Name: n, Needs: []string{"first"}, Lock: "emulator"})
	}
	tasks = append(tasks, Task{Name: "last", Needs: []string{"j1", "j2", "j3", "j4"}})
	res := Schedule(context.Background(), tasks, 4, &Locks{Dir: t.TempDir()}, func(ctx context.Context, tk Task, needs map[string]Result) Result {
		if tk.Lock != "" {
			n := atomic.AddInt32(&cur, 1)
			for {
				m := atomic.LoadInt32(&max)
				if n <= m || atomic.CompareAndSwapInt32(&max, m, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt32(&cur, -1)
		}
		mu.Lock()
		order = append(order, tk.Name)
		mu.Unlock()
		return Result{Job: tk.Name, Status: Pass}
	})
	if max != 1 {
		t.Errorf("lock held by %d at once", max)
	}
	if order[0] != "first" || order[len(order)-1] != "last" || len(res) != 6 {
		t.Errorf("order = %v", order)
	}
}

func TestScrubEnv(t *testing.T) {
	parent := []string{"PATH=/bin", "HOME=/h", "GH_TOKEN=x", "GITHUB_TOKEN=y", "NPM_TOKEN=z", "AWS_SECRET_ACCESS_KEY=q", "RANDOM=1"}
	env := ScrubEnv(parent, RunEnv{Repo: "o/r", SHA: "abc"}, map[string]string{"MY_API_KEY": "k", "FOO": "bar", "BAD": "${{ secrets.X }}"})
	joined := strings.Join(env, "\n")
	for _, bad := range []string{"TOKEN", "SECRET", "API_KEY", "RANDOM", "BAD="} {
		if strings.Contains(joined, bad) {
			t.Errorf("env leaks %s:\n%s", bad, joined)
		}
	}
	for _, good := range []string{"PATH=/bin", "CI=true", "GITHUB_SHA=abc", "FOO=bar"} {
		if !strings.Contains(joined, good) {
			t.Errorf("env lacks %s", good)
		}
	}
}

func pr(author, head, base string) github.PullRequest {
	var p github.PullRequest
	p.Number = 7
	p.User.Login = author
	if head != "" {
		p.Head.Repo = &struct {
			FullName string `json:"full_name"`
		}{head}
	}
	p.Base.Repo = &struct {
		FullName string `json:"full_name"`
	}{base}
	return p
}

func TestTrust(t *testing.T) {
	allow := []string{"me"}
	if err := CheckTrust(pr("me", "o/r", "o/r"), allow, false); err != nil {
		t.Errorf("own PR refused: %v", err)
	}
	if CheckTrust(pr("me", "fork/r", "o/r"), allow, false) == nil {
		t.Error("fork PR allowed")
	}
	if CheckTrust(pr("me", "", "o/r"), allow, false) == nil {
		t.Error("deleted fork allowed")
	}
	if CheckTrust(pr("stranger", "o/r", "o/r"), allow, false) == nil {
		t.Error("stranger allowed")
	}
	if err := CheckTrust(pr("stranger", "fork/r", "o/r"), allow, true); err != nil {
		t.Errorf("--approve refused: %v", err)
	}
}

func TestDecide(t *testing.T) {
	wf := fixture(t)
	out := map[string]map[string]string{"changes": {"rules": "false", "dashboard": "true"}}
	if r := Decide(wf, wf.Jobs["rules"], nil, out, false); r.Status != Skipped {
		t.Errorf("rules = %v", r.Status)
	}
	if r := Decide(wf, wf.Jobs["dashboard"], nil, out, false); r.Status != "" {
		t.Errorf("dashboard should run, got %v %s", r.Status, r.Reason)
	}
	needs := map[string]Result{"a": {Status: Pass}, "b": {Status: Skipped}}
	if r := Decide(wf, wf.Jobs["ci-passed"], needs, out, false); r.Status != Pass {
		t.Errorf("aggregate = %v", r.Status)
	}
	needs["b"] = Result{Status: Fail}
	if r := Decide(wf, wf.Jobs["ci-passed"], needs, out, false); r.Status != Fail {
		t.Errorf("aggregate with failure = %v", r.Status)
	}
}
