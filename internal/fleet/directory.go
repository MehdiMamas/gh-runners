package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/github"
)

// DirectoryRepo is the private repository, in the signed-in user's account,
// where the account's machines list themselves: machines/<id>.json each.
const DirectoryRepo = "gh-runners-machines"

// Default timings of the directory.
const (
	PublishEvery = 5 * time.Minute
	ListEvery    = 60 * time.Second
	// addrCheckEvery is how often the addresses are compared with the
	// published ones; a change is published at once.
	addrCheckEvery = 30 * time.Second
)

// Entry is one machine in the directory.
type Entry struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	OS          string    `json:"os"`
	Arch        string    `json:"arch"`
	Version     string    `json:"version"`
	Addrs       []string  `json:"addrs"`
	Port        int       `json:"port"`
	Fingerprint string    `json:"fingerprint"` // SHA-256 of the machine's peer certificate, hex
	Updated     time.Time `json:"updated"`
}

// same reports whether two entries say the same thing (ignoring Updated).
func (e Entry) same(o Entry) bool {
	e.Updated, o.Updated = time.Time{}, time.Time{}
	return e.ID == o.ID && e.Name == o.Name && e.OS == o.OS && e.Arch == o.Arch && e.Version == o.Version &&
		e.Port == o.Port && e.Fingerprint == o.Fingerprint && slices.Equal(e.Addrs, o.Addrs)
}

// Directory is this account's list of machines, kept as files in a private
// repository of the signed-in user.
type Directory struct {
	// Dir holds the machine id.
	Dir string
	// Client returns the GitHub client of the signed-in user.
	Client func() (*github.Client, error)
	// Self describes this machine; the id is filled in here.
	Self func() Entry
	// OnList gets every fresh list (this machine included).
	OnList func([]Entry)
	Log    func(format string, args ...any)

	// Timings; zero means the defaults above.
	PublishEvery, ListEvery time.Duration

	mu        sync.Mutex
	owner     string // the login, once the repo is known to exist
	cache     map[string]cachedEntry
	published Entry // what was last written
	poke      chan struct{}
}

type cachedEntry struct {
	sha   string
	entry Entry
	ok    bool // the file held a valid entry
}

func (d *Directory) logf(format string, args ...any) {
	if d.Log != nil {
		d.Log(format, args...)
	}
}

// ID is this machine's stable id, made on first use.
func (d *Directory) ID() (string, error) {
	path := filepath.Join(d.Dir, "machine-id")
	if b, err := os.ReadFile(path); err == nil {
		if s := strings.TrimSpace(string(b)); idRe.MatchString(s) {
			return s, nil
		}
	}
	s := randomHex(8)
	if err := os.MkdirAll(d.Dir, 0o700); err != nil {
		return "", err
	}
	return s, os.WriteFile(path, []byte(s+"\n"), 0o600)
}

// ready returns a client and the owner login, creating the repository on
// first use.
func (d *Directory) ready(ctx context.Context) (*github.Client, string, error) {
	c, err := d.Client()
	if err != nil {
		return nil, "", err
	}
	d.mu.Lock()
	owner := d.owner
	d.mu.Unlock()
	if owner != "" {
		return c, owner, nil
	}
	u, _, err := c.Me(ctx)
	if err != nil {
		return nil, "", err
	}
	if _, err := c.GetRepo(ctx, u.Login, DirectoryRepo); err != nil {
		if !github.IsNotFound(err) {
			return nil, "", err
		}
		if _, err := c.CreateRepo(ctx, DirectoryRepo, "gh-runners: the machines signed in to this GitHub account"); err != nil {
			return nil, "", fmt.Errorf("make the private repository %s/%s: %w", u.Login, DirectoryRepo, err)
		}
		d.logf("made the private repository %s/%s for this account's machines", u.Login, DirectoryRepo)
	}
	d.mu.Lock()
	d.owner = u.Login
	d.mu.Unlock()
	return c, u.Login, nil
}

// StaleAfter is how long a machine may go without updating its entry before
// the others remove it.
var StaleAfter = 14 * 24 * time.Hour

func entryPath(id string) string { return "machines/" + id + ".json" }

var idRe = regexp.MustCompile(`^[0-9a-f]{8,64}$`)

// self returns this machine's entry with its id.
func (d *Directory) self() (Entry, error) {
	id, err := d.ID()
	if err != nil {
		return Entry{}, err
	}
	e := d.Self()
	e.ID = id
	return e, nil
}

// Publish writes this machine's entry.
func (d *Directory) Publish(ctx context.Context) error {
	e, err := d.self()
	if err != nil {
		return err
	}
	c, owner, err := d.ready(ctx)
	if err != nil {
		return err
	}
	e.Updated = time.Now().UTC().Truncate(time.Second)
	b, _ := json.MarshalIndent(e, "", "  ")
	sha := ""
	if cur, err := c.GetContents(ctx, owner, DirectoryRepo, entryPath(e.ID)); err == nil {
		sha = cur.SHA
	} else if !github.IsNotFound(err) {
		return err
	}
	if _, err := c.PutContents(ctx, owner, DirectoryRepo, entryPath(e.ID), "gh-runners: "+e.Name, b, sha); err != nil {
		return err
	}
	d.mu.Lock()
	d.published = e
	d.mu.Unlock()
	return nil
}

