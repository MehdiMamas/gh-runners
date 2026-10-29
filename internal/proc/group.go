package proc

import (
	"log"
	"os/exec"
)

// Group holds every process a CI job starts. On Windows it is a job object
// that kills all of them when closed, or when this process dies; elsewhere it
// does nothing. All methods accept a nil Group.
type Group struct {
	g *group
}

// NewGroup creates a group. A failure is logged and gives a Group that does
// nothing, which is today's behaviour.
func NewGroup() *Group {
	g, err := newGroup()
	if err != nil {
		log.Printf("proc: job object: %v", err)
		return &Group{}
	}
	return &Group{g: g}
}

// Add puts a started command's process (and so its descendants) in the group.
func (g *Group) Add(cmd *exec.Cmd) {
	if g == nil || g.g == nil || cmd == nil || cmd.Process == nil {
		return
	}
	if err := g.g.add(cmd.Process.Pid); err != nil {
		log.Printf("proc: add pid %d to job object: %v", cmd.Process.Pid, err)
	}
}

// Close kills everything still in the group.
func (g *Group) Close() {
	if g == nil || g.g == nil {
		return
	}
	if err := g.g.close(); err != nil {
		log.Printf("proc: close job object: %v", err)
	}
	g.g = nil
}
