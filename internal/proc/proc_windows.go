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

// procRef is an open handle: holding it keeps the PID from being reused
// by another process before the kill.
type procRef = windows.Handle

// descendants opens the processes below pid, from a snapshot of the
// process table. Windows has no process group to kill, and taskkill /T
// finds nothing once the parent has exited, so the tree is noted first.
// A process counts as a child only if it started after its parent, since
// a recorded parent PID may belong to a process that exited long ago.
func descendants(pid int) []procRef {
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
	const access = windows.PROCESS_TERMINATE | windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.SYNCHRONIZE
	root, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(root)
	type node struct {
		pid     uint32
		created int64
	}
	var out []procRef
	seen := map[uint32]bool{uint32(pid): true}
	queue := []node{{uint32(pid), created(root)}}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, c := range parent[p.pid] {
			if seen[c] {
				continue
			}
			h, err := windows.OpenProcess(access, false, c)
			if err != nil {
				continue
			}
			t := created(h)
			if t == 0 || p.created == 0 || t < p.created {
				windows.CloseHandle(h)
				continue
			}
			seen[c] = true
			out = append(out, h)
			queue = append(queue, node{c, t})
		}
	}
	return out
}

// created is when the process started, in 100 ns ticks, or 0 if unknown.
func created(h windows.Handle) int64 {
	var c, x, k, u windows.Filetime
	if windows.GetProcessTimes(h, &c, &x, &k, &u) != nil {
		return 0
	}
	return c.Nanoseconds() / 100
}

// killPIDs terminates the processes and closes their handles.
func killPIDs(refs []procRef) {
	for _, h := range refs {
		_ = windows.TerminateProcess(h, 1)
		windows.CloseHandle(h)
	}
}

// treeGone reports whether every process noted before the interrupt has
// exited; a process handle is signalled once its process is gone.
func treeGone(_ *exec.Cmd, known []procRef) bool {
	for _, h := range known {
		if ev, err := windows.WaitForSingleObject(h, 0); err != nil || ev != windows.WAIT_OBJECT_0 {
			return false
		}
	}
	return true
}

// strays lists the processes whose executable file is inside dir, from a
// snapshot of the process table. Each is held by an open handle, so the PID
// cannot be reused before it is killed.
func strays(dir string) []Stray {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	const access = windows.PROCESS_TERMINATE | windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.SYNCHRONIZE
	var found []Stray
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err := windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if int(e.ProcessID) == os.Getpid() {
			continue
		}
		h, err := windows.OpenProcess(access, false, e.ProcessID)
		if err != nil {
			continue
		}
		buf := make([]uint16, 1024)
		n := uint32(len(buf))
		if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
			windows.CloseHandle(h)
			continue
		}
		exe := windows.UTF16ToString(buf[:n])
		if _, ok := below(dir, exe, true); !ok {
			windows.CloseHandle(h)
			continue
		}
		found = append(found, Stray{PID: int(e.ProcessID), Path: exe, ref: h})
	}
	return found
}

func releaseStrays(found []Stray) {
	for _, s := range found {
		windows.CloseHandle(s.ref)
	}
}

// killStrays terminates the strays and what they started. The children are
// noted first: once a parent is gone they cannot be found.
func killStrays(found []Stray) {
	for _, s := range found {
		killPIDs(descendants(s.PID))
		_ = windows.TerminateProcess(s.ref, 1)
		windows.CloseHandle(s.ref)
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
