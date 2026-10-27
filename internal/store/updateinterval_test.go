package store

import (
	"testing"
	"time"
)

func TestParseUpdateInterval(t *testing.T) {
	ok := map[string]time.Duration{"10m": 10 * time.Minute, "2h": 2 * time.Hour, "5m": 5 * time.Minute, "168h": 7 * 24 * time.Hour, "default": 0, "": 0, " Default ": 0, "10min": 10 * time.Minute, "2 hours": 2 * time.Hour, "1 day": 24 * time.Hour}
	for in, want := range ok {
		if got, err := ParseUpdateInterval(in); err != nil || got != want {
			t.Errorf("%q = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"4m59s", "1s", "169h", "-10m", "soon", "10"} {
		if _, err := ParseUpdateInterval(in); err == nil {
			t.Errorf("%q should be refused", in)
		}
	}
}

func TestUpdateEveryDefaultAndStored(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	cfg, _ := LoadConfig()
	if cfg.UpdateEvery() != 6*time.Hour {
		t.Fatalf("default = %v", cfg.UpdateEvery())
	}
	cfg.UpdateInterval = Duration{10 * time.Minute}
	if err := SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	got, _ := LoadConfig()
	if got.UpdateEvery() != 10*time.Minute {
		t.Fatalf("stored = %v", got.UpdateEvery())
	}
	if ShortDuration(10*time.Minute) != "10m" || ShortDuration(6*time.Hour) != "6h" || ShortDuration(90*time.Minute) != "1h30m" {
		t.Fatal("ShortDuration")
	}
}
