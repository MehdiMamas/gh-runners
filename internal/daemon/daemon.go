// Package daemon is the per-machine supervisor: it keeps every registered
// runner's listener running, restarts it with backoff when it exits, runs
// auto-sync, and starts on-demand (JIT) runners for queued jobs.
package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/ops"
	"github.com/MehdiMamas/gh-runners/internal/proc"
	"github.com/MehdiMamas/gh-runners/internal/runner"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

const (
	tick        = 5 * time.Second
	minBackoff  = 5 * time.Second
	maxBackoff  = 5 * time.Minute
	stableAfter = 10 * time.Minute
	stopGrace   = 30 * time.Second
	maxLogSize  = 10 << 20
)

type child struct {
	cmd      *exec.Cmd
	done     chan struct{}
	stopping bool
	state    control.ProcState
}

// Daemon supervises runners on this machine.
type Daemon struct {
	Version string
	Log     *log.Logger

	mu       sync.Mutex
	children map[string]*child
	started  time.Time
	reload   chan struct{}
	syncNow  chan struct{}
	lastSync time.Time
	syncErr  string
	jit      *jitManager

	tokMu    sync.Mutex
	tok      string
	tokAt    time.Time
	ghClient *github.Client
}

// Run blocks until ctx is cancelled or /shutdown is called, then stops
// every runner it started.
func (d *Daemon) Run(ctx context.Context) error {
	if s, err := control.GetStatus(); err == nil {
		return fmt.Errorf("a gh-runners daemon is already running (pid %d)", s.PID)
	}
	if d.Log == nil {
		d.Log = log.New(os.Stderr, "", log.LstdFlags)
	}
	d.children = map[string]*child{}
	d.started = time.Now()
	d.reload = make(chan struct{}, 1)
	d.syncNow = make(chan struct{}, 1)
	d.jit = newJITManager(d)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	secret := randomHex(16)
	info := store.DaemonInfo{PID: os.Getpid(), Addr: ln.Addr().String(), Secret: secret, Version: d.Version, Started: d.started}
	if err := store.SaveDaemonInfo(info); err != nil {
		return err
	}
	defer func() {
		// Leave daemon.json alone if a newer daemon replaced it.
		if cur, err := store.LoadDaemonInfo(); err == nil && cur.PID == info.PID {
			os.Remove(store.DaemonPath())
		}
	}()
	srv := &http.Server{Handler: d.handler(secret, cancel), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	d.Log.Printf("gh-runners daemon %s started (pid %d, home %s)", d.Version, info.PID, store.Home())
	go d.syncLoop(ctx)
	go d.jit.loop(ctx)

	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		d.reconcile()
		select {
		case <-ctx.Done():
			d.Log.Printf("stopping all runners")
			d.stopAll()
			d.jit.stopAll()
			d.Log.Printf("daemon stopped")
			return nil
		case <-t.C:
		case <-d.reload:
		}
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (d *Daemon) handler(secret string, shutdown context.CancelFunc) http.Handler {
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(control.SecretHeader) != secret {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /status", auth(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(d.status())
	}))
	mux.HandleFunc("POST /reload", auth(func(w http.ResponseWriter, r *http.Request) {
		nudge(d.reload)
	}))
	mux.HandleFunc("POST /sync", auth(func(w http.ResponseWriter, r *http.Request) {
		nudge(d.syncNow)
	}))
	mux.HandleFunc("POST /shutdown", auth(func(w http.ResponseWriter, r *http.Request) {
		shutdown()
	}))
	return mux
}

