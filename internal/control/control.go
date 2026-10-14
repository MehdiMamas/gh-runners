// Package control is how CLI commands talk to a running daemon: a small
// HTTP API on 127.0.0.1, guarded by a secret stored in daemon.json (0600).
package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/store"
)

// SecretHeader carries the secret from daemon.json.
const SecretHeader = "X-GH-Runners-Secret"

// ProcState is the daemon's view of one supervised runner.
type ProcState struct {
	ID        string    `json:"id"`
	State     string    `json:"state"` // running | stopped | backoff | jit-idle | jit-busy
	PID       int       `json:"pid,omitempty"`
	Since     time.Time `json:"since"`
	Restarts  int       `json:"restarts"`
	LastExit  string    `json:"last_exit,omitempty"`
	NextStart time.Time `json:"next_start,omitempty"`
}

// Status is GET /status.
type Status struct {
	PID      int         `json:"pid"`
	Version  string      `json:"version"`
	Started  time.Time   `json:"started"`
	Runners  []ProcState `json:"runners"`
	LastSync time.Time   `json:"last_sync,omitempty"`
	SyncErr  string      `json:"sync_error,omitempty"`
	OnDemand string      `json:"on_demand,omitempty"`
	Update   UpdateState `json:"update"`
}

// UpdateState is what the daemon knows about gh-runners releases.
type UpdateState struct {
	Latest  string    `json:"latest,omitempty"`  // newest release seen
	Checked time.Time `json:"checked,omitempty"` // last successful check
	Note    string    `json:"note,omitempty"`    // what it is doing or waiting for
	Error   string    `json:"error,omitempty"`
}

// ErrNotRunning means no daemon answered.
var ErrNotRunning = errors.New("the gh-runners daemon is not running (start it with `gh-runners service install` or `gh-runners daemon`)")

var client = &http.Client{Timeout: 5 * time.Second}

func call(method, path string, out any) error {
	info, err := store.LoadDaemonInfo()
	if err != nil || info.Addr == "" {
		return ErrNotRunning
	}
	req, err := http.NewRequest(method, "http://"+info.Addr+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set(SecretHeader, info.Secret)
	resp, err := client.Do(req)
	if err != nil {
		return ErrNotRunning
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon %s %s: %s", method, path, resp.Status)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// GetStatus asks the daemon what it is supervising.
func GetStatus() (Status, error) {
	var s Status
	err := call(http.MethodGet, "/status", &s)
	return s, err
}

// Reload makes the daemon rescan runners now instead of at its next tick.
func Reload() error { return call(http.MethodPost, "/reload", nil) }

// Sync makes the daemon run auto-sync now.
func Sync() error { return call(http.MethodPost, "/sync", nil) }

// Update makes the daemon install the latest release as soon as no job runs.
func Update() error { return call(http.MethodPost, "/update", nil) }

// Shutdown stops the daemon and every runner it supervises.
func Shutdown() error { return call(http.MethodPost, "/shutdown", nil) }

// WaitStopped waits until the daemon no longer runs the runner id.
func WaitStopped(id string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		s, err := GetStatus()
		if err != nil {
			return nil // no daemon, nothing runs it
		}
		running := false
		for _, p := range s.Runners {
			if p.ID == id && p.PID != 0 {
				running = true
			}
		}
		if !running {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("runner %s is still running after %s", id, timeout)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
