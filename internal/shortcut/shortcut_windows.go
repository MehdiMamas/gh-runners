package shortcut

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func path() string {
	return filepath.Join(os.Getenv("APPDATA"), `Microsoft\Windows\Start Menu\Programs`, "gh-runners.lnk")
}

// psQuote makes s a PowerShell single-quoted string.
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func winArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func install(exe string, args []string) (string, error) {
	p := path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = winArg(a)
	}
	// WindowStyle 7 = minimized: the console window that opens the browser
	// closes a moment later without flashing over the desktop.
	script := fmt.Sprintf(`$s = (New-Object -ComObject WScript.Shell).CreateShortcut(%s); $s.TargetPath = %s; $s.Arguments = %s; $s.WorkingDirectory = %s; $s.WindowStyle = 7; $s.Description = 'Manage GitHub Actions runners on this computer'; $s.Save()`,
		psQuote(p), psQuote(exe), psQuote(strings.Join(quoted, " ")), psQuote(filepath.Dir(exe)))
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("create %s: %v: %s", p, err, strings.TrimSpace(string(out)))
	}
	return p, nil
}

func uninstall() (string, error) {
	p := path()
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return p, nil
}
