package runner

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func writeZip(t *testing.T, path string, names ...string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("x"))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeTarGz(t *testing.T, path string, hdrs ...tar.Header) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, h := range hdrs {
		if h.Typeflag == tar.TypeReg {
			h.Size = 1
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write([]byte("x"))
		}
	}
	tw.Close()
	gz.Close()
}

func TestExtractZip(t *testing.T) {
	dir := t.TempDir()
	ok := filepath.Join(dir, "ok.zip")
	writeZip(t, ok, "run.cmd", "bin/Runner.Listener.exe")
	out := filepath.Join(dir, "out")
	if err := Extract(ok, out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "bin", "Runner.Listener.exe")); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.zip")
	writeZip(t, bad, "../evil.txt")
	if err := Extract(bad, filepath.Join(dir, "out2")); err == nil {
		t.Fatal("zip slip not rejected")
	}
	if _, err := os.Stat(filepath.Join(dir, "evil.txt")); err == nil {
		t.Fatal("zip slip wrote outside the target")
	}
}

func TestExtractTarGz(t *testing.T) {
	dir := t.TempDir()
	ok := filepath.Join(dir, "ok.tar.gz")
	writeTarGz(t, ok,
		tar.Header{Name: "bin/", Typeflag: tar.TypeDir, Mode: 0o755},
		tar.Header{Name: "run.sh", Typeflag: tar.TypeReg, Mode: 0o755},
	)
	out := filepath.Join(dir, "out")
	if err := Extract(ok, out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "run.sh")); err != nil {
		t.Fatal(err)
	}
	for name, h := range map[string]tar.Header{
		"slip":     {Name: "../evil.sh", Typeflag: tar.TypeReg, Mode: 0o644},
		"abs-link": {Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		"up-link":  {Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../../etc/passwd"},
	} {
		bad := filepath.Join(dir, name+".tar.gz")
		writeTarGz(t, bad, h)
		if err := Extract(bad, filepath.Join(dir, "out-"+name)); err == nil {
			t.Errorf("%s not rejected", name)
		}
	}
}

func TestPlatform(t *testing.T) {
	osName, arch, err := Platform()
	if err != nil || osName == "" || arch == "" {
		t.Fatalf("Platform() = %q %q %v", osName, arch, err)
	}
}
