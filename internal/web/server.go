// Package web is the local dashboard: a single page app served by the
// daemon on 127.0.0.1, and the JSON API behind it.
//
// Security: the daemon only listens on loopback. Requests must name a
// loopback host (no DNS rebinding), carry the cookie that the secret link
// from `gh-runners ui` sets, and API calls must send X-GHR (a custom header
// that a cross-site form or image cannot add, so no CSRF).
package web

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/fleet"
	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/ops"
	"github.com/MehdiMamas/gh-runners/internal/service"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

//go:embed assets
var assets embed.FS

// CookieName holds the dashboard secret in the browser.
const CookieName = "ghr_ui"

// Hooks connect the dashboard to the daemon that serves it.
type Hooks struct {
	Version     string
	Status      func() control.Status
	Reload      func()
	SyncNow     func()
	WakeJIT     func()
	ResetClient func()
	UpdateNow   func()
	// Pause stops everything gh-runners runs here; Resume undoes it.
	Pause  func() error
	Resume func() error
	Log    *log.Logger
	// Host is this machine as a main host; Member is its link to one.
	// Either may be nil (no daemon, in tests).
	Host   *fleet.Host
	Member *fleet.Member
}

// Server is the dashboard.
type Server struct {
	h      Hooks
	secret string
	tasks  *taskQueue
	auth   authCache
	repos  repoCache
	remote remoteCache
	login  loginFlow
	// What GitHub says per repository: workflow labels, all runners; and
	// the hand-made runners found on this machine.
	wfLabels  timedCache[[]ops.LabelSet]
	ghRunners timedCache[[]github.Runner]
	found     timedCache[[]ops.Found]
	// spawn runs `gh-runners <args>` detached; tests replace it.
	spawn func(logName string, args ...string) error
}

// New returns a dashboard that unlocks with secret.
func New(h Hooks, secret string) *Server {
	if h.Log == nil {
		h.Log = log.New(log.Writer(), "", log.LstdFlags)
	}
	for _, f := range []*func(){&h.Reload, &h.SyncNow, &h.WakeJIT, &h.ResetClient, &h.UpdateNow} {
		if *f == nil {
			*f = func() {}
		}
	}
	if h.Status == nil {
		h.Status = func() control.Status { return control.Status{} }
	}
	s := &Server{h: h, secret: secret, tasks: newTaskQueue(h.Log)}
	if m := h.Member; m != nil {
		s.auth.brokered = func() (*github.Client, string) {
			if st := m.Status(); st.Joined {
				return m.Client(), st.HostName
			}
			return nil, ""
		}
	}
	s.wfLabels.ttl, s.ghRunners.ttl, s.found.ttl = 5*time.Minute, time.Minute, 2*time.Minute
	s.spawn = func(logName string, args ...string) error {
		return service.Spawn(filepath.Join(store.Home(), logName), args...)
	}
	return s
}

// Handler serves the dashboard and passes everything else to next (the
// daemon's control API).
func (s *Server) Handler(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(assets, "assets")
	files := http.FileServer(http.FS(static))
	mux.HandleFunc("GET /{$}", s.index)
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", noCache(files)))
	mux.Handle("/api/", s.api())
	mux.Handle("/", next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			http.Error(w, "gh-runners only answers on 127.0.0.1", http.StatusMisdirectedRequest)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		mux.ServeHTTP(w, r)
	})
}

func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}

func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) unlocked(r *http.Request) bool {
	c, err := r.Cookie(CookieName)
	return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.secret)) == 1
}

const csp = "default-src 'self'; img-src 'self' https://avatars.githubusercontent.com data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if k := r.URL.Query().Get("k"); k != "" {
		if subtle.ConstantTimeCompare([]byte(k), []byte(s.secret)) == 1 {
			http.SetCookie(w, &http.Cookie{
				Name: CookieName, Value: s.secret, Path: "/", HttpOnly: true,
				SameSite: http.SameSiteStrictMode, MaxAge: 400 * 24 * 3600,
			})
		}
		// Drop the secret from the address bar and history.
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page := "assets/index.html"
	if !s.unlocked(r) {
		page = "assets/locked.html"
		w.WriteHeader(http.StatusUnauthorized)
	}
	b, _ := assets.ReadFile(page)
	_, _ = w.Write(b)
}

