package localci

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultEmulatorPorts are the Firebase emulator defaults (plus the hub and
// logging ports), used when no firebase.json names any.
var DefaultEmulatorPorts = []int{4400, 4500, 8080, 8085, 8180, 9000, 9099, 9150, 9199, 5001}

// PortWait is how long a taken port is retried before the job is blocked;
// PortPoll is the pause between tries. Variables so tests can shorten them.
var (
	PortWait = 60 * time.Second
	PortPoll = 2 * time.Second
)

// EmulatorPorts reads every firebase.json / firebase*.json under root
// (skipping node_modules) and returns the ports of its emulators section.
// With none found it returns the defaults.
func EmulatorPorts(root string) []int {
	seen := map[int]bool{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		n := d.Name()
		if !strings.HasPrefix(n, "firebase") || !strings.HasSuffix(n, ".json") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, port := range ParseEmulatorPorts(b) {
			seen[port] = true
		}
		return nil
	})
	if len(seen) == 0 {
		return append([]int{}, DefaultEmulatorPorts...)
	}
	out := make([]int, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// ParseEmulatorPorts returns the ports in one firebase.json's emulators
// section: every <name>.port, including hub. Bad JSON gives nothing.
func ParseEmulatorPorts(data []byte) []int {
	var cfg struct {
		Emulators map[string]json.RawMessage `json:"emulators"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return nil
	}
	var out []int
	for _, raw := range cfg.Emulators {
		var e struct {
			Port int `json:"port"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Port > 0 && e.Port < 65536 {
			out = append(out, e.Port)
		}
	}
	sort.Ints(out)
	return out
}

// portFree reports whether something can listen on 127.0.0.1:port.
func portFree(port int) bool {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// CheckPorts waits up to PortWait for every port to be free. It returns
// "" when they are, or a message naming the first port still taken and, when
// found, its process and pid.
func CheckPorts(ctx context.Context, ports []int) string {
	deadline := time.Now().Add(PortWait)
	for {
		taken := 0
		for _, p := range ports {
			if !portFree(p) {
				taken = p
				break
			}
		}
		if taken == 0 {
			return ""
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			who := ""
			if name, pid := portOwner(taken); pid != "" {
				who = " by " + name + " (pid " + pid + ")"
				if name == "" {
					who = " by pid " + pid
				}
			}
			return fmt.Sprintf("port %d is used%s, not started by gh-runners", taken, who)
		}
		select {
		case <-ctx.Done():
		case <-time.After(PortPoll):
		}
	}
}

// portOwner finds the process listening on port; empty pid when unknown.
func portOwner(port int) (name, pid string) {
	if runtime.GOOS == "windows" {
		out, err := exec.Command("netstat", "-ano").Output()
		if err != nil {
			return "", ""
		}
		return parseNetstat(string(out), port)
	}
	out, err := exec.Command("lsof", "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-Fpc").Output()
	if err != nil {
		return "", ""
	}
	return parseLsof(string(out))
}

// parseLsof reads `lsof -Fpc` output: p<pid> and c<command> lines.
func parseLsof(out string) (name, pid string) {
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "p") && pid == "":
			pid = l[1:]
		case strings.HasPrefix(l, "c") && name == "":
			name = l[1:]
		}
	}
	return name, pid
}

// parseNetstat finds the LISTENING row for port in `netstat -ano` output.
// The process name is looked up with tasklist, quietly empty on failure.
func parseNetstat(out string, port int) (name, pid string) {
	re := regexp.MustCompile(`:` + strconv.Itoa(port) + `$`)
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) >= 5 && strings.EqualFold(f[3], "LISTENING") && re.MatchString(f[1]) {
			pid = f[4]
			break
		}
	}
	if pid == "" {
		return "", ""
	}
	if b, err := exec.Command("tasklist", "/FI", "PID eq "+pid, "/FO", "CSV", "/NH").Output(); err == nil {
		if f := strings.Split(strings.TrimSpace(string(b)), ","); len(f) > 0 {
			name = strings.Trim(f[0], `"`)
		}
	}
	return name, pid
}
