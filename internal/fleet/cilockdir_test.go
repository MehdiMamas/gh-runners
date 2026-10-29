package fleet

import (
	"path/filepath"
	"testing"
)

func TestCIRunnerLockDirIsShared(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GH_RUNNERS_HOME", home)
	r := &ciRunner{Dir: filepath.Join(home, "ci", "fleet")}
	if got, want := r.lockDir(), filepath.Join(home, "ci", "locks"); got != want {
		t.Fatalf("lockDir = %s, want %s", got, want)
	}
	r.LockDir = "x"
	if r.lockDir() != "x" {
		t.Fatal("LockDir override ignored")
	}
}
