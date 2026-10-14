package ops

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// MachineLabels is the full label set a runner on this machine answers to:
// what GitHub adds by default plus the configured custom labels.
func MachineLabels(cfg store.Config) []string {
	osLabel := map[string]string{"darwin": "macOS", "linux": "Linux", "windows": "Windows"}[runtime.GOOS]
	archLabel := map[string]string{"amd64": "X64", "arm64": "ARM64", "arm": "ARM"}[runtime.GOARCH]
	return append([]string{"self-hosted", osLabel, archLabel}, Labels(cfg, nil)...)
}

// LocalState describes a runner as this machine sees it, for list and the
// dashboard: running, stopped, restarting in 20s, no daemon, ...
func LocalState(r store.Runner, st control.Status, daemonUp bool) (state, lastExit string) {
	var p control.ProcState
	ok := false
	for _, x := range st.Runners {
		if x.ID == r.ID {
			p, ok = x, true
		}
	}
	switch {
	case !r.Configured():
		return "not registered", ""
	case r.Disabled:
		return "stopped", ""
	case !daemonUp:
		return "no daemon", ""
	case ok && p.State == "backoff":
		return fmt.Sprintf("restarting in %s", time.Until(p.NextStart).Round(time.Second)), p.LastExit
	case ok:
		return p.State, p.LastExit
	}
	return "starting", ""
}

// MoveRunners makes dir the runners folder. Existing runners are stopped,
// moved there and started again. An empty dir means the default folder.
func MoveRunners(dir string, log io.Writer) error {
	if dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		dir = filepath.Clean(abs)
	}
	target := dir
	if target == "" {
		target = store.DefaultRunnersDir()
	}
	old := filepath.Clean(store.RunnersDir())
	cfg, err := store.LoadConfig()
	if err != nil {
		return err
	}
	if samePath(old, target) {
		cfg.RunnersDir = dir
		return store.SaveConfig(cfg)
	}
	if within(target, old) || within(old, target) {
		return fmt.Errorf("pick a folder that is not inside %s (or the other way round)", old)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	probe := filepath.Join(target, ".gh-runners-write-test")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		return fmt.Errorf("cannot write to %s: %w", target, err)
	}
	os.Remove(probe)

	runners, err := store.List()
	if err != nil {
		return err
	}
	for _, r := range runners {
		if _, err := os.Stat(filepath.Join(target, r.ID)); err == nil {
			return fmt.Errorf("%s already has a folder named %s", target, r.ID)
		}
	}
	wasOn := map[string]bool{}
	for _, r := range runners {
		if !r.Disabled {
			wasOn[r.ID] = true
			fmt.Fprintf(log, "Stopping %s ...\n", r.Scope())
			if err := stopAndWait(r); err != nil {
				return err
			}
		}
	}
	var moved []string
	var moveErr error
	for _, r := range runners {
		fmt.Fprintf(log, "Moving %s to %s ...\n", r.Scope(), filepath.Join(target, r.ID))
		if err := moveDir(r.Dir(), filepath.Join(target, r.ID)); err != nil {
			moveErr = fmt.Errorf("move %s: %w", r.Scope(), err)
			break
		}
		moved = append(moved, r.ID)
	}
	if moveErr != nil {
		// Put back what already moved so every runner stays in one folder.
		for _, id := range moved {
			_ = moveDir(filepath.Join(target, id), filepath.Join(old, id))
		}
	} else {
		cfg.RunnersDir = dir
		if err := store.SaveConfig(cfg); err != nil {
			moveErr = err
		}
	}
	// Start again whatever was running, in whichever folder it now lives.
	if rs, err := store.List(); err == nil {
		for _, r := range rs {
			if wasOn[r.ID] {
				r.Disabled = false
				_ = store.Save(r)
			}
		}
	}
	_ = control.Reload()
	if moveErr != nil {
		return moveErr
	}
	fmt.Fprintf(log, "Runners now live in %s.\n", target)
	return nil
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// within reports whether p is inside dir.
func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// moveDir renames, or copies and deletes when src and dst are on different
// drives. dst must not exist, or be an empty folder claimed for src; it
// never merges into another runner's folder.
func moveDir(src, dst string) error {
	if runtime.GOOS == "windows" {
		_ = os.Remove(dst) // Rename does not replace even an empty folder there
	}
	var err error
	for i := 0; i < 5; i++ { // Windows holds files for a moment after exit
		if err = os.Rename(src, dst); err == nil {
			return nil
		}
		if entries, statErr := os.ReadDir(dst); statErr == nil {
			if len(entries) > 0 {
				return fmt.Errorf("%s exists already", dst)
			}
			break
		}
		var le *os.LinkError
		if errors.As(err, &le) && isCrossDevice(le.Err) {
			break
		}
		time.Sleep(time.Second)
	}
	if err := copyTree(src, dst); err != nil {
		os.RemoveAll(dst)
		return err
	}
	return removeDir(src)
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, out)
		case info.IsDir():
			return os.MkdirAll(out, info.Mode().Perm()|0o700)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, in); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	})
}
