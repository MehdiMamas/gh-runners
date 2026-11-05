package daemon

import (
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
		if got := holdIdle(tc.busy, tc.jitBusy, tc.max); !reflect.DeepEqual(got, tc.want) {
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
		if got := jitAllowed(tc.max, tc.maxJobs, tc.busy); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}
