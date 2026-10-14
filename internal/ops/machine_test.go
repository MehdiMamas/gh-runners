package ops

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MehdiMamas/gh-runners/internal/store"
)

func TestMoveRunners(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	on := store.Runner{ID: "octo--app", Kind: store.KindRepo, Owner: "octo", Repo: "app", Name: "box"}
	off := store.Runner{ID: "octo--web", Kind: store.KindRepo, Owner: "octo", Repo: "web", Name: "box", Disabled: true}
	for _, r := range []store.Runner{on, off} {
		if err := store.Save(r); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(r.Dir(), "work.txt"), []byte(r.ID), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	oldDir := store.RunnersDir()

	target := filepath.Join(t.TempDir(), "big disk", "runners")
	if err := MoveRunners(target, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := store.RunnersDir(); got != target {
		t.Fatalf("RunnersDir = %s, want %s", got, target)
	}
	rs, err := store.List()
	if err != nil || len(rs) != 2 {
		t.Fatalf("list after move: %v %+v", err, rs)
	}
	for _, r := range rs {
		b, err := os.ReadFile(filepath.Join(target, r.ID, "work.txt"))
		if err != nil || string(b) != r.ID {
			t.Errorf("%s: files not moved: %v", r.ID, err)
		}
		if want := r.ID == off.ID; r.Disabled != want {
			t.Errorf("%s: disabled = %v, want %v", r.ID, r.Disabled, want)
		}
		if _, err := os.Stat(filepath.Join(oldDir, r.ID)); !os.IsNotExist(err) {
			t.Errorf("%s: old folder still there", r.ID)
		}
	}

	if err := MoveRunners(filepath.Join(target, "inner"), io.Discard); err == nil || !strings.Contains(err.Error(), "not inside") {
		t.Fatalf("moving into itself: %v", err)
	}

	// Back to the default folder.
	if err := MoveRunners("", io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := store.RunnersDir(); got != store.DefaultRunnersDir() {
		t.Fatalf("RunnersDir = %s, want the default", got)
	}
	if rs, _ := store.List(); len(rs) != 2 {
		t.Fatalf("runners after moving back: %+v", rs)
	}
}

func TestWithin(t *testing.T) {
	base := filepath.Join(t.TempDir(), "a")
	cases := []struct {
		p    string
		want bool
	}{
		{filepath.Join(base, "b"), true},
		{base, false},
		{filepath.Join(base, "..", "a2"), false},
		{filepath.Dir(base), false},
	}
	for _, c := range cases {
		if got := within(c.p, base); got != c.want {
			t.Errorf("within(%s, %s) = %v, want %v", c.p, base, got, c.want)
		}
	}
}
