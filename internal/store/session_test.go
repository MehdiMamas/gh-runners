package store

import "testing"

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
