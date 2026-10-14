// Package ops implements add, remove and sync on top of the runner,
// store and github packages. CLI commands and the daemon share it.
package ops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/runner"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// MachineName is the runner name used on this machine.
func MachineName(cfg store.Config) string {
	if cfg.RunnerName != "" {
		return cfg.RunnerName
	}
	h, err := os.Hostname()
	if err != nil || h == "" {
		h = "gh-runner"
	}
	h = strings.TrimSuffix(h, ".local")
	return h
}

var labelUnsafe = regexp.MustCompile(`[^a-z0-9._-]+`)

// MachineLabel is a label that targets this machine, e.g. "mehdis-macbook-pro".
func MachineLabel(cfg store.Config) string {
	return strings.Trim(labelUnsafe.ReplaceAllString(strings.ToLower(MachineName(cfg)), "-"), "-")
}

// Labels returns the custom labels for a runner on this machine.
// GitHub adds self-hosted, the OS (Linux/macOS/Windows) and the arch itself.
func Labels(cfg store.Config, extra []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range append(append(append([]string{}, cfg.Labels...), MachineLabel(cfg)), extra...) {
		l = strings.TrimSpace(l)
		if l == "" || seen[strings.ToLower(l)] {
			continue
		}
		seen[strings.ToLower(l)] = true
		out = append(out, l)
	}
	return out
}

// AddOptions control Add.
type AddOptions struct {
	Labels      []string // on top of config labels and the machine label
	Group       string   // org runner group
	Name        string   // overrides the machine name
	AllowPublic bool
	Force       bool   // reconfigure even if already registered
	Source      string // "add" | "sync"
	// Another adds one more runner to a scope that already has one here,
	// in its own folder and under its own name (Name, or the machine name
	// with -2, -3, ...). A runner runs one job at a time, so a second one
	// lets light jobs run while a heavy one is busy.
	Another bool
	// Runner picks, by name or folder id, which runner of the scope Force
	// re-registers when the scope has several.
	Runner string
	// RegistrationToken registers with a token from the repo's
	// Settings > Actions > Runners page instead of calling the API, so no
	// admin token is needed on this machine.
	RegistrationToken string
}

// ErrExists is returned when the scope already has a runner on this machine.
var ErrExists = errors.New("already has a runner on this machine")

