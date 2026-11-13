package proc

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSimHook(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	dir := t.TempDir()
	t.Setenv(simHookVar, "")
	os.Unsetenv(simHookVar)
	env := withSimHook([]string{"A=b"}, dir)
	want := simHookVar + "=" + filepath.Join(dir, "job-completed.sh")
	if env[len(env)-1] != want {
		t.Fatalf("got %v, want last %q", env, want)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "job-completed.sh")); !strings.Contains(string(b), "simctl shutdown all") {
		t.Fatal("script not written")
	}
	// the runner's own .env hook wins
	dir2 := t.TempDir()
	os.WriteFile(filepath.Join(dir2, ".env"), []byte(simHookVar+"=/x.sh\n"), 0o644)
	if got := withSimHook([]string{"A=b"}, dir2); len(got) != 1 {
		t.Fatalf("overrode .env hook: %v", got)
	}
}
