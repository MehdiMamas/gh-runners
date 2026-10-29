//go:build windows

package proc

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func pidRunning(pid uint32) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if windows.GetExitCodeProcess(h, &code) != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

// A process whose parent already exited is still killed by Close.
func TestGroupKillsOrphan(t *testing.T) {
	g := NewGroup()
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		"(Start-Process ping -ArgumentList '-n','120','127.0.0.1' -WindowStyle Hidden -PassThru).Id")
	out, err := startAdd(g, cmd)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("pid %q: %v", out, err)
	}
	pid := uint32(n)
	defer func() {
		if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid); err == nil {
			windows.TerminateProcess(h, 1)
			windows.CloseHandle(h)
		}
	}()
	if !pidRunning(pid) {
		t.Fatal("ping should be running before Close")
	}
	g.Close()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if !pidRunning(pid) {
			return
		}
	}
	t.Fatal("ping still running after Close")
}

// startAdd starts cmd, adds it to g right away and returns its output.
func startAdd(g *Group, cmd *exec.Cmd) (string, error) {
	var buf strings.Builder
	cmd.Stdout = &buf
	if err := cmd.Start(); err != nil {
		return "", err
	}
	g.Add(cmd)
	err := cmd.Wait()
	return buf.String(), err
}