// Add installs and registers a runner for scope, then nudges the daemon.
func Add(ctx context.Context, c *github.Client, scope github.Scope, o AddOptions, log io.Writer) (store.Runner, error) {
	cfg, err := store.LoadConfig()
	if err != nil {
		return store.Runner{}, err
	}
	kind := store.KindRepo
	if scope.IsOrg() {
		kind = store.KindOrg
	} else if o.RegistrationToken == "" {
		repo, err := c.GetRepo(ctx, scope.Owner, scope.Repo)
		if err != nil {
			return store.Runner{}, err
		}
		scope = github.Scope{Owner: repo.Owner.Login, Repo: repo.Name} // canonical case
		if !repo.Private && !o.AllowPublic {
			return store.Runner{}, fmt.Errorf("%s is public: anyone can open a pull request that runs code on your machine. Use --allow-public if you understand the risk", scope)
		}
		if !repo.Permissions.Admin {
			return store.Runner{}, fmt.Errorf("you need admin rights on %s to add a runner", scope)
		}
	}
	r := store.Runner{
		Kind:      kind,
		Owner:     scope.Owner,
		Repo:      scope.Repo,
		Name:      MachineName(cfg),
		Labels:    Labels(cfg, o.Labels),
		Group:     o.Group,
		CreatedAt: time.Now().UTC(),
		Source:    o.Source,
	}
	if o.Name != "" {
		r.Name = strings.TrimSpace(o.Name)
	}
	local, err := store.ForScope(scope.String())
	if err != nil {
		return store.Runner{}, err
	}
	base := store.IDFor(kind, scope.Owner, scope.Repo)
	fresh := false // the folder was claimed for this runner: delete it if Add fails
	if o.Another && len(local) > 0 {
		if r.ID, r.Name, err = claimAnother(ctx, c, scope, base, o, r.Name, local, log); err != nil {
			return store.Runner{}, err
		}
		fresh = true
	} else {
		existing, ok, err := addTarget(scope, local, o)
		if err != nil {
			return existing, err
		}
		r.ID = base
		if ok {
			r.ID = existing.ID
			if o.Name == "" {
				r.Name = existing.Name // --replace below must take over this runner, not another one
			}
		}
		if ok && existing.Configured() {
			r.CreatedAt = existing.CreatedAt
			if err := stopAndWait(existing); err != nil {
				return store.Runner{}, err
			}
			// config.sh refuses an already configured directory; --replace
			// below takes over the old registration on GitHub by name.
			for _, f := range []string{".runner", ".credentials", ".credentials_rsaparams"} {
				_ = os.Remove(filepath.Join(r.Dir(), f))
			}
		}
	}
	added := false
	defer func() {
		if fresh && !added {
			_ = removeDir(r.Dir())
		}
	}()

	pkg, err := runner.EnsurePackage(ctx, c, scope, log)
	if err != nil {
		return store.Runner{}, err
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), configScript())); err != nil {
		fmt.Fprintf(log, "Unpacking runner %s into %s ...\n", pkg.Version, r.Dir())
		if err := runner.Extract(pkg.Path, r.Dir()); err != nil {
			return store.Runner{}, err
		}
	}
	r.Version = pkg.Version
	r.Disabled = true // not until registration succeeds
	if err := store.Save(r); err != nil {
		return store.Runner{}, err
	}
	token := o.RegistrationToken
	if token == "" {
		if token, err = c.RegistrationToken(ctx, scope); err != nil {
			return store.Runner{}, err
		}
	}
	fmt.Fprintf(log, "Registering %q with %s ...\n", r.Name, scope.URL())
	if err := runner.Configure(ctx, r.Dir(), runner.ConfigureOptions{
		URL: scope.URL(), Token: token, Name: r.Name, Labels: r.Labels, Group: r.Group,
		// One more runner must never take over a registration of the same
		// name (another machine's, or one GitHub lists but we could not see).
		NoReplace: fresh && !o.Force,
	}, log); err != nil {
		return store.Runner{}, err
	}
	r.Disabled = false
	if err := store.Save(r); err != nil {
		return store.Runner{}, err
	}
	added = true
	_ = control.Reload()
	return r, nil
}

// addTarget picks the runner of the scope that Add (without Another)
// installs into: none for a scope without one, else the only one or the one
// o.Runner names. It returns ErrExists when that runner is registered and
// Force is not set.
func addTarget(scope github.Scope, local []store.Runner, o AddOptions) (store.Runner, bool, error) {
	if o.Runner != "" {
		r, err := SelectRunner(local, o.Runner)
		if err != nil {
			return store.Runner{}, false, err
		}
		local = []store.Runner{r}
	}
	if len(local) == 0 {
		return store.Runner{}, false, nil
	}
	for _, r := range local {
		if r.Configured() && !o.Force {
			return r, false, fmt.Errorf("%s %w (use --another to add one more, or --force to re-register it)", scope, ErrExists)
		}
	}
	if len(local) > 1 {
		return store.Runner{}, false, fmt.Errorf("%s has %d runners here (%s); pick one with --runner", scope, len(local), runnerNames(local))
	}
	return local[0], true, nil
}

