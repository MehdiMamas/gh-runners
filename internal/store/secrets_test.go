package store

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSecretsRoundTrip(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	abs := filepath.Join(t.TempDir(), "key.p8")
	if err := SetSecret("o/r", "API_KEY", Secret{Value: "v1"}); err != nil {
		t.Fatal(err)
	}
	if err := SetSecret("o/r", "KEYFILE", Secret{File: abs}); err != nil {
		t.Fatal(err)
	}
	m, err := LoadSecrets("o/r")
	if err != nil || m["API_KEY"].Value != "v1" || m["KEYFILE"].File != abs {
		t.Fatalf("got %v %v", m, err)
	}
	names, _ := SecretNames("o/r")
	if len(names) != 2 || names[0] != "API_KEY" || names[1] != "KEYFILE" {
		t.Fatalf("names %v", names)
	}
	if ok, err := RemoveSecret("o/r", "API_KEY"); !ok || err != nil {
		t.Fatalf("remove %v %v", ok, err)
	}
	if ok, _ := RemoveSecret("o/r", "API_KEY"); ok {
		t.Fatal("removed twice")
	}
	if m, _ := LoadSecrets("o/other"); len(m) != 0 {
		t.Fatalf("other repo %v", m)
	}
}

func TestSecretsFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix modes")
	}
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	if err := SetSecret("o/r", "A", Secret{Value: "x"}); err != nil {
		t.Fatal(err)
	}
	p, _ := secretsPath("o/r")
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v %v", st, err)
	}
	if st, err := os.Stat(filepath.Dir(p)); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v %v", st, err)
	}
}

func TestSecretsBadInput(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	for _, n := range []string{"", "1A", "A-B", "A B", "a.b"} {
		if err := SetSecret("o/r", n, Secret{Value: "x"}); err == nil {
			t.Errorf("name %q accepted", n)
		}
	}
	if err := SetSecret("o/r", "A", Secret{}); err == nil {
		t.Error("empty secret accepted")
	}
	if err := SetSecret("o/r", "A", Secret{File: "relative.txt"}); err == nil {
		t.Error("relative file accepted")
	}
	for _, r := range []string{"o", "o/r/x", "../x/y", "o/.."} {
		if err := SetSecret(r, "A", Secret{Value: "x"}); err == nil {
			t.Errorf("repo %q accepted", r)
		}
	}
}
