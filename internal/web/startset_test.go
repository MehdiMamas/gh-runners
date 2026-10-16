package web

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MehdiMamas/gh-runners/internal/store"
)

func saveStopped(t *testing.T, id string) {
	t.Helper()
	r := store.Runner{ID: id, Kind: store.KindRepo, Owner: "o", Repo: id, Name: id, Disabled: true}
	if err := store.Save(r); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(r.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir(), ".runner"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStartLastAndAllEndpoints(t *testing.T) {
	s, h := setup(t)
	saveStopped(t, "a")
	saveStopped(t, "b")
	_ = store.SaveLastSession([]string{"a"})
	reloads := 0
	s.h.Reload = func() { reloads++ }

	var st struct {
		LastSession int `json:"last_session"`
	}
	if api(t, h, "GET", "/api/state", "", &st); st.LastSession != 1 {
		t.Fatalf("state last_session = %d", st.LastSession)
	}
	var out struct{ Started int }
	if code := api(t, h, "POST", "/api/runners/start-last", "", &out); code != 200 || out.Started != 1 || reloads != 1 {
		t.Fatalf("start-last: %d %+v reloads=%d", code, out, reloads)
	}
	if b, _ := store.Load("b"); !b.Disabled {
		t.Fatal("start-last started b")
	}
	if code := api(t, h, "POST", "/api/runners/start-all", "", &out); code != 200 || out.Started != 1 {
		t.Fatalf("start-all: %d %+v", code, out)
	}
	if b, _ := store.Load("b"); b.Disabled {
		t.Fatal("start-all left b stopped")
	}
	_ = store.SetPaused(true)
	saveStopped(t, "c")
	if code := api(t, h, "POST", "/api/runners/start-all", "", nil); code != 409 {
		t.Fatalf("paused start-all: %d", code)
	}
}
