// Package store owns gh-runners' on-disk layout:
//
//	~/.gh-runners/                 (or $GH_RUNNERS_HOME)
//	  config.json                  settings and sync rules
//	  token                        PAT saved by `gh-runners login` (0600)
//	  daemon.json                  port/pid/secret of the running daemon
//	  daemon.log
//	  ui-secret                    unlocks the dashboard in a browser (0600)
//	  cache/                       downloaded runner packages
//	  runners/<id>/                one actions/runner install per registration
//	                               (config "runners_dir" moves this folder)
//	    gh-runners.json            what gh-runners knows about it
//	    gh-runners.log             the runner's output, written by the daemon
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Home is the root directory.
func Home() string {
	if h := os.Getenv("GH_RUNNERS_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".gh-runners")
}

func ConfigPath() string   { return filepath.Join(Home(), "config.json") }
func TokenPath() string    { return filepath.Join(Home(), "token") }
func DaemonPath() string   { return filepath.Join(Home(), "daemon.json") }
func DaemonLog() string    { return filepath.Join(Home(), "daemon.log") }
func CacheDir() string     { return filepath.Join(Home(), "cache") }
func UISecretPath() string { return filepath.Join(Home(), "ui-secret") }

// DefaultRunnersDir is where runners go unless config.json says otherwise.
func DefaultRunnersDir() string { return filepath.Join(Home(), "runners") }

var dirCache struct {
	sync.Mutex
	path string
	mod  time.Time
	size int64
	dir  string
}

// RunnersDir is the folder holding one subfolder per runner: the config's
// runners_dir, or DefaultRunnersDir. It is cached until config.json changes.
func RunnersDir() string {
	path := ConfigPath()
	st, err := os.Stat(path)
	if err != nil {
		return DefaultRunnersDir()
	}
	dirCache.Lock()
	defer dirCache.Unlock()
	if dirCache.path == path && dirCache.mod.Equal(st.ModTime()) && dirCache.size == st.Size() {
		return dirCache.dir
	}
	dir := DefaultRunnersDir()
	if c, err := LoadConfig(); err == nil && c.RunnersDir != "" {
		dir = c.RunnersDir
	}
	dirCache.path, dirCache.mod, dirCache.size, dirCache.dir = path, st.ModTime(), st.Size(), dir
	return dir
}

// UISecret returns the dashboard secret, creating it on first use.
func UISecret() (string, error) {
	if b, err := os.ReadFile(UISecretPath()); err == nil {
		if s := strings.TrimSpace(string(b)); len(s) >= 32 {
			return s, nil
		}
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s := hex.EncodeToString(b)
	if err := os.MkdirAll(Home(), 0o700); err != nil {
		return "", err
	}
	return s, os.WriteFile(UISecretPath(), []byte(s+"\n"), 0o600)
}

// Kind of a runner entry.
const (
	KindRepo = "repo"
	KindOrg  = "org"
)

// Runner is the metadata gh-runners keeps next to each runner install.
type Runner struct {
	ID        string    `json:"id"`    // directory name
	Kind      string    `json:"kind"`  // repo | org
	Owner     string    `json:"owner"` // user or org
	Repo      string    `json:"repo,omitempty"`
	Name      string    `json:"name"`   // runner name on GitHub
	Labels    []string  `json:"labels"` // custom labels (GitHub adds self-hosted/OS/arch)
	Group     string    `json:"group,omitempty"`
	Disabled  bool      `json:"disabled,omitempty"` // daemon leaves it stopped
	Version   string    `json:"version"`            // actions/runner version installed
	CreatedAt time.Time `json:"created_at"`
	Source    string    `json:"source,omitempty"` // "add" | "sync"
}

// Scope is owner/repo or the org name.
func (r Runner) Scope() string {
	if r.Kind == KindOrg {
		return r.Owner
	}
	return r.Owner + "/" + r.Repo
}

// Dir is the runner's install directory.
func (r Runner) Dir() string { return filepath.Join(RunnersDir(), r.ID) }

// LogPath is where the daemon writes the runner's output.
func (r Runner) LogPath() string { return filepath.Join(r.Dir(), "gh-runners.log") }

// Configured reports whether config.sh/config.cmd finished registering it.
func (r Runner) Configured() bool {
	_, err := os.Stat(filepath.Join(r.Dir(), ".runner"))
	return err == nil
}

var unsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// IDFor builds a directory name for a scope, e.g. "MehdiMamas__spotter".
func IDFor(kind, owner, repo string) string {
	id := owner + "__" + repo
	if kind == KindOrg {
		id = "org__" + owner
	}
	return unsafe.ReplaceAllString(id, "-")
}

const metaFile = "gh-runners.json"

// Save writes the runner's metadata.
func Save(r Runner) error {
	if err := os.MkdirAll(r.Dir(), 0o755); err != nil {
		return err
	}
	return writeJSON(filepath.Join(r.Dir(), metaFile), r, 0o644)
}

// Load reads one runner by id.
func Load(id string) (Runner, error) {
	var r Runner
	err := readJSON(filepath.Join(RunnersDir(), id, metaFile), &r)
	if err == nil {
		r.ID = id
	}
	return r, err
}

// List returns every runner with metadata, sorted by scope.
func List() ([]Runner, error) {
	entries, err := os.ReadDir(RunnersDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Runner
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		r, err := Load(e.Name())
		if err != nil {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Find returns the runners matching a scope ("owner/repo", "org", or an id).
func Find(query string) ([]Runner, error) {
	all, err := List()
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	var out []Runner
	for _, r := range all {
		if strings.ToLower(r.Scope()) == q || strings.ToLower(r.ID) == q {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no local runner for %q (see `gh-runners list`)", query)
	}
	return out, nil
}

// Config is ~/.gh-runners/config.json.
type Config struct {
	// Labels added to every runner this machine registers.
	Labels []string `json:"labels"`
	// Name for runners on this machine; defaults to the hostname.
	RunnerName string `json:"runner_name,omitempty"`
	// RunnersDir holds the runner installs; empty means ~/.gh-runners/runners.
	RunnersDir string `json:"runners_dir,omitempty"`
	// DashboardPort is the local port of the web dashboard (default 47321).
	DashboardPort int      `json:"dashboard_port,omitempty"`
	Sync          Sync     `json:"sync"`
	OnDemand      OnDemand `json:"on_demand"`
}

// Sync decides which repositories `gh-runners sync` (and the daemon's
// auto-sync) give a runner.
type Sync struct {
	// Auto makes the daemon run sync every Interval.
	Auto     bool     `json:"auto"`
	Interval Duration `json:"interval,omitempty"`
	// Owners whose repos are synced; empty means the authenticated user.
	Owners []string `json:"owners,omitempty"`
	// Glob patterns on "owner/repo"; empty include means all.
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
	// IncludePublic also syncs public repos (unsafe: forks can run code on your machine).
	IncludePublic bool `json:"include_public,omitempty"`
	IncludeForks  bool `json:"include_forks,omitempty"`
	// Prune removes runners whose repo no longer matches (deleted, archived, excluded).
	Prune bool `json:"prune,omitempty"`
}

// OnDemand configures just-in-time runners: no per-repo install, the daemon
// watches for queued jobs and starts a single-use runner for each.
type OnDemand struct {
	Enabled bool `json:"enabled"`
	// Poll is how often queued jobs are checked.
	Poll Duration `json:"poll,omitempty"`
	// MaxConcurrent caps simultaneous on-demand runners on this machine.
	MaxConcurrent int `json:"max_concurrent,omitempty"`
	// Same repo selection rules as Sync.
	Owners        []string `json:"owners,omitempty"`
	Include       []string `json:"include,omitempty"`
	Exclude       []string `json:"exclude,omitempty"`
	IncludePublic bool     `json:"include_public,omitempty"`
	// ActiveWithin limits polling to repos pushed to recently, to save API calls.
	ActiveWithin Duration `json:"active_within,omitempty"`
}

// Duration marshals as a Go duration string like "30m".
type Duration struct{ time.Duration }

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s == "" {
		d.Duration = 0
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// Or returns d, or def when d is zero.
func (d Duration) Or(def time.Duration) time.Duration {
	if d.Duration <= 0 {
		return def
	}
	return d.Duration
}

// DefaultConfig is used when config.json does not exist yet.
func DefaultConfig() Config {
	return Config{Labels: []string{"gh-runners"}}
}

// LoadConfig reads config.json, or returns the default.
func LoadConfig() (Config, error) {
	c := DefaultConfig()
	err := readJSON(ConfigPath(), &c)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultConfig(), nil
	}
	return c, err
}

// SaveConfig writes config.json.
func SaveConfig(c Config) error {
	// Two saves within the file system's mtime granularity could otherwise
	// leave RunnersDir on the old folder.
	dirCache.Lock()
	dirCache.path = ""
	dirCache.Unlock()
	return writeJSON(ConfigPath(), c, 0o644)
}

// DaemonInfo is how CLI commands find the running daemon.
type DaemonInfo struct {
	PID     int       `json:"pid"`
	Addr    string    `json:"addr"`   // 127.0.0.1:port, also serves the dashboard
	Secret  string    `json:"secret"` // sent as X-GH-Runners-Secret
	Version string    `json:"version"`
	Started time.Time `json:"started"`
}

func LoadDaemonInfo() (DaemonInfo, error) {
	var d DaemonInfo
	err := readJSON(DaemonPath(), &d)
	return d, err
}

func SaveDaemonInfo(d DaemonInfo) error { return writeJSON(DaemonPath(), d, 0o600) }

func readJSON(path string, v any) error {
	var b []byte
	err := retry(func() (err error) { b, err = os.ReadFile(path); return err })
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// writeJSON writes atomically so a reader never sees half a file.
func writeJSON(path string, v any, perm os.FileMode) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), perm); err != nil {
		return err
	}
	return retry(func() error { return os.Rename(tmp, path) })
}

// retry runs fn again for up to about a second while it fails with an error
// that goes away once another reader or writer of the file is done.
func retry(fn func() error) error {
	err := fn()
	for i := 0; i < 50 && err != nil && transient(err); i++ {
		time.Sleep(time.Duration(2+i) * time.Millisecond)
		err = fn()
	}
	return err
}
