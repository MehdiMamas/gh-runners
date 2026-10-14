package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/fleet"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// FleetAPI is the dashboard API as the main host may call it on this
// machine: only the calls fleet.MemberAllowed lists, without the browser
// checks (the fleet connection is authenticated instead).
func (s *Server) FleetAPI() http.Handler {
	mux := s.routes()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !fleet.MemberAllowed(r.Method, r.URL.RequestURI()) {
			fail(w, http.StatusForbidden, errors.New("not allowed from the main host"))
			return
		}
		mux.ServeHTTP(w, r)
	})
}

type machinesView struct {
	MainHost  bool                `json:"main_host"`
	Listening bool                `json:"listening"`
	Port      int                 `json:"port"`
	Error     string              `json:"error,omitempty"`
	Machines  []fleet.Machine     `json:"machines"`
	Member    *fleet.MemberStatus `json:"member,omitempty"`
}

func (s *Server) getMachines(w http.ResponseWriter, r *http.Request) {
	cfg, _ := store.LoadConfig()
	v := machinesView{MainHost: cfg.MainHost, Machines: []fleet.Machine{}}
	if h := s.h.Host; h != nil {
		v.Listening, v.Port = h.Running()
		if err := h.StartErr(); err != nil {
			v.Error = err.Error()
		}
		if v.Port == 0 {
			v.Port = fleetPort(cfg)
		}
		ms, err := h.Machines()
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		v.Machines = ms
	}
	if m := s.h.Member; m != nil {
		st := m.Status()
		v.Member = &st
	}
	writeJSON(w, http.StatusOK, v)
}

func fleetPort(cfg store.Config) int {
	if cfg.FleetPort > 0 {
		return cfg.FleetPort
	}
	return fleet.DefaultPort
}

// StartHost starts listening for machines when this is the main host.
func StartHost(h *fleet.Host) error {
	cfg, err := store.LoadConfig()
	if err != nil || !cfg.MainHost {
		return err
	}
	return h.Start(":" + strconv.Itoa(fleetPort(cfg)))
}

func (s *Server) postMainHost(w http.ResponseWriter, r *http.Request) {
	var body struct {
		On bool `json:"on"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if s.h.Host == nil {
		fail(w, http.StatusServiceUnavailable, errors.New("the daemon is not running"))
		return
	}
	if body.On && s.h.Member != nil && s.h.Member.Joined() {
		fail(w, http.StatusConflict, fmt.Errorf("this machine belongs to the main host %s; leave it first", s.h.Member.Status().HostName))
		return
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	cfg.MainHost = body.On
	if err := store.SaveConfig(cfg); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	if body.On {
		if err := StartHost(s.h.Host); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
	} else {
		s.h.Host.Stop()
	}
	s.getMachines(w, r)
}

func (s *Server) postInvite(w http.ResponseWriter, r *http.Request) {
	if s.h.Host == nil {
		fail(w, http.StatusServiceUnavailable, errors.New("the daemon is not running"))
		return
	}
	code, expires, err := s.h.Host.Invite()
	if err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	c, _ := fleet.ParseCode(code)
	win, unix := fleet.Commands(code)
	writeJSON(w, http.StatusOK, map[string]any{
		"code": code, "expires": since(expires), "windows": win, "unix": unix, "addrs": c.Addrs, "port": c.Port,
	})
}

func (s *Server) postRemoveMachine(w http.ResponseWriter, r *http.Request) {
	if s.h.Host == nil {
		fail(w, http.StatusServiceUnavailable, errors.New("the daemon is not running"))
		return
	}
	if err := s.h.Host.Remove(r.PathValue("id")); err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "removed"})
}

func (s *Server) postLeave(w http.ResponseWriter, r *http.Request) {
	if s.h.Member == nil {
		fail(w, http.StatusServiceUnavailable, errors.New("the daemon is not running"))
		return
	}
	told, err := s.h.Member.Leave(r.Context())
	if err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	s.signedInChanged()
	writeJSON(w, http.StatusOK, map[string]bool{"told_host": told})
}

// busyCheck matches the remote calls that would stop a job: restarting or
// removing a runner, and updating gh-runners.
var busyCheck = regexp.MustCompile(`^/api/(runners/([^/]+)/(restart|remove)|update)$`)

// proxyMachine passes a dashboard call on to a machine. Restart, remove and
// update first ask GitHub whether a runner there is running a job; if one
// is, the page gets a 409 to confirm, and sends confirm_busy=1 to go on.
func (s *Server) proxyMachine(w http.ResponseWriter, r *http.Request) {
	if s.h.Host == nil {
		fail(w, http.StatusServiceUnavailable, errors.New("the daemon is not running"))
		return
	}
	id := r.PathValue("id")
	path := "/api/" + r.PathValue("rest")
	q := r.URL.Query()
	confirmed := q.Get("confirm_busy") == "1"
	q.Del("confirm_busy")
	pq := path
	if len(q) > 0 {
		pq += "?" + q.Encode()
	}
	if !fleet.MemberAllowed(r.Method, pq) {
		fail(w, http.StatusForbidden, fmt.Errorf("%s %s is not allowed on another machine", r.Method, path))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if m := busyCheck.FindStringSubmatch(path); m != nil && r.Method == http.MethodPost && !confirmed {
		busy, err := s.machineBusy(ctx, id, m[2])
		switch {
		case err != nil:
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":   "could not check with GitHub whether a job is running there (" + err.Error() + ")",
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
	status, out, err := s.h.Host.Call(ctx, id, r.Method, pq, body)
	if err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, fleet.ErrOffline) {
			code = http.StatusServiceUnavailable
		}
		fail(w, code, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(out)
}

// machineBusy lists the runners on a machine that GitHub says are running
// a job: runner id only, or all of them.
func (s *Server) machineBusy(ctx context.Context, id, runner string) ([]string, error) {
	status, out, err := s.h.Host.Call(ctx, id, http.MethodGet, "/api/github-runners?fresh=1", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		var e apiError
		_ = json.Unmarshal(out, &e)
		return nil, errors.New(e.Error)
	}
	var states map[string]string
	if err := json.Unmarshal(out, &states); err != nil {
		return nil, err
	}
	names := runnerNames(s.h.Host, id)
	var busy []string
	for rid, st := range states {
		if st == "busy" && (runner == "" || rid == runner) {
			n := names[rid]
			if n == "" {
				n = rid
			}
			busy = append(busy, n)
		}
	}
	return busy, nil
}

func runnerNames(h *fleet.Host, id string) map[string]string {
	out := map[string]string{}
	ms, _ := h.Machines()
	for _, m := range ms {
		if m.ID == id {
			for _, r := range m.Info.Runners {
				out[r.ID] = r.Name + " (" + r.Scope + ")"
			}
		}
	}
	return out
}
