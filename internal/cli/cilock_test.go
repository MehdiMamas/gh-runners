package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/localci"
)

// helper: the test binary itself as the locked command.
func TestLockHelper(t *testing.T) {
	if os.Getenv("LOCK_HELPER") == "" {
		t.Skip()
	}
	if os.Getenv("LOCK_HELPER") == "exit7" {
		os.Exit(7)
	}
	os.Exit(0)
}

func helperArgs(mode string) []string {
	os.Setenv("LOCK_HELPER", mode)
	exe, _ := os.Executable()
	return []string{exe, "-test.run=TestLockHelper"}
}

func TestRunLockedWaitsThenRuns(t *testing.T) {
	dir := t.TempDir()
	holder := &localci.Locks{Dir: dir, Poll: 20 * time.Millisecond}
	rel, err := holder.Lock("emulator")
	if err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(300 * time.Millisecond); rel() }()
	var out, errb bytes.Buffer
	code, err := runLocked(nil, &localci.Locks{Dir: dir, Poll: 20 * time.Millisecond}, "emulator", 10*time.Second, helperArgs("ok"), nil, &out, &errb)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if n := strings.Count(errb.String(), "waiting for the emulator lock"); n != 1 {
		t.Fatalf("wait message printed %d times: %q", n, errb.String())
	}
}

func TestRunLockedTimeoutExits75(t *testing.T) {
	dir := t.TempDir()
	rel, _ := (&localci.Locks{Dir: dir, Poll: 20 * time.Millisecond}).Lock("emulator")
	defer rel()
	var errb bytes.Buffer
	code, err := runLocked(nil, &localci.Locks{Dir: dir, Poll: 20 * time.Millisecond}, "emulator", 200*time.Millisecond, helperArgs("ok"), nil, &bytes.Buffer{}, &errb)
	if code != 75 || err == nil || !strings.Contains(err.Error(), "still held after") {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestRunLockedPassesExitCode(t *testing.T) {
	code, err := runLocked(nil, &localci.Locks{Dir: t.TempDir(), Poll: 20 * time.Millisecond}, "emulator", time.Second, helperArgs("exit7"), nil, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil || code != 7 {
		t.Fatalf("code=%d err=%v", code, err)
	}
}
