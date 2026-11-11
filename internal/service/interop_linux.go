package service

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	maxInteropTries     = 5
	interopProbeTimeout = 5 * time.Second
	interopSockGlob     = "/run/WSL/*_interop"
)

var interopMu sync.Mutex

// winExecTimeout bounds every Windows program the watchdog runs.
var winExecTimeout = 15 * time.Second

// FixInterop makes sure WSL_INTEROP points at a socket that answers. The
// systemd user manager can hand the daemon a socket from an earlier WSL
// session; cmd.exe then hangs. Under WSL it probes the current one and, when
// it is dead, switches to the newest working /run/WSL/*_interop socket.
func FixInterop() {
	fixInterop(os.Getenv, os.Setenv, interopSockGlob, probeInterop)
}

// probeInterop runs cmd.exe against sock under a short timeout.
func probeInterop(sock string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), interopProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "cmd.exe", "/c", "exit 0")
	cmd.Dir = "/mnt/c"
	cmd.Env = append(os.Environ(), "WSL_INTEROP="+sock)
	cmd.WaitDelay = time.Second
	return cmd.Run() == nil
}

// fixInterop is FixInterop with its inputs injected. It returns the socket in
// use after the call ("" when not under WSL).
func fixInterop(getenv func(string) string, setenv func(k, v string) error, glob string, probe func(sock string) bool) string {
	interopMu.Lock()
	defer interopMu.Unlock()
	if getenv("WSL_DISTRO_NAME") == "" {
		return ""
	}
	cur := getenv("WSL_INTEROP")
	if cur != "" && probe(cur) {
		return cur
	}
	socks := interopSockets(glob)
	if len(socks) > maxInteropTries {
		socks = socks[:maxInteropTries]
	}
	shown := cur
	if shown == "" {
		shown = "(unset)"
	}
	for _, s := range socks {
		if s == cur || !probe(s) {
			continue
		}
		if err := setenv("WSL_INTEROP", s); err != nil {
			log.Printf("wsl interop: %v", err)
			return cur
		}
		log.Printf("wsl interop: %s not answering, using %s", shown, s)
		return s
	}
	log.Printf("wsl interop: %s not answering and no other socket does", shown)
	return cur
}

// interopSockets lists the sockets matching glob, newest first.
func interopSockets(glob string) []string {
	names, _ := filepath.Glob(glob)
	type ent struct {
		path string
		mod  time.Time
	}
	var es []ent
	for _, n := range names {
		fi, err := os.Stat(n) // follows symlinks
		if err != nil {
			continue
		}
		es = append(es, ent{n, fi.ModTime()})
	}
	sort.SliceStable(es, func(i, j int) bool { return es[i].mod.After(es[j].mod) })
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.path
	}
	return out
}

// winCommand is exec.CommandContext for a Windows program with the shared
// timeout; the caller must call cancel.
func winCommand(name string, args ...string) (*exec.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), winExecTimeout)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	return cmd, cancel
}

// runWin is run with the Windows timeout.
func runWin(name string, args ...string) error {
	cmd, cancel := winCommand(name, args...)
	defer cancel()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmtRunErr(name, args, err, out)
	}
	return nil
}
