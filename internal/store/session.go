package store

import (
	"os"
	"path/filepath"
	"time"
)

// LastSession is the set of runners that were on when the daemon last
// started with everything stopped. "Run what was on before it closed" starts
// exactly these.
type LastSession struct {
	IDs     []string  `json:"ids"`
	SavedAt time.Time `json:"saved_at,omitzero"`
}

// LastSessionPath is the file that keeps the last session across restarts.
func LastSessionPath() string { return filepath.Join(Home(), "last-session.json") }

// LoadLastSession reads the last session; no file means none.
func LoadLastSession() LastSession {
	var s LastSession
	if err := readJSON(LastSessionPath(), &s); err != nil {
		return LastSession{}
	}
	return s
}

// SaveLastSession remembers ids as the last session. An empty set is never
// saved, so a start with nothing on keeps the earlier session.
func SaveLastSession(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return writeJSON(LastSessionPath(), LastSession{IDs: ids, SavedAt: time.Now().UTC()}, 0o644)
}

// KeepRunningPath is the marker that tells the next daemon start to leave the
// runners on (an update restart, not a fresh start).
func KeepRunningPath() string { return filepath.Join(Home(), "keep-running") }

// SetKeepRunning writes the keep-running marker.
func SetKeepRunning() error {
	if err := os.MkdirAll(Home(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(KeepRunningPath(), nil, 0o644)
}

// ConsumeKeepRunning removes the marker and reports whether it was there.
func ConsumeKeepRunning() bool {
	err := os.Remove(KeepRunningPath())
	return err == nil
}
