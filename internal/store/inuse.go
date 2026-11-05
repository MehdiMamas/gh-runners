package store

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// InUsePath is the file the daemon keeps while the machine is in use (the
// busy_check command exited 75). The CLI reads it to route CI jobs away.
func InUsePath() string { return filepath.Join(Home(), "in-use") }

// InUse reports whether the machine is in use and since when.
func InUse() (since time.Time, ok bool) {
	b, err := os.ReadFile(InUsePath())
	if err != nil {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "since ")))
	if err != nil {
		return time.Time{}, true
	}
	return t, true
}

// SetInUse writes the in-use file; clearing removes it.
func SetInUse(on bool, since time.Time) error {
	if !on {
		if err := os.Remove(InUsePath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return os.WriteFile(InUsePath(), []byte("since "+since.UTC().Format(time.RFC3339)+"\n"), 0o600)
}
