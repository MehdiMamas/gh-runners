package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// maxLines is the most lines a source file may have (see #91).
const maxLines = 800

// bigFiles are the files that were already over the cap when it was set.
// Each may shrink but must not grow past its recorded size.
var bigFiles = map[string]int{
	"internal/web/assets/app.js": 1956,
	"internal/cli/commands.go":   920,
	"internal/fleet/peer.go":     972,
}

func TestFileSizes(t *testing.T) {
	seen := map[string]bool{}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			switch name {
			case ".git", ".worktrees", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		rel := filepath.ToSlash(path)
		isGo := strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
		isJS := strings.HasSuffix(name, ".js") && strings.HasPrefix(rel, "internal/web/assets/")
		if !isGo && !isJS {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		n := strings.Count(string(data), "\n")
		if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
			n++
		}
		recorded, listed := bigFiles[rel]
		if listed {
			seen[rel] = true
		}
		switch {
		case listed && n < maxLines:
			t.Errorf("%s has %d lines, under the cap of %d. Remove it from bigFiles in structure_test.go (see #91).", rel, n, maxLines)
		case listed && n > recorded:
			t.Errorf("%s has %d lines; it is over its recorded size of %d and must not grow. Split it by what it does (see #91).", rel, n, recorded)
		case !listed && n > maxLines:
			t.Errorf("%s has %d lines; the cap is %d. Split it by what it does (see #91).", rel, n, maxLines)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for rel := range bigFiles {
		if !seen[rel] {
			t.Errorf("%s is listed in bigFiles but does not exist. Remove it from the list (see #91).", rel)
		}
	}
}
