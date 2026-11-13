package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProtectedFolderIn(t *testing.T) {
	h := "/Users/me"
	for dir, want := range map[string]string{
		"/Users/me/Documents/runners":          "~/Documents",
		"/Users/me/documents":                  "~/Documents",
		"/Users/me/Library/Mobile Documents/x": "~/Library/Mobile Documents",
		"/Users/me/.gh-runners/runners":        "",
		"/Users/me/actions-runner/_work":       "",
		"/Users/me/DocumentsX":                 "",
	} {
		if got := protectedFolderIn(dir, h); got != want {
			t.Errorf("%s: got %q want %q", dir, got, want)
		}
	}
}

func TestProtectedFolderIn_extra(t *testing.T) {
	h := "/Users/me"
	for dir, want := range map[string]string{
		"/Users/me/Documents":              "~/Documents",
		"/Users/me/Library/CloudStorage/x": "~/Library/CloudStorage",
		"/Users/me/.gh-runners/runners":    "",
	} {
		if got := protectedFolderIn(dir, h); got != want {
			t.Errorf("%s: got %q want %q", dir, got, want)
		}
	}
}

func TestResolveFollowsSymlink(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "Documents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "Documents"), filepath.Join(home, "link")); err != nil {
		t.Skip("no symlinks")
	}
	dir := filepath.Join(home, "link", "runners")
	if got := protectedFolderIn(resolve(dir), resolve(home)); got != "~/Documents" {
		t.Errorf("symlink into Documents: got %q", got)
	}
}
