//go:build windows

package proc

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const createNoWindow = 0x08000000

func supervise(cmd *exec.Cmd) {
	// The child shares the daemon's console (hidden when the daemon was
	// detached), so no window opens and CTRL_BREAK can reach its group.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | createNoWindow,
		HideWindow:    true,
	}
}

func interrupt(cmd *exec.Cmd) {
	_ = windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid))
}

func kill(cmd *exec.Cmd) {
	// cmd.exe -> Runner.Listener -> Runner.Worker: kill the whole tree.
	k := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	k.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
	if err := k.Run(); err != nil {
		_ = cmd.Process.Kill()
	}
}

func env() []string {
	// The daemon's own environment is the one from when it started, and a
	// restart from the dashboard inherits it again; the registry has now.
	fresh, err := windows.GetCurrentProcessToken().Environ(false)
	if err != nil {
		return nil
	}
	return mergeEnv(os.Environ(), fresh)
}

// descendants lists the processes below pid, from a snapshot of the
// process table. Windows has no process group to kill, and taskkill /T
// finds nothing once the parent has exited, so the tree is noted first.
func descendants(pid int) []int {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	parent := map[uint32][]uint32{}
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err := windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		parent[e.ParentProcessID] = append(parent[e.ParentProcessID], e.ProcessID)
	}
	var out []int
	seen := map[uint32]bool{uint32(pid): true}
	queue := []uint32{uint32(pid)}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, c := range parent[p] {
			if !seen[c] {
				seen[c] = true
				out = append(out, int(c))
				queue = append(queue, c)
			}
		}
	}
	return out
}

func killPIDs(pids []int) {
	for _, pid := range pids {
		if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid)); err == nil {
			_ = windows.TerminateProcess(h, 1)
			windows.CloseHandle(h)
		}
	}
}

func alive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	return windows.GetExitCodeProcess(h, &code) == nil && code == 259 // STILL_ACTIVE
}
