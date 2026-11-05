package daemon

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/MehdiMamas/gh-runners/internal/store"
)

func deskDaemon(t *testing.T, check func(context.Context) (bool, error)) (*Daemon, *bytes.Buffer) {
	t.Helper()
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	var buf bytes.Buffer
	return &Daemon{Log: log.New(&buf, "", 0), inUseCheck: check}, &buf
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestDeskCheckInUseAndFree(t *testing.T) {
	on := true
	var failure error
	d, buf := deskDaemon(t, func(context.Context) (bool, error) { return on, failure })
	d.deskCheck(context.Background())
	d.deskCheck(context.Background())
	if !d.deskBusy.Load() || !fileExists(storeInUse()) {
		t.Fatal("exit 75 must mean in use with the in-use file written")
	}
	if n := strings.Count(buf.String(), "machine in use"); n != 1 {
		t.Fatalf("change logged %d times, want once:\n%s", n, buf)
	}
	on = false
	d.deskCheck(context.Background())
	if d.deskBusy.Load() || fileExists(storeInUse()) {
		t.Fatal("exit 0 must mean free with the file removed")
	}
	if n := strings.Count(buf.String(), "machine free"); n != 1 {
		t.Fatalf("free logged %d times, want once:\n%s", n, buf)
	}
	// An error counts as free, and is logged once per change.
	on = true
	d.deskCheck(context.Background())
	failure = errors.New("boom")
	d.deskCheck(context.Background())
	d.deskCheck(context.Background())
	if d.deskBusy.Load() || fileExists(storeInUse()) {
		t.Fatal("an error must count as free")
	}
	if n := strings.Count(buf.String(), "busy_check: boom"); n != 1 {
		t.Fatalf("error logged %d times, want once:\n%s", n, buf)
	}
}

func TestStatusShowsInUse(t *testing.T) {
	d, _ := deskDaemon(t, func(context.Context) (bool, error) { return true, nil })
	d.jit = newJITManager(d)
	d.children = map[string]*child{}
	d.deskCheck(context.Background())
	if st := d.status(); !st.InUse || st.InUseSince.IsZero() {
		t.Fatalf("status: %+v", st)
	}
	if !d.fleetInfo().InUse {
		t.Fatal("fleet info must carry InUse")
	}
}

func storeInUse() string { return store.InUsePath() }
