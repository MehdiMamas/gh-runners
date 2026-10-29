package localci

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type tarEntry struct{ name, body, link string }

// makeLinkTarball writes a tar.gz with a "repo-abc/" top folder like GitHub's.
func makeLinkTarball(t *testing.T, entries []tarEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "src.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: "repo-abc/" + e.name, Mode: 0o644}
		switch {
		case e.link != "":
			h.Typeflag, h.Linkname = tar.TypeSymlink, e.link
		case strings.HasSuffix(e.name, "/"):
			h.Typeflag, h.Mode = tar.TypeDir, 0o755
		default:
			h.Typeflag, h.Size = tar.TypeReg, int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func noSymlinks(t *testing.T) {
	old := symlink
	symlink = func(string, string) error { return errors.New("A required privilege is not held by the client") }
	t.Cleanup(func() { symlink = old })
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestExtractCopiesRefusedLinks(t *testing.T) {
	noSymlinks(t)
	tb := makeLinkTarball(t, []tarEntry{
		{name: "a.txt", body: "hello"},
		{name: "lib/"},
		{name: "lib/x.txt", body: "x"},
		{name: "link.txt", link: "a.txt"},
		{name: "liblink", link: "lib"},
		{name: "chain.txt", link: "link.txt"},
		{name: "sub/"},
		{name: "sub/up.txt", link: "../a.txt"},
		{name: "escape", link: "../../etc"},
		{name: "dangling", link: "nope.txt"},
		{name: "sub/loop", link: ".."},
	})
	dest := filepath.Join(t.TempDir(), "ws")
	skipped, err := Extract(tb, dest)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dest, "link.txt")); got != "hello" {
		t.Errorf("file link = %q", got)
	}
	if got := readFile(t, filepath.Join(dest, "liblink", "x.txt")); got != "x" {
		t.Errorf("dir link = %q", got)
	}
	if got := readFile(t, filepath.Join(dest, "chain.txt")); got != "hello" {
		t.Errorf("chained link = %q", got)
	}
	if got := readFile(t, filepath.Join(dest, "sub", "up.txt")); got != "hello" {
		t.Errorf("relative link = %q", got)
	}
	want := []string{
		"skipped symlink escape -> ../../etc (this PC can't create symlinks)",
		"skipped symlink dangling -> nope.txt (this PC can't create symlinks)",
		"skipped symlink sub/loop -> .. (this PC can't create symlinks)",
	}
	if len(skipped) != len(want) || skipped[0] != want[0] || skipped[1] != want[1] || skipped[2] != want[2] {
		t.Errorf("skipped = %q, want %q", skipped, want)
	}
	for _, n := range []string{"escape", "dangling", "sub/loop"} {
		if _, err := os.Lstat(filepath.Join(dest, n)); err == nil {
			t.Errorf("%s should not exist", n)
		}
	}
}

func TestExtractKeepsWorkingSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	tb := makeLinkTarball(t, []tarEntry{{name: "a.txt", body: "hello"}, {name: "link.txt", link: "a.txt"}})
	dest := filepath.Join(t.TempDir(), "ws")
	skipped, err := Extract(tb, dest)
	if err != nil || len(skipped) != 0 {
		t.Fatalf("skipped=%q err=%v", skipped, err)
	}
	if got, err := os.Readlink(filepath.Join(dest, "link.txt")); err != nil || got != "a.txt" {
		t.Errorf("readlink = %q, %v", got, err)
	}
}

func TestExtractStillRefusesEscapingPaths(t *testing.T) {
	tb := makeLinkTarball(t, []tarEntry{{name: "../../evil.txt", body: "x"}})
	if _, err := Extract(tb, filepath.Join(t.TempDir(), "ws")); err == nil {
		t.Fatal("want an error for a path that escapes")
	}
}
