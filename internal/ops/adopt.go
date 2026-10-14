package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// Found is a runner on this machine that was set up without gh-runners
// (by hand with config.sh, or by a script), found by Scan.
type Found struct {
	Dir     string   `json:"dir"`
	Name    string   `json:"name"`  // runner name on GitHub
	Scope   string   `json:"scope"` // owner/repo or org
	Kind    string   `json:"kind"`  // repo | org
	Owner   string   `json:"owner"`
	Repo    string   `json:"repo,omitempty"`
	Running bool     `json:"running"`           // a process runs from the folder now
	Starter []string `json:"starter,omitempty"` // services that start it now
	Problem string   `json:"problem,omitempty"` // why it cannot be moved in as is
}

// A foreignService starts a hand-made runner: a Windows service, a launchd
// plist or a systemd unit.
type foreignService struct {
	Name       string // what to show
	Path       string // plist or unit file, if any
	NeedsAdmin bool
}

// runnerFile is the part of the runner's .runner settings gh-runners reads.
type runnerFile struct {
	AgentName string `json:"agentName"`
	GitHubURL string `json:"gitHubUrl"`
	Ephemeral bool   `json:"ephemeral"`
}

// readRunnerFile reads dir/.runner, which the runner writes with a BOM.
func readRunnerFile(dir string) (runnerFile, error) {
	var rf runnerFile
	b, err := os.ReadFile(filepath.Join(dir, ".runner"))
	if err != nil {
		return rf, err
	}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if err := json.Unmarshal(b, &rf); err != nil {
		return rf, fmt.Errorf("%s: %w", filepath.Join(dir, ".runner"), err)
	}
	if rf.AgentName == "" || rf.GitHubURL == "" {
		return rf, fmt.Errorf("%s has no runner name or URL", filepath.Join(dir, ".runner"))
	}
	return rf, nil
}

// scopeFromURL turns the runner's gitHubUrl into a scope.
func scopeFromURL(raw string) (github.Scope, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return github.Scope{}, err
	}
	if !strings.EqualFold(u.Host, "github.com") {
		return github.Scope{}, fmt.Errorf("registered with %s; gh-runners only manages github.com runners", u.Host)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch {
	case len(parts) == 2 && parts[0] == "enterprises":
		return github.Scope{}, errors.New("an enterprise runner; gh-runners manages repository and organization runners")
	case len(parts) == 1 && parts[0] != "":
		return github.Scope{Owner: parts[0]}, nil
	case len(parts) == 2 && parts[0] != "" && parts[1] != "":
		return github.Scope{Owner: parts[0], Repo: parts[1]}, nil
	}
	return github.Scope{}, fmt.Errorf("cannot read the repository from %q", raw)
}

// isRunnerDir reports whether dir holds a configured actions/runner.
func isRunnerDir(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".runner")); err != nil {
		return false
	}
	_, err1 := os.Stat(filepath.Join(dir, "config.sh"))
	_, err2 := os.Stat(filepath.Join(dir, "config.cmd"))
	return err1 == nil || err2 == nil
}

