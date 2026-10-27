package store

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseHumanDuration(t *testing.T) {
	ok := map[string]time.Duration{
		"10m": 10 * time.Minute, "1h30m": 90 * time.Minute, "10m0s": 10 * time.Minute,
		"10min": 10 * time.Minute, "10 min": 10 * time.Minute, "10 mins": 10 * time.Minute, "10 Minutes": 10 * time.Minute,
		"2 hours": 2 * time.Hour, "2hrs": 2 * time.Hour, "1.5h": 90 * time.Minute, "1d": 24 * time.Hour, "1 day": 24 * time.Hour,
		"1 day 6 hours": 30 * time.Hour, "1 hour, 30 minutes": 90 * time.Minute, "1 hour and 30 mins": 90 * time.Minute, " 1 week ": 7 * 24 * time.Hour,
	}
	for in, want := range ok {
		if got, err := ParseHumanDuration(in); err != nil || got != want {
			t.Errorf("%q = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "soon", "10", "10 parsecs", "ten minutes", "10 minutes please", "min"} {
		if _, err := ParseHumanDuration(in); err == nil {
			t.Errorf("%q should be refused", in)
		}
	}
}

// The dashboard sends what was typed; the API must take "10min" too.
func TestDurationJSONHumanForms(t *testing.T) {
	var c struct{ D Duration }
	if err := json.Unmarshal([]byte(`{"D":"10min"}`), &c); err != nil || c.D.Duration != 10*time.Minute {
		t.Fatalf("10min = %v, %v", c.D, err)
	}
}
