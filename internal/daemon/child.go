package daemon

import (
	"os/exec"

	"github.com/MehdiMamas/gh-runners/internal/control"
)

type child struct {
	cmd      *exec.Cmd
	done     chan struct{}
	dir      string
	stopping bool
	// healthKill marks a kill by the GitHub health check, so the exit is not
	// counted as a crash.
	healthKill bool
	// held marks a runner stopped on purpose because the host job limit
	// (max_jobs) is reached; it is kept in d.children and not started.
	held  bool
	state control.ProcState
}
