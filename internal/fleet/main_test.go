package fleet

import (
	"os"
	"testing"
)

// TestMain points the state home at a temp dir for the whole package, so tests
// that reach the remote-log and history writers (for example through
// RemoteExecutor.RunJob) never write into the real ~/.gh-runners.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fleet-home-")
	if err != nil {
		panic(err)
	}
	os.Setenv("GH_RUNNERS_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
