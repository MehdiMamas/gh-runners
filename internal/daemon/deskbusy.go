package daemon

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/proc"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// Machine in use (#164): the busy_check command from the config says whether
// someone is using this machine. Exit 0 is free, exit 75 is in use, anything
// else (including a missing program) counts as free. While in use, runners
// take no new jobs (hold.go); a job that runs finishes.
const (
	deskEvery   = 45 * time.Second
	deskTimeout = 15 * time.Second
	inUseExit   = 75
)

// deskLoop runs the check every deskEvery, except while manually paused.
func (d *Daemon) deskLoop(ctx context.Context) {
	// A file left by a crash is cleared; the first check writes it again.
	if err := store.SetInUse(false, time.Time{}); err != nil {
		d.Log.Printf("clear in-use file: %v", err)
	}
	for {
		if !d.paused.Load() {
			d.deskCheck(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(deskEvery):
		}
	}
}

// deskCheck runs the check once and acts on a change.
func (d *Daemon) deskCheck(ctx context.Context) {
	check := d.inUseCheck
	if check == nil {
		check = runBusyCheck
	}
	on, err := check(ctx)
	msg := ""
	if err != nil {
		on, msg = false, err.Error()
	}
	if msg != d.deskErr {
		d.deskErr = msg
		if msg != "" {
			d.Log.Printf("busy_check: %s (counts as free)", msg)
		}
	}
	if on == d.deskBusy.Load() {
		return
	}
	now := time.Now()
	if on {
		d.deskAt.Store(now.UnixNano())
		d.Log.Print("machine in use: runners take no new jobs")
	} else {
		d.Log.Print("machine free: runners back on")
	}
	if err := store.SetInUse(on, now); err != nil {
		d.Log.Printf("write in-use file: %v", err)
	}
	d.deskBusy.Store(on)
	nudge(d.reload)
	if d.jit != nil {
		d.jit.wake()
	}
	d.tellHost()
}

// runBusyCheck runs the configured busy_check command. It reports true for
// exit 75 and false for exit 0; any other result is an error. A command that
// runs past deskTimeout is killed with everything it started.
func runBusyCheck(ctx context.Context) (bool, error) {
	cfg, _ := store.LoadConfig()
	if len(cfg.BusyCheck) == 0 {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, deskTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cfg.BusyCheck[0], cfg.BusyCheck[1:]...)
	hideWindow(cmd)
	cmd.Cancel = func() error { return proc.KillTree(cmd.Process.Pid) }
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if err == nil {
		return false, nil
	}
	if ctx.Err() != nil {
		return false, fmt.Errorf("%s timed out after %s", cfg.BusyCheck[0], deskTimeout)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ee.ExitCode() == inUseExit {
			return true, nil
		}
		return false, fmt.Errorf("%s exited %d", cfg.BusyCheck[0], ee.ExitCode())
	}
	return false, err
}
