package service

// keepaliveTask is the name of the per-user scheduled task that restarts a
// dead daemon on Windows. A copy with its own state directory (home) gets a
// task of its own, so removing it never removes the main one's.
func keepaliveTask(home string) string {
	return Name + " keepalive" + keepaliveSuffix(home)
}

func keepaliveSuffix(home string) string {
	if s := SuffixFor(home); s != "" {
		return " (" + Name + s + ")"
	}
	return ""
}

// keepaliveArgs is the schtasks command line that creates the task: every 5
// minutes, current user, no elevation. conhost --headless keeps the console
// program from opening a window, as the WSL watchdog task does.
func keepaliveArgs(exe, home string) []string {
	tr := `conhost.exe --headless "` + exe + `"`
	if home != "" {
		tr += ` --home "` + home + `"`
	}
	tr += ` daemon --detach --keepalive`
	return []string{"/Create", "/F", "/SC", "MINUTE", "/MO", "5", "/TN", keepaliveTask(home), "/TR", tr}
}

// needKeepalive tells whether the task must be created: the login entry is
// set but the task is missing.
func needKeepalive(installed, exists bool) bool { return installed && !exists }

// EnsureKeepalive creates the keepalive task for an install that predates it
// (auto-update only restarts the service). It reports whether it created the
// task; on other OS it does nothing.
func EnsureKeepalive() (bool, error) { return ensureKeepalive() }
