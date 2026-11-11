package daemon

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

func TestDrainDue(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		since time.Time
		busy  bool
		want  bool
	}{
		{"not pending", time.Time{}, true, false},
		{"just started", now.Add(-time.Minute), true, false},
		{"just under", now.Add(-updateDrainAfter + time.Second), true, false},
		{"exactly due", now.Add(-updateDrainAfter), true, true},
		{"long overdue", now.Add(-3 * time.Hour), true, true},
		{"nothing busy", now.Add(-3 * time.Hour), false, false},
	}
	for _, c := range cases {
		if got := drainDue(c.since, now, c.busy, updateDrainAfter); got != c.want {
			t.Errorf("%s: drainDue = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNoteWaitingDrainsOnceAndEnds(t *testing.T) {
	var buf bytes.Buffer
	d := &Daemon{Log: log.New(&buf, "", 0)}
	start := time.Now()
	d.noteWaiting(start)
	if d.draining.Load() {
		t.Fatal("draining right away")
	}
	d.noteWaiting(start.Add(updateDrainAfter))
	d.noteWaiting(start.Add(updateDrainAfter + time.Minute))
	if !d.draining.Load() || !d.holdAll() {
		t.Fatal("not draining after the wait")
	}
	if n := strings.Count(buf.String(), "update pending: draining runners"); n != 1 {
		t.Fatalf("drain line logged %d times: %q", n, buf.String())
	}
	d.endDrain()
	if d.draining.Load() || d.holdAll() || d.updSince.Load() != 0 {
		t.Fatal("drain not lifted")
	}
}
