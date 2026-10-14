//go:build windows

package ops

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

// windowsService is the service config.cmd --runasservice installed for
// the runner in dir; its name is kept in dir/.service.
func windowsService(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, ".service"))
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(strings.TrimPrefix(string(b), "\xef\xbb\xbf"))
	if name == "" {
		return ""
	}
	cmd := exec.Command("sc.exe", "query", name)
	hideWindow(cmd)
	if cmd.Run() != nil {
		return "" // not installed any more
	}
	return name
}

func foreignServices(dir string) []foreignService {
	if name := windowsService(dir); name != "" {
		return []foreignService{{Name: "Windows service " + name, NeedsAdmin: true}}
	}
	return nil
}

// stopForeign stops and deletes the runner's Windows service. That needs
// administrator rights, so when the plain attempt is refused Windows asks
// for them (a UAC prompt on this screen).
func stopForeign(dir string, log io.Writer) error {
	name := windowsService(dir)
	if name == "" {
		return nil
	}
	fmt.Fprintf(log, "Removing the Windows service %s ...\n", name)
	sc := func(args ...string) (string, error) {
		cmd := exec.Command("sc.exe", args...)
		hideWindow(cmd)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	// "FAILED 5:" is access denied; any other stop failure (1062: not
	// started) still lets the delete go ahead.
	out, _ := sc("stop", name)
	if !strings.Contains(out, "FAILED 5:") {
		for i := 0; i < 30; i++ {
			if q, _ := sc("query", name); strings.Contains(q, "STOPPED") {
				break
			}
			time.Sleep(time.Second)
		}
		out, err := sc("delete", name)
		if err == nil {
			return nil
		}
		if !strings.Contains(out, "FAILED 5:") {
			return fmt.Errorf("could not delete the service %s: %s", name, strings.TrimSpace(out))
		}
	}
	fmt.Fprintln(log, "That needs administrator rights: confirm the Windows prompt on this screen.")
	script := fmt.Sprintf(`$p = Start-Process -FilePath cmd.exe -ArgumentList '/c sc.exe stop "%[1]s" & timeout /t 5 /nobreak >nul & sc.exe delete "%[1]s"' -Verb RunAs -WindowStyle Hidden -Wait -PassThru; exit $p.ExitCode`, name)
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	hideWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		if windowsService(dir) != "" {
			return fmt.Errorf("the service %s is still installed (the administrator prompt was declined?): %s", name, strings.TrimSpace(string(out)))
		}
	}
	if windowsService(dir) != "" {
		return fmt.Errorf("the service %s is still installed", name)
	}
	return nil
}

// procsIn lists processes whose program file is inside dir.
func procsIn(dir string) []int {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	prefix := strings.ToLower(filepath.Clean(dir)) + `\`
	self := uint32(os.Getpid())
	var out []int
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ProcessID == 0 || e.ProcessID == self {
			continue
		}
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, e.ProcessID)
		if err != nil {
			continue
		}
		buf := make([]uint16, windows.MAX_LONG_PATH)
		n := uint32(len(buf))
		err = windows.QueryFullProcessImageName(h, 0, &buf[0], &n)
		windows.CloseHandle(h)
		if err == nil && strings.HasPrefix(strings.ToLower(windows.UTF16ToString(buf[:n])), prefix) {
			out = append(out, int(e.ProcessID))
		}
	}
	return out
}

// terminate ends a process; Windows has no polite signal for a console
// program in another session, so both calls kill.
func terminate(pid int, _ bool) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	_ = windows.TerminateProcess(h, 1)
	windows.CloseHandle(h)
}
