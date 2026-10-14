package store

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Pause is the machine-level pause: while it is on, the daemon runs no
// runner, no on-demand runner, no auto-sync and no update check. It is kept
// apart from each runner's Disabled flag, so resuming starts exactly the
// runners that were not stopped by hand.
type Pause struct {
	Paused bool      `json:"paused"`
	Since  time.Time `json:"since,omitzero"`
}

// PausePath is the file that makes a pause survive reboots and restarts.
func PausePath() string { return filepath.Join(Home(), "paused.json") }

// LoadPause reads the pause state; no file means not paused.
func LoadPause() Pause {
	var p Pause
	if err := readJSON(PausePath(), &p); err != nil {
		return Pause{}
	}
	return p
}

// IsPaused reports whether this machine is paused.
func IsPaused() bool { return LoadPause().Paused }

// SetPaused turns the pause on or off. Pausing twice keeps the first time.
func SetPaused(on bool) error {
	if !on {
		err := os.Remove(PausePath())
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if cur := LoadPause(); cur.Paused {
		return nil
	}
	return writeJSON(PausePath(), Pause{Paused: true, Since: time.Now().UTC()}, 0o644)
}