// apiError is how failures reach the page.
type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, apiError{err.Error()})
}

func readBody(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return errors.New("bad request body: " + err.Error())
	}
	return nil
}

// Reset forgets what the dashboard cached about GitHub (the daemon calls it
// when this machine joins or leaves a main host).
func (s *Server) Reset() { s.signedInChanged() }

// routes is the dashboard API without the browser checks.
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", s.getState)
	mux.HandleFunc("GET /api/repos", s.getRepos)
	mux.HandleFunc("GET /api/github-runners", s.getRemote)
	mux.HandleFunc("POST /api/add", s.postAdd)
	mux.HandleFunc("GET /api/workflow-labels", s.getWorkflowLabels)
	mux.HandleFunc("POST /api/repo-runners", s.postRepoRunners)
	mux.HandleFunc("PUT /api/runner-labels", s.putRunnerLabels)
	mux.HandleFunc("GET /api/found", s.getFound)
	mux.HandleFunc("POST /api/adopt", s.postAdopt)
	mux.HandleFunc("POST /api/runners/{id}/start", s.postStartStop(false))
	mux.HandleFunc("POST /api/runners/{id}/stop", s.postStartStop(true))
	mux.HandleFunc("POST /api/runners/{id}/restart", s.postRestart)
	mux.HandleFunc("POST /api/runners/{id}/remove", s.postRemove)
	mux.HandleFunc("GET /api/runners/{id}/log", s.getRunnerLog)
	mux.HandleFunc("GET /api/daemon-log", s.getDaemonLog)
	mux.HandleFunc("GET /api/sync/plan", s.getSyncPlan)
	mux.HandleFunc("POST /api/sync/apply", s.postSyncApply)
	mux.HandleFunc("PUT /api/config", s.putConfig)
	mux.HandleFunc("POST /api/runners-dir", s.postRunnersDir)
	mux.HandleFunc("GET /api/fs", s.getFS)
	mux.HandleFunc("POST /api/fs/mkdir", s.postMkdir)
	mux.HandleFunc("POST /api/service/{action}", s.postService)
	mux.HandleFunc("GET /api/doctor", s.getDoctor)
	mux.HandleFunc("POST /api/update", s.postUpdate)
	mux.HandleFunc("POST /api/pause", s.postPause)
	mux.HandleFunc("POST /api/resume", s.postResume)
	mux.HandleFunc("POST /api/login/device", s.postDeviceLogin)
	mux.HandleFunc("GET /api/login/device", s.getDeviceLogin)
	mux.HandleFunc("POST /api/login/token", s.postTokenLogin)
	mux.HandleFunc("POST /api/logout", s.postLogout)
	mux.HandleFunc("GET /api/tasks", s.getTasks)
	mux.HandleFunc("GET /api/tasks/{id}", s.getTask)
	return mux
}

func (s *Server) api() http.Handler {
	mux := s.routes()
	mux.HandleFunc("GET /api/machines", s.getMachines)
	mux.HandleFunc("POST /api/machines/host", s.postMainHost)
	mux.HandleFunc("POST /api/machines/invite", s.postInvite)
	mux.HandleFunc("POST /api/machines/leave", s.postLeave)
	mux.HandleFunc("POST /api/machines/{id}/remove", s.postRemoveMachine)
	mux.HandleFunc("/api/machines/{id}/api/{rest...}", s.proxyMachine)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-GHR") != "1" {
			fail(w, http.StatusForbidden, errors.New("missing X-GHR header"))
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
			fail(w, http.StatusForbidden, errors.New("cross-origin request refused"))
			return
		}
		if !s.unlocked(r) {
			fail(w, http.StatusUnauthorized, errors.New("locked: open the dashboard with `gh-runners ui`"))
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func since(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
