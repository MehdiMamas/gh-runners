package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestWaitIdle(t *testing.T) {
	defer func(b func(context.Context) (string, bool), e time.Duration) { busyNow, idleEvery = b, e }(busyNow, idleEvery)
	idleEvery = time.Millisecond

	answers := []string{"the job on mac-1 (o/r) to finish", "the job on mac-1 (o/r) to finish", ""}
	calls := 0
	busyNow = func(context.Context) (string, bool) {
		why := answers[calls]
		calls++
		return why, false
	}
	var out bytes.Buffer
	if err := waitIdle(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("restarted after %d checks, want 3 (only once idle)", calls)
	}
	if n := strings.Count(out.String(), "Waiting for"); n != 1 {
		t.Fatalf("printed the wait %d times:\n%s", n, out.String())
	}

	// Cancelled while busy: no restart.
	busyNow = func(context.Context) (string, bool) { return "a job", false }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitIdle(ctx, &out); err == nil {
		t.Fatal("waitIdle returned nil while a job was running")
	}

	// Can't tell (no daemon, not signed in): restart at once.
	busyNow = func(context.Context) (string, bool) { return "", true }
	if err := waitIdle(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
}