// List reads the directory. A file is fetched only when its sha changed
// since the last List.
func (d *Directory) List(ctx context.Context) ([]Entry, error) {
	c, owner, err := d.ready(ctx)
	if err != nil {
		return nil, err
	}
	files, err := c.ListContents(ctx, owner, DirectoryRepo, "machines")
	if err != nil && !github.IsNotFound(err) {
		return nil, err
	}
	d.mu.Lock()
	if d.cache == nil {
		d.cache = map[string]cachedEntry{}
	}
	old := d.cache
	d.mu.Unlock()
	next := map[string]cachedEntry{}
	var out []Entry
	for _, f := range files {
		id, ok := strings.CutSuffix(f.Name, ".json")
		if f.Type != "file" || !ok || !idRe.MatchString(id) {
			continue
		}
		ce, have := old[id]
		if !have || ce.sha != f.SHA {
			ce = cachedEntry{sha: f.SHA}
			cont, err := c.GetContents(ctx, owner, DirectoryRepo, f.Path)
			if err != nil {
				return nil, err
			}
			var e Entry
			if raw, err := cont.Data(); err == nil && json.Unmarshal(raw, &e) == nil &&
				e.ID == id && hex64.MatchString(e.Fingerprint) && e.Port > 0 && e.Port <= 65535 {
				ce.entry, ce.ok = e, true
			} else {
				d.logf("directory: ignoring %s: not a valid entry", f.Path)
			}
		}
		next[id] = ce
		if !ce.ok {
			continue
		}
		// A running machine rewrites its file every few minutes, so one
		// untouched for StaleAfter is gone (reinstalled, wiped, retired):
		// drop it rather than list a ghost forever.
		if self, _ := d.ID(); id != self && !ce.entry.Updated.IsZero() && time.Since(ce.entry.Updated) > StaleAfter {
			if err := c.DeleteContents(ctx, owner, DirectoryRepo, f.Path, "gh-runners: remove "+ce.entry.Name+" (not seen for two weeks)", f.SHA); err != nil {
				d.logf("directory: removing stale %s: %v", f.Path, err)
			}
			delete(next, id)
			continue
		}
		out = append(out, ce.entry)
	}
	d.mu.Lock()
	d.cache = next
	d.mu.Unlock()
	slices.SortFunc(out, func(a, b Entry) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// Remove deletes a machine's entry. A machine that is still running
// publishes itself again within five minutes.
func (d *Directory) Remove(ctx context.Context, id string) error {
	if !idRe.MatchString(id) {
		return errors.New("unknown machine")
	}
	c, owner, err := d.ready(ctx)
	if err != nil {
		return err
	}
	cur, err := c.GetContents(ctx, owner, DirectoryRepo, entryPath(id))
	if err != nil {
		if github.IsNotFound(err) {
			return nil
		}
		return err
	}
	if err := c.DeleteContents(ctx, owner, DirectoryRepo, entryPath(id), "gh-runners: remove "+id, cur.SHA); err != nil {
		return err
	}
	d.mu.Lock()
	delete(d.cache, id)
	d.mu.Unlock()
	return nil
}

// Unpublish removes this machine's own entry (on sign-out).
func (d *Directory) Unpublish(ctx context.Context) error {
	id, err := d.ID()
	if err != nil {
		return err
	}
	return d.Remove(ctx, id)
}

// Refresh asks Run to list again now.
func (d *Directory) Refresh() {
	d.mu.Lock()
	if d.poke == nil {
		d.poke = make(chan struct{}, 1)
	}
	ch := d.poke
	d.mu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Run publishes and lists until ctx ends: publish every PublishEvery and
// when the addresses change, list every ListEvery.
func (d *Directory) Run(ctx context.Context) {
	pe, le := d.PublishEvery, d.ListEvery
	if pe <= 0 {
		pe = PublishEvery
	}
	if le <= 0 {
		le = ListEvery
	}
	d.mu.Lock()
	if d.poke == nil {
		d.poke = make(chan struct{}, 1)
	}
	poke := d.poke
	d.mu.Unlock()
	check := min(addrCheckEvery, pe)
	lastPub := time.Time{}
	pubT, listT, chkT := time.NewTicker(pe), time.NewTicker(le), time.NewTicker(check)
	defer pubT.Stop()
	defer listT.Stop()
	defer chkT.Stop()
	publish := func() {
		if err := d.Publish(ctx); err != nil {
			d.logf("directory: publish: %v", err)
			return
		}
		lastPub = time.Now()
	}
	list := func() {
		es, err := d.List(ctx)
		if err != nil {
			d.logf("directory: list: %v", err)
			return
		}
		if d.OnList != nil {
			d.OnList(es)
		}
	}
	publish()
	list()
	for {
		select {
		case <-ctx.Done():
			return
		case <-pubT.C:
			publish()
		case <-chkT.C:
			if e, err := d.self(); err == nil {
				d.mu.Lock()
				changed := !d.published.same(e)
				d.mu.Unlock()
				if changed && time.Since(lastPub) > 5*time.Second {
					publish()
					list()
				}
			}
		case <-listT.C:
			list()
		case <-poke:
			list()
		}
	}
}
