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
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/fleet"
	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/localci"
	"github.com/MehdiMamas/gh-runners/internal/ops"
	"github.com/MehdiMamas/gh-runners/internal/proc"
	"github.com/MehdiMamas/gh-runners/internal/runner"
	"github.com/MehdiMamas/gh-runners/internal/store"
	"github.com/MehdiMamas/gh-runners/internal/web"
)

// stopGrace is how long a stopping runner gets to exit on its own before its
// whole process tree is killed (a variable so tests need not wait it out).
var stopGrace = 30 * time.Second

const (
	tick        = 5 * time.Second
	minBackoff  = 5 * time.Second
	maxBackoff  = 5 * time.Minute
	stableAfter = 10 * time.Minute
	maxLogSize  = 10 << 20

	// DefaultPort is where the dashboard listens, so its address (and the
	// browser's cookie for it) stays the same across restarts.
	DefaultPort = 47321
)

type child struct {
	cmd      *exec.Cmd
	done     chan struct{}
	dir      string
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
	paused   atomic.Bool    // see pause.go
	strays   map[string]int // runner id -> processes left after a stop, see sweep.go
	seen     map[int]bool   // strays the last sweep saw
	sweeping atomic.Bool
	updated  atomic.Bool // a new binary was installed; see stopResult

	updateNow chan struct{}
	// updateCfg wakes the update loop when settings may have changed.
	updateCfg chan struct{}
	upd       control.UpdateState
	// ciBusy names the queued or running Local CI task, or "" when none.
	ciBusy func() string

	tokMu    sync.Mutex
	tok      string
	tokAt    time.Time
	ghClient *github.Client

	host     *fleet.Host
	member   *fleet.Member
	peers    *fleet.Peers
	brokered *github.Client
}

