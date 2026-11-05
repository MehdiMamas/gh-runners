package daemon

import (
	"io"
	"log"
	"reflect"
	"testing"
)

func TestHoldIdle(t *testing.T) {
	for _, tc := range []struct {
		name    string
		busy    map[string]bool
		jitBusy int
		max     int
		want    []string
	}{
		{"no limit", map[string]bool{"a": false, "b": true}, 0, 0, nil},
		{"negative limit", map[string]bool{"a": false, "b": true}, 0, -1, nil},
		{"below the limit", map[string]bool{"a": false, "b": false}, 0, 1, nil},
		{"limit reached holds every idle runner", map[string]bool{"a": true, "b": false, "c": false}, 0, 1, []string{"b", "c"}},
		{"busy runners are never held", map[string]bool{"a": true, "b": true, "c": false}, 0, 2, []string{"c"}},
		{"jit busy counts toward the limit", map[string]bool{"a": false, "b": false}, 1, 1, []string{"a", "b"}},
		{"jit busy alone below the limit", map[string]bool{"a": false}, 1, 2, nil},
	} {
		if got := holdIdle(tc.busy, tc.jitBusy, tc.max, false); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestJitAllowed(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		max, maxJobs, busy, want int
	}{
		{"unset", 2, 0, 5, 2},
		{"cap above max", 2, 5, 0, 2},
		{"max below the cap", 1, 4, 1, 1},
		{"cap leaves room", 4, 3, 1, 2},
		{"floored at 0", 2, 1, 3, 0},
	} {
		if got := jitAllowed(tc.max, tc.maxJobs, tc.busy, false); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestHoldIdleInUse(t *testing.T) {
	busy := map[string]bool{"a": true, "b": false, "c": false}
	// In use, the limit counts as reached with no max_jobs and no jobs.
	if got := holdIdle(busy, 0, 0, true); !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Errorf("in use, busy runner kept: got %v", got)
	}
	if got := holdIdle(map[string]bool{"a": false}, 0, 5, true); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("in use, far below the limit: got %v", got)
	}
	if got := holdIdle(busy, 0, 0, false); got != nil {
		t.Errorf("free, no limit: got %v", got)
	}
}

func TestJitAllowedInUse(t *testing.T) {
	if got := jitAllowed(2, 0, 0, true); got != 0 {
		t.Errorf("in use: got %d, want 0", got)
	}
	if got := jitAllowed(2, 5, 0, true); got != 0 {
		t.Errorf("in use with a cap: got %d, want 0", got)
	}
}

func TestApplyLimitInUseHoldsThenLifts(t *testing.T) {
	d := &Daemon{Log: log.New(io.Discard, "", 0), children: map[string]*child{}, jit: newJITManager(nil)}
	// A runner that is held, nothing running: with the machine free again the
	// hold lifts only after holdTicks ticks, even without max_jobs.
	d.children["a"] = &child{held: true}
	for i := 1; i < holdTicks; i++ {
		if !d.applyLimit(0, false) {
			t.Fatalf("tick %d: hold lifted early", i)
		}
	}
	if d.applyLimit(0, false) {
		t.Fatal("hold did not lift after holdTicks free ticks")
	}
	if d.children["a"].held {
		t.Fatal("runner still held")
	}
	// In use again: the counter restarts, the hold stays.
	d.children["a"].held = true
	if !d.applyLimit(0, true) || d.below != 0 {
		t.Fatalf("in use must hold and reset the counter, below=%d", d.below)
	}
}
