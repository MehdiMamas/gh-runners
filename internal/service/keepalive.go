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

// keepaliveCommand is the program and arguments the task runs. conhost
// --headless keeps the console program from opening a window, as the WSL
// watchdog task does.
func keepaliveCommand(exe, home string) (command, args string) {
	args = `--headless "` + exe + `"`
	if home != "" {
		args += ` --home "` + home + `"`
	}
	return "conhost.exe", args + ` daemon --detach --keepalive`
}

// keepaliveArgs is the plain schtasks command line (every 5 minutes, current
// user, no elevation) used when the S4U task cannot be created. It runs only
// while the user is signed in.
func keepaliveArgs(exe, home string) []string {
	c, a := keepaliveCommand(exe, home)
	return []string{"/Create", "/F", "/SC", "MINUTE", "/MO", "5", "/TN", keepaliveTask(home), "/TR", c + " " + a}
}

// keepaliveXML is the S4U task definition for the keepalive task.
func keepaliveXML(exe, home, user string) string {
	c, a := keepaliveCommand(exe, home)
	return taskXML(keepaliveTask(home), user, c, a, "PT72H")
}

type keepaliveStep int

const (
	planNothing keepaliveStep = iota
	planCreate                // login entry set, task missing: create it
	planMigrate               // task logs on interactively: recreate as S4U
)

// keepalivePlan decides what the daemon start does. queryXML and queryErr are
// the result of `schtasks /Query /XML`, only read when the task exists.
func keepalivePlan(installed, exists bool, queryXML string, queryErr error) keepaliveStep {
	switch {
	case !installed:
		return planNothing
	case !exists:
		return planCreate
	case needS4U(queryXML, queryErr):
		return planMigrate
	}
	return planNothing
}

// EnsureKeepalive creates the keepalive task for an install that predates it
// (auto-update only restarts the service). It reports whether it created the
// task; on other OS it does nothing.
func EnsureKeepalive() (bool, error) { return ensureKeepalive() }
