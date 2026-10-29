package localci

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/proc"
)

// Acquire waits for the named lock in arrival order and returns the function
// that releases it. Every waiter writes a ticket file into <Dir>/<name>.queue
// and only tries the file lock when its ticket is the oldest live one. The
// file lock stays the real mutual exclusion. onWait, if not nil, is called
// once when the caller has to wait. When ctx ends, the ticket is removed and
// ctx.Err() is returned.
//
// The first ticket can still find the lock held by an old binary or by a
// holder that predates the queue. It keeps polling, which is correct.
func (l *Locks) Acquire(ctx context.Context, name string, onWait func()) (func(), error) {
	qdir := filepath.Join(l.Dir, name+".queue")
	if err := os.MkdirAll(qdir, 0o755); err != nil {
		return nil, err
	}
	pid := os.Getpid()
	n := time.Now().UnixNano()
	var ticket string
	for {
		ticket = fmt.Sprintf("%020d-%d", n, pid)
		f, err := os.OpenFile(filepath.Join(qdir, ticket), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			f.Close()
			break
		}
		if !os.IsExist(err) {
			return nil, err
		}
		n++
	}
	remove := func() { os.Remove(filepath.Join(qdir, ticket)) }
	poll := l.Poll
	if poll <= 0 {
		poll = 250 * time.Millisecond
	}
	waited := false
	for {
		if first, err := l.firstTicket(qdir, ticket, pid); err != nil {
			remove()
			return nil, err
		} else if first {
			rel, ok, err := l.TryLock(name)
			if err != nil {
				remove()
				return nil, err
			}
			if ok {
				remove()
				return rel, nil
			}
		}
		if !waited && onWait != nil {
			onWait()
		}
		waited = true
		select {
		case <-ctx.Done():
			remove()
			return nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// firstTicket removes the tickets of dead processes and reports whether
// ticket is the oldest one left.
func (l *Locks) firstTicket(qdir, ticket string, self int) (bool, error) {
	ents, err := os.ReadDir(qdir)
	if err != nil {
		return false, err
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, nm := range names {
		if nm == ticket {
			return true, nil
		}
		i := strings.LastIndex(nm, "-")
		if i < 0 {
			continue
		}
		pid, err := strconv.Atoi(nm[i+1:])
		if err != nil {
			continue
		}
		if pid != self && !proc.Alive(pid) {
			os.Remove(filepath.Join(qdir, nm))
			continue
		}
		return false, nil
	}
	return false, nil
}
