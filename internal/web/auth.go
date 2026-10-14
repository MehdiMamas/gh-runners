package web

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// authCache remembers who we are signed in as, so the page can poll state
// every few seconds without running `gh auth token` and GET /user each time.
type authCache struct {
	mu     sync.Mutex
	at     time.Time
	src    github.TokenSource
	user   github.User
	scopes string
	err    error
}

type authView struct {
	SignedIn  bool   `json:"signed_in"`
	Login     string `json:"login,omitempty"`
	Name      string `json:"name,omitempty"`
	Avatar    string `json:"avatar,omitempty"`
	Source    string `json:"source,omitempty"`
	Scopes    string `json:"scopes,omitempty"`
	Saved     bool   `json:"saved"` // signed in through gh-runners (token file)
	Error     string `json:"error,omitempty"`
	CanDevice bool   `json:"can_device"`
}

func (a *authCache) reset() {
	a.mu.Lock()
	a.at = time.Time{}
	a.mu.Unlock()
}

func (a *authCache) get(ctx context.Context) (github.TokenSource, github.User, string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ttl := 5 * time.Minute
	if a.err != nil {
		ttl = 20 * time.Second
	}
	if !a.at.IsZero() && time.Since(a.at) < ttl {
		return a.src, a.user, a.scopes, a.err
	}
	a.at = time.Now()
	a.src, a.err = github.ResolveToken(store.TokenPath())
	a.user, a.scopes = github.User{}, ""
	if a.err == nil {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		a.user, a.scopes, a.err = github.New(a.src.Token).Me(ctx)
		if a.err != nil {
			var ae *github.APIError
			if errors.As(a.err, &ae) && ae.Status == http.StatusUnauthorized {
				a.err = errors.New("the GitHub token from " + a.src.Source + " is no longer valid; sign in again")
			}
		}
	}
	return a.src, a.user, a.scopes, a.err
}

func (s *Server) authView(ctx context.Context) authView {
	src, user, scopes, err := s.auth.get(ctx)
	v := authView{CanDevice: github.ClientID() != ""}
	if _, serr := os.Stat(store.TokenPath()); serr == nil {
		v.Saved = true
	}
	if err != nil {
		v.Error = err.Error()
		return v
	}
	v.SignedIn, v.Login, v.Name, v.Avatar, v.Source, v.Scopes = true, user.Login, user.Name, user.AvatarURL, src.Source, scopes
	if src.Source == store.TokenPath() {
		v.Source = "signed in from gh-runners"
	}
	return v
}

// client is a GitHub client for the signed-in user.
func (s *Server) client(ctx context.Context) (*github.Client, string, error) {
	src, user, _, err := s.auth.get(ctx)
	if err != nil {
		return nil, "", err
	}
	return github.New(src.Token), user.Login, nil
}

func (s *Server) saveToken(token string) error {
	if err := os.MkdirAll(store.Home(), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(store.TokenPath(), []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	s.signedInChanged()
	return nil
}

func (s *Server) signedInChanged() {
	s.auth.reset()
	s.repos.reset()
	s.remote.reset()
	s.h.ResetClient()
}

// loginFlow is the one device-flow sign-in in progress.
type loginFlow struct {
	mu     sync.Mutex
	state  string // "" | pending | done | failed
	code   github.DeviceCode
	err    string
	login  string
	cancel context.CancelFunc
}

type deviceView struct {
	State   string `json:"state"`
	Code    string `json:"user_code,omitempty"`
	URL     string `json:"verification_uri,omitempty"`
	Expires int    `json:"expires_in,omitempty"`
	Error   string `json:"error,omitempty"`
	Login   string `json:"login,omitempty"`
}

func (l *loginFlow) view() deviceView {
	l.mu.Lock()
	defer l.mu.Unlock()
	return deviceView{l.state, l.code.UserCode, l.code.VerificationURI, l.code.ExpiresIn, l.err, l.login}
}

func (s *Server) postDeviceLogin(w http.ResponseWriter, r *http.Request) {
	id := github.ClientID()
	if id == "" {
		fail(w, http.StatusNotImplemented, errors.New("this build has no GitHub sign-in app; paste a token instead"))
		return
	}
	code, err := github.StartDeviceFlow(r.Context(), id, github.SignInScopes)
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(code.ExpiresIn+30)*time.Second)
	l := &s.login
	l.mu.Lock()
	if l.cancel != nil {
		l.cancel()
	}
	l.state, l.code, l.err, l.login, l.cancel = "pending", code, "", "", cancel
	l.mu.Unlock()
	go func() {
		defer cancel()
		token, err := github.WaitDeviceToken(ctx, id, code)
		if errors.Is(err, context.Canceled) {
			return // replaced by a newer sign-in
		}
		var login string
		if err == nil {
			var me github.User
			if me, _, err = github.New(token).Me(ctx); err == nil {
				login = me.Login
				err = s.saveToken(token)
			}
		}
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.code.DeviceCode != code.DeviceCode {
			return
		}
		if err != nil {
			l.state, l.err = "failed", err.Error()
			return
		}
		l.state, l.login = "done", login
		s.h.Log.Printf("dashboard: signed in to GitHub as %s", login)
	}()
	writeJSON(w, http.StatusOK, l.view())
}

func (s *Server) getDeviceLogin(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.login.view())
}

func (s *Server) postTokenLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	token := strings.TrimSpace(body.Token)
	if token == "" {
		fail(w, http.StatusBadRequest, errors.New("paste a token"))
		return
	}
	me, _, err := github.New(token).Me(r.Context())
	if err != nil {
		fail(w, http.StatusBadRequest, errors.New("GitHub did not accept that token: "+err.Error()))
		return
	}
	if err := s.saveToken(token); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"login": me.Login})
}

func (s *Server) postLogout(w http.ResponseWriter, r *http.Request) {
	if err := os.Remove(store.TokenPath()); err != nil && !os.IsNotExist(err) {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	s.signedInChanged()
	writeJSON(w, http.StatusOK, s.authView(r.Context()))
}
