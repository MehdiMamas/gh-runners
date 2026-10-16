package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/MehdiMamas/gh-runners/internal/store"
)

func cfgRunner(id string, disabled bool) store.Runner {
	return store.Runner{ID: id, Kind: store.KindRepo, Owner: "o", Repo: id, Name: id, Disabled: disabled}
}

func TestStopAtStartRules(t *testing.T) {
	rs := []store.Runner{cfgRunner("a", false), cfgRunner("b", true)}
	if stop, _ := stopAtStart(rs, true, false); len(stop) != 0 {
		t.Fatal("paused machine must be left alone")
	}
	if stop, _ := stopAtStart(rs, false, true); len(stop) != 0 {
		t.Fatal("keep-running must leave runners on")
	}
}

func TestStartStopped(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	d := &Daemon{Log: log.New(io.Discard, "", 0)}
	for _, r := range []store.Runner{cfgRunner("a", false), cfgRunner("b", true)} {
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
	d.startStopped()
	a, _ := store.Load("a")
	if !a.Disabled {
		t.Fatal("a should be stopped")
	}
	if got := store.LoadLastSession().IDs; len(got) != 1 || got[0] != "a" {
		t.Fatalf("last session = %v", got)
	}
	// A second start with everything already stopped keeps the session.
	d.startStopped()
	if got := store.LoadLastSession().IDs; len(got) != 1 || got[0] != "a" {
		t.Fatalf("empty start overwrote session: %v", got)
	}
	// Keep-running marker: runners stay on.
	a.Disabled = false
	_ = store.Save(a)
	_ = store.SetKeepRunning()
	d.startStopped()
	if a, _ = store.Load("a"); a.Disabled {
		t.Fatal("keep-running marker ignored")
	}
	if store.ConsumeKeepRunning() {
		t.Fatal("marker not consumed")
	}
}
