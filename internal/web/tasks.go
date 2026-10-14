package web

import (
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
	State   string    `json:"state"` // queued | running | done | failed
	Error   string    `json:"error,omitempty"`
	Created time.Time `json:"created"`
	Ended   time.Time `json:"ended,omitzero"`
	out     []byte
	run     func(w io.Writer) error
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
	ID      int    `json:"id"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Error   string `json:"error,omitempty"`
	Created string `json:"created"`
	Ended   string `json:"ended,omitempty"`
	Log     string `json:"log"`
}

func (t *task) view(full bool) taskView {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.out
	if !full && len(out) > 4096 {
		out = out[len(out)-4096:]
	}
	return taskView{t.ID, t.Title, t.State, t.Error, since(t.Created), since(t.Ended), string(out)}
}

type taskQueue struct {
	log   *log.Logger
	mu    sync.Mutex
	next  int
	list  []*task
	queue chan *task
}

func newTaskQueue(l *log.Logger) *taskQueue {
	q := &taskQueue{log: l, queue: make(chan *task, 64)}
	go q.work()
	return q
}

func (q *taskQueue) work() {
	for t := range q.queue {
		t.mu.Lock()
		t.State = "running"
		t.mu.Unlock()
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
		if err != nil {
			t.State, t.Error = "failed", err.Error()
			q.log.Printf("dashboard: %s failed: %v", t.Title, err)
		}
		t.mu.Unlock()
	}
}

// add queues a task and returns its id.
func (q *taskQueue) add(title string, run func(w io.Writer) error) (*task, error) {
	q.mu.Lock()
	q.next++
	t := &task{ID: q.next, Title: title, State: "queued", Created: time.Now(), run: run}
	q.list = append(q.list, t)
	if len(q.list) > 50 {
		q.list = q.list[len(q.list)-50:]
	}
	q.mu.Unlock()
	select {
	case q.queue <- t:
		return t, nil
	default:
		return nil, fmt.Errorf("too many tasks waiting; try again in a minute")
	}
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
	t, err := s.tasks.add(title, run)
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
