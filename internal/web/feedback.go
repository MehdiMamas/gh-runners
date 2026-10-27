package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/feedback"
	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

type feedbackReq struct {
	Text      string `json:"text"`
	RunID     string `json:"run_id"`
	Doctor    bool   `json:"doctor"`
	DaemonLog bool   `json:"daemon_log"`
	Body      string `json:"body"` // send: the preview the user read
}

type feedbackView struct {
	Body     string `json:"body,omitempty"`
	SignedIn bool   `json:"signed_in"`
	Login    string `json:"login,omitempty"`
	Message  string `json:"message,omitempty"`
	URL      string `json:"url,omitempty"`
	Number   int    `json:"number,omitempty"`
}

// feedbackToken is the user's own token. A machine signed in only through the
// main host does not count: the issue must be filed under the user's account.
func feedbackToken() (github.TokenSource, error) {
	ts, err := github.ResolveToken(store.TokenPath())
	if err != nil {
		return ts, errors.New("sign in to GitHub on this machine first (Settings > GitHub account): feedback is filed under your own account")
	}
	return ts, nil
}

func (s *Server) postFeedbackPreview(w http.ResponseWriter, r *http.Request) {
	var req feedbackReq
	if err := readBody(r, &req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	v := feedbackView{}
	var secrets []string
	if ts, err := feedbackToken(); err == nil {
		secrets = append(secrets, ts.Token)
		v.SignedIn = true
		if me, _, err := github.New(ts.Token).Me(ctx); err == nil {
			v.Login = me.Login
		} else {
			v.SignedIn, v.Message = false, "GitHub did not accept the saved token; sign in again (Settings > GitHub account)"
		}
	} else {
		v.Message = err.Error()
	}
	o := feedback.Options{Text: req.Text, RunID: req.RunID, Doctor: req.Doctor, DaemonLog: req.DaemonLog, Version: s.h.Version}
	if o.Doctor {
		st := s.h.Status()
		o.Status = &st
	}
	if req.RunID != "" {
		for _, run := range s.ciRuns() {
			if run.ID == req.RunID && run.Task > 0 {
				if t := s.ci.get(run.Task); t != nil {
					o.TaskLog = t.view(true).Log
				}
			}
		}
	}
	v.Body = feedback.Build(req.Text, s.h.Version, feedback.Collect(ctx, o), secrets)
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) postFeedback(w http.ResponseWriter, r *http.Request) {
	var req feedbackReq
	if err := readBody(r, &req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Text) == "" || strings.TrimSpace(req.Body) == "" {
		fail(w, http.StatusBadRequest, errors.New("write what happened and preview it first"))
		return
	}
	ts, err := feedbackToken()
	if err != nil {
		fail(w, http.StatusUnauthorized, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	is, err := feedback.Send(ctx, github.New(ts.Token), ts.Token, req.Text, req.Body)
	if err != nil {
		fail(w, http.StatusBadGateway, errors.New("GitHub refused the issue: "+err.Error()))
		return
	}
	s.h.Log.Printf("dashboard: feedback filed as issue %d", is.Number)
	writeJSON(w, http.StatusOK, feedbackView{SignedIn: true, URL: is.HTMLURL, Number: is.Number})
}
