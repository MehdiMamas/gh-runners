//go:build darwin || linux

package ops

import (
	"reflect"
	"testing"
)

func TestMatchProcs(t *testing.T) {
	ps := `  101 /bin/bash /Users/me/actions-runner/run.sh
  102 /Users/me/actions-runner/bin/Runner.Listener run
  103 /Users/me/actions-runner-light/bin/Runner.Listener run
  104 vim /Users/me/notes.txt
  105 /Users/me/actions-runner/bin/Runner.Worker spawnclient 1 2
`
	got := matchProcs(ps, "/Users/me/actions-runner", 105)
	if want := []int{101, 102}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
