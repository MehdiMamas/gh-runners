package daemon

import (
	"bytes"
	"context"
	"errors"
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

func TestDrainHoldsOnDemand(t *testing.T) {
	d := &Daemon{}
	d.draining.Store(true)
	if !d.holdAll() {
		t.Fatal("draining does not hold")
	}
	if n := jitAllowed(3, 0, 0, d.holdAll()); n != 0 {
		t.Fatalf("jitAllowed while draining = %d, want 0", n)
	}
}

func TestNoteWaitOnlyForRunnerJobs(t *testing.T) {
	d := &Daemon{Log: log.New(&bytes.Buffer{}, "", 0)}
	start := time.Now()
	d.noteWait("the job on r1 (o/r) to finish", start)
	d.noteWait("the job on r1 (o/r) to finish", start.Add(updateDrainAfter))
	if !d.draining.Load() {
		t.Fatal("runner job wait did not drain")
	}
	since := d.updSince.Load()
	d.noteWait("a local CI run is in progress", start.Add(updateDrainAfter))
	if !d.draining.Load() || d.updSince.Load() != since {
		t.Fatal("a CI wait reset the drain")
	}
	d.endDrain()
	d.noteWait("GitHub to answer (x)", start)
	if d.updSince.Load() != 0 || d.draining.Load() {
		t.Fatal("GitHub wait started the clock")
	}
	d.noteWait("2"+jitWaitSuffix, start)
	if d.updSince.Load() == 0 {
		t.Fatal("on-demand wait not noted")
	}
}

func TestCheckUpdateErrorEndsDrain(t *testing.T) {
	old := latestRelease
	defer func() { latestRelease = old }()
	latestRelease = func(context.Context) (string, error) { return "", errors.New("504") }
	d := &Daemon{Version: "v0.1.0", Log: log.New(&bytes.Buffer{}, "", 0)}
	d.updSince.Store(time.Now().UnixNano())
	d.draining.Store(true)
	next, pending := d.checkUpdate(context.Background(), false)
	if d.draining.Load() || d.updSince.Load() != 0 || pending || next != time.Hour {
		t.Fatalf("drain kept after failed check: %v %v", next, pending)
	}
}

func TestCheckUpdateNotReleasableEndsDrain(t *testing.T) {
	d := &Daemon{Version: "dev", Log: log.New(&bytes.Buffer{}, "", 0)}
	d.draining.Store(true)
	d.checkUpdate(context.Background(), false)
	if d.draining.Load() {
		t.Fatal("drain kept for a dev build")
	}
}