func nudge(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

func (d *Daemon) status() control.Status {
	d.mu.Lock()
	s := control.Status{PID: os.Getpid(), Version: d.Version, Started: d.started, LastSync: d.lastSync, SyncErr: d.syncErr}
	for _, c := range d.children {
		s.Runners = append(s.Runners, c.state)
	}
	d.mu.Unlock()
	s.Runners = append(s.Runners, d.jit.states()...)
	s.OnDemand = d.jit.summary()
	sort.Slice(s.Runners, func(i, j int) bool { return s.Runners[i].ID < s.Runners[j].ID })
	return s
}

// reconcile starts runners that should run and stops the ones that shouldn't.
func (d *Daemon) reconcile() {
	runners, err := store.List()
	if err != nil {
		d.Log.Printf("list runners: %v", err)
		return
	}
	want := map[string]store.Runner{}
	for _, r := range runners {
		if !r.Disabled && r.Configured() {
			want[r.ID] = r
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	for id, c := range d.children {
		if _, ok := want[id]; ok {
			continue
		}
		if c.cmd != nil && !c.stopping {
			c.stopping = true
			c.state.State = "stopping"
			d.Log.Printf("[%s] stopping", id)
			go proc.Stop(c.cmd, c.done, stopGrace)
		}
		if c.cmd == nil {
			delete(d.children, id)
		}
	}
	for id, r := range want {
		c := d.children[id]
		if c == nil {
			c = &child{state: control.ProcState{ID: id, State: "stopped", Since: now}}
			d.children[id] = c
		}
		if c.cmd != nil || now.Before(c.state.NextStart) {
			continue
		}
		if err := d.start(r, c); err != nil {
			d.Log.Printf("[%s] start failed: %v", id, err)
			d.backoff(c, err.Error())
		}
	}
}

// start launches the runner listener; caller holds d.mu.
func (d *Daemon) start(r store.Runner, c *child) error {
	out, err := openLog(r.LogPath())
	if err != nil {
		return err
	}
	cmd := runner.RunCommand(r.Dir(), "")
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Stdin = nil
	proc.Supervise(cmd)
	if err := cmd.Start(); err != nil {
		out.Close()
		return err
	}
	c.cmd, c.done, c.stopping = cmd, make(chan struct{}), false
	c.state.State, c.state.PID, c.state.Since = "running", cmd.Process.Pid, time.Now()
	c.state.NextStart = time.Time{}
	d.Log.Printf("[%s] started (pid %d)", r.ID, cmd.Process.Pid)
	go func() {
		err := cmd.Wait()
		out.Close()
		close(c.done)
		d.mu.Lock()
		defer d.mu.Unlock()
		exit := "exited"
		if err != nil {
			exit = err.Error()
		}
		d.Log.Printf("[%s] %s", r.ID, exit)
		ranFor := time.Since(c.state.Since)
		c.cmd, c.state.PID = nil, 0
		if c.stopping {
			c.state.State, c.state.LastExit, c.state.Since = "stopped", exit, time.Now()
			delete(d.children, r.ID)
			return
		}
		if ranFor > stableAfter {
			c.state.Restarts = 0
		}
		d.backoff(c, exit)
		nudge(d.reload)
	}()
	return nil
}

// backoff schedules the next start; caller holds d.mu.
func (d *Daemon) backoff(c *child, reason string) {
	c.state.Restarts++
	wait := minBackoff << min(c.state.Restarts-1, 10)
	if wait > maxBackoff {
		wait = maxBackoff
	}
	c.state.State, c.state.LastExit = "backoff", reason
	c.state.NextStart = time.Now().Add(wait)
}

func (d *Daemon) stopAll() {
	d.mu.Lock()
	var wg sync.WaitGroup
	for _, c := range d.children {
		if c.cmd != nil {
			c.stopping = true
			wg.Add(1)
			go func(c *child) {
				defer wg.Done()
				proc.Stop(c.cmd, c.done, stopGrace)
			}(c)
		}
	}
	d.mu.Unlock()
	wg.Wait()
}

// openLog appends to a runner log, rotating it once it grows past 10 MB.
func openLog(path string) (*os.File, error) {
	if st, err := os.Stat(path); err == nil && st.Size() > maxLogSize {
		_ = os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
}

// client returns a GitHub client, re-resolving the token every 10 minutes
// so a `gh auth refresh` is picked up.
func (d *Daemon) client() (*github.Client, error) {
	d.tokMu.Lock()
	defer d.tokMu.Unlock()
	if d.ghClient != nil && time.Since(d.tokAt) < 10*time.Minute {
		return d.ghClient, nil
	}
	ts, err := github.ResolveToken(store.TokenPath())
	if err != nil {
		return nil, err
	}
	if d.ghClient == nil || ts.Token != d.tok {
		d.ghClient = github.New(ts.Token)
	}
	d.tok, d.tokAt = ts.Token, time.Now()
	return d.ghClient, nil
}

// syncLoop runs `sync` on the configured interval when auto-sync is on.
func (d *Daemon) syncLoop(ctx context.Context) {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		forced := false
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-d.syncNow:
			forced = true
		}
		cfg, err := store.LoadConfig()
		interval := time.Hour
		if err == nil {
			interval = cfg.Sync.Interval.Or(time.Hour)
		}
		if err == nil && (cfg.Sync.Auto || forced) {
			err = d.runSync(ctx, cfg)
		}
		d.mu.Lock()
		d.lastSync = time.Now()
		d.syncErr = ""
		if err != nil {
			d.syncErr = err.Error()
			d.Log.Printf("sync: %v", err)
		}
		d.mu.Unlock()
		timer.Reset(interval)
	}
}

func (d *Daemon) runSync(ctx context.Context, cfg store.Config) error {
	c, err := d.client()
	if err != nil {
		return err
	}
	plan, err := ops.PlanSync(ctx, c, cfg.Sync)
	if err != nil {
		return err
	}
	if len(plan.Add) == 0 && len(plan.Prune) == 0 {
		return nil
	}
	d.Log.Printf("sync: adding %d, pruning %d", len(plan.Add), len(plan.Prune))
	ops.ApplySync(ctx, c, &plan, logWriter{d.Log})
	if len(plan.Errors) > 0 {
		return errors.New(plan.Errors[0])
	}
	return nil
}

type logWriter struct{ l *log.Logger }

func (w logWriter) Write(p []byte) (int, error) {
	w.l.Print(string(p))
	return len(p), nil
}

var _ io.Writer = logWriter{}
