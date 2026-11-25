package daemon

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/MehdiMamas/gh-runners/internal/ops"
)

func TestPruneDockerDaily(t *testing.T) {
	defer func(f func() bool) { ops.DockerAvailable = f }(ops.DockerAvailable)
	defer func(f func() (string, error)) { dockerPrune = f }(dockerPrune)
	avail, runs := false, 0
	ops.DockerAvailable = func() bool { return avail }
	dockerPrune = func() (string, error) {
		runs++
		return "Deleted Images:\nTotal reclaimed space: 1.5GB\n", nil
	}
	var buf bytes.Buffer
	d := &Daemon{Log: log.New(&buf, "", 0)}
	d.pruneDocker() // no Docker: nothing runs, the day is not spent
	if runs != 0 || !d.lastPrune.IsZero() {
		t.Fatal("pruned without Docker")
	}
	avail = true
	d.pruneDocker() // first pass
	d.pruneDocker() // same day
	if runs != 1 {
		t.Fatalf("ran %d times, want 1", runs)
	}
	if !strings.Contains(buf.String(), "freed 1.5GB") {
		t.Fatalf("log: %q", buf.String())
	}
	d.lastPrune = d.lastPrune.Add(-pruneEvery)
	d.pruneDocker()
	if runs != 2 {
		t.Fatalf("ran %d times after a day, want 2", runs)
	}
}
