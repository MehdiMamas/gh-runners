package localci

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/proc"
)

func queueLen(dir string) int {
	e, _ := os.ReadDir(filepath.Join(dir, "emulator.queue"))
	return len(e)
}

func TestLockServesWaitersInOrder(t *testing.T) {
	for i := 0; i < 20; i++ {
		dir := t.TempDir()
		mk := func() *Locks { return &Locks{Dir: dir, Poll: 10 * time.Millisecond} }
		rel, err := mk().Lock("emulator")
		if err != nil {
			t.Fatal(err)
		}
		order := make(chan string, 2)
		start := func(who string) {
			go func() {
				r, err := mk().Acquire(context.Background(), "emulator", nil)
				if err != nil {
					order <- "err"
					return
				}
				order <- who
				time.Sleep(50 * time.Millisecond)
				r()
			}()
		}
		start("A")
		for queueLen(dir) < 1 {
			time.Sleep(time.Millisecond)
		}
		start("B")
		for queueLen(dir) < 2 {
			time.Sleep(time.Millisecond)
		}
		rel()
		if a, b := <-order, <-order; a != "A" || b != "B" {
			t.Fatalf("round %d: order %s, %s", i, a, b)
		}
	}
}

func TestLockSkipsDeadTicket(t *testing.T) {
	dir := t.TempDir()
	pid := 0x7ffffff0
	for proc.Alive(pid) {
		pid--
	}
	q := filepath.Join(dir, "emulator.queue")
	os.MkdirAll(q, 0o755)
	dead := filepath.Join(q, fmt.Sprintf("%020d-%d", 1, pid))
	os.WriteFile(dead, nil, 0o644)
	l := &Locks{Dir: dir, Poll: 10 * time.Millisecond}
	rel, err := l.Acquire(context.Background(), "emulator", nil)
	if err != nil {
		t.Fatal(err)
	}
	rel()
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatalf("dead ticket still there: %v", err)
	}
}

func TestAcquireTimeoutRemovesTicket(t *testing.T) {
	dir := t.TempDir()
	h := &Locks{Dir: dir, Poll: 10 * time.Millisecond}
	rel, err := h.Lock("emulator")
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	w := &Locks{Dir: dir, Poll: 10 * time.Millisecond}
	if _, err := w.Acquire(ctx, "emulator", nil); err != context.DeadlineExceeded {
		t.Fatalf("err = %v", err)
	}
	if n := queueLen(dir); n != 0 {
		t.Fatalf("%d tickets left", n)
	}
}
