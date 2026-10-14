package proc

import (
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