// claimAnother names one more runner for a scope and claims its folder,
// base__<name>. Without a picked name it tries the machine name, then
// name-2, name-3, ..., skipping names this scope already uses here or on
// GitHub and folders another Add claimed a moment ago.
func claimAnother(ctx context.Context, c *github.Client, scope github.Scope, base string, o AddOptions, name string, local []store.Runner, log io.Writer) (string, string, error) {
	used := map[string]bool{}
	for _, r := range local {
		used[strings.ToLower(r.Name)] = true
	}
	remote := map[string]bool{}
	if list, err := c.ListRunners(ctx, scope); err == nil {
		for _, gr := range list {
			remote[strings.ToLower(gr.Name)] = true
		}
	} else if o.Name == "" {
		fmt.Fprintf(log, "Could not list the runners of %s on GitHub (%v); picking a name from the local ones.\n", scope, err)
	}
	if o.Name != "" {
		switch {
		case !validName(name):
			return "", "", fmt.Errorf("%q is not a valid runner name (letters, digits, spaces, . _ - only, at most 64)", name)
		case used[strings.ToLower(name)]:
			return "", "", fmt.Errorf("%s already has a runner named %q on this machine", scope, name)
		case remote[strings.ToLower(name)] && !o.Force:
			return "", "", fmt.Errorf("a runner named %q is already registered to %s; pick another name (or --force to take over that registration)", name, scope)
		}
		id := store.IDWithName(base, name)
		ok, err := store.Claim(id)
		if err != nil {
			return "", "", err
		}
		if !ok {
			return "", "", fmt.Errorf("the folder %s exists already; pick another name, or delete it if no runner uses it", filepath.Join(store.RunnersDir(), id))
		}
		return id, name, nil
	}
	for n := 1; n <= 100; n++ {
		cand := name
		if n > 1 {
			cand = fmt.Sprintf("%s-%d", name, n)
		}
		if used[strings.ToLower(cand)] || remote[strings.ToLower(cand)] {
			continue
		}
		id := store.IDWithName(base, cand)
		ok, err := store.Claim(id)
		if err != nil {
			return "", "", err
		}
		if ok {
			return id, cand, nil
		}
	}
	return "", "", fmt.Errorf("could not find a free runner name for %s; pick one with --name", scope)
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9._ -]{1,64}$`)

func validName(name string) bool { return nameRe.MatchString(name) && strings.TrimSpace(name) == name }

// SelectRunner returns the runner of list whose name or folder id is sel.
func SelectRunner(list []store.Runner, sel string) (store.Runner, error) {
	sel = strings.TrimSpace(sel)
	for _, r := range list {
		if strings.EqualFold(r.Name, sel) || strings.EqualFold(r.ID, sel) {
			return r, nil
		}
	}
	if len(list) == 0 {
		return store.Runner{}, fmt.Errorf("no local runner named %q", sel)
	}
	return store.Runner{}, fmt.Errorf("no runner named %q for %s here (it has %s)", sel, list[0].Scope(), runnerNames(list))
}

func runnerNames(list []store.Runner) string {
	names := make([]string, len(list))
	for i, r := range list {
		names[i] = r.Name
	}
	return strings.Join(names, ", ")
}

func configScript() string {
	if runtime.GOOS == "windows" {
		return "config.cmd"
	}
	return "config.sh"
}

// stopAndWait disables the runner and waits for the daemon to stop it.
func stopAndWait(r store.Runner) error {
	r.Disabled = true
	if err := store.Save(r); err != nil {
		return err
	}
	_ = control.Reload()
	return control.WaitStopped(r.ID, 90*time.Second)
}

// SetDisabled starts or stops a runner via the daemon.
func SetDisabled(r store.Runner, disabled bool) error {
	r.Disabled = disabled
	if err := store.Save(r); err != nil {
		return err
	}
	if err := control.Reload(); err != nil && !errors.Is(err, control.ErrNotRunning) {
		return err
	}
	return nil
}

// Remove unregisters a runner and deletes its install. With force, local
// files go even when GitHub cannot be told (repo deleted, no access).
func Remove(ctx context.Context, c *github.Client, r store.Runner, force bool, log io.Writer) error {
	if err := stopAndWait(r); err != nil {
		return err
	}
	scope := github.Scope{Owner: r.Owner, Repo: r.Repo}
	var regErr error
	if r.Configured() {
		token, err := c.RemoveToken(ctx, scope)
		if err == nil {
			fmt.Fprintf(log, "Unregistering %q from %s ...\n", r.Name, scope)
			err = runner.Unconfigure(ctx, r.Dir(), token, log)
		}
		if err != nil {
			regErr = err
			// The config script can fail when the runner is already gone
			// on GitHub; delete by name through the API as a fallback.
			if runners, lerr := c.ListRunners(ctx, scope); lerr == nil {
				regErr = nil
				for _, gr := range runners {
					if gr.Name == r.Name {
						regErr = c.DeleteRunner(ctx, scope, gr.ID)
					}
				}
			} else if github.IsNotFound(lerr) {
				regErr = nil // repo/org gone: nothing left to unregister
			}
		}
	}
	if regErr != nil && !force {
		return fmt.Errorf("could not unregister %q of %s (runner left stopped; rerun with --force to delete it locally anyway): %w", r.Name, r.Scope(), regErr)
	}
	return removeDir(r.Dir())
}

// removeDir retries because Windows keeps files locked for a moment after
// the runner process exits.
func removeDir(dir string) error {
	var err error
	for i := 0; i < 10; i++ {
		if err = os.RemoveAll(dir); err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return err
}

// RepoFilter is the shared repo selection of sync and on-demand mode.
type RepoFilter struct {
	Owners        []string
	Include       []string
	Exclude       []string
	IncludePublic bool
	IncludeForks  bool
}

// Match reports whether a repo is selected, and why not if it isn't.
func (f RepoFilter) Match(r github.Repo, me string) (bool, string) {
	owners := f.Owners
	if len(owners) == 0 {
		owners = []string{me}
	}
	ownerOK := false
	for _, o := range owners {
		if strings.EqualFold(o, r.Owner.Login) {
			ownerOK = true
		}
	}
	name := strings.ToLower(r.FullName)
	switch {
	case !ownerOK:
		return false, "owner not selected"
	case r.Archived:
		return false, "archived"
	case r.Disabled:
		return false, "disabled"
	case !r.Permissions.Admin:
		return false, "no admin rights"
	case !r.Private && !f.IncludePublic:
		return false, "public"
	case r.Fork && !f.IncludeForks:
		return false, "fork"
	case len(f.Include) > 0 && !globAny(f.Include, name):
		return false, "not in include"
	case globAny(f.Exclude, name):
		return false, "excluded"
	}
	return true, ""
}

func globAny(patterns []string, name string) bool {
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if !strings.Contains(p, "/") {
			p = "*/" + p
		}
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// SyncPlan is what Sync would change.
type SyncPlan struct {
	Add    []github.Repo
	Prune  []store.Runner
	Have   int
	Errors []string
}

// PlanSync compares your repositories with the local runners.
func PlanSync(ctx context.Context, c *github.Client, s store.Sync) (SyncPlan, error) {
	me, _, err := c.Me(ctx)
	if err != nil {
		return SyncPlan{}, err
	}
	repos, err := c.ListMyRepos(ctx)
	if err != nil {
		return SyncPlan{}, err
	}
	local, err := store.List()
	if err != nil {
		return SyncPlan{}, err
	}
	have := map[string][]store.Runner{}
	for _, r := range local {
		if r.Kind == store.KindRepo {
			key := strings.ToLower(r.Scope())
			have[key] = append(have[key], r)
		}
	}
	f := RepoFilter{Owners: s.Owners, Include: s.Include, Exclude: s.Exclude, IncludePublic: s.IncludePublic, IncludeForks: s.IncludeForks}
	var plan SyncPlan
	wanted := map[string]bool{}
	for _, repo := range repos {
		ok, _ := f.Match(repo, me.Login)
		if !ok {
			continue
		}
		key := strings.ToLower(repo.FullName)
		wanted[key] = true
		if anyConfigured(have[key]) {
			plan.Have++
			continue
		}
		plan.Add = append(plan.Add, repo)
	}
	if s.Prune {
		for key, rs := range have {
			for _, r := range rs {
				// Only prune what sync created; hand-added runners stay.
				if !wanted[key] && r.Source == "sync" {
					plan.Prune = append(plan.Prune, r)
				}
			}
		}
	}
	return plan, nil
}

func anyConfigured(rs []store.Runner) bool {
	for _, r := range rs {
		if r.Configured() {
			return true
		}
	}
	return false
}

// ApplySync adds and prunes per the plan, continuing past per-repo errors.
func ApplySync(ctx context.Context, c *github.Client, plan *SyncPlan, log io.Writer) {
	for _, repo := range plan.Add {
		fmt.Fprintf(log, "+ %s\n", repo.FullName)
		_, err := Add(ctx, c, github.Scope{Owner: repo.Owner.Login, Repo: repo.Name}, AddOptions{Source: "sync", Force: true}, log)
		if err != nil {
			plan.Errors = append(plan.Errors, fmt.Sprintf("%s: %v", repo.FullName, err))
			fmt.Fprintf(log, "  failed: %v\n", err)
		}
	}
	for _, r := range plan.Prune {
		fmt.Fprintf(log, "- %s\n", r.Scope())
		if err := Remove(ctx, c, r, true, log); err != nil {
			plan.Errors = append(plan.Errors, fmt.Sprintf("%s: %v", r.Scope(), err))
		}
	}
}
