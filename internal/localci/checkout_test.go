package localci

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckoutMakesGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "a.txt"), []byte("a\nb\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644)
	var log bytes.Buffer
	if err := gitInit(context.Background(), dir, os.Environ(), &log, "o/r", "abc123"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(log.String(), "::warning") {
		t.Fatalf("unexpected warning: %s", log.String())
	}
	git := func(args ...string) string {
		c := exec.Command("git", args...)
		c.Dir = dir
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	top, _ := filepath.EvalSymlinks(git("rev-parse", "--show-toplevel"))
	want, _ := filepath.EvalSymlinks(dir)
	if filepath.Clean(top) != filepath.Clean(want) {
		t.Errorf("toplevel %q, want %q", top, want)
	}
	if s := git("status", "--porcelain"); s != "" {
		t.Errorf("status not clean: %q", s)
	}
	if u := git("remote", "get-url", "origin"); u != "https://github.com/o/r.git" {
		t.Errorf("origin %q", u)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Errorf("removing a repo dir: %v", err)
	}
}

func TestCheckoutWithoutGitWarns(t *testing.T) {
	t.Setenv("PATH", "")
	var log bytes.Buffer
	if err := gitInit(context.Background(), t.TempDir(), nil, &log, "o/r", "abc"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "::warning checkout:") {
		t.Errorf("log = %q", log.String())
	}
}
