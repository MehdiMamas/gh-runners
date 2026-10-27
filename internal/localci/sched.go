package localci

import (
	"context"
	"os"
	"path/filepath"
	"sync"
)

// Locks hands out named machine-wide locks: a mutex for jobs in this
// process and a file lock for other gh-runners processes on the same
// machine.
type Locks struct {
	Dir string // where the lock files live

	mu sync.Mutex
	in map[string]*sync.Mutex
}

// Lock waits for the named lock and returns the function that releases it.
func (l *Locks) Lock(name string) (func(), error) {
	l.mu.Lock()
	if l.in == nil {
		l.in = map[string]*sync.Mutex{}
	}
	m := l.in[name]
	if m == nil {
		m = &sync.Mutex{}
		l.in[name] = m
	}
	l.mu.Unlock()
	m.Lock()
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		m.Unlock()
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(l.Dir, name+".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		m.Unlock()
		return nil, err
	}
	if err := flock(f); err != nil {
		f.Close()
		m.Unlock()
		return nil, err
	}
	return func() {
		funlock(f)
		f.Close()
		m.Unlock()
	}, nil
}

// Task is one schedulable unit.
type Task struct {
	Name  string
	Needs []string
	Lock  string // named lock held while it runs; empty for none
}

// Schedule runs the tasks, at most conc at a time, each after all its needs
// have finished. run gets the results of the task's needs. A need that is
// not in tasks counts as already finished. Tasks that share a Lock never
// overlap.
func Schedule(ctx context.Context, tasks []Task, conc int, locks *Locks, run func(ctx context.Context, t Task, needs map[string]Result) Result) map[string]Result {
	if conc < 1 {
		conc = 1
	}
	results := map[string]Result{}
	done := map[string]chan struct{}{}
	for _, t := range tasks {
		done[t.Name] = make(chan struct{})
	}
	var mu sync.Mutex
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for _, t := range tasks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(done[t.Name])
			for _, n := range t.Needs {
				if ch, ok := done[n]; ok {
					select {
					case <-ch:
					case <-ctx.Done():
					}
				}
			}
			mu.Lock()
			needs := map[string]Result{}
			for _, n := range t.Needs {
				if r, ok := results[n]; ok {
					needs[n] = r
				}
			}
			mu.Unlock()
			var r Result
			if ctx.Err() != nil {
				r = Result{Job: t.Name, Status: Skipped, Reason: "cancelled"}
			} else {
				// Take the lock before a slot, so a job waiting on the
				// emulator does not hold a slot another job could use.
				release := func() {}
				if t.Lock != "" {
					rel, err := locks.Lock(t.Lock)
					if err != nil {
						r = Result{Job: t.Name, Status: Fail, Reason: "lock: " + err.Error()}
					} else {
						release = rel
					}
				}
				if r.Job == "" {
					sem <- struct{}{}
					r = run(ctx, t, needs)
					<-sem
				}
				release()
			}
			mu.Lock()
			results[t.Name] = r
			mu.Unlock()
		}()
	}
	wg.Wait()
	return results
}
