// Package control is how CLI commands talk to a running daemon: a small
// HTTP API on 127.0.0.1, guarded by a secret stored in daemon.json (0600).
package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/localci"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// SecretHeader carries the secret from daemon.json.
const SecretHeader = "X-GH-Runners-Secret"

// ProcState is the daemon's view of one supervised runner.
type ProcState struct {
	ID    string `json:"id"`
	State string `json:"state"` // running | stopping | stopped | backoff | jit-idle | jit-busy
	PID   int    `json:"pid,omitempty"`
	// Strays counts leftover processes of the runner that nothing supervises;
	// while there are some it is not "stopped".
	Strays    int       `json:"strays,omitempty"`
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
	// Paused is the machine-level pause: nothing runs until Resume.
	Paused      bool      `json:"paused,omitempty"`
	PausedSince time.Time `json:"paused_since,omitzero"`
}

// UpdateState is what the daemon knows about gh-runners releases.
type UpdateState struct {
	Latest  string    `json:"latest,omitempty"` // newest release seen
	Checked time.Time `json:"checked,omitzero"` // last successful check
	Note    string    `json:"note,omitempty"`   // what it is doing or waiting for
	Error   string    `json:"error,omitempty"`
	Every   string    `json:"every,omitempty"` // check interval in use
	Next    time.Time `json:"next,omitzero"`   // when the next check is due
}

// ErrNotRunning means no daemon answered.
var ErrNotRunning = errors.New("the gh-runners daemon is not running (start it with `gh-runners service install` or `gh-runners daemon`)")

var client = &http.Client{Timeout: 5 * time.Second}

// slowClient is for calls that wait for processes to stop.
var slowClient = &http.Client{Timeout: 90 * time.Second}

func call(method, path string, out any) error { return do(client, method, path, out) }

func callSlow(method, path string, out any) error { return do(slowClient, method, path, out) }

func do(client *http.Client, method, path string, out any) error {
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

// Fleet makes the daemon look at its main host link again (after join or
// leave).
func Fleet() error { return call(http.MethodPost, "/fleet", nil) }

// Sync makes the daemon run auto-sync now.
func Sync() error { return call(http.MethodPost, "/sync", nil) }

// Update makes the daemon install the latest release as soon as no job runs.
func Update() error { return call(http.MethodPost, "/update", nil) }

// Pause stops every runner on this machine, with its processes, and holds
// on-demand runners, sync and updates until Resume. It returns once they
// are down.
func Pause() error { return callSlow(http.MethodPost, "/pause", nil) }

// Resume starts the runners that were running before Pause and turns
// on-demand, sync and updates back on.
func Resume() error { return call(http.MethodPost, "/resume", nil) }

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

// CIRunRequest is POST /ci/run: run one CI job on a fleet member.
type CIRunRequest struct {
	MemberID string          `json:"member_id"`
	Spec     localci.JobSpec `json:"spec"`
}

// CIMachines lists the fleet members that can run CI jobs. The error is
// ErrNotRunning when this machine is not a running main host.
func CIMachines() ([]localci.Machine, error) {
	var ms []localci.Machine
	err := call(http.MethodGet, "/ci/machines", &ms)
	return ms, err
}

// CIExecutor runs jobs on one fleet member through the local daemon, which
// is the main host. It implements localci.Executor.
type CIExecutor struct{ MemberID string }

// RunJob sends the job and waits for the result; a job can take a long time,
// so there is no timeout beyond ctx.
func (e CIExecutor) RunJob(ctx context.Context, spec localci.JobSpec) localci.Result {
	fail := func(msg string) localci.Result {
		return localci.Result{Job: spec.Name, Log: spec.Log, Status: localci.Fail, Reason: msg}
	}
	info, err := store.LoadDaemonInfo()
	if err != nil || info.Addr == "" {
		return fail(ErrNotRunning.Error())
	}
	spec.Env = nil // the member builds its own environment
	body, err := json.Marshal(CIRunRequest{MemberID: e.MemberID, Spec: spec})
	if err != nil {
		return fail(err.Error())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+info.Addr+"/ci/run", bytes.NewReader(body))
	if err != nil {
		return fail(err.Error())
	}
	req.Header.Set(SecretHeader, info.Secret)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return localci.Result{Job: spec.Name, Log: spec.Log, Status: localci.Skipped, Reason: "cancelled"}
		}
		return fail("lost the daemon: " + err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fail("daemon: " + resp.Status)
	}
	var res localci.Result
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return fail("bad answer from the daemon")
	}
	return res
}

// CIDispatcher sends each CI job to the machine that fits: this one, or a
// fleet member when this machine is the running main host.
func CIDispatcher() *localci.Dispatcher {
	host, _ := os.Hostname()
	cfg, _ := store.LoadConfig()
	self := localci.LocalMachine(host, cfg.CIEmulatorOK())
	return &localci.Dispatcher{
		Machines: func() []localci.Machine {
			ms := []localci.Machine{self}
			if remote, err := CIMachines(); err == nil {
				ms = append(ms, remote...)
			}
			return ms
		},
		Remote: func(m localci.Machine) localci.Executor { return CIExecutor{MemberID: m.ID} },
		Locks:  &localci.Locks{Dir: filepath.Join(store.Home(), "ci", "locks")},
	}
}
