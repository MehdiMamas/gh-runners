package store

import (
	"os"
	"strconv"
	"testing"
	"time"
)

func TestLastSessionRoundTrip(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	if got := LoadLastSession(); len(got.IDs) != 0 {
		t.Fatalf("fresh home: %+v", got)
	}
	if err := SaveLastSession([]string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	got := LoadLastSession()
	if len(got.IDs) != 2 || got.IDs[0] != "a" || got.SavedAt.IsZero() {
		t.Fatalf("after save: %+v", got)
	}
	if err := SaveLastSession(nil); err != nil || len(LoadLastSession().IDs) != 2 {
		t.Fatalf("an empty set overwrote the session: %+v %v", LoadLastSession(), err)
	}
}

func TestKeepRunningMarker(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	if ConsumeKeepRunning() {
		t.Fatal("marker on a fresh home")
	}
	if err := SetKeepRunning(); err != nil {
		t.Fatal(err)
	}
	if !ConsumeKeepRunning() || ConsumeKeepRunning() {
		t.Fatal("marker must be consumed exactly once")
	}
}

func TestKeepRunningStaleMarker(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	if err := SetKeepRunning(); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(KeepRunningPath(), old, old); err != nil {
		t.Fatal(err)
	}
	if ConsumeKeepRunning() {
		t.Fatal("stale marker honoured")
	}
	if _, err := os.Stat(KeepRunningPath()); err == nil {
		t.Fatal("stale marker not removed")
	}
}

func writeMarker(t *testing.T, content string, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(Home(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(KeepRunningPath(), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-age)
	if err := os.Chtimes(KeepRunningPath(), old, old); err != nil {
		t.Fatal(err)
	}
}

func TestKeepRunningBootTime(t *testing.T) {
	bt, err := BootTime()
	if err != nil {
		t.Skipf("no boot time here: %v", err)
	}
	unix := func(d time.Duration) string { return strconv.FormatInt(bt.Add(d).Unix(), 10) }
	for _, c := range []struct {
		name    string
		content string
		age     time.Duration
		want    bool
	}{
		{"same boot", unix(0), 0, true},
		{"within tolerance", unix(2 * time.Second), 0, true},
		{"before a reboot", unix(-time.Hour), 0, false},
		{"stale, same boot", unix(0), time.Hour, false},
		{"legacy empty marker", "", 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("GH_RUNNERS_HOME", t.TempDir())
			writeMarker(t, c.content, c.age)
			if got := ConsumeKeepRunning(); got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestSetKeepRunningWritesBootTime(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	bt, err := BootTime()
	if err != nil {
		t.Skipf("no boot time here: %v", err)
	}
	if err := SetKeepRunning(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(KeepRunningPath())
	if string(b) != strconv.FormatInt(bt.Unix(), 10) {
		t.Fatalf("marker %q, boot time %d", b, bt.Unix())
	}
	ClearKeepRunning()
	if _, err := os.Stat(KeepRunningPath()); err == nil {
		t.Fatal("ClearKeepRunning left the marker")
	}
}