// ScanRoots are where hand-made runners usually live: the home folder,
// and the drive root on Windows (GitHub's instructions use C:\actions-runner).
func ScanRoots() []string {
	var roots []string
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, home)
		if runtime.GOOS == "windows" {
			roots = append(roots, filepath.VolumeName(home)+`\`)
		}
	}
	switch runtime.GOOS {
	case "darwin":
		roots = append(roots, "/opt", "/Users/Shared")
	case "linux":
		roots = append(roots, "/opt", "/srv")
	}
	return roots
}

var (
	runnerish = regexp.MustCompile(`(?i)runner|actions|github|\bci\b`)
	skipDirs  = map[string]bool{
		"node_modules": true, "library": true, "appdata": true, "applications": true, "pictures": true,
		"music": true, "movies": true, "videos": true, "windows": true, "program files": true,
		"program files (x86)": true, "programdata": true, "$recycle.bin": true, "system volume information": true,
	}
)

// Scan looks for configured runners in roots: each root, its folders, and
// one level deeper inside folders named like runner, actions or github.
// Runners gh-runners manages are left out.
func Scan(roots []string) []Found {
	seen := map[string]bool{}
	var dirs []string
	add := func(d string) {
		key := strings.ToLower(filepath.Clean(d))
		if seen[key] || !isRunnerDir(d) || samePath(d, store.Home()) || within(d, store.Home()) || within(d, store.RunnersDir()) {
			return
		}
		seen[key] = true
		dirs = append(dirs, d)
	}
	children := func(d string) []string {
		entries, err := os.ReadDir(d)
		if err != nil {
			return nil
		}
		var out []string
		for _, e := range entries {
			n := e.Name()
			if !e.IsDir() || strings.HasPrefix(n, ".") || skipDirs[strings.ToLower(n)] {
				continue
			}
			out = append(out, filepath.Join(d, n))
		}
		return out
	}
	for _, root := range roots {
		add(root)
		for _, c := range children(root) {
			add(c)
			if runnerish.MatchString(filepath.Base(c)) {
				for _, cc := range children(c) {
					add(cc)
				}
			}
		}
	}
	out := []Found{}
	for _, d := range dirs {
		out = append(out, Inspect(d))
	}
	return out
}

// Inspect describes the runner in dir.
func Inspect(dir string) Found {
	f := Found{Dir: dir}
	rf, err := readRunnerFile(dir)
	if err != nil {
		f.Problem = err.Error()
		return f
	}
	f.Name = rf.AgentName
	sc, err := scopeFromURL(rf.GitHubURL)
	if err != nil {
		f.Problem = err.Error()
	} else {
		f.Scope, f.Owner, f.Repo, f.Kind = sc.String(), sc.Owner, sc.Repo, store.KindRepo
		if sc.IsOrg() {
			f.Kind = store.KindOrg
		}
	}
	if rf.Ephemeral {
		f.Problem = "an ephemeral runner (it unregisters after one job)"
	}
	f.Running = len(procsIn(dir)) > 0
	for _, s := range foreignServices(dir) {
		f.Starter = append(f.Starter, s.Name)
	}
	return f
}

// Adopt moves a hand-made runner into gh-runners: it checks on GitHub that
// the runner is idle, stops whatever starts it now (service, launchd or
// systemd), moves its folder into the runners folder and hands it to the
// daemon. The registration stays: same name, labels and runner id.
func Adopt(ctx context.Context, c *github.Client, dir string, log io.Writer) (store.Runner, error) {
	dir = filepath.Clean(dir)
	if !isRunnerDir(dir) {
		return store.Runner{}, fmt.Errorf("%s is not a configured GitHub Actions runner folder", dir)
	}
	if within(dir, store.RunnersDir()) {
		return store.Runner{}, fmt.Errorf("%s is already in the gh-runners folder", dir)
	}
	f := Inspect(dir)
	if f.Problem != "" {
		return store.Runner{}, fmt.Errorf("%s: %s", dir, f.Problem)
	}
	scope := github.Scope{Owner: f.Owner, Repo: f.Repo}
	gr, err := remoteRunner(ctx, c, scope, f.Name)
	if err != nil {
		return store.Runner{}, err
	}
	if gr.Busy {
		return store.Runner{}, fmt.Errorf("%q is running a job right now; move it once the job is done", f.Name)
	}
	var labels []string
	for _, l := range gr.Labels {
		if l.Type != "read-only" && !IsBuiltinLabel(l.Name) {
			labels = append(labels, l.Name)
		}
	}

	fmt.Fprintf(log, "Stopping %q (%s) ...\n", f.Name, dir)
	if err := stopForeign(dir, log); err != nil {
		return store.Runner{}, err
	}
	if err := killProcsIn(dir, log); err != nil {
		return store.Runner{}, err
	}
	// It may have picked up a job between the check and the stop.
	if gr, err := remoteRunner(ctx, c, scope, f.Name); err == nil && gr.Busy {
		fmt.Fprintln(log, "GitHub still shows it busy; the job it had will be retried or fail.")
	}

	id := freeID(store.IDFor(f.Kind, f.Owner, f.Repo), f.Name)
	r := store.Runner{
		ID: id, Kind: f.Kind, Owner: f.Owner, Repo: f.Repo, Name: f.Name, Labels: labels,
		CreatedAt: time.Now().UTC(), Source: "adopted", Version: runnerVersion(dir),
	}
	dst := r.Dir()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return store.Runner{}, err
	}
	fmt.Fprintf(log, "Moving %s to %s ...\n", dir, dst)
	if err := moveDir(dir, dst); err != nil {
		return store.Runner{}, fmt.Errorf("could not move the folder (the runner is stopped; start it again from %s with run, or retry): %w", dir, err)
	}
	// svc.sh keeps the name of the service it installed here.
	_ = os.Remove(filepath.Join(dst, ".service"))
	if err := store.Save(r); err != nil {
		return store.Runner{}, err
	}
	_ = control.Reload()
	fmt.Fprintf(log, "%q is now run by gh-runners from %s.\n", r.Name, dst)
	return r, nil
}

func remoteRunner(ctx context.Context, c *github.Client, scope github.Scope, name string) (github.Runner, error) {
	list, err := c.ListRunners(ctx, scope)
	if err != nil {
		return github.Runner{}, err
	}
	for _, gr := range list {
		if gr.Name == name {
			return gr, nil
		}
	}
	return github.Runner{}, fmt.Errorf("%q is not registered to %s on GitHub any more; delete its folder, or add a new runner instead", name, scope)
}

// freeID is base, or base plus the runner name when this machine already
// has a runner for the same repository.
func freeID(base, name string) string {
	taken := func(id string) bool {
		_, err := os.Stat(filepath.Join(store.RunnersDir(), id))
		return err == nil
	}
	if !taken(base) {
		return base
	}
	id := base + "__" + strings.Trim(unsafeID.ReplaceAllString(name, "-"), "-")
	for i := 2; taken(id); i++ {
		id = fmt.Sprintf("%s__%s-%d", base, strings.Trim(unsafeID.ReplaceAllString(name, "-"), "-"), i)
	}
	return id
}

var unsafeID = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// runnerVersion asks the runner in dir for its version.
func runnerVersion(dir string) string {
	exe := filepath.Join(dir, "bin", "Runner.Listener")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--version")
	cmd.Dir = dir
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// killProcsIn stops processes started from dir and waits for them to go.
func killProcsIn(dir string, log io.Writer) error {
	pids := procsIn(dir)
	if len(pids) == 0 {
		return nil
	}
	fmt.Fprintf(log, "Stopping %d process(es) running from %s ...\n", len(pids), dir)
	for _, pid := range pids {
		terminate(pid, false)
	}
	for i := 0; i < 20; i++ {
		if len(procsIn(dir)) == 0 {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	for _, pid := range procsIn(dir) {
		terminate(pid, true)
	}
	time.Sleep(2 * time.Second)
	if left := procsIn(dir); len(left) > 0 {
		return fmt.Errorf("processes %v from %s keep running; something restarts them (a service or a script). Stop it and try again", left, dir)
	}
	return nil
}

// containsPath reports whether text names dir or a file inside it, so
// /x/actions-runner does not match /x/actions-runner-light.
func containsPath(text, dir string) bool {
	sep := string(filepath.Separator)
	for i := 0; ; {
		j := indexFold(text[i:], dir)
		if j < 0 {
			return false
		}
		end := i + j + len(dir)
		if end == len(text) || strings.ContainsRune(sep+`"'<	 `+"\n", rune(text[end])) {
			return true
		}
		i = end
	}
}

func indexFold(s, sub string) int {
	if runtime.GOOS == "linux" {
		return strings.Index(s, sub)
	}
	return strings.Index(strings.ToLower(s), strings.ToLower(sub))
}
