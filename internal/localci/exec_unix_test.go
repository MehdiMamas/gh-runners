//go:build !windows

package localci

import "testing"

func TestRunJobSelfSigterm(t *testing.T) {
	res, log := runSteps(t, "    steps:\n      - run: kill -TERM $$\n")
	if res.Status != Fail {
		t.Fatalf("got %v %q\n%s", res.Status, res.Reason, log)
	}
}
