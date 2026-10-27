package localci

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Status is the outcome of one job.
type Status string

const (
	Pass        Status = "PASS"
	Fail        Status = "FAIL"
	Skipped     Status = "SKIPPED"
	Unsupported Status = "UNSUPPORTED"
)

// Result is what happened to one job.
type Result struct {
	Job      string
	Status   Status
	Reason   string // why it was skipped or unsupported, or the failing step
	Duration time.Duration
	Log      string // path of the job's log file
}

// JobSpec is everything an executor needs to run one job.
type JobSpec struct {
	Name    string
	Job     *Job
	Tarball string // downloaded source archive
	RunDir  string // per-job folder: workspace and artifacts go here
	Log     string
	Env     []string // scrubbed environment, KEY=VALUE
}

// Executor runs one job. The local executor runs it on this machine; a
// fleet member can implement the same interface later.
type Executor interface {
	RunJob(ctx context.Context, spec JobSpec) Result
}

// LocalExecutor runs jobs natively on this machine.
type LocalExecutor struct{}

// RunEnv describes the pull request a run is for.
type RunEnv struct {
	Repo      string // owner/repo
	SHA       string
	Ref       string
	Workspace string
}

var secretKey = regexp.MustCompile(`(?i)TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|API_?KEY`)

// ScrubEnv builds the environment steps run in: a few basics from the
// parent plus CI=true and the GITHUB_* values a script may read. Nothing
// else from the parent leaks in, and no key that looks like a credential is
// ever kept.
func ScrubEnv(parent []string, r RunEnv, extra map[string]string) []string {
	have := map[string]string{}
	for _, kv := range parent {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch k {
		case "PATH", "HOME", "USER", "LANG", "TMPDIR", "SHELL", "LOGNAME":
			have[k] = v
		}
	}
	have["CI"] = "true"
	have["GITHUB_SHA"] = r.SHA
	have["GITHUB_REF"] = r.Ref
	have["GITHUB_WORKSPACE"] = r.Workspace
	have["GITHUB_REPOSITORY"] = r.Repo
	have["GITHUB_EVENT_NAME"] = "pull_request"
	for k, v := range extra {
		if strings.Contains(v, "${{") {
			continue // an expression we cannot evaluate
		}
		have[k] = v
	}
	var out []string
	for k, v := range have {
		if secretKey.MatchString(k) {
			continue
		}
		out = append(out, k+"="+v)
	}
	return out
}

// Extract unpacks a GitHub source tarball into dest, dropping its single
// top-level folder and refusing paths that escape dest.
func Extract(tarball, dest string) error {
	f, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		_, rel, _ := strings.Cut(filepath.ToSlash(h.Name), "/")
		if rel == "" {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if target != dest && !strings.HasPrefix(target, dest+string(filepath.Separator)) {
			return fmt.Errorf("tarball path escapes the workspace: %s", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			if err = os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			var w *os.File
			if w, err = os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o777|0o200); err != nil {
				return err
			}
			_, err = io.Copy(w, tr)
			if cerr := w.Close(); err == nil {
				err = cerr
			}
		case tar.TypeSymlink:
			if err = os.MkdirAll(filepath.Dir(target), 0o755); err == nil {
				err = os.Symlink(h.Linkname, target)
			}
		}
		if err != nil {
			return err
		}
	}
}

// RunJob extracts the source into the job's own folder (jobs run in parallel
// and share files like package.json, so each gets a fresh copy), then runs
// the steps in order.
func (LocalExecutor) RunJob(ctx context.Context, spec JobSpec) Result {
	start := time.Now()
	res := Result{Job: spec.Name, Log: spec.Log}
	finish := func(s Status, reason string) Result {
		res.Status, res.Reason, res.Duration = s, reason, time.Since(start)
		return res
	}
	if err := os.MkdirAll(filepath.Dir(spec.Log), 0o755); err != nil {
		return finish(Fail, err.Error())
	}
	lf, err := os.Create(spec.Log)
	if err != nil {
		return finish(Fail, err.Error())
	}
	defer lf.Close()
	ws := filepath.Join(spec.RunDir, "src")
	if err := Extract(spec.Tarball, ws); err != nil {
		fmt.Fprintln(lf, "extract failed:", err)
		return finish(Fail, "extract failed")
	}
	env := append([]string{}, spec.Env...)
	env = append(env, "GITHUB_WORKSPACE="+ws)
	failed, failedStep := false, ""
	for i, st := range spec.Job.Steps {
		label := st.Name
		if label == "" {
			label = st.Uses
		}
		if label == "" {
			label = firstLine(st.Run)
		}
		switch EvalIf(st.If, nil) {
		case CondFailure:
			if !failed {
				continue
			}
		case CondAlways:
		case CondTrue:
			if failed {
				continue
			}
		default:
			fmt.Fprintf(lf, "::step %d (%s): skipped, unsupported condition %q\n", i+1, label, st.If)
			continue
		}
		fmt.Fprintf(lf, "::step %d: %s\n", i+1, label)
		var err error
		if st.Uses != "" {
			err = runUses(ctx, st, ws, spec.RunDir, env, lf)
		} else {
			err = runScript(ctx, st, spec.Job, ws, env, lf)
		}
		if err != nil {
			fmt.Fprintf(lf, "::error %v\n", err)
			if !failed {
				failed, failedStep = true, label
			}
		}
	}
	if failed {
		return finish(Fail, "step failed: "+failedStep)
	}
	return finish(Pass, "")
}

