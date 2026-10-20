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
	base := []string{"HOME=/h", "PATH=/daemon/bin"}
	if got := strings.Join(withPathFile(base, dir), "|"); got != "HOME=/h|PATH=/daemon/bin" {
		t.Fatalf("no .path file changed the env: %s", got)
	}
	if err := os.WriteFile(filepath.Join(dir, ".path"), []byte("/mine/bin:/daemon/bin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(withPathFile(base, dir), "|"); got != "HOME=/h|PATH=/mine/bin:/daemon/bin" {
		t.Fatalf(".path did not take precedence: %s", got)
	}
}
