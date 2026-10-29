package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/MehdiMamas/gh-runners/internal/localci"
)

func newIdleDaemon(t *testing.T) *Daemon {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	d := &Daemon{Version: "test"}
	d.jit = newJITManager(d)
	return d
}

func TestUpdateHeldWhileLocalCITaskRuns(t *testing.T) {
	d := newIdleDaemon(t)
	d.ciBusy = func() string { return "Local CI task 3 to finish" }
	if why := d.busy(context.Background()); !strings.Contains(why, "Local CI task 3") {
		t.Fatalf("busy = %q, want the running Local CI task", why)
	}
	waiting, err := d.applyUpdate(context.Background(), "v9.9.9")
	if err != nil || !waiting {
		t.Fatalf("applyUpdate = %v, %v; want waiting", waiting, err)
	}
	if n := d.upd.Note; !strings.Contains(n, "Local CI task 3") {
		t.Fatalf("update note = %q", n)
	}
}

func TestUpdateHeldWhileLocalCITaskPending(t *testing.T) {
	d := newIdleDaemon(t)
	d.ciBusy = func() string { return "Local CI task 7 to finish" }
	if why := d.busy(context.Background()); why == "" {
		t.Fatal("busy is empty while a task is queued")
	}
}

func TestUpdateProceedsWhenLocalCIQueueEmpty(t *testing.T) {
	d := newIdleDaemon(t)
	d.ciBusy = func() string { return "" }
	if why := d.busy(context.Background()); why != "" {
		t.Fatalf("busy = %q, want empty", why)
	}
}

func TestUpdateHeldWhileCIRunGoes(t *testing.T) {
	d := newIdleDaemon(t)
	if why := d.busy(context.Background()); why != "" {
		t.Fatalf("busy = %q, want empty", why)
	}
	_, done := localci.StartRunning(localci.RunningRun{Dir: t.TempDir()})
	if why := d.busy(context.Background()); !strings.Contains(why, "local CI run") {
		t.Fatalf("busy = %q, want the CI run", why)
	}
	done()
	if why := d.busy(context.Background()); why != "" {
		t.Fatalf("busy = %q after the run ended", why)
	}
}