// knownUses is the set of actions the local CI can stand in for.
func knownUses(uses string) bool {
	for _, p := range []string{"actions/checkout", "actions/setup-node", "actions/setup-java", "subosito/flutter-action", "actions/upload-artifact"} {
		if strings.HasPrefix(uses, p+"@") || uses == p {
			return true
		}
	}
	return false
}

func runUses(ctx context.Context, st Step, ws, runDir string, env []string, log io.Writer) error {
	name, _, _ := strings.Cut(st.Uses, "@")
	switch name {
	case "actions/checkout":
		return nil
	case "actions/setup-node":
		out, err := capture(ctx, ws, env, "node", "--version")
		if err != nil {
			return fmt.Errorf("node is not installed: %v", err)
		}
		want := st.With["node-version"]
		if want == "" && st.With["node-version-file"] != "" {
			b, _ := os.ReadFile(filepath.Join(ws, st.With["node-version-file"]))
			want = strings.TrimSpace(string(b))
		}
		if wm, gm := majorOf(want), majorOf(out); wm != "" && wm != gm {
			fmt.Fprintf(log, "warning: workflow wants node %s, this machine has %s; continuing\n", want, strings.TrimSpace(out))
		}
		return nil
	case "actions/setup-java":
		return runCmd(ctx, ws, env, log, "java", "-version")
	case "subosito/flutter-action":
		return runCmd(ctx, ws, env, log, "flutter", "--version")
	case "actions/upload-artifact":
		return copyArtifact(st, ws, runDir, log)
	}
	return fmt.Errorf("unsupported action %s", st.Uses)
}

func runScript(ctx context.Context, st Step, j *Job, ws string, env []string, log io.Writer) error {
	script := st.Run
	if runtime.GOOS != "linux" {
		script = strings.ReplaceAll(script, "playwright install --with-deps", "playwright install")
	}
	dir := ws
	wd := st.WorkDir
	if wd == "" {
		wd = j.WorkDir
	}
	if wd != "" {
		dir = filepath.Join(ws, filepath.FromSlash(wd))
	}
	stepEnv := env
	for k, v := range mergeEnv(j.Env, st.Env) {
		if !strings.Contains(v, "${{") && !secretKey.MatchString(k) {
			stepEnv = append(stepEnv, k+"="+v)
		}
	}
	return runCmd(ctx, dir, stepEnv, log, "bash", "-eo", "pipefail", "-c", script)
}

func mergeEnv(a, b map[string]string) map[string]string {
	m := map[string]string{}
	for k, v := range a {
		m[k] = v
	}
	for k, v := range b {
		m[k] = v
	}
	return m
}

func runCmd(ctx context.Context, dir string, env []string, log io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, log, log
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func capture(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.Output()
	return string(out), err
}

var majorRe = regexp.MustCompile(`\d+`)

// majorOf returns the first number in a version like "v20.11.0" or "lts/iron".
func majorOf(v string) string {
	m := majorRe.FindString(v)
	if m == "" {
		return ""
	}
	n, _ := strconv.Atoi(m)
	return strconv.Itoa(n)
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}

// copyArtifact copies upload-artifact's with.path entries (one per line)
// into <runDir>/artifacts/<name>/.
func copyArtifact(st Step, ws, runDir string, log io.Writer) error {
	out := filepath.Join(runDir, "artifacts", st.With["name"])
	for _, p := range strings.Split(st.With["path"], "\n") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		src := filepath.Join(ws, filepath.FromSlash(p))
		if _, err := os.Stat(src); err != nil {
			fmt.Fprintf(log, "artifact path %s not found, skipping\n", p)
			continue
		}
		if err := copyTree(src, filepath.Join(out, filepath.Base(src))); err != nil {
			return fmt.Errorf("copy artifact %s: %w", p, err)
		}
	}
	return nil
}

// copyTree copies a file or folder (symlinks are skipped).
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(target, 0o755)
		case info.Mode().IsRegular():
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(target, b, info.Mode().Perm())
		}
		return nil
	})
}
