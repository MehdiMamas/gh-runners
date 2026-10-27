package daemon

import (
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/store"
)

func TestUpdateEveryReadsStoredValue(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	if got := updateEvery(); got != 6*time.Hour {
		t.Fatalf("default = %v", got)
	}
	cfg, _ := store.LoadConfig()
	cfg.UpdateInterval = store.Duration{Duration: 10 * time.Minute}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if got := updateEvery(); got != 10*time.Minute {
		t.Fatalf("stored = %v", got)
	}
}

func TestRescheduled(t *testing.T) {
	now := time.Now()
	due := now.Add(5 * time.Hour)
	// last check 1h ago, new interval 10m: overdue, runs in seconds
	if got := rescheduled(now.Add(-time.Hour), now, 10*time.Minute, due); got != 5*time.Second {
		t.Fatalf("overdue = %v", got)
	}
	// last check 1m ago, interval 10m: 9m left
	if got := rescheduled(now.Add(-time.Minute), now, 10*time.Minute, due); got != 9*time.Minute {
		t.Fatalf("recent = %v", got)
	}
	// never checked: keep the sooner of due and the interval
	if got := rescheduled(time.Time{}, now, 10*time.Minute, due); got != 10*time.Minute {
		t.Fatalf("never = %v", got)
	}
	if got := rescheduled(time.Time{}, now, 6*time.Hour, now.Add(time.Minute)); got != time.Minute {
		t.Fatalf("never soon = %v", got)
	}
}
