package ops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// fakeRunners is a GitHub that lists these runners for o/r.
func fakeRunners(t *testing.T, names ...string) *github.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/actions/runners" {
			http.NotFound(w, r)
			return
		}
		var b strings.Builder
		for i, n := range names {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"id":%d,"name":%q,"status":"online"}`, i+1, n)
		}
		fmt.Fprintf(w, `{"total_count":%d,"runners":[%s]}`, len(names), b.String())
	}))
	t.Cleanup(srv.Close)
	c := github.New("t")
	c.BaseURL = srv.URL
	return c
}

// oldRunner writes a runner the way v0.4.0 left it: the scope's only
// runner, in runners/<owner>__<repo>, registered.
func oldRunner(t *testing.T) store.Runner {
	t.Helper()
	r := store.Runner{ID: "o__r", Kind: store.KindRepo, Owner: "o", Repo: "r", Name: "box", Labels: []string{"heavy"}}
	if err := store.Save(r); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{".runner", ".credentials"} {
		if err := os.WriteFile(filepath.Join(r.Dir(), f), []byte("secret "+f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		out[strings.TrimPrefix(p, dir)] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestClaimAnotherNames(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	old := oldRunner(t)
	before := snapshot(t, old.Dir())
	scope := github.Scope{Owner: "o", Repo: "r"}
	local := []store.Runner{old}
	// box is here, box-2 belongs to another machine on GitHub.
	c := fakeRunners(t, "box", "box-2")
	ctx := context.Background()

	claim := func(o AddOptions) (string, string, error) {
		name := "box"
		if o.Name != "" {
			name = o.Name
		}
		return claimAnother(ctx, c, scope, "o__r", o, name, local, io.Discard)
	}
	type res struct{ ID, Name, Err string }
	run := func(o AddOptions) res {
		id, name, err := claim(o)
		r := res{ID: id, Name: name}
		if err != nil {
			r.Err = err.Error()
		}
		return r
	}
	got := []res{
		run(AddOptions{Another: true}),
		run(AddOptions{Another: true}),
		run(AddOptions{Another: true, Name: "light"}),
		run(AddOptions{Another: true, Name: "light"}),
		run(AddOptions{Another: true, Name: "BOX"}),
		run(AddOptions{Another: true, Name: "box-2"}),
		run(AddOptions{Another: true, Name: "box-2", Force: true}),
		run(AddOptions{Another: true, Name: "a/b"}),
	}
	want := []string{
		"o__r__box-3 box-3 ",
		"o__r__box-4 box-4 ",
		"o__r__light light ",
		"  the folder ",
		"  o/r already has a runner named \"BOX\" on this machine",
		"  a runner named \"box-2\" is already registered to o/r",
		"o__r__box-2 box-2 ",
		"  \"a/b\" is not a valid runner name",
	}
	for i, g := range got {
		s := g.ID + " " + g.Name + " " + g.Err
		if !strings.HasPrefix(s, want[i]) {
			t.Errorf("claim %d = %q, want prefix %q", i, s, want[i])
		}
	}
	// The v0.4.0 runner was not touched: same folder, same files, still the
	// scope's first runner.
	if after := snapshot(t, old.Dir()); !reflect.DeepEqual(after, before) || len(after) != 3 {
		t.Errorf("old runner changed:\nbefore %v\nafter  %v", before, after)
	}
	list, err := store.ForScope("o/r")
	if err != nil || len(list) != 1 || list[0].ID != "o__r" || !list[0].Configured() {
		t.Fatalf("ForScope after claims: %+v %v", list, err)
	}
}

func TestClaimAnotherConcurrent(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	old := oldRunner(t)
	// GitHub cannot be asked: names come from the local runners and the
	// folders other adds claimed.
	c := github.New("t")
	c.BaseURL = "http://127.0.0.1:1"
	const n = 12
	ids := make([]string, n)
	names := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, name, err := claimAnother(context.Background(), c, github.Scope{Owner: "o", Repo: "r"}, "o__r", AddOptions{Another: true}, "box", []store.Runner{old}, io.Discard)
			if err != nil {
				t.Error(err)
			}
			ids[i], names[i] = id, name
		}(i)
	}
	wg.Wait()
	sort.Strings(ids)
	for i := 1; i < n; i++ {
		if ids[i] == ids[i-1] || ids[i] == "" {
			t.Fatalf("two adds got the same folder: %v", ids)
		}
	}
	for _, name := range names {
		if strings.EqualFold(name, "box") {
			t.Fatalf("an add took the name of the runner already here: %v", names)
		}
	}
}

func TestAddTarget(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	scope := github.Scope{Owner: "o", Repo: "r"}
	old := oldRunner(t)
	half := store.Runner{ID: "o__r__light", Kind: store.KindRepo, Owner: "o", Repo: "r", Name: "light"} // not registered yet
	if err := store.Save(half); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := addTarget(scope, nil, AddOptions{}); ok || err != nil {
		t.Errorf("no runner: %v %v", ok, err)
	}
	if _, _, err := addTarget(scope, []store.Runner{old}, AddOptions{}); !errors.Is(err, ErrExists) || !strings.Contains(err.Error(), "--another") {
		t.Errorf("registered runner without force: %v", err)
	}
	if r, ok, err := addTarget(scope, []store.Runner{old}, AddOptions{Force: true}); !ok || err != nil || r.ID != "o__r" {
		t.Errorf("force: %+v %v %v", r, ok, err)
	}
	if _, _, err := addTarget(scope, []store.Runner{old, half}, AddOptions{Force: true}); err == nil || !strings.Contains(err.Error(), "--runner") {
		t.Errorf("force with two runners: %v", err)
	}
	if r, ok, err := addTarget(scope, []store.Runner{old, half}, AddOptions{Runner: "LIGHT"}); !ok || err != nil || r.ID != "o__r__light" {
		t.Errorf("finish the half-added one: %+v %v %v", r, ok, err)
	}
	if r, ok, err := addTarget(scope, []store.Runner{old, half}, AddOptions{Runner: "o__r", Force: true}); !ok || err != nil || r.Name != "box" {
		t.Errorf("re-register by folder id: %+v %v %v", r, ok, err)
	}
	if _, _, err := addTarget(scope, []store.Runner{old, half}, AddOptions{Runner: "nope"}); err == nil || !strings.Contains(err.Error(), "box, light") {
		t.Errorf("unknown runner: %v", err)
	}
}

func TestPlanSyncSeveralRunners(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	runners := []store.Runner{
		{ID: "o__r", Kind: store.KindRepo, Owner: "o", Repo: "r", Name: "box", Source: "sync"},
		{ID: "o__r__box-2", Kind: store.KindRepo, Owner: "o", Repo: "r", Name: "box-2", Source: "add"},
		{ID: "o__gone", Kind: store.KindRepo, Owner: "o", Repo: "gone", Name: "box", Source: "sync"},
		{ID: "o__gone__box-2", Kind: store.KindRepo, Owner: "o", Repo: "gone", Name: "box-2", Source: "sync"},
		{ID: "o__gone__mine", Kind: store.KindRepo, Owner: "o", Repo: "gone", Name: "mine", Source: "add"},
	}
	for _, r := range runners {
		if err := store.Save(r); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(r.Dir(), ".runner"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			_, _ = io.WriteString(w, `{"login":"o"}`)
		case "/user/repos":
			if r.URL.Query().Get("page") == "1" || r.URL.Query().Get("page") == "" {
				_, _ = io.WriteString(w, `[{"full_name":"o/r","name":"r","private":true,"owner":{"login":"o"},"permissions":{"admin":true}},
					{"full_name":"o/new","name":"new","private":true,"owner":{"login":"o"},"permissions":{"admin":true}}]`)
				return
			}
			_, _ = io.WriteString(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := github.New("t")
	c.BaseURL = srv.URL
	plan, err := PlanSync(context.Background(), c, store.Sync{Owners: []string{"o"}, Prune: true})
	if err != nil {
		t.Fatal(err)
	}
	var add, prune []string
	for _, r := range plan.Add {
		add = append(add, r.FullName)
	}
	for _, r := range plan.Prune {
		prune = append(prune, r.ID)
	}
	sort.Strings(prune)
	if plan.Have != 1 || strings.Join(add, ",") != "o/new" || strings.Join(prune, ",") != "o__gone,o__gone__box-2" {
		t.Fatalf("plan: have %d, add %v, prune %v", plan.Have, add, prune)
	}
}
