package localci

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MehdiMamas/gh-runners/internal/store"
)

func TestActiveJobs(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	if got := ActiveJobs(); len(got) != 0 {
		t.Fatalf("no jobs yet, got %v", got)
	}
	done := StartActive(ActiveJob{Repo: "o/r", PR: 7, Job: "test", Machine: "mac"})
	got := ActiveJobs()
	if len(got) != 1 || got[0].Repo != "o/r" || got[0].PR != 7 || got[0].PID != os.Getpid() || got[0].Started.IsZero() {
		t.Fatalf("one running job, got %+v", got)
	}
	done()
	if got := ActiveJobs(); len(got) != 0 {
		t.Fatalf("done removes the job, got %v", got)
	}
}

func TestActiveJobsDropsDeadProcess(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	path := filepath.Join(activeDir(), "999999-1.json")
	if err := os.MkdirAll(activeDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(path, ActiveJob{Repo: "o/r", Job: "x", PID: 999999}, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ActiveJobs(); len(got) != 0 {
		t.Fatalf("a dead process's job is not running, got %v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("its file is removed, stat: %v", err)
	}
}

func TestPRFromRef(t *testing.T) {
	for ref, want := range map[string]int{"refs/pull/42/merge": 42, "refs/pull/5/head": 5, "refs/heads/main": 0, "": 0} {
		if got := PRFromRef(ref); got != want {
			t.Errorf("PRFromRef(%q) = %d, want %d", ref, got, want)
		}
	}
}
