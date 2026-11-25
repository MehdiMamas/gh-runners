package proc

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestJobHookScripts(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		name, s := jobHook(goos)
		if strings.Contains(s, "%s") {
			t.Fatalf("%s: placeholder left", goos)
		}
		if (goos == "windows") != strings.HasSuffix(name, ".ps1") {
			t.Fatalf("%s: wrong file %s", goos, name)
		}
		if got := strings.Contains(s, "simctl shutdown all"); got != (goos == "darwin") {
			t.Fatalf("%s: simctl=%v", goos, got)
		}
		if !strings.Contains(s, "Runner.Worker") || !strings.Contains(s, "_work") {
			t.Fatalf("%s: missing worker check or _work guard", goos)
		}
	}
}

func TestJobHookEnv(t *testing.T) {
	dir := t.TempDir()
	os.Unsetenv(jobHookVar)
	env := withJobHook([]string{"A=b"}, dir)
	name, script := jobHook(runtime.GOOS)
	if want := jobHookVar + "=" + filepath.Join(dir, name); env[len(env)-1] != want {
		t.Fatalf("got %v, want last %q", env, want)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, name)); string(b) != script {
		t.Fatal("script not written")
	}
	// the user's own hook, in the environment or the runner's .env, wins
	if got := withJobHook([]string{"A=b", jobHookVar + "=/y.sh"}, dir); len(got) != 2 {
		t.Fatalf("overrode env hook: %v", got)
	}
	dir2 := t.TempDir()
	os.WriteFile(filepath.Join(dir2, ".env"), []byte(jobHookVar+"=/x.sh\n"), 0o644)
	if got := withJobHook([]string{"A=b"}, dir2); len(got) != 1 {
		t.Fatalf("overrode .env hook: %v", got)
	}
}

// The sh hook empties a checkout under _work and nothing else.
func TestJobHookShGuard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh hook")
	}
	dir := t.TempDir()
	_, script := jobHook("linux")
	hook := filepath.Join(dir, "job-completed.sh")
	os.WriteFile(hook, []byte(script), 0o755)
	ws := filepath.Join(dir, "_work", "repo", "repo")
	other := filepath.Join(dir, "elsewhere")
	for _, d := range []string{ws, other} {
		os.MkdirAll(filepath.Join(d, "sub"), 0o755)
		os.WriteFile(filepath.Join(d, "sub", "f"), nil, 0o644)
		os.WriteFile(filepath.Join(d, ".hidden"), nil, 0o644)
	}
	run := func(w string) {
		c := exec.Command("sh", hook)
		c.Env = append(os.Environ(), "GITHUB_WORKSPACE="+w)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	count := func(d string) int { e, _ := os.ReadDir(d); return len(e) }
	for _, w := range []string{"", other, filepath.Join(dir, "_work"), filepath.Join(dir, "_work") + "/", dir, "/", ws + "/../.."} {
		run(w)
	}
	if count(ws) != 2 || count(other) != 2 {
		t.Fatal("guard let a bad path through")
	}
	run(ws)
	if count(ws) != 0 {
		t.Fatalf("checkout not emptied: %d left", count(ws))
	}
	if _, err := os.Stat(ws); err != nil {
		t.Fatal("workspace folder itself must stay")
	}
}
