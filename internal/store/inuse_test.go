package store

import (
	"reflect"
	"testing"
	"time"
)

func TestInUseRoundTrip(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	if _, ok := InUse(); ok {
		t.Fatal("in use on a fresh home")
	}
	at := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	if err := SetInUse(true, at); err != nil {
		t.Fatal(err)
	}
	since, ok := InUse()
	if !ok || !since.Equal(at) {
		t.Fatalf("got %v %v, want %v true", since, ok, at)
	}
	if err := SetInUse(false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := InUse(); ok {
		t.Fatal("still in use after clearing")
	}
	if err := SetInUse(false, time.Time{}); err != nil {
		t.Fatalf("clearing when not in use: %v", err)
	}
}

func TestBusyCheckConfigRoundTrip(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	cfg := Config{BusyCheck: []string{"sh", "-c", "exit 75"}}
	if err := SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig()
	if err != nil || !reflect.DeepEqual(got.BusyCheck, cfg.BusyCheck) {
		t.Fatalf("got %v %v", got.BusyCheck, err)
	}
}
