package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/store"
)

// postPause stops everything gh-runners runs on this machine until Resume.
// A job running now would be cancelled, so like a restart it first answers
// 409 with what is running; confirm_busy=1 goes on.
func (s *Server) postPause(w http.ResponseWriter, r *http.Request) {
	if s.h.Pause == nil {
		fail(w, http.StatusServiceUnavailable, errors.New("the daemon is not running"))
		return
	}
	if r.URL.Query().Get("confirm_busy") != "1" {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		busy, err := s.busyHere(ctx)
		cancel()
		switch {
		case err != nil:
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":   "could not check with GitHub whether a job is running here (" + err.Error() + ")",
				"confirm": true,
			})
			return
		case len(busy) > 0:
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "running a job now: " + strings.Join(busy, ", "), "busy": busy, "confirm": true,
			})
			return
		}
	}
	if err := s.h.Pause(); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"paused": true})
}

func (s *Server) postResume(w http.ResponseWriter, r *http.Request) {
	if s.h.Resume == nil {
		fail(w, http.StatusServiceUnavailable, errors.New("the daemon is not running"))
		return
	}
	if err := s.h.Resume(); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"paused": false})
}

// busyHere lists the runners of this machine that run a job now, as
// "name (scope)": GitHub says which, and on-demand runners say themselves.
// Nothing to ask when the machine is paused already or nothing runs.
func (s *Server) busyHere(ctx context.Context) ([]string, error) {
	st := s.h.Status()
	if st.Paused {
		return nil, nil
	}
	var busy []string
	for _, p := range st.Runners {
		if p.State == "jit-busy" {
			busy = append(busy, p.ID)
		}
	}
	running := false
	for _, p := range st.Runners {
		if p.PID != 0 && !strings.HasPrefix(p.State, "jit") {
			running = true
		}
	}
	if !running {
		return busy, nil
	}
	states, err := s.remoteStates(ctx, true)
	if err != nil {
		return nil, err
	}
	runners, _ := store.List()
	for _, r := range runners {
		if !r.Disabled && states[r.ID] == "busy" {
			busy = append(busy, r.Name+" ("+r.Scope()+")")
		}
	}
	return busy, nil
}
