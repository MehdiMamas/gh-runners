package web

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// A task is a long operation (add, remove, sync, move) started from the
// page. Tasks run one at a time, so two adds never race on the download
// cache or the daemon; the page polls their output.
type task struct {
	mu      sync.Mutex
	ID      int       `json:"id"`
	Title   string    `json:"title"`
	State   string    `json:"state"` // queued | running | done | failed | cancelled
	Error   string    `json:"error,omitempty"`
	Created time.Time `json:"created"`
	Ended   time.Time `json:"ended,omitzero"`
	Scopes  []string  `json:"scopes,omitempty"` // repos it adds to or changes
	out     []byte
	// cancel stops a running task; cancelled is set when someone asked for
	// it, so the task ends as cancelled, not failed.
	cancel    context.CancelFunc
	cancelled bool
	run       func(w io.Writer) error
}

const maxTaskLog = 256 << 10

func (t *task) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.out = append(t.out, p...)
	if len(t.out) > maxTaskLog {
		t.out = append([]byte("[...]\n"), t.out[len(t.out)-maxTaskLog/2:]...)
	}
	return len(p), nil
}

type taskView struct {
	ID      int      `json:"id"`
	Title   string   `json:"title"`
	State   string   `json:"state"`
	Error   string   `json:"error,omitempty"`
	Created string   `json:"created"`
	Ended   string   `json:"ended,omitempty"`
	Scopes  []string `json:"scopes,omitempty"`
	Log     string   `json:"log"`
}

func (t *task) view(full bool) taskView {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.out
	if !full && len(out) > 4096 {
		out = out[len(out)-4096:]
	}
	return taskView{t.ID, t.Title, t.State, t.Error, since(t.Created), since(t.Ended), t.Scopes, string(out)}
}

type taskQueue struct {
	log  *log.Logger
	mu   sync.Mutex
	next int
	list []*task
	// pending are the queued tasks in the order they run; wake tells the
	// worker one was added.
	pending []*task
	wake    chan struct{}
}

const maxPending = 64

func newTaskQueue(l *log.Logger) *taskQueue {
	q := &taskQueue{log: l, wake: make(chan struct{}, 1)}
	go q.work()
	return q
}

func (q *taskQueue) work() {
	for {
		t := q.pop()
		q.log.Printf("dashboard: %s", t.Title)
		err := func() (err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("internal error: %v", r)
				}
			}()
			return t.run(t)
		}()
		t.mu.Lock()
		t.State, t.Ended = "done", time.Now()
		if t.cancelled {
			t.State = "cancelled"
			q.log.Printf("dashboard: %s cancelled", t.Title)
		} else if err != nil {
			t.State, t.Error = "failed", err.Error()
			q.log.Printf("dashboard: %s failed: %v", t.Title, err)
		}
		t.mu.Unlock()
	}
}

// pop waits for the first queued task and marks it running.
func (q *taskQueue) pop() *task {
	for {
		q.mu.Lock()
		if len(q.pending) > 0 {
			t := q.pending[0]
			q.pending = q.pending[1:]
			t.mu.Lock()
			t.State = "running"
			t.mu.Unlock()
			q.mu.Unlock()
			return t
		}
		q.mu.Unlock()
		<-q.wake
	}
}

// moveFront moves a queued task to the front. It returns false when the task is
// not queued.
func (q *taskQueue) moveFront(t *task) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, p := range q.pending {
		if p == t {
			copy(q.pending[1:i+1], q.pending[:i])
			q.pending[0] = t
			return true
		}
	}
	return false
}

// stop cancels a task: a queued one is taken out of the queue, a running one
// has its context cancelled. It returns false when the task has ended.
func (q *taskQueue) stop(t *task) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	switch t.State {
	case "queued":
		for i, p := range q.pending {
			if p == t {
				q.pending = append(q.pending[:i:i], q.pending[i+1:]...)
				break
			}
		}
		t.State, t.cancelled, t.Ended = "cancelled", true, time.Now()
	case "running":
		t.cancelled = true
		if t.cancel != nil {
			t.cancel()
		}
	default:
		return false
	}
	return true
}

// setCancel lets a running task register how to stop it. It reports whether
// the task was cancelled already.
func (t *task) setCancel(c context.CancelFunc) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cancel = c
	return t.cancelled
}

// add queues a task and returns its id.
func (q *taskQueue) add(title string, scopes []string, run func(w io.Writer) error) (*task, error) {
	q.mu.Lock()
	q.next++
	t := &task{ID: q.next, Title: title, State: "queued", Created: time.Now(), Scopes: scopes, run: run}
	if len(q.pending) >= maxPending {
		q.mu.Unlock()
		return nil, fmt.Errorf("too many tasks waiting; try again in a minute")
	}
	q.list = append(q.list, t)
	if len(q.list) > 50 {
		q.list = q.list[len(q.list)-50:]
	}
	q.pending = append(q.pending, t)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return t, nil
}

func (q *taskQueue) get(id int) *task {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, t := range q.list {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (q *taskQueue) views() []taskView {
	q.mu.Lock()
	list := append([]*task(nil), q.list...)
	q.mu.Unlock()
	out := make([]taskView, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		out = append(out, list[i].view(false))
	}
	return out
}

func (q *taskQueue) busy() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, t := range q.list {
		t.mu.Lock()
		if t.State == "queued" || t.State == "running" {
			n++
		}
		t.mu.Unlock()
	}
	return n
}

func (s *Server) startTask(w http.ResponseWriter, title string, run func(w io.Writer) error) {
	s.startScopedTask(w, title, nil, run)
}

// startScopedTask queues a task that works on these repositories, so the
// page can show them as busy until it is done.
func (s *Server) startScopedTask(w http.ResponseWriter, title string, scopes []string, run func(w io.Writer) error) {
	t, err := s.tasks.add(title, scopes, run)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"task": t.ID})
}

func (s *Server) getTasks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.tasks.views())
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	t := s.tasks.get(id)
	if t == nil {
		fail(w, http.StatusNotFound, fmt.Errorf("no task %d", id))
		return
	}
	writeJSON(w, http.StatusOK, t.view(true))
}
