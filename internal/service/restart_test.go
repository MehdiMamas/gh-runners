package service

import (
	"errors"
	"testing"
	"time"
)

// (a) The old daemon keeps answering and no new one starts: kick again after
// each step, then fail at the timeout.
func TestRestartLoopKicksAgainThenFails(t *testing.T) {
	kicks := 0
	err := restartLoop(
		func(bool) error { kicks++; return nil },
		func() (int, error) { return 100, nil },
		100*time.Millisecond, 20*time.Millisecond)
	if err == nil {
		t.Fatal("want an error at the timeout")
	}
	if kicks < 3 {
		t.Errorf("kicks = %d, want at least 3", kicks)
	}
}

// (b) The first spawn fails; the second kick starts the new daemon.
func TestRestartLoopSecondKickStarts(t *testing.T) {
	kicks := 0
	status := func() (int, error) {
		if kicks >= 2 {
			return 200, nil
		}
		return 100, nil
	}
	err := restartLoop(
		func(bool) error { kicks++; return nil },
		status, 2*time.Second, 30*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if kicks != 2 {
		t.Errorf("kicks = %d, want 2", kicks)
	}
}

// (c) A new PID at once: one kick only.
func TestRestartLoopNewPIDAtOnce(t *testing.T) {
	kicks := 0
	calls := 0
	status := func() (int, error) {
		calls++
		if calls == 1 {
			return 100, nil // old daemon, read before the kick
		}
		return 200, nil
	}
	err := restartLoop(
		func(bool) error { kicks++; return nil },
		status, 2*time.Second, 30*time.Millisecond)
	if err != nil || kicks != 1 {
		t.Errorf("err = %v, kicks = %d, want nil and 1", err, kicks)
	}
}

func TestRestartLoopFirstKickError(t *testing.T) {
	want := errors.New("boom")
	err := restartLoop(
		func(bool) error { return want },
		func() (int, error) { return 0, errors.New("down") },
		time.Second, 10*time.Millisecond)
	if !errors.Is(err, want) {
		t.Errorf("err = %v", err)
	}
}
