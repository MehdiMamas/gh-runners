package localci

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// ToolNames are the tools a machine reports, in the order they are shown.
var ToolNames = []string{"bash", "node", "java", "flutter", "playwright"}

// toolProbes are the commands that tell whether a tool works.
var toolProbes = map[string][]string{
	"node":       {"node", "--version"},
	"java":       {"java", "-version"},
	"flutter":    {"flutter", "--version"},
	"playwright": {"npx", "--no-install", "playwright", "--version"},
}

// BashPath finds the bash that run: steps use. On Windows that is Git Bash
// (never the WSL launcher in System32); elsewhere it is bash on PATH.
func BashPath() (string, error) {
	if runtime.GOOS != "windows" {
		return exec.LookPath("bash")
	}
	var roots []string
	for _, k := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
		if v := os.Getenv(k); v != "" {
			roots = append(roots, filepath.Join(v, "Git"))
		}
	}
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		roots = append(roots, filepath.Join(v, "Programs", "Git"))
	}
	for _, r := range roots {
		for _, rel := range []string{filepath.Join("bin", "bash.exe"), filepath.Join("usr", "bin", "bash.exe")} {
			p := filepath.Join(r, rel)
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
	}
	p, err := exec.LookPath("bash")
	if err == nil && !strings.Contains(strings.ToLower(p), `\system32\`) {
		return p, nil
	}
	return "", exec.ErrNotFound
}

// DetectTools runs each tool to see which ones work on this machine.
func DetectTools() []string {
	found := map[string]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	mark := func(n string, ok bool) {
		mu.Lock()
		found[n] = ok
		mu.Unlock()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		p, err := BashPath()
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		mark("bash", exec.CommandContext(ctx, p, "--version").Run() == nil)
	}()
	for n, argv := range toolProbes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			mark(n, exec.CommandContext(ctx, argv[0], argv[1:]...).Run() == nil)
		}()
	}
	wg.Wait()
	var out []string
	for _, n := range ToolNames {
		if found[n] {
			out = append(out, n)
		}
	}
	return out
}

var (
	toolsMu   sync.Mutex
	toolsAt   time.Time
	toolsHave []string
)

// CachedTools is DetectTools, remembered for a few minutes so a daemon can
// report it often.
func CachedTools() []string {
	toolsMu.Lock()
	defer toolsMu.Unlock()
	if toolsHave == nil || time.Since(toolsAt) > 10*time.Minute {
		toolsHave, toolsAt = DetectTools(), time.Now()
		if toolsHave == nil {
			toolsHave = []string{}
		}
	}
	return append([]string{}, toolsHave...)
}

// Requires lists what a job needs from the machine it runs on.
func (j *Job) Requires() (tools []string, emulator bool) {
	tools = []string{"bash", "node"}
	for _, s := range j.Steps {
		if strings.HasPrefix(s.Uses, "subosito/flutter-action") {
			tools = append(tools, "flutter")
			break
		}
	}
	if j.NeedsJava() {
		tools = append(tools, "java")
		emulator = true
	}
	sort.Strings(tools)
	return tools, emulator
}

// Missing returns the needed tools a set does not have.
func Missing(have, need []string) []string {
	var out []string
	for _, n := range need {
		found := false
		for _, h := range have {
			found = found || h == n
		}
		if !found {
			out = append(out, n)
		}
	}
	return out
}