// dockerEvery is how often the docker label is re-checked.
const dockerEvery = 10 * time.Minute

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
	d.strays, d.seen = map[string]int{}, map[int]bool{}
	d.started = time.Now()
	d.reload = make(chan struct{}, 1)
	d.syncNow = make(chan struct{}, 1)
	d.updateNow = make(chan struct{}, 1)
	d.updateCfg = make(chan struct{}, 1)
	d.jit = newJITManager(d)
	d.paused.Store(store.IsPaused())
	d.startStopped()

	// ctx is cancelled by an OS signal (launchd, a service stop); the
	// derived one is also cancelled by /shutdown, which is a deliberate stop.
	signalled := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cfg, _ := store.LoadConfig()
	ln, err := listen(ctx, cfg.DashboardPort)
	if err != nil {
		return err
	}
	uiSecret, err := store.UISecret()
	if err != nil {
		ln.Close()
		return fmt.Errorf("dashboard secret: %w", err)
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
	d.host = &fleet.Host{Dir: store.FleetDir(), Token: d.hostToken, Log: d.Log.Printf, CIRepos: ciRepos, Info: d.fleetInfo}
	d.member = &fleet.Member{
		Dir: store.FleetDir(), Info: d.fleetInfo, Log: d.Log.Printf,
		CIDir:        filepath.Join(store.Home(), "ci", "fleet"),
		CITools:      localci.CachedTools,
		CIEmulatorOK: func() bool { c, _ := store.LoadConfig(); return c.CIEmulatorOK() },
	}
	d.brokered = d.member.Client()
	hostname, _ := os.Hostname()
	d.peers = &fleet.Peers{
		Dir: store.FleetDir(), Client: d.localClient, Name: hostname, Version: d.Version, Log: d.Log.Printf,
		Info:         d.fleetInfo,
		AllowControl: func() bool { c, _ := store.LoadConfig(); return c.AllowControlOn() },
		CIDir:        d.member.CIDir,
		CITools:      d.member.CITools,
		CIEmulatorOK: d.member.CIEmulatorOK,
	}
	d.member.IsPeer = d.peers.Linked
	dash := web.New(web.Hooks{
		Version:     d.Version,
		Status:      d.status,
		Reload:      func() { nudge(d.reload); nudge(d.updateCfg) },
		SyncNow:     func() { nudge(d.syncNow) },
		WakeJIT:     d.jit.wake,
		ResetClient: d.resetClient,
		UpdateNow:   func() { nudge(d.updateNow) },
		Pause:       d.Pause,
		Resume:      d.Resume,
		Log:         d.Log,
		Host:        d.host,
		Member:      d.member,
		Peers:       d.peers,
	}, uiSecret)
	d.member.API = dash.FleetAPI()
	d.peers.API = dash.FleetAPI()
	d.ciBusy = dash.CIBusy
	d.member.OnChange = func() {
		d.resetClient()
		dash.Reset()
	}
	srv := &http.Server{Handler: dash.Handler(d.handler(secret, cancel)), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	d.Log.Printf("gh-runners daemon %s started (pid %d, home %s, dashboard http://%s)", d.Version, info.PID, store.Home(), info.Addr)
	if err := web.StartHost(d.host); err != nil {
		d.Log.Printf("main host: %v", err)
	}
	defer d.host.Stop()
	defer d.peers.Stop()
	go d.peerLoop(ctx)
	go d.member.Run(ctx)
	go d.syncLoop(ctx)
	go d.dockerLoop(ctx)
	go d.jit.loop(ctx)
	go d.updateLoop(ctx)
	go dash.CIWatch(ctx)

	t := time.NewTicker(tick)
	defer t.Stop()
	lastSweep := time.Now()
	for {
		d.reconcile()
		if time.Since(lastSweep) >= sweepEvery && d.sweeping.CompareAndSwap(false, true) {
			lastSweep = time.Now()
			go func() {
				defer d.sweeping.Store(false)
				d.sweep()
			}()
		}
		select {
		case <-ctx.Done():
			if signalled.Err() != nil {
				d.keepRunningForRestart()
			}
			d.Log.Printf("stopping all runners")
			d.stopAll()
			d.jit.stopAll()
			dash.WaitCI(15 * time.Second)
			d.Log.Printf("daemon stopped")
			return stopResult(d.updated.Load(), signalled.Err() != nil)
		case <-t.C:
		case <-d.reload:
		}
	}
}

// ErrUpdated is returned when a signal stops a daemon that has just
// installed a new binary. It makes the process exit non-zero, which launchd
// (KeepAlive SuccessfulExit=false) and systemd (Restart=on-failure) treat
// as a crash and start again, even when the helper that was to restart the
// service died with the old daemon. Existing service files need no change.
var ErrUpdated = errors.New("stopped after installing an update; the service manager starts the new version")

func stopResult(updated, signalled bool) error {
	if updated && signalled {
		return ErrUpdated
	}
	return nil
}

// keepRunningForRestart leaves the keep-running marker when a signal stops
// the daemon while runners are on: launchd or the service manager is most
// likely restarting it, and the runners should come back with it. A paused
// machine keeps its own state, and nothing on means nothing to keep.
func (d *Daemon) keepRunningForRestart() {
	if d.paused.Load() {
		return
	}
	runners, err := store.List()
	if err != nil {
		return
	}
	for _, r := range runners {
		if r.Configured() && !r.Disabled {
			if err := store.SetKeepRunning(); err != nil {
				d.Log.Printf("keep-running marker: %v", err)
			}
			return
		}
	}
}

// listen takes the dashboard port, waiting a little in case the daemon
// that just stopped still holds it, then falls back to any free port.
func listen(ctx context.Context, port int) (net.Listener, error) {
	if port <= 0 {
		port = DefaultPort
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for i := 0; ; i++ {
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			return ln, nil
		}
		if i == 10 || ctx.Err() != nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	return net.Listen("tcp", "127.0.0.1:0")
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
		nudge(d.updateCfg)
	}))
	mux.HandleFunc("POST /sync", auth(func(w http.ResponseWriter, r *http.Request) {
		nudge(d.syncNow)
	}))
	mux.HandleFunc("POST /update", auth(func(w http.ResponseWriter, r *http.Request) {
		nudge(d.updateNow)
	}))
	mux.HandleFunc("POST /fleet", auth(func(w http.ResponseWriter, r *http.Request) {
		d.member.Reload()
		d.member.OnChange()
	}))
	mux.HandleFunc("POST /pause", auth(func(w http.ResponseWriter, r *http.Request) {
		if err := d.Pause(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	mux.HandleFunc("POST /resume", auth(func(w http.ResponseWriter, r *http.Request) {
		if err := d.Resume(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	mux.HandleFunc("GET /ci/machines", auth(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(d.ciMachines())
	}))
	mux.HandleFunc("POST /ci/run", auth(func(w http.ResponseWriter, r *http.Request) {
		var req control.CIRunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var ex localci.Executor = fleet.RemoteExecutor{Host: d.host, MemberID: req.MemberID}
		if !d.isHostMember(req.MemberID) {
			ex = fleet.PeerExecutor{Peers: d.peers, ID: req.MemberID}
		}
		res := ex.RunJob(r.Context(), req.Spec)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
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
	s := control.Status{PID: os.Getpid(), Version: d.Version, Started: d.started, LastSync: d.lastSync, SyncErr: d.syncErr, Update: d.upd}
	s.Paused = d.paused.Load()
	if s.Paused {
		s.PausedSince = store.LoadPause().Since
	}
	for id, c := range d.children {
		s.Runners = append(s.Runners, c.state)
		if n := d.strays[id]; n > 0 {
			s.Runners[len(s.Runners)-1].Strays = n
		}
	}
	for id, n := range d.strays {
		if d.children[id] == nil && n > 0 {
			s.Runners = append(s.Runners, control.ProcState{ID: id, State: "stopping", Strays: n})
		}
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
		if !r.Disabled && r.Configured() && !d.paused.Load() {
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
			go d.stopChild(id, c, stopGrace)
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
	// A listener left by an older version or a crashed daemon would clash
	// with the new one.
	d.logKilled(r.ID, proc.KillStrays(r.Dir()))
	delete(d.strays, r.ID)
	cmd := runner.RunCommand(r.Dir(), "")
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Stdin = nil
	proc.Supervise(cmd)
	if err := cmd.Start(); err != nil {
		out.Close()
		return err
	}
	c.cmd, c.done, c.dir, c.stopping = cmd, make(chan struct{}), r.Dir(), false
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
		if c.stopping {
			// stopChild finishes the stop once the whole tree is gone.
			c.state.LastExit = exit
			return
		}
		c.cmd, c.state.PID = nil, 0
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
	for id, c := range d.children {
		if c.cmd != nil {
			c.stopping = true
			wg.Add(1)
			go func(id string, c *child) {
				defer wg.Done()
				d.stopChild(id, c, stopGrace)
			}(id, c)
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
		// Not signed in here: a machine with a main host has the host
		// make its GitHub calls.
		if d.member != nil && d.member.Joined() {
			return d.brokered, nil
		}
		return nil, err
	}
	if d.ghClient == nil || ts.Token != d.tok {
		d.ghClient = github.New(ts.Token)
	}
	d.tok, d.tokAt = ts.Token, time.Now()
	return d.ghClient, nil
}

// localClient is the GitHub client of this machine's own sign-in. A machine
// that only borrows its main host's token has none.
func (d *Daemon) localClient() (*github.Client, error) {
	c, err := d.client()
	if err != nil {
		return nil, err
	}
	if c == d.brokered {
		return nil, errors.New("not signed in to GitHub on this machine")
	}
	return c, nil
}

// peerLoop keeps the account machines (see fleet.Peers) running exactly
// while this machine is signed in to GitHub itself.
func (d *Daemon) peerLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	last := ""
	for {
		if _, err := d.localClient(); err == nil {
			if err := d.peers.Start(ctx); err != nil && err.Error() != last {
				d.Log.Printf("account machines: %v", err)
				last = err.Error()
			}
		} else {
			d.peers.Stop()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// hostToken is the token the main host makes members' GitHub calls with.
// It is used here only; it never goes to a member.
func (d *Daemon) hostToken(context.Context) (string, error) {
	c, err := d.client()
	if err != nil {
		return "", err
	}
	return c.Token, nil
}

// ciMachines lists the machines that take CI jobs: the host's members and
// the account's other PCs, each once.
func (d *Daemon) ciMachines() []localci.Machine {
	out := d.host.CIMachines()
	for _, m := range d.peers.CIMachines() {
		if !slices.ContainsFunc(out, func(o localci.Machine) bool { return o.ID == m.ID }) {
			out = append(out, m)
		}
	}
	return out
}

// isHostMember reports whether id is a member of this main host (otherwise
// a job for it goes to the account's other PCs).
func (d *Daemon) isHostMember(id string) bool {
	ms, _ := d.host.Machines()
	return slices.ContainsFunc(ms, func(m fleet.Machine) bool { return m.ID == id })
}

// fleetInfo is what this machine tells its main host.
func (d *Daemon) fleetInfo() fleet.Info {
	host, _ := os.Hostname()
	info := fleet.Info{Hostname: host, OS: runtime.GOOS, Arch: runtime.GOARCH, Version: d.Version, Runners: []fleet.RunnerInfo{}, Paused: d.paused.Load()}
	if d.peers != nil {
		info.Login, info.MachineID = d.peers.Login(), d.peers.ID()
	}
	st := d.status()
	runners, _ := store.List()
	for _, r := range runners {
		state, _ := ops.LocalState(r, st, true)
		info.Runners = append(info.Runners, fleet.RunnerInfo{ID: r.ID, Scope: r.Scope(), Name: r.Name, State: state, Labels: r.Labels})
	}
	return info
}

// resetClient drops the cached token after a sign-in or sign-out.
func (d *Daemon) resetClient() {
	d.tokMu.Lock()
	d.ghClient, d.tok, d.tokAt = nil, "", time.Time{}
	d.tokMu.Unlock()
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
		if d.paused.Load() {
			timer.Reset(d.pausedWait())
			continue
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

// dockerLoop keeps the docker label of this machine's runners right: at
// start and then every dockerEvery, it adds the label when Docker works and
// removes it when it does not.
func (d *Daemon) dockerLoop(ctx context.Context) {
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		timer.Reset(dockerEvery)
		if d.paused.Load() {
			continue
		}
		c, err := d.client()
		if err != nil {
			continue
		}
		changes, err := ops.SyncDockerLabel(ctx, c)
		for _, ch := range changes {
			d.Log.Printf("docker: %s", ch)
		}
		if err != nil {
			d.Log.Printf("docker label: %v", err)
		}
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

// ciRepos lists the repos whose source fleet members may fetch for CI jobs.
func ciRepos() []string {
	c, err := store.LoadConfig()
	if err != nil {
		return nil
	}
	var out []string
	for r := range c.CI {
		out = append(out, r)
	}
	return out
}
