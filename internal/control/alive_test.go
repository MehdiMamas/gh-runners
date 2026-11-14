package control

import (
	"net"
	"os"
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/store"
)

// A daemon that holds its port but does not answer /status still counts as
// running; one whose port is closed does not.
func TestAliveBusyDaemon(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	old := client.Timeout
	client.Timeout = 200 * time.Millisecond
	defer func() { client.Timeout = old }()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	info := store.DaemonInfo{PID: os.Getppid(), Addr: ln.Addr().String(), Secret: "s"}
	if err := store.SaveDaemonInfo(info); err != nil {
		t.Fatal(err)
	}
	if pid, ok := Alive(); !ok || pid != info.PID {
		t.Fatalf("busy daemon: got %d %v, want %d true", pid, ok, info.PID)
	}
	ln.Close()
	if _, ok := Alive(); ok {
		t.Fatal("closed port: still counted as running")
	}
}
