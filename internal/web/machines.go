package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	// CanManage is set on a member whose main host lets it manage the fleet:
	// Machines is then the host's list, and calls go through the host.
	CanManage bool `json:"can_manage,omitempty"`
	// Account is set on a machine signed in to GitHub: Machines then also
	// lists the account's other PCs (Peer machines). AccountError says why
	// they cannot be listed, and AllowControl whether they may control this
	// machine.
	Account      bool   `json:"account,omitempty"`
	AccountError string `json:"account_error,omitempty"`
	AccountPort  int    `json:"account_port,omitempty"`
	AllowControl bool   `json:"allow_control"`
	// Login is the GitHub login this machine is signed in with, so the page
	// can tell members of the same account (which move to account machines)
	// from the others.
	Login string `json:"login,omitempty"`
}

// fleetManageAPI is what the main host serves to a member it lets manage
// the fleet (see Host.fleetCall, which has checked the path already).
func (s *Server) fleetManageAPI() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/machines", func(w http.ResponseWriter, r *http.Request) {
		ms, err := s.h.Host.Machines()
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, machinesView{MainHost: true, Machines: append([]fleet.Machine{s.h.Host.Self()}, ms...)})
	})
	mux.HandleFunc("/api/machines/{id}/api/{rest...}", s.proxyMachine)
	return mux
}

// viaHost reports whether this machine is a joined member, whose fleet
// calls go through the main host.
func (s *Server) viaHost() bool {
	cfg, _ := store.LoadConfig()
	return !cfg.MainHost && s.h.Member != nil && s.h.Member.Joined()
}

func (s *Server) postManage(w http.ResponseWriter, r *http.Request) {
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
	if err := s.h.Host.SetManage(r.PathValue("id"), body.On); err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	s.getMachines(w, r)
}

// callMachine runs a dashboard call on a machine: "host" is this machine.
func (s *Server) callMachine(ctx context.Context, id, method, pq string, body []byte) (int, []byte, error) {
	if s.isPeer(id) {
		return s.h.Peers.Call(ctx, id, method, pq, body)
	}
	if id != fleet.HostID {
		return s.h.Host.Call(ctx, id, method, pq, body)
	}
	if !fleet.MemberAllowed(method, pq) {
		return 0, nil, fmt.Errorf("%s %s is not allowed on another machine", method, pq)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://fleet"+pq, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	s.FleetAPI().ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes(), nil
}

// isPeer reports whether id is another PC of this GitHub account.
func (s *Server) isPeer(id string) bool { return s.h.Peers != nil && s.h.Peers.Has(id) }

func (s *Server) getMachines(w http.ResponseWriter, r *http.Request) {
	cfg, _ := store.LoadConfig()
	v := machinesView{MainHost: cfg.MainHost, Machines: []fleet.Machine{}, AllowControl: cfg.AllowControlOn()}
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
		if s.viaHost() {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			status, out, err := m.FleetCall(ctx, http.MethodGet, "/api/machines", nil)
			cancel()
			var hv machinesView
			if err == nil && status == http.StatusOK && json.Unmarshal(out, &hv) == nil {
				v.Machines, v.CanManage = hv.Machines, true
			}
		}
	}
	if p := s.h.Peers; p != nil {
		if on, port := p.Running(); on {
			v.Account, v.AccountPort, v.Login = true, port, p.Login()
			p.WantAll() // the tab is open: link through the relay where needed
			for _, pe := range p.Peers() {
				v.Machines = append(v.Machines, pe.Machine())
			}
		} else if err := p.StartErr(); err != nil {
			v.Account, v.AccountError = true, err.Error()
		}
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

// postAllowControl is the switch for letting the account's other PCs
// control this one.
func (s *Server) postAllowControl(w http.ResponseWriter, r *http.Request) {
	var body struct {
		On bool `json:"on"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	cfg.AllowControl = &body.On
	if err := store.SaveConfig(cfg); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
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
	if id := r.PathValue("id"); s.isPeer(id) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := s.h.Peers.Remove(ctx, id); err != nil {
			fail(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "removed"})
		return
	}
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
// removing a runner, updating gh-runners, and pausing the machine.
var busyCheck = regexp.MustCompile(`^/api/(runners/([^/]+)/(stop|restart|remove)|update|pause)$`)

// startSetPath matches Run last and Run all.
var startSetPath = regexp.MustCompile(`^/api/runners/start-(last|all)$`)

// proxyMachine passes a dashboard call on to a machine. Restart, remove and
// update first ask GitHub whether a runner there is running a job; if one
// is, the page gets a 409 to confirm, and sends confirm_busy=1 to go on.
func (s *Server) proxyMachine(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	peer := s.isPeer(id)
	if s.viaHost() && !peer {
		s.proxyViaHost(w, r)
		return
	}
	if s.h.Host == nil && !peer {
		fail(w, http.StatusServiceUnavailable, errors.New("the daemon is not running"))
		return
	}
	path := "/api/" + r.PathValue("rest")
	q := r.URL.Query()
	confirmed := q.Get("confirm_busy") == "1"
	q.Del("confirm_busy")
	if path == "/api/pause" || strings.HasSuffix(path, "/stop") {
		// The host asked GitHub itself above; the member need not again.
		q.Set("confirm_busy", "1")
	}
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
	status, out, err := s.callMachine(ctx, id, r.Method, pq, body)
	if err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, fleet.ErrOffline) {
			code = http.StatusServiceUnavailable
		}
		fail(w, code, err)
		return
	}
	if status == http.StatusForbidden && startSetPath.MatchString(path) {
		// Members before v0.6.5 do not allow Run last / Run all from the host.
		fail(w, status, errors.New("that machine runs an older gh-runners: update it (Update on its card), then try again"))
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
	status, out, err := s.callMachine(ctx, id, http.MethodGet, "/api/github-runners?fresh=1", nil)
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
	names := s.runnerNames(id)
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

func (s *Server) runnerNames(id string) map[string]string {
	out := map[string]string{}
	var ms []fleet.Machine
	if h := s.h.Host; h != nil {
		ms, _ = h.Machines()
		ms = append(ms, h.Self())
	}
	if s.h.Peers != nil {
		for _, pe := range s.h.Peers.Peers() {
			ms = append(ms, pe.Machine())
		}
	}
	for _, m := range ms {
		if m.ID == id {
			for _, r := range m.Info.Runners {
				out[r.ID] = r.Name + " (" + r.Scope + ")"
			}
		}
	}
	return out
}

// proxyViaHost passes a call on a machine to the main host, which checks
// that it allowed this machine to manage the fleet, asks about busy runners
// and forwards it.
func (s *Server) proxyViaHost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	status, out, err := s.h.Member.FleetCall(ctx, r.Method, r.URL.RequestURI(), body)
	if err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, fleet.ErrNotConnected) {
			code = http.StatusServiceUnavailable
		}
		fail(w, code, err)
		return
	}
	if status == http.StatusForbidden {
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(out, &e) == nil && e.Message != "" {
			fail(w, status, errors.New(e.Message))
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(out)
}
