// Package runner installs, registers and removes actions/runner copies.
package runner

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/proc"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// Platform returns the actions/runner os and architecture names for this machine.
func Platform() (osName, arch string, err error) {
	switch runtime.GOOS {
	case "darwin":
		osName = "osx"
	case "linux":
		osName = "linux"
	case "windows":
		osName = "win"
	default:
		return "", "", fmt.Errorf("actions/runner does not support %s", runtime.GOOS)
	}
	switch runtime.GOARCH {
	case "amd64":
		arch = "x64"
	case "arm64":
		arch = "arm64"
	case "arm":
		arch = "arm"
	default:
		return "", "", fmt.Errorf("actions/runner does not support %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return osName, arch, nil
}

var versionRe = regexp.MustCompile(`(\d+\.\d+\.\d+)`)

// Package is a downloaded, checksum-verified runner archive.
type Package struct {
	Path    string
	Version string
}

// EnsurePackage downloads the runner package for this machine into the cache
// (once per version) and verifies its SHA-256.
func EnsurePackage(ctx context.Context, c *github.Client, scope github.Scope, log io.Writer) (Package, error) {
	osName, arch, err := Platform()
	if err != nil {
		return Package{}, err
	}
	// The per-scope list needs admin rights and is empty for repos that
	// never ran a workflow; the actions/runner release is the fallback.
	downloads, _ := c.Downloads(ctx, scope)
	d := pick(downloads, osName, arch)
	if d == nil {
		if downloads, err = c.RunnerReleaseDownloads(ctx); err != nil {
			return Package{}, err
		}
		d = pick(downloads, osName, arch)
	}
	if d == nil {
		return Package{}, fmt.Errorf("GitHub offers no runner package for %s-%s", osName, arch)
	}
	version := versionRe.FindString(d.Filename)
	path := filepath.Join(store.CacheDir(), d.Filename)
	if ok, _ := checksumMatches(path, d.SHA256); ok {
		return Package{path, version}, nil
	}
	if err := os.MkdirAll(store.CacheDir(), 0o755); err != nil {
		return Package{}, err
	}
	fmt.Fprintf(log, "Downloading %s ...\n", d.Filename)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.DownloadURL, nil)
	if err != nil {
		return Package{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Package{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Package{}, fmt.Errorf("download %s: %s", d.DownloadURL, resp.Status)
	}
	// A temp file of its own: an add from the CLI and one from the
	// dashboard may download the same package at once.
	f, err := os.CreateTemp(store.CacheDir(), d.Filename+".*.part")
	if err != nil {
		return Package{}, err
	}
	tmp := f.Name()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return Package{}, err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return Package{}, err
	}
	if sum := hex.EncodeToString(h.Sum(nil)); d.SHA256 != "" && !strings.EqualFold(sum, d.SHA256) {
		os.Remove(tmp)
		return Package{}, fmt.Errorf("checksum mismatch for %s: got %s, want %s", d.Filename, sum, d.SHA256)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		// The other download may have finished first and hold the file open.
		if ok, _ := checksumMatches(path, d.SHA256); ok {
			return Package{path, version}, nil
		}
		return Package{}, err
	}
	return Package{path, version}, nil
}

func pick(downloads []github.Download, osName, arch string) *github.Download {
	for i := range downloads {
		if downloads[i].OS == osName && downloads[i].Architecture == arch {
			return &downloads[i]
		}
	}
	return nil
}

func checksumMatches(path, want string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return want == "" || strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want), nil
}

// Extract unpacks a .tar.gz or .zip runner package into dir.
func Extract(archive, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if strings.HasSuffix(archive, ".zip") {
		return extractZip(archive, dir)
	}
	return extractTarGz(archive, dir)
}

// safeJoin rejects archive entries that would land outside dir.
func safeJoin(dir, name string) (string, error) {
	p := filepath.Join(dir, name)
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes the target directory", name)
	}
	return p, nil
}

func extractTarGz(archive, dir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		p, err := safeJoin(dir, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeFile(p, tr, os.FileMode(h.Mode)&0o777|0o600); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if filepath.IsAbs(h.Linkname) || strings.HasPrefix(h.Linkname, "/") || strings.HasPrefix(h.Linkname, `\`) || filepath.VolumeName(h.Linkname) != "" {
				return fmt.Errorf("archive symlink %q is absolute", h.Name)
			}
			if _, err := safeJoin(dir, filepath.Join(filepath.Dir(h.Name), h.Linkname)); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			os.Remove(p)
			if err := os.Symlink(h.Linkname, p); err != nil {
				return err
			}
		}
	}
}

func extractZip(archive, dir string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, zf := range zr.File {
		p, err := safeJoin(dir, zf.Name)
		if err != nil {
			return err
		}
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		err = writeFile(p, rc, zf.Mode()&0o777|0o600)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func writeFile(p string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// script returns the command for one of the runner's scripts ("config" or "run").
func script(dir, name string, args ...string) *exec.Cmd {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", append([]string{"/c", filepath.Join(dir, name+".cmd")}, args...)...)
	} else {
		cmd = exec.Command(filepath.Join(dir, name+".sh"), args...)
	}
	cmd.Dir = dir
	return cmd
}

// ConfigureOptions are passed to the runner's config script.
type ConfigureOptions struct {
	URL    string
	Token  string
	Name   string
	Labels []string
	Group  string
	// NoReplace fails when GitHub already has a runner of that name instead
	// of taking over its registration.
	NoReplace bool
}

// Configure registers the runner installed in dir with GitHub.
func Configure(ctx context.Context, dir string, o ConfigureOptions, out io.Writer) error {
	args := []string{"--unattended", "--url", o.URL, "--token", o.Token, "--name", o.Name, "--work", "_work"}
	if !o.NoReplace {
		args = append(args, "--replace")
	}
	if len(o.Labels) > 0 {
		args = append(args, "--labels", strings.Join(o.Labels, ","))
	}
	if o.Group != "" {
		args = append(args, "--runnergroup", o.Group)
	}
	cmd := script(dir, "config", args...)
	cmd.Stdout, cmd.Stderr = out, out
	if err := runCtx(ctx, cmd); err != nil {
		return fmt.Errorf("runner config script failed: %w", err)
	}
	return nil
}

// Unconfigure removes the registration of the runner installed in dir.
func Unconfigure(ctx context.Context, dir, token string, out io.Writer) error {
	cmd := script(dir, "config", "remove", "--token", token)
	cmd.Stdout, cmd.Stderr = out, out
	return runCtx(ctx, cmd)
}

// RunCommand is the long-running listener the daemon supervises.
// With a JIT config the runner takes one job and exits.
func RunCommand(dir string, jitConfig string) *exec.Cmd {
	if jitConfig != "" {
		cmd := script(dir, "run", "--jitconfig", jitConfig)
		cmd.Env = proc.EnvFor(dir)
		return cmd
	}
	cmd := script(dir, "run")
	cmd.Env = proc.EnvFor(dir)
	return cmd
}

func runCtx(ctx context.Context, cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return ctx.Err()
	}
}
