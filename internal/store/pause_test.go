package store

import "testing"

func TestPauseRoundTrip(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	if IsPaused() {
		t.Fatal("paused on a fresh home")
	}
	if err := SetPaused(true); err != nil {
		t.Fatal(err)
	}
	first := LoadPause()
	if !first.Paused || first.Since.IsZero() || !IsPaused() {
		t.Fatalf("after pause: %+v", first)
	}
	if err := SetPaused(true); err != nil || !LoadPause().Since.Equal(first.Since) {
		t.Fatalf("pausing twice moved Since: %+v %v", LoadPause(), err)
	}
	if err := SetPaused(false); err != nil || IsPaused() {
		t.Fatalf("after resume: %+v %v", LoadPause(), err)
	}
	if err := SetPaused(false); err != nil {
		t.Fatalf("resume when not paused: %v", err)
	}
}
