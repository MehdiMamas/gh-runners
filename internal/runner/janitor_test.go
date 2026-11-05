package runner

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/proc"
)

func mk(t *testing.T, p string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func has(p string) bool { _, err := os.Lstat(p); return err == nil }

func age(t *testing.T, p string, d time.Duration) {
	t.Helper()
	old := time.Now().Add(-d)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
}

func newRunner(t *testing.T) (root, dir string) {
	root = t.TempDir()
	dir = filepath.Join(root, "r1")
	mk(t, filepath.Join(dir, "bin.1.0", "x"), 10)
	mk(t, filepath.Join(dir, "bin.2.0", "x"), 10)
	mk(t, filepath.Join(dir, "externals.1.0", "x"), 10)
	mk(t, filepath.Join(dir, "externals.2.0", "x"), 10)
	if err := os.Symlink("bin.2.0", filepath.Join(dir, "bin")); err != nil {
		t.Skip("no symlinks:", err)
	}
	if err := os.Symlink("externals.2.0", filepath.Join(dir, "externals")); err != nil {
		t.Fatal(err)
	}
	mk(t, filepath.Join(dir, "_work", "_actions", "a"), 10)
	mk(t, filepath.Join(dir, "_work", "_tool", "t"), 10)
	mk(t, filepath.Join(dir, "_work", "_update", "u"), 10)
	mk(t, filepath.Join(dir, "_work", "repo", "src.txt"), 10)
	return root, dir
}

func never(string) bool { return false }

func TestOldVersionsRemoved(t *testing.T) {
	root, dir := newRunner(t)
	CleanRunners(root, never)
	for p, want := range map[string]bool{"bin.1.0": false, "bin.2.0": true, "externals.1.0": false, "externals.2.0": true, "_work/_update": false} {
		if has(filepath.Join(dir, p)) != want {
			t.Errorf("%s present = %v, want %v", p, !want, want)
		}
	}
}

func TestRealBinDirLeftAlone(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "r1")
	mk(t, filepath.Join(dir, "bin", "x"), 1)
	mk(t, filepath.Join(dir, "bin.1.0", "x"), 1)
	CleanRunners(root, never)
	if !has(filepath.Join(dir, "bin.1.0")) {
		t.Error("versions removed although bin is not a link")
	}
}

func TestBusyRunnerWorkUntouched(t *testing.T) {
	root, dir := newRunner(t)
	age(t, filepath.Join(dir, "_work", "repo"), 100*time.Hour)
	CleanRunners(root, func(string) bool { return true })
	if !has(filepath.Join(dir, "_work", "repo")) {
		t.Error("busy runner's work folder removed")
	}
}

func TestIdleStaleWorkEmptiedExceptCaches(t *testing.T) {
	root, dir := newRunner(t)
	age(t, filepath.Join(dir, "_work", "repo"), 100*time.Hour)
	if freed := CleanRunners(root, never); freed == 0 {
		t.Error("freed 0")
	}
	if has(filepath.Join(dir, "_work", "repo")) {
		t.Error("stale checkout kept")
	}
	for _, k := range []string{"_actions", "_tool"} {
		if !has(filepath.Join(dir, "_work", k)) {
			t.Errorf("%s removed", k)
		}
	}
	if ents, _ := filepath.Glob(filepath.Join(dir, "_work", ".trash-*")); len(ents) != 0 {
		t.Errorf("trash left: %v", ents)
	}
}

func TestIdleFreshSmallWorkKept(t *testing.T) {
	root, dir := newRunner(t)
	CleanRunners(root, never)
	if !has(filepath.Join(dir, "_work", "repo")) {
		t.Error("fresh small checkout removed")
	}
}

func TestOverCapEmptied(t *testing.T) {
	root, dir := newRunner(t)
	f := filepath.Join(dir, "_work", "repo", "big")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(f, workCap+1); err != nil {
		t.Fatal(err)
	}
	CleanRunners(root, never)
	if has(filepath.Join(dir, "_work", "repo")) {
		t.Error("over-cap checkout kept")
	}
}

func TestOldTrashRemoved(t *testing.T) {
	root, dir := newRunner(t)
	mk(t, filepath.Join(dir, "_work", ".trash-1", "old", "f"), 10)
	CleanRunners(root, func(string) bool { return true })
	if has(filepath.Join(dir, "_work", ".trash-1")) {
		t.Error("old trash kept")
	}
}

func TestLongJobs(t *testing.T) {
	old := strayList
	defer func() { strayList = old }()
	now := time.Now()
	strayList = func(string) []proc.Stray {
		return []proc.Stray{
			{PID: 1, Path: "/r/bin/Runner.Worker spawnclient", Start: now.Add(-4 * time.Hour)},
			{PID: 2, Path: "/r/bin/Runner.Worker spawnclient", Start: now.Add(-10 * time.Minute)},
			{PID: 3, Path: "/r/bin/Runner.Listener run", Start: now.Add(-9 * time.Hour)},
			{PID: 4, Path: "/r/bin/Runner.Worker spawnclient"},
		}
	}
	got := LongJobs("/r", 3*time.Hour)
	if len(got) != 1 || got[0].PID != 1 {
		t.Fatalf("limit 3h: got %+v, want only pid 1", got)
	}
	if got := LongJobs("/r", 5*time.Minute); len(got) != 2 {
		t.Fatalf("limit 5m: got %+v, want pids 1 and 2", got)
	}
	if got := LongJobs("/r", 0); got != nil {
		t.Fatalf("limit 0: got %+v, want none", got)
	}
}
