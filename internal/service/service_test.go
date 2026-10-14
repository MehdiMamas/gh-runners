package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSuffixFor(t *testing.T) {
	u, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	if s := SuffixFor(""); s != "" {
		t.Errorf("main copy: got %q", s)
	}
	if s := SuffixFor(filepath.Join(u, ".gh-runners")); s != "" {
		t.Errorf("default dir given explicitly: got %q", s)
	}
	a := SuffixFor(filepath.Join(t.TempDir(), "a"))
	b := SuffixFor(filepath.Join(t.TempDir(), "b"))
	if len(a) != 9 || a[0] != '-' || a == b {
		t.Errorf("other dirs: got %q and %q", a, b)
	}
	d := filepath.Join(t.TempDir(), "same")
	if SuffixFor(d) != SuffixFor(d+string(filepath.Separator)) {
		t.Errorf("trailing separator changes the suffix")
	}
}
