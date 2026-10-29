package localci

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Test binaries must listen on loopback only: a wildcard listener makes
// Windows Firewall and macOS Local Network ask the user for permission.
func TestNoWildcardListenInTests(t *testing.T) {
	root := filepath.Join("..", "..")
	pats := []string{`Listen("tcp", ":`, `"0.0.0.0`}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == ".worktrees" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") || d.Name() == "nowildcard_test.go" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			for _, p := range pats {
				if strings.Contains(line, p) {
					t.Errorf("%s:%d listens on a non-loopback address: %s", path, i+1, strings.TrimSpace(line))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
