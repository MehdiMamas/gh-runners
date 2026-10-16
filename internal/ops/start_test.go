package ops

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MehdiMamas/gh-runners/internal/store"
)

func saveConfigured(t *testing.T, id string, disabled bool) {
	t.Helper()
	r := store.Runner{ID: id, Kind: store.KindRepo, Owner: "o", Repo: id, Name: id, Disabled: disabled}
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

func TestEnableLastAndAll(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	saveConfigured(t, "a", true)
	saveConfigured(t, "b", true)
	saveConfigured(t, "c", true)
	_ = store.SaveLastSession([]string{"a", "gone", "b"})
	nudges := 0
	nudge := func() error { nudges++; return nil }

	got, err := Enable(LastSessionRunners(), nudge)
	if err != nil || len(got) != 2 || nudges != 1 {
		t.Fatalf("last: %v %v nudges=%d", got, err, nudges)
	}
	if c, _ := store.Load("c"); !c.Disabled {
		t.Fatal("c must stay stopped")
	}
	got, err = Enable(ConfiguredRunners(), nudge)
	if err != nil || len(got) != 1 || got[0].ID != "c" {
		t.Fatalf("all: %v %v", got, err)
	}
	if got, _ = Enable(ConfiguredRunners(), nudge); len(got) != 0 || nudges != 2 {
		t.Fatalf("nothing left to start: %v nudges=%d", got, nudges)
	}
}

func TestEnableRefusesWhenPaused(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	saveConfigured(t, "a", true)
	_ = store.SetPaused(true)
	if _, err := Enable(ConfiguredRunners(), nil); !errors.Is(err, ErrPaused) {
		t.Fatalf("err = %v", err)
	}
	if a, _ := store.Load("a"); !a.Disabled {
		t.Fatal("a started while paused")
	}
}
