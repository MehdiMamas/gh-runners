package proc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeEnv(t *testing.T) {
	base := []string{`=C:=C:/work`, `Path=C:/old`, `HOME=C:/me`, `KEEP=1`}
	fresh := []string{`PATH=C:/old;C:/swift/bin`, `Home=C:/me`, `NEW=2`}
	got := strings.Join(mergeEnv(base, fresh), "|")
	want := `=C:=C:/work|PATH=C:/old;C:/swift/bin|Home=C:/me|KEEP=1|NEW=2`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestWithPathFile(t *testing.T) {
	dir := t.TempDir()
	sep := string(os.PathListSeparator)
	join := func(p ...string) string { return strings.Join(p, sep) }
	base := []string{"HOME=/h", "PATH=" + join("/daemon/bin", "/new/bin")}
	if got := strings.Join(withPathFile(base, dir), "|"); got != "HOME=/h|PATH="+join("/daemon/bin", "/new/bin") {
		t.Fatalf("no .path file changed the env: %s", got)
	}
	file := join("/mine/bin", "/daemon/bin", "/mine/bin") + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".path"), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	want := "HOME=/h|PATH=" + join("/mine/bin", "/daemon/bin", "/new/bin")
	if got := strings.Join(withPathFile(base, dir), "|"); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}
