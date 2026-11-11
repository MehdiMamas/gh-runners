package service

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type interopEnv struct {
	vars   map[string]string
	probed []string
	live   map[string]bool
	logs   *bytes.Buffer
}

func (e *interopEnv) run(t *testing.T, glob string) string {
	t.Helper()
	e.logs = &bytes.Buffer{}
	log.SetOutput(e.logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return fixInterop(func(k string) string { return e.vars[k] },
		func(k, v string) error { e.vars[k] = v; return nil }, glob,
		func(s string) bool { e.probed = append(e.probed, s); return e.live[s] })
}

func fakeSockets(t *testing.T, names map[string]time.Duration) string {
	t.Helper()
	dir := t.TempDir()
	now := time.Now()
	for n, age := range names {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "*_interop")
}

func TestFixInteropCurrentAnswers(t *testing.T) {
	glob := fakeSockets(t, map[string]time.Duration{"1_interop": 0})
	e := &interopEnv{vars: map[string]string{"WSL_DISTRO_NAME": "U", "WSL_INTEROP": "cur"}, live: map[string]bool{"cur": true}}
	e.run(t, glob)
	if e.vars["WSL_INTEROP"] != "cur" || e.logs.Len() != 0 || len(e.probed) != 1 {
		t.Errorf("env %v logs %q probed %v", e.vars, e.logs, e.probed)
	}
}

func TestFixInteropPicksNewestWorking(t *testing.T) {
	glob := fakeSockets(t, map[string]time.Duration{
		"1_interop": time.Hour, "2_interop": time.Minute, "3_interop": 2 * time.Hour})
	dir := filepath.Dir(glob)
	// the symlink case: a link to the working socket
	link := filepath.Join(dir, "9_interop")
	if err := os.Symlink(filepath.Join(dir, "1_interop"), link); err != nil {
		t.Fatal(err)
	}
	e := &interopEnv{vars: map[string]string{"WSL_DISTRO_NAME": "U", "WSL_INTEROP": "dead"},
		live: map[string]bool{filepath.Join(dir, "1_interop"): true, link: true, filepath.Join(dir, "3_interop"): true}}
	e.run(t, glob)
	got := e.vars["WSL_INTEROP"]
	if got != link && got != filepath.Join(dir, "1_interop") {
		t.Fatalf("env %v", e.vars)
	}
	// 2_interop (newest) is tried first and fails
	if e.probed[1] != filepath.Join(dir, "2_interop") {
		t.Errorf("order %v", e.probed)
	}
	if n := strings.Count(e.logs.String(), "wsl interop: dead not answering, using "+got); n != 1 {
		t.Errorf("logs %q", e.logs)
	}
}

func TestFixInteropNoneAnswers(t *testing.T) {
	glob := fakeSockets(t, map[string]time.Duration{"1_interop": 0})
	e := &interopEnv{vars: map[string]string{"WSL_DISTRO_NAME": "U", "WSL_INTEROP": "dead"}, live: map[string]bool{}}
	e.run(t, glob)
	if e.vars["WSL_INTEROP"] != "dead" || !strings.Contains(e.logs.String(), "not answering") {
		t.Errorf("env %v logs %q", e.vars, e.logs)
	}
}

func TestFixInteropNotWSL(t *testing.T) {
	glob := fakeSockets(t, map[string]time.Duration{"1_interop": 0})
	e := &interopEnv{vars: map[string]string{"WSL_INTEROP": "dead"}, live: map[string]bool{}}
	e.run(t, glob)
	if len(e.probed) != 0 || e.logs.Len() != 0 {
		t.Errorf("probed %v logs %q", e.probed, e.logs)
	}
}

func TestWinCommandTimesOut(t *testing.T) {
	old := winExecTimeout
	winExecTimeout = 200 * time.Millisecond
	defer func() { winExecTimeout = old }()
	cmd, cancel := winCommand("sleep", "30")
	defer cancel()
	start := time.Now()
	if err := cmd.Run(); err == nil {
		t.Fatal("expected an error")
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
}

func TestFixInteropUnsetAndCap(t *testing.T) {
	names := map[string]time.Duration{}
	for i := 0; i < 8; i++ {
		names[string(rune('a'+i))+"_interop"] = time.Duration(i) * time.Minute
	}
	glob := fakeSockets(t, names)
	e := &interopEnv{vars: map[string]string{"WSL_DISTRO_NAME": "U"}, live: map[string]bool{}}
	e.run(t, glob)
	if len(e.probed) != maxInteropTries {
		t.Errorf("probed %d sockets: %v", len(e.probed), e.probed)
	}
	if !strings.Contains(e.logs.String(), "(unset) not answering") || e.vars["WSL_INTEROP"] != "" {
		t.Errorf("env %v logs %q", e.vars, e.logs)
	}
}
